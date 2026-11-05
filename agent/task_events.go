package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrTaskEventCapacity = errors.New("agent: event source capacity exhausted")
var ErrTaskEventCursor = errors.New("agent: invalid event cursor")

// TaskEventCondition 只引用宿主已授权的事件源，不携带URL、发布权限或业务后续步骤。
type TaskEventCondition struct {
	SourceID string   `json:"source_id" bson:"sourceID"`
	After    uint64   `json:"after" bson:"after"`
	Types    []string `json:"types,omitempty" bson:"types,omitempty"`
}

// TaskExternalEvent 的Seq是本源的持久到达顺序；ProducerSequence/OccurredAt只是来源事实，不决定交付次序。
type TaskExternalEvent struct {
	Seq              uint64          `json:"seq" bson:"seq"`
	Key              string          `json:"key" bson:"key"`
	Type             string          `json:"type" bson:"type"`
	Digest           string          `json:"-" bson:"digest"`
	ProducerSequence string          `json:"producer_sequence,omitempty" bson:"producerSequence,omitempty"`
	OccurredAt       time.Time       `json:"occurred_at,omitempty" bson:"occurredAt,omitempty"`
	PublishedAt      time.Time       `json:"published_at" bson:"publishedAt"`
	BodyExpiresAt    time.Time       `json:"body_expires_at" bson:"bodyExpiresAt"`
	Payload          json.RawMessage `json:"payload,omitempty" bson:"payload,omitempty"`
	Availability     string          `json:"availability" bson:"availability"`
}

// TaskEventSource 由可信宿主创建；Key仅用于宿主幂等，模型不能指定其他租户或发布者。
// Records保留去重指纹至源到期，正文可更早回收；满额必须拒绝，不能静默覆盖未读取事件。
type TaskEventSource struct {
	State       string              `json:"state,omitempty" bson:"state,omitempty"`
	SuccessorID string              `json:"successor_id,omitempty" bson:"successorID,omitempty"`
	ID          string              `json:"id" bson:"_id"`
	Scope       string              `json:"-" bson:"scope"`
	Key         string              `json:"-" bson:"key"`
	Label       string              `json:"label" bson:"label"`
	Revision    int64               `json:"-" bson:"revision"`
	Capacity    int                 `json:"capacity" bson:"capacity"`
	Head        uint64              `json:"head" bson:"head"`
	CreatedAt   time.Time           `json:"created_at" bson:"createdAt"`
	ExpiresAt   time.Time           `json:"expires_at" bson:"expiresAt"`
	Records     []TaskExternalEvent `json:"-" bson:"records"`
}

// TaskEventStore按(scope,key)原子创建源，按revision原子追加；同一源序号与去重在一次CAS中提交。
// List只返回源元数据，Get供可信运行时进行源内筛选；适配器仍须绑定租户与会话命名空间。
type TaskEventStore interface {
	CreateEventSource(context.Context, TaskEventSource) (TaskEventSource, bool, error)
	GetEventSource(context.Context, string, string) (TaskEventSource, error)
	ListEventSources(context.Context, string, string, int) ([]TaskEventSource, error)
	ReplaceEventSource(context.Context, TaskEventSource, int64) (bool, error)
	CollectEvents(context.Context, time.Time) error
}

// TaskEventPage明确事件属于数据事实；payload不授予权限，正文失效不表示事件从未发生。
type TaskEventPage struct {
	Source        TaskEventSource     `json:"source"`
	Events        []TaskExternalEvent `json:"events"`
	Next          uint64              `json:"next"`
	More          bool                `json:"more"`
	UntrustedData bool                `json:"untrusted_data"`
}

// TaskEventMatch固定触发等待的事件序号；重投不能换成后来另一条事实。
type TaskEventMatch struct {
	SuccessorID string `json:"successor_id,omitempty" bson:"successorID,omitempty"`
	SourceID    string `json:"source_id" bson:"sourceID"`
	Seq         uint64 `json:"seq,omitempty" bson:"seq,omitempty"`
	State       string `json:"state" bson:"state"`
}

// MemoryTaskEventStore用于内存宿主/协议测试，多个运行时可共享同一实例。
type MemoryTaskEventStore struct {
	mu      sync.Mutex
	sources map[string]TaskEventSource
}

