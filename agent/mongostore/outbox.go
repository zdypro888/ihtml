package mongostore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// OutboxStore 固定集合、作用域和可选同文档字段，兼容原生事务及已有业务的单文档原子更新。
// EmbeddedPath 只能由宿主构造，不接受模型路径；嵌入模式不会删除原业务文档。
type OutboxStore struct {
	Collection   *mongo.Collection
	Scope        string
	EmbeddedPath string
}

func (s *OutboxStore) field(name string) string {
	if s.EmbeddedPath == "" {
		return name
	}
	return s.EmbeddedPath + "." + name
}
func (s *OutboxStore) valid() bool {
	return s.Collection != nil && s.Scope != "" && !strings.ContainsAny(s.EmbeddedPath, ".$\x00")
}

// AppendInTransaction 要求同一实际 Mongo 事务；普通 HTTP/远端副作用不能冒充本地事务。
func (s *OutboxStore) AppendInTransaction(ctx context.Context, e agent.TaskOutboxEntry) error {
	if err := agent.TaskProtocolVersion(e.StorageVersion); err != nil {
		return err
	}
	e.StorageVersion = agent.TaskStorageVersion
	session := mongo.SessionFromContext(ctx)
	if !s.valid() || s.EmbeddedPath != "" || session == nil || !session.TransactionRunning() || e.Scope != s.Scope || e.Revision != 1 || e.Delivered || !e.ExpiresAt.After(time.Now()) {
		return agent.ErrTaskConflict
	}
	_, err := s.Collection.InsertOne(ctx, e)
	return err
}

// AtomicFields 返回嵌入式提交字段，宿主必须与真实业务事实在同一个 UpdateOne/CAS 中写入。
// Pending 时由宿主移除原记录 TTL；ACK 后才能恢复它，避免 TTL 丢弃未投递事实。
func (s *OutboxStore) AtomicFields(e agent.TaskOutboxEntry) (bson.M, error) {
	if err := agent.TaskProtocolVersion(e.StorageVersion); err != nil {
		return nil, err
	}
	e.StorageVersion = agent.TaskStorageVersion
	if !s.valid() || s.EmbeddedPath == "" || e.Scope != s.Scope || e.Revision != 1 || e.Delivered || !e.ExpiresAt.After(time.Now()) {
		return nil, agent.ErrTaskConflict
	}
	return bson.M{s.EmbeddedPath: e}, nil
}
func (s *OutboxStore) decode(raw bson.Raw) (agent.TaskOutboxEntry, error) {
	var e agent.TaskOutboxEntry
	var err error
	if s.EmbeddedPath == "" {
		err = bson.Unmarshal(raw, &e)
	} else {
		err = raw.Lookup(s.EmbeddedPath).Unmarshal(&e)
	}
	if err == nil {
		err = agent.TaskProtocolVersion(e.StorageVersion)
	}
	return e, err
}

// ClaimOutbox 将到期未交付事实也领取出来分类为 dead，不能让过期项静默消失。
func (s *OutboxStore) ClaimOutbox(ctx context.Context, owner string, lease time.Duration) (agent.TaskOutboxEntry, bool, error) {
	if !s.valid() || owner == "" || lease <= 0 {
		return agent.TaskOutboxEntry{}, false, agent.ErrTaskConflict
	}
	now := time.Now().UTC()
	f := bson.M{s.field("scope"): s.Scope, s.field("delivered"): false, s.field("dead"): bson.M{"$ne": true}, s.field("leaseUntil"): bson.M{"$lte": now}, s.field("retryAt"): bson.M{"$lte": now}}
	update := bson.M{"$set": bson.M{s.field("owner"): owner, s.field("leaseUntil"): now.Add(lease)}, "$inc": bson.M{s.field("revision"): 1, s.field("attempts"): 1}}
	var raw bson.Raw
	err := s.Collection.FindOneAndUpdate(ctx, versionFilter(f, s.field("storageVersion")), update, options.FindOneAndUpdate().SetSort(bson.D{{Key: s.field("retryAt"), Value: 1}, {Key: s.field("createdAt"), Value: 1}, {Key: "_id", Value: 1}}).SetReturnDocument(options.After)).Decode(&raw)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return agent.TaskOutboxEntry{}, false, nil
	}
	if err != nil {
		return agent.TaskOutboxEntry{}, false, err
	}
	e, err := s.decode(raw)
	return e, err == nil, err
}
func (s *OutboxStore) mutate(ctx context.Context, e agent.TaskOutboxEntry, fields bson.M) error {
	if !s.valid() {
		return agent.ErrTaskStorageConfiguration
	}
	if err := agent.TaskProtocolVersion(e.StorageVersion); err != nil {
		return err
	}
	if e.Scope != s.Scope {
		return agent.ErrTaskNotFound
	}
	f := bson.M{s.field("_id"): e.ID, s.field("scope"): s.Scope, s.field("owner"): e.Owner, s.field("revision"): e.Revision, s.field("delivered"): false, s.field("leaseUntil"): bson.M{"$gt": time.Now().UTC()}}
	changes := bson.M{}
	for k, v := range fields {
		changes[s.field(k)] = v
	}
	if done, _ := fields["delivered"].(bool); done && s.EmbeddedPath != "" {
		changes["expiresAt"] = e.ExpiresAt
	}
	r, err := s.Collection.UpdateOne(ctx, versionFilter(f, s.field("storageVersion")), bson.M{"$set": changes, "$inc": bson.M{s.field("revision"): 1}})
	if err == nil && r.MatchedCount != 1 {
		return agent.ErrTaskConflict
	}
	return err
}

