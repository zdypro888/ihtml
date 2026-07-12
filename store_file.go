package ihtml

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileStore 把每个用户的数据存成一个 JSON 文件(<dir>/u<sha256>.json),
// 写入走临时文件+rename 保证原子性。适合单机小规模部署与本地开发;
// 大规模生产建议自行实现 Store 接入数据库。
type FileStore struct {
	dir string
	mu  sync.Mutex // 文件级读-改-写串行化
}

var (
	_ Store          = (*FileStore)(nil)
	_ UserStateStore = (*FileStore)(nil)
	_ RevisionStore  = (*FileStore)(nil)
)

// NewFileStore 创建文件存储, dir 不存在时自动创建。
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("ihtml: create store dir: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

type fileUserData struct {
	Items     []Item                     `json:"items"`
	KV        map[string]json.RawMessage `json:"kv,omitempty"`
	Revisions []Revision                 `json:"revisions,omitempty"` // 新的在前
}

// fileName 始终散列完整用户标识，避免在默认大小写不敏感的文件系统上
// Alice/alice 指向同一个文件，也避免用户名出现在磁盘路径中。
func (s *FileStore) fileName(user string) string {
	sum := sha256.Sum256([]byte(user))
	return filepath.Join(s.dir, "u"+hex.EncodeToString(sum[:])+".json")
}

// legacyFileName 返回 v0.5 及更早版本使用的文件名，仅用于无损就地迁移。
func (s *FileStore) legacyFileName(user string) string {
	safe := true
	for _, r := range user {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			safe = false
			break
		}
	}
	name := user
	if !safe || user == "" || len(user) > 64 {
		sum := sha256.Sum256([]byte(user))
		name = "u" + hex.EncodeToString(sum[:16])
	}
	return filepath.Join(s.dir, name+".json")
}

func (s *FileStore) load(user string) (*fileUserData, error) {
	raw, err := os.ReadFile(s.fileName(user))
	if errors.Is(err, fs.ErrNotExist) {
		legacy := s.legacyFileName(user)
		if legacy != s.fileName(user) {
			raw, err = os.ReadFile(legacy)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return &fileUserData{KV: map[string]json.RawMessage{}}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	data := &fileUserData{}
	if err := json.Unmarshal(raw, data); err != nil {
		return nil, fmt.Errorf("ihtml: corrupt store file for user %q: %w", user, err)
	}
	if data.KV == nil {
		data.KV = map[string]json.RawMessage{}
	}
	return data, nil
}

func (s *FileStore) save(user string, data *fileUserData) error {
	// 紧凑编码: MarshalIndent 会重排 RawMessage 内部空白, 破坏 KV 值的字节保真。
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	target := s.fileName(user)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	legacy := s.legacyFileName(user)
	if legacy != target {
		_ = os.Remove(legacy) // 新文件已经原子落盘，旧文件清理失败不影响正确性
	}
	return nil
}

func (s *FileStore) update(user string, fn func(*fileUserData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return err
	}
	if err := fn(data); err != nil {
		return err
	}
	return s.save(user, data)
}

// Items 实现 Store。
func (s *FileStore) Items(_ context.Context, user string) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}

// ReplaceItems 实现 Store。
func (s *FileStore) ReplaceItems(_ context.Context, user string, items []Item) error {
	return s.update(user, func(d *fileUserData) error {
		d.Items = cloneItems(items)
		return nil
	})
}

// KVGet 实现 Store。
func (s *FileStore) KVGet(_ context.Context, user, key string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return nil, err
	}
	v, ok := data.KV[key]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

// KVSet 实现 Store。
func (s *FileStore) KVSet(_ context.Context, user, key string, value json.RawMessage) error {
	return s.update(user, func(d *fileUserData) error {
		d.KV[key] = append(json.RawMessage(nil), value...)
		return nil
	})
}

// KVDelete 实现 Store。
func (s *FileStore) KVDelete(_ context.Context, user, key string) error {
	return s.update(user, func(d *fileUserData) error {
		delete(d.KV, key)
		return nil
	})
}

// KVKeys 实现 Store。
func (s *FileStore) KVKeys(_ context.Context, user string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(data.KV))
	for k := range data.KV {
		keys = append(keys, k)
	}
	return keys, nil
}

// EraseUser 实现 Store。
func (s *FileStore) EraseUser(_ context.Context, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, name := range []string{s.fileName(user), s.legacyFileName(user)} {
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// UserState 原子读取用户的完整状态。
func (s *FileStore) UserState(_ context.Context, user string) (UserState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return UserState{}, err
	}
	return cloneUserState(UserState{Items: data.Items, KV: data.KV, Revisions: data.Revisions}), nil
}

// ReplaceUserState 原子替换用户的完整状态。
func (s *FileStore) ReplaceUserState(_ context.Context, user string, state UserState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneUserState(state)
	return s.save(user, &fileUserData{Items: next.Items, KV: next.KV, Revisions: next.Revisions})
}

// Revisions 实现 Store。
func (s *FileStore) Revisions(_ context.Context, user string) ([]RevisionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return nil, err
	}
	out := make([]RevisionInfo, 0, len(data.Revisions))
	for i := range data.Revisions {
		out = append(out, data.Revisions[i].Info())
	}
	return out, nil
}

// RevisionItems 实现 Store。
func (s *FileStore) RevisionItems(_ context.Context, user, id string) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return nil, err
	}
	for i := range data.Revisions {
		if data.Revisions[i].ID == id {
			return cloneItems(data.Revisions[i].Items), nil
		}
	}
	return nil, ErrNotFound
}

// Revision 返回指定完整快照。
func (s *FileStore) Revision(_ context.Context, user, id string) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.load(user)
	if err != nil {
		return Revision{}, err
	}
	for i := range data.Revisions {
		if data.Revisions[i].ID == id {
			return cloneRevisions(data.Revisions[i : i+1])[0], nil
		}
	}
	return Revision{}, ErrNotFound
}

// AddRevision 实现 Store。
func (s *FileStore) AddRevision(_ context.Context, user string, rev Revision, keep int) error {
	return s.update(user, func(d *fileUserData) error {
		rev = cloneRevisions([]Revision{rev})[0]
		d.Revisions = append([]Revision{rev}, d.Revisions...)
		if keep > 0 && len(d.Revisions) > keep {
			d.Revisions = d.Revisions[:keep]
		}
		return nil
	})
}
