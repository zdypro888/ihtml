package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrTaskResultExpired = errors.New("agent: result body expired")
var ErrTaskResultUnavailable = errors.New("agent: result body unavailable")
var ErrTaskResultRange = errors.New("agent: invalid result byte range")

// TaskResultRecord 是不可变的最终执行事实与正文提交。Body 只用于写入，元数据查询不得返回它。
// Store 仅在正文全部持久后发布此记录；正文过期后仍保留事实，不能靠重放恢复结果。
type TaskResultRecord struct {
	Scope           string    `bson:"scope"`
	TaskID          string    `bson:"taskID"`
	Digest          string    `bson:"digest"`
	State           string    `bson:"state"`
	Error           string    `bson:"error,omitempty"`
	Started         bool      `bson:"started"`
	Size            int64     `bson:"size"`
	SHA256          string    `bson:"sha256"`
	Encoding        string    `bson:"encoding"`
	Availability    string    `bson:"availability"`
	CreatedAt       time.Time `bson:"createdAt"`
	ExpiresAt       time.Time `bson:"expiresAt"`
	LedgerExpiresAt time.Time `bson:"ledgerExpiresAt"`
	Body            []byte    `bson:"-"`
}

// TaskResultStore 必须在(scope,taskID)内原子发布不可变结果，重复补写相同事实须成功。
// GetResult 在正文过期后仍返回元数据；ReadResult 必须独立核对正文期限和范围。
type TaskResultStore interface {
	PutResult(context.Context, TaskResultRecord) (TaskResultRecord, error)
	GetResult(context.Context, string, string) (TaskResultRecord, error)
	ReadResult(context.Context, string, string, int64, int) ([]byte, error)
	CollectResults(context.Context, time.Time) error
}

// TaskResultRef 将执行事实与模型传输额度分离，句柄始终由当前授权作用域解析。
type TaskResultRef struct {
	ID           string    `json:"id" bson:"id"`
	Size         int64     `json:"size_bytes" bson:"size"`
	SHA256       string    `json:"sha256" bson:"sha256"`
	Encoding     string    `json:"encoding" bson:"encoding"`
	Availability string    `json:"availability" bson:"availability"`
	ExpiresAt    time.Time `json:"expires_at" bson:"expiresAt"`
}

// TaskResultPage 使用正文的字节偏移，NextOffset 可直接用于下一段。UTF-8 分段不截断字符。
type TaskResultPage struct {
	Result     TaskResultRef `json:"result"`
	Offset     int64         `json:"offset"`
	NextOffset int64         `json:"next_offset"`
	EOF        bool          `json:"eof"`
	Text       string        `json:"text,omitempty"`
	Data       string        `json:"data,omitempty"`
}

// MemoryTaskResultStore 供内存宿主和协议测试使用；生产宿主须注入持久实现。
type MemoryTaskResultStore struct {
	mu      sync.Mutex
	records map[string]TaskResultRecord
}

// NewMemoryTaskResultStore 创建隔离的结果目录，供会话内宿主或协议测试复用。
func NewMemoryTaskResultStore() *MemoryTaskResultStore {
	return &MemoryTaskResultStore{records: map[string]TaskResultRecord{}}
}

func resultKey(scope, id string) string { return taskHash([]string{scope, id}) }
func resultRef(record TaskResultRecord) *TaskResultRef {
	return &TaskResultRef{ID: record.TaskID, Size: record.Size, SHA256: record.SHA256, Encoding: record.Encoding, Availability: record.Availability, ExpiresAt: record.ExpiresAt}
}

// TaskResultIdentity 对不可变内容及执行事实求指纹，适配器可用于原子去重；不包含重试时间。
func TaskResultIdentity(record TaskResultRecord) string {
	return taskHash([]any{record.Scope, record.TaskID, record.Digest, record.State, record.Error, record.Started, record.Size, record.SHA256, record.Encoding, record.Availability})
}

