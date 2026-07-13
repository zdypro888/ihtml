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
	"time"
)

// FileStore 把每个用户的数据存成一个 JSON 文件(<dir>/u<sha256>.json),
// 写入走临时文件+fsync+rename+目录同步并保持原子替换；在平台/文件系统
// 支持文件与目录同步语义时同时提供崩溃/掉电持久性。不支持目录同步的
// 平台只能保证原子可见性，不能承诺掉电后目录项已落盘。
// 适合单机小规模部署与本地开发; 大规模生产建议使用 sqlstore 或自行实现 Store。
type FileStore struct {
	dir      string
	lockName string
}

// POSIX record locks are process-scoped: the same process may acquire the same
// inode again through a bind mount, volume alias, or differently normalized
// path. A single process-wide gate is deliberately conservative, but it
// closes that correctness hole without an unbounded path-lock registry.
// FileStore targets small deployments, so cross-directory serialization is an
// acceptable tradeoff; separate processes still coordinate through lockName.
var fileStoreProcessGate = make(chan struct{}, 1)

var (
	_ Store                   = (*FileStore)(nil)
	_ UserStateStore          = (*FileStore)(nil)
	_ VersionedUserStateStore = (*FileStore)(nil)
	_ RevisionStore           = (*FileStore)(nil)
)

// NewFileStore 创建文件存储, dir 不存在时自动创建。
func NewFileStore(dir string) (*FileStore, error) {
	if err := checkFileStorePlatform(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("ihtml: resolve store dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("ihtml: create store dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("ihtml: resolve store symlinks: %w", err)
	}
	return &FileStore{
		dir: resolved, lockName: filepath.Join(resolved, ".ihtml.lock"),
	}, nil
}

// withFileStoreLock 把同目录的所有读写放入一个跨句柄、跨进程
// 临界区。仅给写路径加锁不足以保证 CAS：读取必须与 rename 互斥，
// 否则读者可在另一进程正在替换时建立过期基线。
func withFileStoreLock[T any](ctx context.Context, s *FileStore, fn func() (T, error)) (result T, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case fileStoreProcessGate <- struct{}{}:
		defer func() { <-fileStoreProcessGate }()
	case <-ctx.Done():
		return result, ctx.Err()
	}

	lock, err := os.OpenFile(s.lockName, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return result, fmt.Errorf("ihtml: open store lock: %w", err)
	}
	if err := lockFileStoreFileContext(ctx, lock); err != nil {
		return result, errors.Join(fmt.Errorf("ihtml: lock store: %w", err), lock.Close())
	}
	defer func() {
		err = errors.Join(err, unlockFileStoreFile(lock), lock.Close())
	}()
	return fn()
}

func lockFileStoreFileContext(ctx context.Context, file *os.File) error {
	delay := time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryLockFileStoreFile(file)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 25*time.Millisecond)
	}
}