// NewMemoryTaskEventStore创建隔离事件目录，不宣称可跨进程恢复。
func NewMemoryTaskEventStore() *MemoryTaskEventStore {
	return &MemoryTaskEventStore{sources: map[string]TaskEventSource{}}
}
func cloneEventSource(source TaskEventSource) TaskEventSource {
	source.Records = append([]TaskExternalEvent(nil), source.Records...)
	for i := range source.Records {
		source.Records[i].Payload = append(json.RawMessage(nil), source.Records[i].Payload...)
	}
	return source
}

// CreateEventSource在一个锁内固定宿主幂等键，重复创建只返回原源。
func (s *MemoryTaskEventStore) CreateEventSource(ctx context.Context, row TaskEventSource) (TaskEventSource, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskEventSource{}, false, err
	}
	for _, old := range s.sources {
		if old.Scope == row.Scope && old.Key == row.Key && old.ExpiresAt.After(time.Now()) {
			return cloneEventSource(old), false, nil
		}
	}
	count := 0
	for _, source := range s.sources {
		if source.Scope == row.Scope && source.ExpiresAt.After(time.Now()) {
			count++
		}
	}
	if count >= 256 {
		return TaskEventSource{}, false, ErrTaskEventCapacity
	}
	if _, exists := s.sources[row.ID]; exists {
		return TaskEventSource{}, false, ErrTaskConflict
	}
	s.sources[row.ID] = cloneEventSource(row)
	return cloneEventSource(row), true, nil
}

// GetEventSource核对作用域和源保留期，返回副本而非可绕过CAS的可变记录。
func (s *MemoryTaskEventStore) GetEventSource(ctx context.Context, scope, id string) (TaskEventSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskEventSource{}, err
	}
	row, ok := s.sources[id]
	if !ok || row.Scope != scope || !row.ExpiresAt.After(time.Now()) {
		return row, ErrTaskNotFound
	}
	return cloneEventSource(row), nil
}