// AckOutbox/RetryOutbox/FailOutbox 只更新当前有效租约，不删除事实或修改载荷。
func (s *OutboxStore) AckOutbox(ctx context.Context, e agent.TaskOutboxEntry) error {
	return s.mutate(ctx, e, bson.M{"delivered": true, "owner": "", "leaseUntil": time.Time{}})
}
func (s *OutboxStore) RetryOutbox(ctx context.Context, e agent.TaskOutboxEntry, at time.Time) error {
	return s.mutate(ctx, e, bson.M{"retryAt": at, "owner": "", "leaseUntil": time.Time{}})
}
func (s *OutboxStore) FailOutbox(ctx context.Context, e agent.TaskOutboxEntry, code string, at time.Time, dead bool) error {
	return s.mutate(ctx, e, bson.M{"failureCode": code, "dead": dead, "retryAt": at, "owner": "", "leaseUntil": time.Time{}})
}

// ListOutbox 只返回脱敏投递状态和稳定分页，不返回事件正文。
func (s *OutboxStore) ListOutbox(ctx context.Context, after string, limit int) ([]agent.TaskOutboxEntry, error) {
	if !s.valid() {
		return nil, agent.ErrTaskStorageConfiguration
	}
	if limit < 1 || limit > 128 {
		return nil, agent.ErrTaskConflict
	}
	f := bson.M{s.field("scope"): s.Scope}
	if after != "" {
		f[s.field("_id")] = bson.M{"$gt": after}
	}
	cursor, err := s.Collection.Find(ctx, f, options.Find().SetSort(bson.D{{Key: s.field("_id"), Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	out := []agent.TaskOutboxEntry{}
	for cursor.Next(ctx) {
		var raw bson.Raw
		if err := cursor.Decode(&raw); err != nil {
			return nil, err
		}
		e, err := s.decode(raw)
		if err != nil {
			return nil, err
		}
		e.Event.Payload = nil
		e.Owner = ""
		out = append(out, e)
	}
	return out, cursor.Err()
}

// RequeueOutbox 需要当前版本并保留原事件，不修改过期事实期限，不重执行业务。
func (s *OutboxStore) RequeueOutbox(ctx context.Context, id string, revision int64) error {
	if !s.valid() {
		return agent.ErrTaskStorageConfiguration
	}
	if id == "" || revision < 1 {
		return agent.ErrTaskConflict
	}
	f := bson.M{s.field("_id"): id, s.field("scope"): s.Scope, s.field("revision"): revision, s.field("delivered"): false, s.field("expiresAt"): bson.M{"$gt": time.Now().UTC()}, s.field("leaseUntil"): bson.M{"$lte": time.Now().UTC()}}
	r, err := s.Collection.UpdateOne(ctx, versionFilter(f, s.field("storageVersion")), bson.M{"$set": bson.M{s.field("dead"): false, s.field("retryAt"): time.Time{}, s.field("failureCode"): ""}, "$inc": bson.M{s.field("revision"): 1}})
	if err == nil && r.MatchedCount != 1 {
		return agent.ErrTaskConflict
	}
	return err
}
