package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrTaskContextCapacity = errors.New("agent: task context capacity reached")
var ErrTaskContextInvalid = errors.New("agent: invalid task context mutation")

// TaskContextReference 仅记录证据句柄与观察版本，不授权运行时读取或执行其指向的业务。
type TaskContextReference struct {
	Kind     string `json:"kind" bson:"kind"`
	ID       string `json:"id" bson:"id"`
	Revision int64  `json:"revision,omitempty" bson:"revision,omitempty"`
}

// TaskContextDocument 保存 AI 公开维护的目标、约束与工作事实，不保存隐藏推理或可执行业务流程。
// State 是 AI 对工作进度的声明，不代表已取得授权或原业务执行成功。
type TaskContextDocument struct {
	Goal        string                 `json:"goal" bson:"goal"`
	Constraints []string               `json:"constraints,omitempty" bson:"constraints,omitempty"`
	Pending     []string               `json:"pending,omitempty" bson:"pending,omitempty"`
	Evidence    []TaskContextReference `json:"evidence,omitempty" bson:"evidence,omitempty"`
	Notes       string                 `json:"notes,omitempty" bson:"notes,omitempty"`
	State       string                 `json:"state" bson:"state"`
}

// TaskContextRecord 是当前可见版本；删除保留墓碑，不能让旧创建重投复活同一上下文。
type TaskContextRecord struct {
	ID              string              `json:"id" bson:"id"`
	Scope           string              `json:"-" bson:"scope"`
	Revision        int64               `json:"revision" bson:"revision"`
	Document        TaskContextDocument `json:"document" bson:"document"`
	Deleted         bool                `json:"deleted,omitempty" bson:"deleted,omitempty"`
	CreatedAt       time.Time           `json:"created_at" bson:"createdAt"`
	UpdatedAt       time.Time           `json:"updated_at" bson:"updatedAt"`
	ExpiresAt       time.Time           `json:"expires_at" bson:"expiresAt"`
	AppliedRevision int64               `json:"applied_revision,omitempty" bson:"-"`
	Replayed        bool                `json:"replayed,omitempty" bson:"-"`
	DeleteKey       string              `json:"-" bson:"deleteKey,omitempty"`
	DeleteDigest    string              `json:"-" bson:"deleteDigest,omitempty"`
}

// TaskContextMutation 的限制来自宿主，模型参数不能改变配额或指纹；Key 绑定可信逻辑调用。
type TaskContextMutation struct {
	ID, Scope, Key, Digest             string
	ExpectedRevision                   int64
	Document                           TaskContextDocument
	Delete                             bool
	Now, ExpiresAt                     time.Time
	MaxRecords, MaxMutations, MaxBytes int
}

// TaskContextReceipt 保留轻量去重事实，重复保存返回当前版本与原已应用版本，不覆盖后续更新。
type TaskContextReceipt struct {
	ID        string    `bson:"id"`
	Digest    string    `bson:"digest"`
	Revision  int64     `bson:"revision"`
	ExpiresAt time.Time `bson:"expiresAt"`
}

// TaskContextBucket 将同一作用域的 CAS、配额与去重合在一次原子提交中，适配不要求数据库事务。
// 最高128个64KiB文档和4096个轻量回执，保持协调文档低于Mongo的16MiB限制。
type TaskContextBucket struct {
	Scope     string                        `bson:"scope"`
	Revision  int64                         `bson:"revision"`
	Records   map[string]TaskContextRecord  `bson:"records"`
	Receipts  map[string]TaskContextReceipt `bson:"receipts"`
	ExpiresAt time.Time                     `bson:"expiresAt"`
}

// TaskContextStore 的 Commit 必须原子核对版本、去重和作用域配额，不得以最后写入覆盖冲突。
type TaskContextStore interface {
	CommitContext(context.Context, TaskContextMutation) (TaskContextRecord, error)
	GetContext(context.Context, string, string) (TaskContextRecord, error)
	ListContexts(context.Context, string, string, int) (TaskContextPage, error)
	CollectContexts(context.Context, time.Time) error
}