// ListEventSources只列当前作用域元数据，省略所有事件正文。
func (s *MemoryTaskEventStore) ListEventSources(ctx context.Context, scope, after string, limit int) ([]TaskEventSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []TaskEventSource{}
	for _, row := range s.sources {
		if row.Scope == scope && row.ID > after && row.ExpiresAt.After(time.Now()) {
			row = cloneEventSource(row)
			row.Records = nil
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ReplaceEventSource同时核对版本与固定源身份，旧发布者不能覆盖后来的事件。
func (s *MemoryTaskEventStore) ReplaceEventSource(ctx context.Context, row TaskEventSource, revision int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	old, ok := s.sources[row.ID]
	if !ok || old.Scope != row.Scope || old.Revision != revision || !old.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	if row.Key != old.Key || row.Label != old.Label || row.Capacity != old.Capacity || !row.CreatedAt.Equal(old.CreatedAt) || row.ExpiresAt.Before(old.ExpiresAt) || row.Revision != revision+1 {
		return false, ErrTaskConflict
	}
	s.sources[row.ID] = cloneEventSource(row)
	return true, nil
}

// CollectEvents先清正文，保留序号/指纹；源过期后才删除去重记录。
func (s *MemoryTaskEventStore) CollectEvents(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for id, row := range s.sources {
		if !row.ExpiresAt.After(now) {
			delete(s.sources, id)
			continue
		}
		changed := false
		for i := range row.Records {
			if !row.Records[i].BodyExpiresAt.After(now) && len(row.Records[i].Payload) > 0 {
				row.Records[i].Payload = nil
				row.Records[i].Availability = "expired"
				changed = true
			}
		}
		if changed {
			row.Revision++
			s.sources[id] = row
		}
	}
	return nil
}

// TaskEventPublisher是可信Go宿主入口；模型工具不提供创建源或发布事件的能力。
type TaskEventPublisher struct {
	Store                    TaskEventStore
	Retention, BodyRetention time.Duration
	MaxRecords               int
	Observe                  func(context.Context, string, TaskObservation) error
}

func (p TaskEventPublisher) defaults() TaskEventPublisher {
	if p.Retention == 0 {
		p.Retention = 30 * 24 * time.Hour
	}
	if p.BodyRetention == 0 {
		p.BodyRetention = 24 * time.Hour
	}
	if p.MaxRecords == 0 {
		p.MaxRecords = 256
	}
	return p
}

// OpenSource以宿主提供的稳定键创建授权源，标签是数据而不是系统提示。
func (p TaskEventPublisher) OpenSource(ctx context.Context, scope, key, label string) (TaskEventSource, error) {
	p = p.defaults()
	if p.Store == nil || scope == "" || len(scope) > 256 || key == "" || len(key) > 256 || len(label) > 512 || !utf8.ValidString(label) || !utf8.ValidString(key) || p.Retention <= 0 || p.BodyRetention <= 0 || p.BodyRetention > p.Retention || p.MaxRecords < 1 || p.MaxRecords > 256 {
		return TaskEventSource{}, errors.New("agent: invalid event source")
	}
	now := time.Now().UTC()
	row := TaskEventSource{ID: rand.Text(), Scope: scope, Key: key, Label: label, Revision: 1, Capacity: p.MaxRecords, CreatedAt: now, ExpiresAt: now.Add(p.Retention)}
	saved, created, err := p.Store.CreateEventSource(ctx, row)
	if err != nil {
		return saved, err
	}
	if saved.Label != label || saved.Capacity != p.MaxRecords {
		return saved, ErrTaskConflict
	}
	saved.Records = nil
	if created {
		p.observe(ctx, scope, TaskObservation{Kind: "event_source_created", SourceID: saved.ID, Revision: saved.Revision, State: "available", At: saved.CreatedAt})
	}
	return saved, nil
}
func canonicalEventPayload(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`null`)
	}
	if len(raw) > 8192 {
		return nil, errors.New("agent: event payload too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var data any
	if err := decoder.Decode(&data); err != nil {
		return nil, errors.New("agent: invalid event payload")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("agent: invalid event payload")
	}
	out, err := json.Marshal(data)
	if len(out) > 8192 {
		return nil, errors.New("agent: event payload too large")
	}
	return out, err
}

// Publish去重键与内容指纹必须一致；乱序的生产者序号仍按持久到达顺序交付，不能跳过迟到事件。
func (p TaskEventPublisher) Publish(ctx context.Context, scope, id string, event TaskExternalEvent) (TaskExternalEvent, error) {
	p = p.defaults()
	if p.Store == nil || scope == "" || id == "" || event.Key == "" || len(event.Key) > 256 || event.Type == "" || len(event.Type) > 128 || len(event.ProducerSequence) > 128 || strings.TrimSpace(event.Type) != event.Type || p.MaxRecords < 1 || p.MaxRecords > 256 || p.Retention <= 0 || p.BodyRetention <= 0 || p.BodyRetention > p.Retention || !utf8.ValidString(event.Key) || !utf8.ValidString(event.Type) || !utf8.ValidString(event.ProducerSequence) || !event.OccurredAt.IsZero() && (event.OccurredAt.Year() < 1 || event.OccurredAt.Year() > 9999) {
		return TaskExternalEvent{}, errors.New("agent: invalid external event")
	}
	payload, err := canonicalEventPayload(event.Payload)
	if err != nil {
		return TaskExternalEvent{}, err
	}
	event.Payload = payload
	event.Digest = taskHash([]any{event.Key, event.Type, event.ProducerSequence, event.OccurredAt, event.Payload})
	for {
		if err := ctx.Err(); err != nil {
			return TaskExternalEvent{}, err
		}
		source, err := p.Store.GetEventSource(ctx, scope, id)
		if err != nil {
			return TaskExternalEvent{}, err
		}
		for _, prior := range source.Records {
			if prior.Key == event.Key {
				if prior.Digest != event.Digest {
					return TaskExternalEvent{}, ErrTaskConflict
				}
				if !prior.BodyExpiresAt.After(time.Now()) {
					prior.Payload = nil
					prior.Availability = "expired"
				}
				return prior, nil
			}
		}
		if sourceIsClosed(source) {
			return TaskExternalEvent{}, ErrTaskEventClosed
		}
		capacity := source.Capacity
		if capacity < 1 || capacity > p.MaxRecords {
			capacity = p.MaxRecords
		}
		if len(source.Records) >= capacity {
			return TaskExternalEvent{}, ErrTaskEventCapacity
		}
		event.Seq = source.Head + 1
		event.PublishedAt = time.Now().UTC()
		event.BodyExpiresAt = minTaskTime(event.PublishedAt.Add(p.BodyRetention), source.ExpiresAt)
		event.Availability = "available"
		revision := source.Revision
		source.Head = event.Seq
		source.Records = append(source.Records, event)
		source.Revision++
		ok, err := p.Store.ReplaceEventSource(ctx, source, revision)
		if err != nil {
			return TaskExternalEvent{}, err
		}
		if ok {
			p.observe(ctx, scope, TaskObservation{Kind: "event_published", SourceID: source.ID, EventID: fmt.Sprintf("%s:%d", source.ID, event.Seq), Revision: source.Revision, State: "available", At: event.PublishedAt})
			return event, nil
		}
		select {
		case <-ctx.Done():
			return TaskExternalEvent{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

// EventPublisher返回宿主可调用的发布器，不向模型传递任何凭据或发布工具。
func (r *TaskRuntime) EventPublisher() TaskEventPublisher {
	return TaskEventPublisher{Store: r.cfg.Events, Retention: r.cfg.LedgerRetention, BodyRetention: r.cfg.ResultRetention, MaxRecords: r.cfg.MaxEventRecords, Observe: r.RecordObservation}
}

// EventSources分页列当前授权作用域的源，模型不能扩大范围。
func (r *TaskRuntime) EventSources(ctx context.Context, scope, after string, limit int) ([]TaskEventSource, string, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return nil, "", ErrTaskNotFound
	}
	if limit < 1 || limit > 128 {
		return nil, "", ErrTaskEventCursor
	}
	rows, err := r.cfg.Events.ListEventSources(ctx, scope, after, limit)
	next := ""
	if len(rows) == limit {
		next = rows[len(rows)-1].ID
	}
	for i := range rows {
		rows[i].Records = nil
	}
	return rows, next, WrapTaskStorage(err)
}

// ReadEvents仅读取授权源的有序事实；cursor不接受尚未发生的未来序号。
func (r *TaskRuntime) ReadEvents(ctx context.Context, scope, id string, after uint64, limit int) (TaskEventPage, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope {
		return TaskEventPage{}, ErrTaskNotFound
	}
	if limit < 1 || limit > 8 {
		return TaskEventPage{}, ErrTaskEventCursor
	}
	source, err := r.cfg.Events.GetEventSource(ctx, scope, id)
	if err != nil {
		return TaskEventPage{}, WrapTaskStorage(err)
	}
	if after > source.Head {
		return TaskEventPage{}, ErrTaskEventCursor
	}
	page := TaskEventPage{Source: source, Events: []TaskExternalEvent{}, Next: after, UntrustedData: true}
	page.Source.Records = nil
	pageBytes := 0
	for _, event := range source.Records {
		if event.Seq <= after {
			continue
		}
		if len(page.Events) == limit {
			page.More = true
			break
		}
		if !event.BodyExpiresAt.After(time.Now()) {
			event.Payload = nil
			event.Availability = "expired"
		}
		encoded, _ := json.Marshal(event)
		if pageBytes+len(encoded) > 64<<10 && len(page.Events) > 0 {
			page.More = true
			break
		}
		pageBytes += len(encoded)
		page.Events = append(page.Events, event)
		page.Next = event.Seq
	}
	return page, nil
}
func eventTypeMatches(types []string, kind string) bool {
	if len(types) == 0 {
		return true
	}
	for _, wanted := range types {
		if wanted == kind {
			return true
		}
	}
	return false
}

// 观测失败不能改写已经持久的外部事实，也不能诱导发布者改key重投。
func (p TaskEventPublisher) observe(ctx context.Context, scope string, row TaskObservation) {
	if p.Observe == nil {
		return
	}
	defer func() { _ = recover() }()
	_ = p.Observe(ctx, scope, row)
}