func (s *FileStore) withLock(ctx context.Context, fn func() error) error {
	_, err := withFileStoreLock(ctx, s, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}

type fileUserData struct {
	Items     []Item
	KV        map[string]json.RawMessage
	Revisions []Revision // 新的在前
	Version   uint64     // 每次写入单调递增; 旧文件缺省为 0
}

const fileStoreFormatVersion = 2

// fileUserDataDisk 把内存表示与磁盘编码分开。v1（无
// format_version）直接把 RawMessage 写入 kv；encoding/json 会对
// Marshaler 返回的 JSON 做 compact/HTML escape，因而无法保留 KV 原始
// 字节。v2 用 []byte 的 base64 JSON 编码保真，同时为旧版二进制写入
// 语义等价的 LegacyKV，保证顺序升级后的回滚读取不会把全部 KV 当成空。
// 新版读取时仍以 KVBytes 为权威。
type fileUserDataDisk struct {
	FormatVersion int                        `json:"format_version,omitempty"`
	Items         []Item                     `json:"items"`
	LegacyKV      map[string]json.RawMessage `json:"kv,omitempty"`
	KVBytes       map[string][]byte          `json:"kv_bytes,omitempty"`
	Revisions     []Revision                 `json:"revisions,omitempty"`
	Version       uint64                     `json:"version,omitempty"`
}

func (d fileUserData) MarshalJSON() ([]byte, error) {
	kv := make(map[string][]byte, len(d.KV))
	legacy := make(map[string]json.RawMessage, len(d.KV))
	for key, value := range d.KV {
		kv[key] = append([]byte(nil), value...)
		// RawMessage is expected to contain JSON. Preserve invalid bytes in the
		// v2 field instead of making the whole store unwritable; old binaries
		// simply cannot represent such a value.
		if json.Valid(value) {
			legacy[key] = append(json.RawMessage(nil), value...)
		}
	}
	return json.Marshal(fileUserDataDisk{
		FormatVersion: fileStoreFormatVersion,
		Items:         d.Items,
		LegacyKV:      legacy,
		KVBytes:       kv,
		Revisions:     d.Revisions,
		Version:       d.Version,
	})
}

func (d *fileUserData) UnmarshalJSON(raw []byte) error {
	var disk fileUserDataDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		return err
	}
	if disk.FormatVersion < 0 || disk.FormatVersion > fileStoreFormatVersion {
		return fmt.Errorf("unsupported file store format version %d", disk.FormatVersion)
	}
	kv := make(map[string]json.RawMessage, len(disk.LegacyKV)+len(disk.KVBytes))
	for key, value := range disk.LegacyKV {
		kv[key] = append(json.RawMessage(nil), value...)
	}
	// 如果文件同时含旧新字段（例如中断的外部迁移），v2
	// 字节值为权威值，其余旧 key 仍无损保留。
	for key, value := range disk.KVBytes {
		kv[key] = append(json.RawMessage(nil), value...)
	}
	d.Items = disk.Items
	d.KV = kv
	d.Revisions = disk.Revisions
	d.Version = disk.Version
	return nil
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
	// fileUserData.MarshalJSON 把 KV 作为字节编码，避免 encoding/json
	// 重排 RawMessage 或转义 HTML 字符。
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	target := s.fileName(user)
	tmp, err := writeTempFileSync(s.dir, "."+filepath.Base(target)+".tmp-*", raw)
	if err != nil {
		return err
	}
	if err := replaceFileStoreFile(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// rename 后同步目录, 否则崩溃/掉电时目录项可能仍指向旧文件甚至丢失。
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("ihtml: sync store dir: %w", err)
	}
	legacy := s.legacyFileName(user)
	if legacy != target {
		_ = os.Remove(legacy) // 新文件已经原子落盘，旧文件清理失败不影响正确性
	}
	return nil
}

// writeTempFileSync 以唯一名称写入并 fsync，避免多进程共用固定
// .tmp 路径时互相 truncate。返回的临时文件由调用方 rename 或删除。
func writeTempFileSync(dir, pattern string, raw []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	keep := false
	defer func() {
		if !keep {
			_ = f.Close()
			_ = os.Remove(name)
		}
	}()
	if _, err := f.Write(raw); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	keep = true
	return name, nil
}

// syncDir 同步目录元数据。只对平台明确报告的“目录 fsync
// 不支持”降级；权限、I/O 或设备错误必须传给调用方。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil && !fileStoreDirSyncUnsupported(syncErr) {
		return errors.Join(syncErr, closeErr)
	}
	return closeErr
}

// errFileStoreNoop 由 update 回调返回, 表示无需写盘(也不推进版本)。
var errFileStoreNoop = errors.New("ihtml: file store noop")

func (s *FileStore) update(ctx context.Context, user string, fn func(*fileUserData) error) error {
	return s.withLock(ctx, func() error {
		data, err := s.load(user)
		if err != nil {
			return err
		}
		if err := fn(data); err != nil {
			if errors.Is(err, errFileStoreNoop) {
				return nil
			}
			return err
		}
		data.Version++
		return s.save(user, data)
	})
}

// Items 实现 Store。
func (s *FileStore) Items(ctx context.Context, user string) ([]Item, error) {
	return withFileStoreLock(ctx, s, func() ([]Item, error) {
		data, err := s.load(user)
		if err != nil {
			return nil, err
		}
		return data.Items, nil
	})
}

// ReplaceItems 实现 Store。
func (s *FileStore) ReplaceItems(ctx context.Context, user string, items []Item) error {
	return s.update(ctx, user, func(d *fileUserData) error {
		d.Items = cloneItems(items)
		return nil
	})
}

