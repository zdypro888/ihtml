package mongostore

import (
	"context"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func createIndexes(ctx context.Context, c *mongo.Collection, models ...mongo.IndexModel) error {
	if c == nil {
		return agent.ErrTaskStorageConfiguration
	}
	models = append(models, mongo.IndexModel{Keys: bson.D{{Key: "storageVersion", Value: 1}}})
	_, err := c.Indexes().CreateMany(ctx, models)
	return err
}
func ttl(field string) mongo.IndexModel {
	return mongo.IndexModel{Keys: bson.D{{Key: field, Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)}
}

// EnsureIndexes 只创建本适配器协议需要的索引；集合名称和数据库由宿主固定，不隐式迁移数据。
func (s *TaskStore) EnsureIndexes(ctx context.Context) error {
	for _, item := range []struct {
		c     *mongo.Collection
		state string
	}{{s.Tasks, "info.state"}, {s.Waits, "state"}} {
		if err := createIndexes(ctx, item.c,
			mongo.IndexModel{Keys: bson.D{{Key: "subject", Value: 1}, {Key: "conversation", Value: 1}, {Key: "scope", Value: 1}, {Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)},
			ttl("expiresAt"), mongo.IndexModel{Keys: bson.D{{Key: "subject", Value: 1}, {Key: "conversation", Value: 1}, {Key: "scope", Value: 1}, {Key: item.state, Value: 1}, {Key: "_id", Value: 1}}}); err != nil {
			return err
		}
	}
	return nil
}

// EnsureIndexes 保留正文和幂等墓碑各自的期限。
func (s *ResultStore) EnsureIndexes(ctx context.Context) error {
	if err := createIndexes(ctx, s.Manifests, ttl("ledgerExpiresAt")); err != nil {
		return err
	}
	return createIndexes(ctx, s.Chunks, ttl("expiresAt"))
}

// EnsureIndexes 不删除未投递事实；嵌入模式只增加领取索引，父文档 TTL 由原业务管理。
func (s *OutboxStore) EnsureIndexes(ctx context.Context) error {
	if !s.valid() {
		return agent.ErrTaskStorageConfiguration
	}
	models := []mongo.IndexModel{{Keys: bson.D{{Key: s.field("scope"), Value: 1}, {Key: s.field("delivered"), Value: 1}, {Key: s.field("retryAt"), Value: 1}, {Key: s.field("leaseUntil"), Value: 1}, {Key: s.field("createdAt"), Value: 1}}}}
	if s.EmbeddedPath == "" {
		m := ttl("expiresAt")
		m.Options.SetPartialFilterExpression(bson.M{"delivered": true})
		models = append(models, m)
	}
	if s.EmbeddedPath != "" {
		models = append(models, mongo.IndexModel{Keys: bson.D{{Key: s.field("storageVersion"), Value: 1}}})
	}
	return createIndexes(ctx, s.Collection, models...)
}

// EnsureIndexes 维护事件目录、源期限及按作用域分页索引。
func (s *EventStore) EnsureIndexes(ctx context.Context) error {
	if err := createIndexes(ctx, s.Sources, ttl("source.expiresAt"), mongo.IndexModel{Keys: bson.D{{Key: "subject", Value: 1}, {Key: "conversation", Value: 1}, {Key: "source.scope", Value: 1}, {Key: "source._id", Value: 1}}}); err != nil {
		return err
	}
	return createIndexes(ctx, s.Catalogs, ttl("expiresAt"))
}

// EnsureIndexes 回收已过完整保留期的上下文桶。
func (s *ContextStore) EnsureIndexes(ctx context.Context) error {
	return createIndexes(ctx, s.Collection, ttl("expiresAt"))
}

// EnsureIndexes 观测与权威执行记录使用独立保留期。
func (s *ObservationStore) EnsureIndexes(ctx context.Context) error {
	return createIndexes(ctx, s.Collection, ttl("expiresAt"), mongo.IndexModel{Keys: bson.D{{Key: "subject", Value: 1}, {Key: "conversation", Value: 1}, {Key: "scope", Value: 1}, {Key: "id", Value: 1}}})
}

// EnsureIndexes 只清除已经超过最长租约和停止屏障保留期的控制记录。
func (s *ControlStore) EnsureIndexes(ctx context.Context) error {
	return createIndexes(ctx, s.Collection, ttl("expiresAt"))
}