// TaskContextSummary 是轻量目录；正文仅在按号读取时返回，墓碑不出现在目录中。
type TaskContextSummary struct {
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	Goal      string    `json:"goal"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TaskContextPage 明确分页和剩余额度，不能把部分目标目录误当作完整工作上下文。
type TaskContextPage struct {
	Contexts           []TaskContextSummary `json:"contexts"`
	Next               string               `json:"next,omitempty"`
	RecordsUsed        int                  `json:"records_used"`
	MutationsUsed      int                  `json:"mutations_used"`
	RecordCapacity     int                  `json:"record_capacity,omitempty"`
	MutationCapacity   int                  `json:"mutation_capacity,omitempty"`
	RemainingRecords   int                  `json:"remaining_records"`
	RemainingMutations int                  `json:"remaining_mutations"`
	MaxDocumentBytes   int                  `json:"max_document_bytes"`
}

func cloneContextDocument(doc TaskContextDocument) TaskContextDocument {
	doc.Constraints = append([]string(nil), doc.Constraints...)
	doc.Pending = append([]string(nil), doc.Pending...)
	doc.Evidence = append([]TaskContextReference(nil), doc.Evidence...)
	return doc
}
func cloneContextRecord(row TaskContextRecord) TaskContextRecord {
	row.Document = cloneContextDocument(row.Document)
	return row
}

// CloneTaskContextBucket 使存储适配器可以在副本上执行协议，失败时不泄露部分原地修改。
func CloneTaskContextBucket(bucket TaskContextBucket) TaskContextBucket {
	out := bucket
	out.Records = make(map[string]TaskContextRecord, len(bucket.Records))
	out.Receipts = make(map[string]TaskContextReceipt, len(bucket.Receipts))
	for id, row := range bucket.Records {
		out.Records[id] = cloneContextRecord(row)
	}
	for key, row := range bucket.Receipts {
		out.Receipts[key] = row
	}
	return out
}
func contextIDValid(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// ValidateTaskContextMutation 校验存储协议边界，防止绕过运行时写入超大目录或模糊删除版本。
func ValidateTaskContextMutation(m TaskContextMutation) error {
	if m.Scope == "" || len(m.Scope) > 256 || !contextIDValid(m.ID) || len(m.Key) != 64 || len(m.Digest) != 64 || m.ExpectedRevision < 0 || m.Now.IsZero() || !m.ExpiresAt.After(m.Now) || m.MaxRecords < 1 || m.MaxRecords > 128 || m.MaxMutations < 1 || m.MaxMutations > 4096 || m.MaxBytes < 1024 || m.MaxBytes > 64<<10 {
		return ErrTaskContextInvalid
	}
	if _, err := hex.DecodeString(m.Key); err != nil {
		return ErrTaskContextInvalid
	}
	if _, err := hex.DecodeString(m.Digest); err != nil {
		return ErrTaskContextInvalid
	}
	if m.Delete {
		if m.ExpectedRevision < 1 {
			return ErrTaskContextInvalid
		}
		return nil
	}
	d := m.Document
	if strings.TrimSpace(d.Goal) == "" || len(d.Goal) > 8192 || len(d.Notes) > 16384 || len(d.Constraints) > 64 || len(d.Pending) > 128 || len(d.Evidence) > 128 || !utf8.ValidString(d.Goal) || !utf8.ValidString(d.Notes) {
		return ErrTaskContextInvalid
	}
	if d.State != "active" && d.State != "paused" && d.State != "completed" && d.State != "abandoned" {
		return ErrTaskContextInvalid
	}
	for _, items := range [][]string{d.Constraints, d.Pending} {
		for _, item := range items {
			if len(item) > 4096 || !utf8.ValidString(item) {
				return ErrTaskContextInvalid
			}
		}
	}
	for _, ref := range d.Evidence {
		if ref.Kind == "" || len(ref.Kind) > 64 || ref.ID == "" || len(ref.ID) > 256 || ref.Revision < 0 || !utf8.ValidString(ref.Kind) || !utf8.ValidString(ref.ID) {
			return ErrTaskContextInvalid
		}
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > m.MaxBytes {
		return ErrTaskContextInvalid
	}
	return nil
}

// CollectTaskContextBucket 回收过期文档与去重回执；墓碑过期前仍计入配额，避免创建/删除绕开容量。
func CollectTaskContextBucket(bucket TaskContextBucket, now time.Time) TaskContextBucket {
	bucket = CloneTaskContextBucket(bucket)
	for id, row := range bucket.Records {
		if !row.ExpiresAt.After(now) {
			delete(bucket.Records, id)
		}
	}
	for key, row := range bucket.Receipts {
		if !row.ExpiresAt.After(now) {
			delete(bucket.Receipts, key)
		}
	}
	bucket.ExpiresAt = time.Time{}
	for _, row := range bucket.Records {
		if row.ExpiresAt.After(bucket.ExpiresAt) {
			bucket.ExpiresAt = row.ExpiresAt
		}
	}
	for _, row := range bucket.Receipts {
		if row.ExpiresAt.After(bucket.ExpiresAt) {
			bucket.ExpiresAt = row.ExpiresAt
		}
	}
	return bucket
}

// ApplyTaskContextMutation 是通用原子更新规则；宿主须在同一bucket版本CAS成功后才返回该结果。
func ApplyTaskContextMutation(bucket TaskContextBucket, m TaskContextMutation) (TaskContextBucket, TaskContextRecord, error) {
	if err := ValidateTaskContextMutation(m); err != nil {
		return bucket, TaskContextRecord{}, err
	}
	if bucket.Scope != "" && bucket.Scope != m.Scope {
		return bucket, TaskContextRecord{}, ErrTaskNotFound
	}
	bucket = CollectTaskContextBucket(bucket, m.Now)
	bucket.Scope = m.Scope
	if receipt, ok := bucket.Receipts[m.Key]; ok {
		if receipt.Digest != m.Digest {
			return bucket, TaskContextRecord{}, ErrTaskConflict
		}
		row, ok := bucket.Records[receipt.ID]
		if !ok {
			return bucket, TaskContextRecord{}, ErrTaskNotFound
		}
		row = cloneContextRecord(row)
		row.Replayed = true
		row.AppliedRevision = receipt.Revision
		return bucket, row, nil
	}
	// 删除墓碑自身保存去重事实；即使更新回执额度已满，用户仍能移除长期正文。
	for _, prior := range bucket.Records {
		if prior.Deleted && prior.DeleteKey == m.Key {
			if prior.DeleteDigest != m.Digest {
				return bucket, TaskContextRecord{}, ErrTaskConflict
			}
			prior = cloneContextRecord(prior)
			prior.Replayed = true
			prior.AppliedRevision = prior.Revision
			return bucket, prior, nil
		}
	}
	old, exists := bucket.Records[m.ID]
	if exists && (old.Revision != m.ExpectedRevision || old.Deleted) || !exists && m.ExpectedRevision != 0 {
		return bucket, TaskContextRecord{}, ErrTaskConflict
	}
	if m.Delete && !exists {
		return bucket, TaskContextRecord{}, ErrTaskNotFound
	}
	if !m.Delete && len(bucket.Receipts) >= m.MaxMutations || !exists && len(bucket.Records) >= m.MaxRecords {
		return bucket, TaskContextRecord{}, ErrTaskContextCapacity
	}
	row := TaskContextRecord{ID: m.ID, Scope: m.Scope, Revision: m.ExpectedRevision + 1, Document: cloneContextDocument(m.Document), Deleted: m.Delete, CreatedAt: m.Now, UpdatedAt: m.Now, ExpiresAt: m.ExpiresAt}
	if exists {
		row.CreatedAt = old.CreatedAt
	}
	if m.Delete {
		row.Document = TaskContextDocument{}
		row.DeleteKey, row.DeleteDigest = m.Key, m.Digest
	}
	bucket.Records[m.ID] = row
	// 当前上下文延长保留期时，同一ID的旧去重事实一起保留；否则旧创建重投会在目标仍存活时生成副本。
	for key, receipt := range bucket.Receipts {
		if receipt.ID == m.ID && receipt.ExpiresAt.Before(m.ExpiresAt) {
			receipt.ExpiresAt = m.ExpiresAt
			bucket.Receipts[key] = receipt
		}
	}
	if !m.Delete {
		bucket.Receipts[m.Key] = TaskContextReceipt{ID: m.ID, Digest: m.Digest, Revision: row.Revision, ExpiresAt: m.ExpiresAt}
	}
	bucket.Revision++
	if m.ExpiresAt.After(bucket.ExpiresAt) {
		bucket.ExpiresAt = m.ExpiresAt
	}
	row.AppliedRevision = row.Revision
	return bucket, row, nil
}

// TaskContextBucketPage 返回显式排序的可见目录；删除和过期正文不从目录泄露。
func TaskContextBucketPage(bucket TaskContextBucket, now time.Time, after string, limit int) (TaskContextPage, error) {
	if limit < 1 || limit > 32 || len(after) > 128 {
		return TaskContextPage{}, ErrTaskContextInvalid
	}
	bucket = CollectTaskContextBucket(bucket, now)
	page := TaskContextPage{Contexts: []TaskContextSummary{}, RecordsUsed: len(bucket.Records), MutationsUsed: len(bucket.Receipts)}
	ids := []string{}
	for id, row := range bucket.Records {
		if id > after && !row.Deleted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		page.Next = ids[limit-1]
		ids = ids[:limit]
	}
	for _, id := range ids {
		row := bucket.Records[id]
		goal := []rune(row.Document.Goal)
		if len(goal) > 256 {
			goal = goal[:256]
		}
		page.Contexts = append(page.Contexts, TaskContextSummary{ID: row.ID, Revision: row.Revision, Goal: string(goal), State: row.Document.State, UpdatedAt: row.UpdatedAt, ExpiresAt: row.ExpiresAt})
	}
	return page, nil
}

// MemoryTaskContextStore 供内存宿主与协议测试共享；生产宿主须注入持久适配。
type MemoryTaskContextStore struct {
	mu      sync.Mutex
	buckets map[string]TaskContextBucket
}

// NewMemoryTaskContextStore 创建独立上下文目录，多个运行时可共享同一实例。
func NewMemoryTaskContextStore() *MemoryTaskContextStore {
	return &MemoryTaskContextStore{buckets: map[string]TaskContextBucket{}}
}

// CommitContext 在同一锁内执行通用quota/CAS/dedup，不会部分提交。
func (s *MemoryTaskContextStore) CommitContext(ctx context.Context, m TaskContextMutation) (TaskContextRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskContextRecord{}, err
	}
	bucket, row, err := ApplyTaskContextMutation(s.buckets[m.Scope], m)
	if err == nil && !row.Replayed {
		s.buckets[m.Scope] = bucket
	}
	return row, err
}

// GetContext 返回当前可核对版本；墓碑只含删除事实而不含旧正文。
func (s *MemoryTaskContextStore) GetContext(ctx context.Context, scope, id string) (TaskContextRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskContextRecord{}, err
	}
	row, ok := s.buckets[scope].Records[id]
	if !ok || !row.ExpiresAt.After(time.Now()) {
		return TaskContextRecord{}, ErrTaskNotFound
	}
	return cloneContextRecord(row), nil
}

// ListContexts 仅列当前作用域，显式游标不会拼接其他身份的目录。
func (s *MemoryTaskContextStore) ListContexts(ctx context.Context, scope, after string, limit int) (TaskContextPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskContextPage{}, err
	}
	return TaskContextBucketPage(s.buckets[scope], time.Now(), after, limit)
}

// CollectContexts 回收正文、墓碑与去重窗口，不执行任何任务。
func (s *MemoryTaskContextStore) CollectContexts(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for scope, bucket := range s.buckets {
		bucket = CollectTaskContextBucket(bucket, now)
		if len(bucket.Records) == 0 && len(bucket.Receipts) == 0 {
			delete(s.buckets, scope)
		} else {
			s.buckets[scope] = bucket
		}
	}
	return nil
}
func (r *TaskRuntime) contextScope(scope string) bool {
	return scope != "" && len(scope) <= 256 && (r.cfg.Scope == "" || r.cfg.Scope == scope)
}

// ReadContext 读取当前作用域的显式工作事实；它不赋予原引用资源的读取或执行授权。
func (r *TaskRuntime) ReadContext(ctx context.Context, scope, id string) (TaskContextRecord, error) {
	if !r.contextScope(scope) || !contextIDValid(id) {
		return TaskContextRecord{}, ErrTaskNotFound
	}
	row, err := r.cfg.Contexts.GetContext(ctx, scope, id)
	if err == nil && (row.Scope != scope || row.ID != id) {
		return TaskContextRecord{}, ErrTaskNotFound
	}
	return row, WrapTaskStorage(err)
}

// ListContexts 返回轻量目录及宿主额度快照，不把全部长期正文塞回模型上下文。
func (r *TaskRuntime) ListContexts(ctx context.Context, scope, after string, limit int) (TaskContextPage, error) {
	if !r.contextScope(scope) {
		return TaskContextPage{}, ErrTaskNotFound
	}
	page, err := r.cfg.Contexts.ListContexts(ctx, scope, after, limit)
	page.RecordCapacity = r.cfg.MaxContexts
	page.MutationCapacity = r.cfg.MaxContextMutations
	page.RemainingRecords = max(0, r.cfg.MaxContexts-page.RecordsUsed)
	page.RemainingMutations = max(0, r.cfg.MaxContextMutations-page.MutationsUsed)
	page.MaxDocumentBytes = r.cfg.MaxContextBytes
	return page, WrapTaskStorage(err)
}
func (r *TaskRuntime) writeContext(ctx context.Context, scope, turn, key, id string, expected int64, doc TaskContextDocument, remove bool) (TaskContextRecord, error) {
	if !r.contextScope(scope) || turn == "" || key == "" || len(turn) > 512 || len(key) > 512 || expected < 0 {
		return TaskContextRecord{}, ErrTaskContextInvalid
	}
	if doc.State == "" {
		doc.State = "active"
	}
	requestedID := id
	if id == "" {
		if expected != 0 || remove {
			return TaskContextRecord{}, ErrTaskContextInvalid
		}
		id = "ctx_" + rand.Text()
	}
	epoch, err := r.scopeEpoch(ctx, scope)
	if err != nil {
		return TaskContextRecord{}, WrapTaskStorage(err)
	}
	if err = r.checkEpoch(ctx, scope, epoch, true); err != nil {
		return TaskContextRecord{}, WrapTaskStorage(err)
	}
	now := time.Now().UTC()
	mutation := TaskContextMutation{ID: id, Scope: scope, Key: taskHash([]string{scope, turn, key}), Digest: taskHash([]any{requestedID, expected, doc, remove}), ExpectedRevision: expected, Document: doc, Delete: remove, Now: now, ExpiresAt: now.Add(r.cfg.ContextRetention), MaxRecords: r.cfg.MaxContexts, MaxMutations: r.cfg.MaxContextMutations, MaxBytes: r.cfg.MaxContextBytes}
	row, err := r.cfg.Contexts.CommitContext(ctx, mutation)
	if err != nil {
		var failure *TaskFailure
		if errors.Is(err, ErrTaskContextCapacity) || errors.Is(err, ErrTaskConflict) || errors.Is(err, ErrTaskNotFound) || errors.Is(err, ErrTaskContextInvalid) {
			failure = TaskFailureFrom(err, "not_started", false, id, nil)
		} else {
			// 存储应答丢失可能已提交，返回可核对ID，不能把不确定写入描述成未开始。
			failure = TaskStorageCommitFailure(err, "unknown", id, nil)
		}
		_ = r.RecordObservation(ctx, scope, TaskObservation{ContextID: id, Kind: "context_write_failed", State: failure.ExecutionState, Code: failure.Code, TurnID: turn, InvocationID: key, At: now})
		return row, failure
	}
	if !row.Replayed {
		kind, state := "context_saved", row.Document.State
		if remove {
			kind, state = "context_deleted", "deleted"
		}
		_ = r.RecordObservation(ctx, scope, TaskObservation{ContextID: row.ID, Kind: kind, State: state, Revision: row.AppliedRevision, TurnID: turn, InvocationID: key, At: row.UpdatedAt})
	}
	return row, nil
}

// SaveContext 保存 AI 明确维护的当前版本；冲突要求先读取再决定合并，不自动覆盖。
func (r *TaskRuntime) SaveContext(ctx context.Context, scope, turn, key, id string, expected int64, doc TaskContextDocument) (TaskContextRecord, error) {
	return r.writeContext(ctx, scope, turn, key, id, expected, doc, false)
}

// DeleteContext 写入墓碑，原文不会继续从读取/目录返回，也不会因此取消已执行的业务。
func (r *TaskRuntime) DeleteContext(ctx context.Context, scope, turn, key, id string, expected int64) (TaskContextRecord, error) {
	return r.writeContext(ctx, scope, turn, key, id, expected, TaskContextDocument{}, true)
}