// KVGet 实现 Store。
func (s *FileStore) KVGet(ctx context.Context, user, key string) (json.RawMessage, error) {
	return withFileStoreLock(ctx, s, func() (json.RawMessage, error) {
		data, err := s.load(user)
		if err != nil {
			return nil, err
		}
		v, ok := data.KV[key]
		if !ok {
			return nil, ErrNotFound
		}
		return v, nil
	})
}

// KVSet 实现 Store。
func (s *FileStore) KVSet(ctx context.Context, user, key string, value json.RawMessage) error {
	return s.update(ctx, user, func(d *fileUserData) error {
		d.KV[key] = append(json.RawMessage(nil), value...)
		return nil
	})
}

// KVDelete 实现 Store。
func (s *FileStore) KVDelete(ctx context.Context, user, key string) error {
	return s.update(ctx, user, func(d *fileUserData) error {
		delete(d.KV, key)
		return nil
	})
}

// KVKeys 实现 Store。
func (s *FileStore) KVKeys(ctx context.Context, user string) ([]string, error) {
	return withFileStoreLock(ctx, s, func() ([]string, error) {
		data, err := s.load(user)
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(data.KV))
		for k := range data.KV {
			keys = append(keys, k)
		}
		return keys, nil
	})
}

// EraseUser 实现 Store。
func (s *FileStore) EraseUser(ctx context.Context, user string) error {
	return s.withLock(ctx, func() error {
		data, err := s.load(user)
		if err != nil {
			return err
		}
		// 数据清空后仍保留版本 tombstone，防止 purge 前的过期
		// expected 在用户重建后出现 ABA。
		return s.save(user, &fileUserData{KV: make(map[string]json.RawMessage), Version: data.Version + 1})
	})
}

// UserState 原子读取用户的完整状态。
func (s *FileStore) UserState(ctx context.Context, user string) (UserState, error) {
	return withFileStoreLock(ctx, s, func() (UserState, error) {
		data, err := s.load(user)
		if err != nil {
			return UserState{}, err
		}
		return cloneUserState(UserState{Items: data.Items, KV: data.KV, Revisions: data.Revisions, Version: data.Version}), nil
	})
}

// ReplaceUserState 原子替换用户的完整状态。
func (s *FileStore) ReplaceUserState(ctx context.Context, user string, state UserState) error {
	return s.withLock(ctx, func() error {
		data, err := s.load(user)
		if err != nil {
			return err
		}
		next := cloneUserState(state)
		return s.save(user, &fileUserData{Items: next.Items, KV: next.KV, Revisions: next.Revisions, Version: data.Version + 1})
	})
}

// ReplaceUserStateVersioned 实现 VersionedUserStateStore。
func (s *FileStore) ReplaceUserStateVersioned(ctx context.Context, user string, state UserState, expected uint64) error {
	return s.withLock(ctx, func() error {
		data, err := s.load(user)
		if err != nil {
			return err
		}
		if data.Version != expected {
			return ErrVersionConflict
		}
		next := cloneUserState(state)
		return s.save(user, &fileUserData{Items: next.Items, KV: next.KV, Revisions: next.Revisions, Version: expected + 1})
	})
}

// Revisions 实现 Store。
func (s *FileStore) Revisions(ctx context.Context, user string) ([]RevisionInfo, error) {
	return withFileStoreLock(ctx, s, func() ([]RevisionInfo, error) {
		data, err := s.load(user)
		if err != nil {
			return nil, err
		}
		out := make([]RevisionInfo, 0, len(data.Revisions))
		for i := range data.Revisions {
			out = append(out, data.Revisions[i].Info())
		}
		return out, nil
	})
}

// RevisionItems 实现 Store。
func (s *FileStore) RevisionItems(ctx context.Context, user, id string) ([]Item, error) {
	return withFileStoreLock(ctx, s, func() ([]Item, error) {
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
	})
}

// Revision 返回指定完整快照。
func (s *FileStore) Revision(ctx context.Context, user, id string) (Revision, error) {
	return withFileStoreLock(ctx, s, func() (Revision, error) {
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
	})
}

// AddRevision 实现 Store。
func (s *FileStore) AddRevision(ctx context.Context, user string, rev Revision, keep int) error {
	return s.update(ctx, user, func(d *fileUserData) error {
		rev = cloneRevisions([]Revision{rev})[0]
		d.Revisions = append([]Revision{rev}, d.Revisions...)
		if keep > 0 && len(d.Revisions) > keep {
			d.Revisions = d.Revisions[:keep]
		}
		return nil
	})
}