// PutResult 校验完整正文后不可变提交，重复写入只接受同一执行事实。
func (s *MemoryTaskResultStore) PutResult(ctx context.Context, record TaskResultRecord) (TaskResultRecord, error) {
	if err := ctx.Err(); err != nil {
		return TaskResultRecord{}, err
	}
	if err := ValidateTaskResult(record); err != nil {
		return TaskResultRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := resultKey(record.Scope, record.TaskID)
	if old, ok := s.records[key]; ok {
		if TaskResultIdentity(old) != TaskResultIdentity(record) {
			return TaskResultRecord{}, ErrTaskConflict
		}
		old.Body = nil
		return old, nil
	}
	record.Body = append([]byte(nil), record.Body...)
	s.records[key] = record
	record.Body = nil
	return record, nil
}

// GetResult 只返回元数据，正文过期后仍可核对执行事实。
func (s *MemoryTaskResultStore) GetResult(ctx context.Context, scope, id string) (TaskResultRecord, error) {
	if err := ctx.Err(); err != nil {
		return TaskResultRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[resultKey(scope, id)]
	if !ok || !record.LedgerExpiresAt.After(time.Now()) {
		return TaskResultRecord{}, ErrTaskNotFound
	}
	record.Body = nil
	return record, nil
}

// ReadResult 每次核对作用域、保留期和字节范围，不依赖后台清理及时运行。
func (s *MemoryTaskResultStore) ReadResult(ctx context.Context, scope, id string, offset int64, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[resultKey(scope, id)]
	if !ok || !record.LedgerExpiresAt.After(time.Now()) {
		return nil, ErrTaskNotFound
	}
	if !record.ExpiresAt.After(time.Now()) {
		return nil, ErrTaskResultExpired
	}
	if record.Availability != "available" {
		return nil, ErrTaskResultUnavailable
	}
	if offset < 0 || offset > int64(len(record.Body)) || limit < 0 || limit > 1<<20 {
		return nil, ErrTaskResultRange
	}
	end := min(int64(len(record.Body)), offset+int64(limit))
	return append([]byte(nil), record.Body[offset:end]...), nil
}

// CollectResults 分别回收正文和最终事实，避免正文失效被误当作调用从未执行。
func (s *MemoryTaskResultStore) CollectResults(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, record := range s.records {
		if !record.LedgerExpiresAt.After(now) {
			delete(s.records, key)
			continue
		}
		if !record.ExpiresAt.After(now) {
			record.Body = nil
			s.records[key] = record
		}
	}
	return nil
}

// ReadResult 读取当前仍获准工具的完整结果片段，不接受模型指定新的存储路径或作用域。
func (r *TaskRuntime) ReadResult(ctx context.Context, scope, id string, offset int64, limit int, allowed map[string]bool) (TaskResultPage, error) {
	if scope == "" || r.cfg.Scope != "" && scope != r.cfg.Scope {
		return TaskResultPage{}, ErrTaskNotFound
	}
	if offset < 0 || limit < 1 || limit > 64<<10 {
		return TaskResultPage{}, ErrTaskResultRange
	}
	record, err := r.cfg.Store.GetTask(ctx, scope, id)
	if err != nil || !allowed[record.Info.Tool] {
		return TaskResultPage{}, ErrTaskNotFound
	}
	meta, err := r.cfg.Results.GetResult(ctx, scope, id)
	if err != nil {
		return TaskResultPage{}, err
	}
	if meta.Digest != record.Digest {
		return TaskResultPage{}, ErrTaskConflict
	}
	if !meta.ExpiresAt.After(time.Now()) {
		return TaskResultPage{}, ErrTaskResultExpired
	}
	if meta.Availability != "available" {
		return TaskResultPage{}, ErrTaskResultUnavailable
	}
	if offset > meta.Size {
		return TaskResultPage{}, ErrTaskResultRange
	}
	// 多取一个字符的最大宽度，确保跨存储块的 UTF-8 页尾仍完整。
	data, err := r.cfg.Results.ReadResult(ctx, scope, id, offset, limit+utf8.UTFMax)
	if err != nil {
		return TaskResultPage{}, err
	}
	if int64(len(data)) != min(int64(limit+utf8.UTFMax), meta.Size-offset) {
		return TaskResultPage{}, ErrTaskResultUnavailable
	}
	end := min(len(data), limit)
	if meta.Encoding == "utf-8" {
		if len(data) > 0 && !utf8.RuneStart(data[0]) {
			return TaskResultPage{}, ErrTaskResultRange
		}
		for end < len(data) && !utf8.RuneStart(data[end]) {
			end++
		}
		if !utf8.Valid(data[:end]) {
			return TaskResultPage{}, ErrTaskResultUnavailable
		}
	}
	page := TaskResultPage{Result: *resultRef(meta), Offset: offset, NextOffset: offset + int64(end), EOF: offset+int64(end) == meta.Size}
	if meta.Encoding == "utf-8" {
		page.Text = string(data[:end])
	} else {
		page.Data = base64.StdEncoding.EncodeToString(data[:end])
	}
	return page, nil
}

func (r *TaskRuntime) makeResult(t *toolTask, tool Tool, result string, callErr error, started bool, cancelled bool, digest string) TaskResultRecord {
	state := "completed"
	publicError := ""
	if callErr != nil {
		state = "failed"
		if cancelled {
			state = "cancelled"
		}
		if started && tool.Effect != ToolEffectRead {
			state = "unknown"
		}
		publicError = "工具调用未取得完整结果；请查询原业务记录，不自动重放。"
	}
	now := time.Now().UTC()
	body := []byte(result)
	sum := sha256.Sum256(body)
	encoding := "utf-8"
	if !utf8.Valid(body) {
		encoding = "base64"
	}
	availability := "available"
	if len(body) > r.cfg.MaxStoredResultBytes {
		availability = "size_exceeded"
		body = nil
	}
	return TaskResultRecord{Scope: t.scope, TaskID: t.id, Digest: digest, State: state, Error: publicError, Started: started, Size: int64(len(result)), SHA256: hex.EncodeToString(sum[:]), Encoding: encoding, Availability: availability, CreatedAt: now, ExpiresAt: now.Add(r.cfg.ResultRetention), LedgerExpiresAt: now.Add(r.cfg.LedgerRetention), Body: body}
}

func (r *TaskRuntime) applyResult(ctx context.Context, record *TaskRecord, result TaskResultRecord) error {
	if result.Digest != record.Digest || result.Started != record.Started {
		return ErrTaskConflict
	}
	record.Info.State, record.Info.Error = result.State, result.Error
	record.Info.ResultRef = resultRef(result)
	record.Info.Result = ""
	record.ResultExpiresAt, record.ExpiresAt = result.ExpiresAt, result.LedgerExpiresAt
	record.Info.ResultExpired = !result.ExpiresAt.After(time.Now())
	if result.Availability == "available" && result.Encoding == "utf-8" && result.Size <= int64(r.cfg.MaxResultBytes) && !record.Info.ResultExpired {
		body, err := r.cfg.Results.ReadResult(ctx, record.Scope, record.ID, 0, int(result.Size))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		if int64(len(body)) != result.Size || hex.EncodeToString(sum[:]) != result.SHA256 {
			return ErrTaskResultUnavailable
		}
		record.Info.Result = string(body)
	}
	record.LeaseUntil = time.Time{}
	return nil
}

// resultResponse 在模型传输额度不足时只返回可读引用，不改变已知执行状态。
func resultResponse(info TaskInfo) string {
	message := "完整结果请用 task_result 按字节偏移分段读取；无需重新执行原调用。"
	if info.ResultRef != nil && info.ResultRef.Availability != "available" {
		message = "执行事实已保存，但正文超过宿主完整存储额度或已经过期；请核对原业务回执，不要重放原调用。"
	}
	raw, _ := json.Marshal(map[string]any{"status": "result_reference", "execution_state": info.State, "task_id": info.ID, "result": info.ResultRef, "message": message})
	return string(raw)
}

// ValidateTaskResult 校验写入契约；存储适配器必须拒绝与大小/摘要不一致的正文。
func ValidateTaskResult(record TaskResultRecord) error {
	if record.Scope == "" || record.TaskID == "" || record.Digest == "" || record.Size < 0 || len(record.SHA256) != sha256.Size*2 || record.CreatedAt.IsZero() || !record.ExpiresAt.After(record.CreatedAt) || record.LedgerExpiresAt.Before(record.ExpiresAt) {
		return ErrTaskResultUnavailable
	}
	if _, err := hex.DecodeString(record.SHA256); err != nil {
		return ErrTaskResultUnavailable
	}
	if record.State != "completed" && record.State != "failed" && record.State != "cancelled" && record.State != "unknown" {
		return ErrTaskResultUnavailable
	}
	if record.Encoding != "utf-8" && record.Encoding != "base64" {
		return ErrTaskResultUnavailable
	}
	if record.Availability == "available" {
		sum := sha256.Sum256(record.Body)
		if int64(len(record.Body)) != record.Size || hex.EncodeToString(sum[:]) != record.SHA256 || record.Encoding == "utf-8" && !utf8.Valid(record.Body) {
			return ErrTaskResultUnavailable
		}
	} else if record.Availability != "size_exceeded" || len(record.Body) != 0 {
		return ErrTaskResultUnavailable
	}
	return nil
}

// persistTaskResult 的重试只补写既有事实。已提交的内容使账本可在下一实例恢复，不再调用原 Handler。
func (r *TaskRuntime) persistTaskResult(t *toolTask, outcome TaskResultRecord) (TaskRecord, error) {
	ctx, stop := context.WithTimeout(context.Background(), r.cfg.ResultSaveTimeout)
	defer stop()
	var record TaskRecord
	var lastErr error
	delay := 10 * time.Millisecond
	committed := false
	for {
		if !committed {
			var saved TaskResultRecord
			saved, lastErr = r.cfg.Results.PutResult(ctx, outcome)
			if lastErr == nil {
				outcome, committed = saved, true
			}
		}
		if committed {
			record, lastErr = r.commitTaskResult(ctx, t.scope, t.id, outcome)
			if lastErr == nil {
				return record, nil
			}
		}
		if errors.Is(lastErr, ErrTaskConflict) || errors.Is(lastErr, ErrTaskNotFound) {
			return record, lastErr
		}
		select {
		case <-ctx.Done():
			return record, lastErr
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Second)
	}
}

// commitTaskResult 允许依据已提交的真实回执修正失租推断，但不能覆盖另一份已完成回执。
func (r *TaskRuntime) commitTaskResult(ctx context.Context, scope, id string, outcome TaskResultRecord) (TaskRecord, error) {
	for range 8 {
		record, err := r.cfg.Store.GetTask(ctx, scope, id)
		if err != nil {
			return record, err
		}
		if outcome.Scope != scope || outcome.TaskID != id || record.Digest != outcome.Digest || record.Started != outcome.Started {
			return record, ErrTaskConflict
		}
		if record.Info.ResultRef != nil {
			if record.Info.ResultRef.SHA256 != outcome.SHA256 || record.Info.State != outcome.State {
				return record, ErrTaskConflict
			}
			return record, nil
		}
		revision := record.Revision
		if err = r.applyResult(ctx, &record, outcome); err != nil {
			return record, err
		}
		record.Revision++
		record.UpdatedAt = time.Now().UTC()
		ok, err := r.cfg.Store.ReplaceTask(ctx, record, revision)
		if err != nil {
			return record, err
		}
		if ok {
			r.signal()
			return record, nil
		}
	}
	return TaskRecord{}, ErrTaskConflict
}
