package mongostore

import (
	"context"
	"errors"
	"time"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type TaskDocument struct {
	agent.TaskRecord `bson:",inline"`
	Subject          string `bson:"subject"`
	Conversation     string `bson:"conversation"`
}

type WaitDocument struct {
	agent.TaskWaitRecord `bson:",inline"`
	Subject              string `bson:"subject"`
	Conversation         string `bson:"conversation"`
}

// TaskStore 只适配正常 Mongo CAS，不解释业务接口或代替任务执行器。
// 命名空间由可信绑定固定，所有查询同时约束 owner/conversation/scope。
type TaskStore struct {
	Tasks        *mongo.Collection
	Waits        *mongo.Collection
	Subject      string
	Conversation string
	BeforeCreate func(context.Context, string) error
}

func (s *TaskStore) Filter(Scope string) bson.M {
	f := bson.M{"subject": s.Subject, "conversation": s.Conversation}
	if Scope != "" {
		f["scope"] = Scope
	}
	return f
}

func (s *TaskStore) CreateTask(ctx context.Context, r agent.TaskRecord) (agent.TaskRecord, bool, error) {
	if err := agent.TaskProtocolVersion(r.StorageVersion); err != nil {
		return r, false, err
	}
	r.StorageVersion = agent.TaskStorageVersion
	if s.BeforeCreate != nil {
		if err := s.BeforeCreate(ctx, r.Scope); err != nil {
			return r, false, err
		}
	}
	_, err := s.Tasks.InsertOne(ctx, TaskDocument{r, s.Subject, s.Conversation})
	if mongo.IsDuplicateKeyError(err) {
		old, e := s.FindTask(ctx, r.Scope, r.Key)
		return old, false, e
	}
	return r, err == nil, err
}

func (s *TaskStore) GetTask(ctx context.Context, Scope, id string) (agent.TaskRecord, error) {
	if Scope == "" || id == "" {
		return agent.TaskRecord{}, agent.ErrTaskNotFound
	}
	f := s.Filter(Scope)
	f["_id"] = id
	var d TaskDocument
	err := decodeVersioned(s.Tasks.FindOne(ctx, f), &d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return d.TaskRecord, err
}

func (s *TaskStore) FindTask(ctx context.Context, Scope, key string) (agent.TaskRecord, error) {
	if Scope == "" || key == "" {
		return agent.TaskRecord{}, agent.ErrTaskNotFound
	}
	f := s.Filter(Scope)
	f["key"] = key
	var d TaskDocument
	err := decodeVersioned(s.Tasks.FindOne(ctx, f), &d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return d.TaskRecord, err
}

func (s *TaskStore) ReplaceTask(ctx context.Context, r agent.TaskRecord, revision int64) (bool, error) {
	if err := agent.TaskProtocolVersion(r.StorageVersion); err != nil {
		return false, err
	}
	r.StorageVersion = agent.TaskStorageVersion
	if r.Revision != revision+1 {
		return false, agent.ErrTaskConflict
	}
	f := s.Filter(r.Scope)
	f["_id"] = r.ID
	f["revision"] = revision
	f["key"] = r.Key
	f["digest"] = r.Digest
	result, err := s.Tasks.ReplaceOne(ctx, compatibleFilter(f), TaskDocument{r, s.Subject, s.Conversation})
	return err == nil && result.MatchedCount == 1, err
}

func (s *TaskStore) ListTasks(ctx context.Context, Scope string, query agent.TaskScan) ([]agent.TaskRecord, error) {
	f := s.Filter(Scope)
	if query.ActiveOnly {
		f["info.state"] = bson.M{"$in": bson.A{"queued", "running", "cancelling"}}
	}
	if query.After != "" {
		f["_id"] = bson.M{"$gt": query.After}
	}
	if query.Limit < 1 || query.Limit > 512 {
		query.Limit = 128
	}
	cursor, err := s.Tasks.Find(ctx, f, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(query.Limit)).SetProjection(bson.M{"args": 0, "info.result": 0}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	out := []agent.TaskRecord{}
	for cursor.Next(ctx) {
		var d TaskDocument
		if err = decodeVersioned(cursor, &d); err != nil {
			return nil, err
		}
		out = append(out, d.TaskRecord)
	}
	return out, cursor.Err()
}

func (s *TaskStore) CreateWait(ctx context.Context, r agent.TaskWaitRecord) (agent.TaskWaitRecord, bool, error) {
	if err := agent.TaskProtocolVersion(r.StorageVersion); err != nil {
		return r, false, err
	}
	r.StorageVersion = agent.TaskStorageVersion
	if s.BeforeCreate != nil {
		if err := s.BeforeCreate(ctx, r.Scope); err != nil {
			return r, false, err
		}
	}
	_, err := s.Waits.InsertOne(ctx, WaitDocument{r, s.Subject, s.Conversation})
	if mongo.IsDuplicateKeyError(err) {
		f := s.Filter(r.Scope)
		f["key"] = r.Key
		var d WaitDocument
		e := decodeVersioned(s.Waits.FindOne(ctx, f), &d)
		return d.TaskWaitRecord, false, e
	}
	if err == nil && s.BeforeCreate != nil {
		_ = s.BeforeCreate(ctx, r.Scope)
	}
	return r, err == nil, err
}

func (s *TaskStore) ReplaceWait(ctx context.Context, r agent.TaskWaitRecord, revision int64) (bool, error) {
	if err := agent.TaskProtocolVersion(r.StorageVersion); err != nil {
		return false, err
	}
	r.StorageVersion = agent.TaskStorageVersion
	if r.Revision != revision+1 {
		return false, agent.ErrTaskConflict
	}
	f := s.Filter(r.Scope)
	f["_id"] = r.ID
	f["revision"] = revision
	f["key"] = r.Key
	f["digest"] = r.Digest
	result, err := s.Waits.ReplaceOne(ctx, compatibleFilter(f), WaitDocument{r, s.Subject, s.Conversation})
	return err == nil && result.MatchedCount == 1, err
}

func (s *TaskStore) ListWaits(ctx context.Context, Scope string, query agent.TaskScan) ([]agent.TaskWaitRecord, error) {
	f := s.Filter(Scope)
	if query.ActiveOnly {
		f["state"] = bson.M{"$in": bson.A{"waiting", "delivering"}}
	}
	if query.After != "" {
		f["_id"] = bson.M{"$gt": query.After}
	}
	if query.Limit < 1 || query.Limit > 512 {
		query.Limit = 128
	}
	cursor, err := s.Waits.Find(ctx, f, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(query.Limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	out := []agent.TaskWaitRecord{}
	for cursor.Next(ctx) {
		var d WaitDocument
		if err = decodeVersioned(cursor, &d); err != nil {
			return nil, err
		}
		out = append(out, d.TaskWaitRecord)
	}
	return out, cursor.Err()
}

// 清结果保留幂等墓碑；账本过保留期后才删除。TTL 是补充，读取和主动回收不依赖 TTL 准时执行。
func (s *TaskStore) Collect(ctx context.Context, now time.Time) error {
	f := s.Filter("")
	f["info.state"] = bson.M{"$nin": bson.A{"queued", "running", "cancelling"}}
	f["resultExpiresAt"] = bson.M{"$lte": now}
	f["info.resultExpired"] = bson.M{"$ne": true}
	if _, err := s.Tasks.UpdateMany(ctx, compatibleFilter(f), bson.M{"$set": bson.M{"info.result": "", "info.resultExpired": true}, "$inc": bson.M{"revision": 1}}); err != nil {
		return err
	}
	f["info.state"] = bson.M{"$nin": bson.A{"queued", "running", "cancelling", "unknown"}}
	delete(f, "info.resultExpired")
	if _, err := s.Tasks.UpdateMany(ctx, compatibleFilter(f), bson.M{"$unset": bson.M{"args": ""}}); err != nil {
		return err
	}
	f = s.Filter("")
	f["info.state"] = bson.M{"$nin": bson.A{"queued", "running", "cancelling"}}
	f["expiresAt"] = bson.M{"$lte": now}
	if _, err := s.Tasks.DeleteMany(ctx, compatibleFilter(f)); err != nil {
		return err
	}
	f = s.Filter("")
	f["expiresAt"] = bson.M{"$lte": now}
	_, err := s.Waits.DeleteMany(ctx, compatibleFilter(f))
	return err
}

func (s *TaskStore) GetWait(ctx context.Context, Scope, id string) (agent.TaskWaitRecord, error) {
	if Scope == "" || id == "" {
		return agent.TaskWaitRecord{}, agent.ErrTaskNotFound
	}
	f := s.Filter(Scope)
	f["_id"] = id
	var d WaitDocument
	err := decodeVersioned(s.Waits.FindOne(ctx, f), &d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return d.TaskWaitRecord, err
}

// FindWait 在共享等待额度接纳之前按原逻辑键查询；不可用时不伪造新的登记。
func (s *TaskStore) FindWait(ctx context.Context, Scope, key string) (agent.TaskWaitRecord, error) {
	if Scope == "" || key == "" {
		return agent.TaskWaitRecord{}, agent.ErrTaskNotFound
	}
	Filter := s.Filter(Scope)
	Filter["key"] = key
	var doc WaitDocument
	err := decodeVersioned(s.Waits.FindOne(ctx, Filter), &doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return doc.TaskWaitRecord, err
}
