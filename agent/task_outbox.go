package agent

import (
	"context"
	"encoding/json"
	"time"
)

// TaskOutboxEntry 是与业务事实原子提交的事件，不含重放业务 Handler 的能力。
// ID 必须由业务提交身份稳定导出；同 ID 的事实不能改变，ACK 前不允许清除正文。
type TaskOutboxEntry struct {
	ID         string            `bson:"_id" json:"id"`
	Scope      string            `bson:"scope" json:"-"`
	SourceID   string            `bson:"sourceID" json:"source_id"`
	Event      TaskExternalEvent `bson:"event" json:"event"`
	Revision   int64             `bson:"revision" json:"-"`
	Owner      string            `bson:"owner" json:"-"`
	LeaseUntil time.Time         `bson:"leaseUntil" json:"-"`
	RetryAt    time.Time         `bson:"retryAt" json:"-"`
	Delivered  bool              `bson:"delivered" json:"delivered"`
	CreatedAt  time.Time         `bson:"createdAt" json:"created_at"`
	ExpiresAt  time.Time         `bson:"expiresAt" json:"expires_at"`
}

// TaskOutboxStore 只负责租约投递。写入必须由宿主在原业务事务/同文档更新完成，不能分开双写。
// Claim 只取未交付且到期可用的记录；Ack/Retry 必须按 Revision、Owner 和有效租约 CAS。
// 未投递过期记录必须保留为阻塞事实供运维处理，不能删除或重做原业务。
type TaskOutboxStore interface {
	ClaimOutbox(context.Context, string, time.Duration) (TaskOutboxEntry, bool, error)
	AckOutbox(context.Context, TaskOutboxEntry) error
	RetryOutbox(context.Context, TaskOutboxEntry, time.Time) error
}

// NewTaskOutboxEntry 固定事件身份与内容，供宿主在真正原子边界插入。
func NewTaskOutboxEntry(scope, source, id, kind string, payload json.RawMessage, retention time.Duration) (TaskOutboxEntry, error) {
	now := time.Now().UTC()
	if scope == "" || source == "" || id == "" || len(id) > 128 || kind == "" || len(kind) > 128 || retention <= 0 || retention > 30*24*time.Hour {
		return TaskOutboxEntry{}, ErrTaskConflict
	}
	body, err := canonicalEventPayload(payload)
	if err != nil {
		return TaskOutboxEntry{}, err
	}
	return TaskOutboxEntry{ID: taskHash([]string{scope, source, id}), Scope: scope, SourceID: source, Event: TaskExternalEvent{Key: id, Type: kind, Payload: body, OccurredAt: now}, Revision: 1, CreatedAt: now, ExpiresAt: now.Add(retention)}, nil
}

// DeliverTaskOutbox 每次最多投递一项。发布成功但 ACK 丢失时只重发同一事件键，不调用业务。
func DeliverTaskOutbox(ctx context.Context, store TaskOutboxStore, publisher TaskEventPublisher, owner string) (bool, error) {
	if store == nil || publisher.Store == nil || owner == "" {
		return false, ErrTaskConflict
	}
	entry, ok, err := store.ClaimOutbox(ctx, owner, 30*time.Second)
	if err != nil || !ok {
		return false, err
	}
	if !entry.ExpiresAt.After(time.Now()) {
		return true, ErrTaskResultExpired
	}
	_, err = publisher.Publish(ctx, entry.Scope, entry.SourceID, entry.Event)
	if err != nil {
		// 重试的是已提交事实的投递；不因来源暂不可用而重新执行业务。
		if retryErr := store.RetryOutbox(ctx, entry, time.Now().UTC().Add(time.Second)); retryErr != nil {
			return true, retryErr
		}
		return true, err
	}
	return true, store.AckOutbox(ctx, entry)
}
