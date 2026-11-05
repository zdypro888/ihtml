package mongostore

import (
	"context"
	"errors"
	"time"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type ContextBucket struct {
	StorageVersion          int    `bson:"storageVersion,omitempty"`
	ID                      string `bson:"_id"`
	Subject                 string `bson:"subject"`
	Conversation            string `bson:"conversation"`
	agent.TaskContextBucket `bson:",inline"`
}

// ContextStore 用单作用域文档CAS一次提交版本、配额和去重，不依赖副本集事务。
// 用户/会话命名空间固定于可信绑定，模型无法选择其他租户的目录或存储键。
type ContextStore struct {
	Collection            *mongo.Collection
	Subject, Conversation string
}

func (s *ContextStore) Filter(Scope string) bson.M {
	Filter := bson.M{"subject": s.Subject, "conversation": s.Conversation}
	if Scope != "" {
		Filter["scope"] = Scope
		Filter["_id"] = digest(encode([]string{s.Subject, s.Conversation, Scope}))
	}
	return Filter
}

func (s *ContextStore) Bucket(ctx context.Context, Scope string) (ContextBucket, error) {
	var Bucket ContextBucket
	err := decodeVersioned(s.Collection.FindOne(ctx, s.Filter(Scope)), &Bucket)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return Bucket, err
}

// CommitContext 先在副本上运行共享协议，再以bucket版本CAS发布；冲突不覆盖其他AI轮次的更新。
func (s *ContextStore) CommitContext(ctx context.Context, m agent.TaskContextMutation) (agent.TaskContextRecord, error) {
	if err := agent.ValidateTaskContextMutation(m); err != nil {
		return agent.TaskContextRecord{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return agent.TaskContextRecord{}, err
		}
		prior, err := s.Bucket(ctx, m.Scope)
		if err != nil && !errors.Is(err, agent.ErrTaskNotFound) {
			return agent.TaskContextRecord{}, err
		}
		current, record, err := agent.ApplyTaskContextMutation(prior.TaskContextBucket, m)
		if err != nil {
			return record, err
		}
		if record.Replayed {
			return record, nil
		}
		doc := ContextBucket{StorageVersion: agent.TaskStorageVersion, ID: digest(encode([]string{s.Subject, s.Conversation, m.Scope})), Subject: s.Subject, Conversation: s.Conversation, TaskContextBucket: current}
		if prior.Revision == 0 {
			_, err = s.Collection.InsertOne(ctx, doc)
			if err == nil {
				return record, nil
			}
			if !mongo.IsDuplicateKeyError(err) {
				return agent.TaskContextRecord{}, err
			}
		} else {
			Filter := s.Filter(m.Scope)
			Filter["revision"] = prior.Revision
			result, e := s.Collection.ReplaceOne(ctx, compatibleFilter(Filter), doc)
			if e != nil {
				return agent.TaskContextRecord{}, e
			}
			if result.MatchedCount == 1 {
				return record, nil
			}
		}
		select {
		case <-ctx.Done():
			return agent.TaskContextRecord{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

// GetContext 返回当前授权作用域的指定版本，墓碑只返回删除事实。
func (s *ContextStore) GetContext(ctx context.Context, Scope, id string) (agent.TaskContextRecord, error) {
	Bucket, err := s.Bucket(ctx, Scope)
	if err != nil {
		return agent.TaskContextRecord{}, err
	}
	row, ok := Bucket.Records[id]
	if !ok || !row.ExpiresAt.After(time.Now()) || row.Scope != Scope {
		return agent.TaskContextRecord{}, agent.ErrTaskNotFound
	}
	return row, nil
}

// ListContexts 输出轻量目录与显式游标，不向列表透传长期任务正文。
func (s *ContextStore) ListContexts(ctx context.Context, Scope, after string, limit int) (agent.TaskContextPage, error) {
	Bucket, err := s.Bucket(ctx, Scope)
	if errors.Is(err, agent.ErrTaskNotFound) {
		return agent.TaskContextBucketPage(agent.TaskContextBucket{}, time.Now(), after, limit)
	}
	if err != nil {
		return agent.TaskContextPage{}, err
	}
	return agent.TaskContextBucketPage(Bucket.TaskContextBucket, time.Now(), after, limit)
}

// CollectContexts 只维护此用户/会话内的上下文，版本条件防止覆盖并发保存。
func (s *ContextStore) CollectContexts(ctx context.Context, now time.Time) error {
	cursor, err := s.Collection.Find(ctx, s.Filter(""))
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		var prior ContextBucket
		if err := decodeVersioned(cursor, &prior); err != nil {
			return err
		}
		next := agent.CollectTaskContextBucket(prior.TaskContextBucket, now)
		Filter := s.Filter(prior.Scope)
		Filter["revision"] = prior.Revision
		if len(next.Records) == 0 && len(next.Receipts) == 0 {
			if _, err = s.Collection.DeleteOne(ctx, compatibleFilter(Filter)); err != nil {
				return err
			}
			continue
		}
		if len(next.Records) == len(prior.Records) && len(next.Receipts) == len(prior.Receipts) {
			continue
		}
		next.Revision = prior.Revision + 1
		prior.StorageVersion = agent.TaskStorageVersion
		prior.TaskContextBucket = next
		if _, err = s.Collection.ReplaceOne(ctx, compatibleFilter(Filter), prior); err != nil {
			return err
		}
	}
	return cursor.Err()
}
