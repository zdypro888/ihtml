package authorization

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// MemoryStore 用于单进程宿主与测试；部署需跨重启时应注入持久存储。
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	claims  map[string]Claim
}

// NewMemoryStore 创建具备相同授权和原子领取语义的进程内存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]Record{}, claims: map[string]Claim{}}
}
func detached(r Record) Record {
	raw, _ := json.Marshal(r)
	var copy Record
	_ = json.Unmarshal(raw, &copy)
	return copy
}

// Create 保留独立快照并回收过期记录，容量耗尽时拒绝新增。
func (s *MemoryStore) Create(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateRecord(r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[r.ID]; ok {
		return ErrConflict
	}
	n := 0
	for id, v := range s.records {
		if !v.ExpiresAt.After(time.Now()) {
			delete(s.records, id)
			for key, claim := range s.claims {
				if claim.GrantID == id {
					delete(s.claims, key)
				}
			}
			continue
		}
		if v.Binding.SamePrincipal(r.Binding) {
			n++
		}
	}
	if n >= MaxRecords {
		return ErrConflict
	}
	s.records[r.ID] = detached(r)
	return nil
}
func (s *MemoryStore) get(binding Binding, id string) (Record, error) {
	r, ok := s.records[id]
	if !ok || !r.Binding.SamePrincipal(binding) {
		return Record{}, ErrNotFound
	}
	return r, nil
}

// Get 返回脱离存储的副本，避免调用方改写已批准范围。
func (s *MemoryStore) Get(ctx context.Context, binding Binding, id string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(binding, id)
	return detached(r), err
}

// List 保留全部当前绑定记录，供状态查询与停止撤销使用。
func (s *MemoryStore) List(ctx context.Context, binding Binding) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Record{}
	for _, r := range s.records {
		if r.Binding.SamePrincipal(binding) {
			out = append(out, detached(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Activate 将匹配签名摘要的待确认记录原子转为生效，不延长有效期。
func (s *MemoryStore) Activate(ctx context.Context, binding Binding, id, digest string, now time.Time) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(binding, id)
	if err != nil {
		return Record{}, err
	}
	if r.Digest != digest || !r.ExpiresAt.After(now) || (r.State != Pending && r.State != Active) {
		return Record{}, ErrDenied
	}
	r.State = Active
	r.UpdatedAt = now
	s.records[id] = r
	return detached(r), nil
}

// Claim 与撤销共用同一锁，每个授权目标最多产生一次 Fresh 凭据。
func (s *MemoryStore) Claim(ctx context.Context, q ClaimRequest) (Claim, error) {
	if err := ctx.Err(); err != nil {
		return Claim{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(q.Binding, q.ID)
	if err != nil {
		return Claim{}, err
	}
	if err = ValidateClaimRequest(r, q); err != nil {
		return Claim{}, err
	}
	key := q.ID + "/" + q.Key
	if old, ok := s.claims[key]; ok {
		old.Fresh = false
		return old, nil
	}
	claim := Claim{GrantID: q.ID, Key: q.Key, Invocation: q.Invocation, State: Claimed}
	s.claims[key] = claim
	claim.Fresh = true
	return claim, nil
}

// Finish 允许撤销后迟到的真实回执落库，禁止覆盖已有不同结果。
func (s *MemoryStore) Finish(ctx context.Context, binding Binding, claim Claim, outcome Outcome, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidOutcome(outcome) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(binding, claim.GrantID)
	if err != nil {
		return err
	}
	if r.Binding != binding {
		return ErrDenied
	}
	key := claim.GrantID + "/" + claim.Key
	old, ok := s.claims[key]
	if !ok || old.Invocation != claim.Invocation {
		return ErrDenied
	}
	if old.State != Claimed {
		if old.State == outcome.State && old.Result == outcome.Result {
			return nil
		}
		return ErrConflict
	}
	old.State = outcome.State
	old.Result = outcome.Result
	s.claims[key] = old
	return nil
}

// Revoke 原子关闭后续领取；已有目标占用和回执仍保留。
func (s *MemoryStore) Revoke(ctx context.Context, binding Binding, id string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.get(binding, id)
	if err != nil {
		return err
	}
	r.State = Revoked
	r.UpdatedAt = now
	s.records[id] = r
	return nil
}
