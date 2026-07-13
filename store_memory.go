package ihtml

import (
	"context"
	"encoding/json"
	"sync"
)

// MemoryStore 是纯内存 Store 实现, 适合测试与临时体验, 进程退出即丢失。
type MemoryStore struct {
	mu    sync.RWMutex
	users map[string]*memoryUser
}

var (
	_ Store                   = (*MemoryStore)(nil)
	_ UserStateStore          = (*MemoryStore)(nil)
	_ VersionedUserStateStore = (*MemoryStore)(nil)
	_ RevisionStore           = (*MemoryStore)(nil)
)

type memoryUser struct {
	items     []Item
	kv        map[string]json.RawMessage
	revisions []Revision // 新的在前
	version   uint64     // 每次写入单调递增, 供条件写检测交错
}

// NewMemoryStore 创建内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{users: make(map[string]*memoryUser)}
}

func (s *MemoryStore) user(user string, create bool) *memoryUser {
	u := s.users[user]
	if u == nil && create {
		u = &memoryUser{kv: make(map[string]json.RawMessage)}
		s.users[user] = u
	}
	return u
}

// Items 实现 Store。
func (s *MemoryStore) Items(_ context.Context, user string) ([]Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.user(user, false)
	if u == nil {
		return nil, nil
	}
	return cloneItems(u.items), nil
}

// ReplaceItems 实现 Store。
func (s *MemoryStore) ReplaceItems(_ context.Context, user string, items []Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.user(user, true)
	u.items = cloneItems(items)
	u.version++
	return nil
}

// KVGet 实现 Store。
func (s *MemoryStore) KVGet(_ context.Context, user, key string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.user(user, false)
	if u == nil {
		return nil, ErrNotFound
	}
	v, ok := u.kv[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append(json.RawMessage(nil), v...), nil
}

// KVSet 实现 Store。
func (s *MemoryStore) KVSet(_ context.Context, user, key string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.user(user, true)
	u.kv[key] = append(json.RawMessage(nil), value...)
	u.version++
	return nil
}

// KVDelete 实现 Store。
func (s *MemoryStore) KVDelete(_ context.Context, user, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.user(user, false); u != nil {
		if _, ok := u.kv[key]; ok {
			delete(u.kv, key)
			u.version++
		}
	}
	return nil
}

// KVKeys 实现 Store。
func (s *MemoryStore) KVKeys(_ context.Context, user string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.user(user, false)
	if u == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(u.kv))
	for k := range u.kv {
		keys = append(keys, k)
	}
	return keys, nil
}

// EraseUser 实现 Store。
func (s *MemoryStore) EraseUser(_ context.Context, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.users, user)
	return nil
}

// UserState 原子读取用户的完整状态。
func (s *MemoryStore) UserState(_ context.Context, user string) (UserState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.user(user, false)
	if u == nil {
		return UserState{KV: map[string]json.RawMessage{}}, nil
	}
	return cloneUserState(UserState{Items: u.items, KV: u.kv, Revisions: u.revisions, Version: u.version}), nil
}

// ReplaceUserState 原子替换用户的完整状态。
func (s *MemoryStore) ReplaceUserState(_ context.Context, user string, state UserState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneUserState(state)
	var version uint64
	if u := s.user(user, false); u != nil {
		version = u.version
	}
	s.users[user] = &memoryUser{items: next.Items, kv: next.KV, revisions: next.Revisions, version: version + 1}
	return nil
}

// ReplaceUserStateVersioned 实现 VersionedUserStateStore。
func (s *MemoryStore) ReplaceUserStateVersioned(_ context.Context, user string, state UserState, expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var version uint64
	if u := s.user(user, false); u != nil {
		version = u.version
	}
	if version != expected {
		return ErrVersionConflict
	}
	next := cloneUserState(state)
	s.users[user] = &memoryUser{items: next.Items, kv: next.KV, revisions: next.Revisions, version: expected + 1}
	return nil
}

// Revisions 实现 Store。
func (s *MemoryStore) Revisions(_ context.Context, user string) ([]RevisionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.user(user, false)
	if u == nil {
		return nil, nil
	}
	out := make([]RevisionInfo, 0, len(u.revisions))
	for i := range u.revisions {
		out = append(out, u.revisions[i].Info())
	}
	return out, nil
}

// RevisionItems 实现 Store。
func (s *MemoryStore) RevisionItems(_ context.Context, user, id string) ([]Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.user(user, false)
	if u == nil {
		return nil, ErrNotFound
	}
	for i := range u.revisions {
		if u.revisions[i].ID == id {
			return cloneItems(u.revisions[i].Items), nil
		}
	}
	return nil, ErrNotFound
}

// Revision 返回指定完整快照。
func (s *MemoryStore) Revision(_ context.Context, user, id string) (Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.user(user, false)
	if u == nil {
		return Revision{}, ErrNotFound
	}
	for i := range u.revisions {
		if u.revisions[i].ID == id {
			return cloneRevisions(u.revisions[i : i+1])[0], nil
		}
	}
	return Revision{}, ErrNotFound
}

// AddRevision 实现 Store。
func (s *MemoryStore) AddRevision(_ context.Context, user string, rev Revision, keep int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.user(user, true)
	rev = cloneRevisions([]Revision{rev})[0]
	u.revisions = append([]Revision{rev}, u.revisions...)
	if keep > 0 && len(u.revisions) > keep {
		u.revisions = u.revisions[:keep]
	}
	u.version++
	return nil
}
