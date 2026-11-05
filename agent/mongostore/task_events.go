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

type EventDocument struct {
	StorageVersion int                   `bson:"storageVersion,omitempty"`
	ID             string                `bson:"_id"`
	Subject        string                `bson:"subject"`
	Conversation   string                `bson:"conversation"`
	Source         agent.TaskEventSource `bson:"source"`
}

type EventReservation struct {
	ID          string    `bson:"id"`
	Published   bool      `bson:"published"`
	Key         string    `bson:"key"`
	LabelDigest string    `bson:"labelDigest"`
	CreatedAt   time.Time `bson:"createdAt"`
	ExpiresAt   time.Time `bson:"expiresAt"`
}

type EventCatalog struct {
	StorageVersion int                `bson:"storageVersion,omitempty"`
	ID             string             `bson:"_id"`
	Subject        string             `bson:"subject"`
	Conversation   string             `bson:"conversation"`
	Scope          string             `bson:"scope"`
	Revision       int64              `bson:"revision"`
	ExpiresAt      time.Time          `bson:"expiresAt"`
	Sources        []EventReservation `bson:"sources"`
}

// EventStore只保存宿主授权的事实源，不解释业务类型或生成后续步骤。
// catalog在单文档CAS中预留有界源数；源正文自身也只以单文档CAS追加，避免序号/去重分离提交。
type EventStore struct {
	Sources, Catalogs     *mongo.Collection
	Subject, Conversation string
}

func (s *EventStore) SourceID(Scope, id string) string {
	return digest(encode([]string{s.Subject, s.Conversation, Scope, id}))
}

func (s *EventStore) Filter(Scope, id string) bson.M {
	return bson.M{"_id": s.SourceID(Scope, id), "subject": s.Subject, "conversation": s.Conversation, "source.scope": Scope, "source._id": id}
}

// CreateEventSource先固定源数预留再发布源，崩溃预留只可由原key补写，不能为并发创建穿透额度。
func (s *EventStore) CreateEventSource(ctx context.Context, row agent.TaskEventSource) (agent.TaskEventSource, bool, error) {
	catalogID := s.SourceID(row.Scope, "event-source-catalog")
	key := digest(row.Key)
	labelDigest := digest(encode([]any{row.Label, row.Capacity}))
	for {
		if err := ctx.Err(); err != nil {
			return agent.TaskEventSource{}, false, err
		}
		var catalog EventCatalog
		err := decodeVersioned(s.Catalogs.FindOne(ctx, bson.M{"_id": catalogID, "subject": s.Subject, "conversation": s.Conversation, "scope": row.Scope}), &catalog)
		if errors.Is(err, mongo.ErrNoDocuments) {
			catalog = EventCatalog{StorageVersion: agent.TaskStorageVersion, ID: catalogID, Subject: s.Subject, Conversation: s.Conversation, Scope: row.Scope}
		} else if err != nil {
			return agent.TaskEventSource{}, false, err
		}
		now := time.Now()
		live := catalog.Sources[:0]
		var reserved *EventReservation
		for _, entry := range catalog.Sources {
			if !entry.ExpiresAt.After(now) {
				continue
			}
			live = append(live, entry)
			if entry.Key == key {
				copy := entry
				reserved = &copy
			}
		}
		catalog.Sources = live
		if reserved != nil {
			if reserved.LabelDigest != labelDigest {
				return agent.TaskEventSource{}, false, agent.ErrTaskConflict
			}
			row.ID, row.CreatedAt, row.ExpiresAt = reserved.ID, reserved.CreatedAt, reserved.ExpiresAt
			if reserved.Published {
				old, e := s.GetEventSource(ctx, row.Scope, row.ID)
				if e == nil && (old.Key != row.Key || old.Label != row.Label || old.Capacity != row.Capacity) {
					e = agent.ErrTaskConflict
				}
				return old, false, e
			}
			_, err = s.Sources.InsertOne(ctx, EventDocument{StorageVersion: agent.TaskStorageVersion, ID: s.SourceID(row.Scope, row.ID), Subject: s.Subject, Conversation: s.Conversation, Source: row})
			created := err == nil
			if mongo.IsDuplicateKeyError(err) {
				var old agent.TaskEventSource
				old, err = s.GetEventSource(ctx, row.Scope, row.ID)
				if err == nil && (old.Key != row.Key || old.Label != row.Label || old.Capacity != row.Capacity) {
					err = agent.ErrTaskConflict
				}
				row = old
			}
			if err != nil {
				return row, false, err
			}
			result, err := s.Catalogs.UpdateOne(ctx, compatibleFilter(bson.M{"_id": catalogID, "subject": s.Subject, "conversation": s.Conversation, "scope": row.Scope, "sources": bson.M{"$elemMatch": bson.M{"id": row.ID, "key": key, "labelDigest": labelDigest, "published": bson.M{"$ne": true}}}}), bson.M{"$set": bson.M{"sources.$[entry].published": true}, "$inc": bson.M{"revision": 1}}, options.UpdateOne().SetArrayFilters([]any{bson.M{"entry.id": row.ID, "entry.key": key, "entry.labelDigest": labelDigest}}))
			if err != nil {
				return row, false, err
			}
			created = result.MatchedCount == 1
			if !created {
				err = s.Catalogs.FindOne(ctx, bson.M{"_id": catalogID, "subject": s.Subject, "conversation": s.Conversation, "scope": row.Scope, "sources": bson.M{"$elemMatch": bson.M{"id": row.ID, "key": key, "labelDigest": labelDigest, "published": true}}}).Err()
				if errors.Is(err, mongo.ErrNoDocuments) {
					err = agent.ErrTaskConflict
				}
			}
			return row, created, err
		}
		if len(catalog.Sources) >= 256 {
			return agent.TaskEventSource{}, false, agent.ErrTaskEventCapacity
		}
		catalog.StorageVersion = agent.TaskStorageVersion
		revision := catalog.Revision
		catalog.Revision++
		catalog.Sources = append(catalog.Sources, EventReservation{ID: row.ID, Key: key, LabelDigest: labelDigest, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt})
		if row.ExpiresAt.After(catalog.ExpiresAt) {
			catalog.ExpiresAt = row.ExpiresAt
		}
		ok := false
		if revision == 0 {
			_, err = s.Catalogs.InsertOne(ctx, catalog)
			if mongo.IsDuplicateKeyError(err) {
				err = nil
			} else {
				ok = err == nil
			}
		} else {
			var result *mongo.UpdateResult
			result, err = s.Catalogs.ReplaceOne(ctx, compatibleFilter(bson.M{"_id": catalogID, "revision": revision, "subject": s.Subject, "conversation": s.Conversation, "scope": row.Scope}), catalog)
			ok = err == nil && result.MatchedCount == 1
		}
		if err != nil {
			return agent.TaskEventSource{}, false, err
		}
		if ok {
			continue
		}
		select {
		case <-ctx.Done():
			return agent.TaskEventSource{}, false, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

// GetEventSource同时核对可信租户、会话、作用域和源期限。
func (s *EventStore) GetEventSource(ctx context.Context, Scope, id string) (agent.TaskEventSource, error) {
	var doc EventDocument
	Filter := s.Filter(Scope, id)
	Filter["source.expiresAt"] = bson.M{"$gt": time.Now()}
	err := decodeVersioned(s.Sources.FindOne(ctx, Filter), &doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return doc.Source, err
}

// ListEventSources只返回元数据，不将宿主key或事件正文放入模型发现结果。
func (s *EventStore) ListEventSources(ctx context.Context, Scope, after string, limit int) ([]agent.TaskEventSource, error) {
	if limit < 1 || limit > 128 {
		return nil, agent.ErrTaskEventCursor
	}
	Filter := bson.M{"subject": s.Subject, "conversation": s.Conversation, "source.scope": Scope, "source.expiresAt": bson.M{"$gt": time.Now()}, "source._id": bson.M{"$gt": after}}
	cursor, err := s.Sources.Find(ctx, Filter, options.Find().SetSort(bson.D{{Key: "source._id", Value: 1}}).SetLimit(int64(limit)).SetProjection(bson.M{"source.records": 0}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	out := []agent.TaskEventSource{}
	for cursor.Next(ctx) {
		var doc EventDocument
		if err = decodeVersioned(cursor, &doc); err != nil {
			return nil, err
		}
		out = append(out, doc.Source)
	}
	return out, cursor.Err()
}

// ReplaceEventSource拒绝过期版本，事件序号与去重指纹在同一持久替换中生效。
func (s *EventStore) ReplaceEventSource(ctx context.Context, row agent.TaskEventSource, revision int64) (bool, error) {
	if row.Revision != revision+1 {
		return false, agent.ErrTaskConflict
	}

	// 先延长目录预留，再更新源；崩溃至多多保留配额，不能产生同键第二个源。
	if _, err := s.Catalogs.UpdateOne(ctx, compatibleFilter(bson.M{"_id": s.SourceID(row.Scope, "event-source-catalog"), "sources.id": row.ID}), bson.M{"$max": bson.M{"expiresAt": row.ExpiresAt, "sources.$.expiresAt": row.ExpiresAt}}); err != nil {
		return false, err
	}

	Filter := s.Filter(row.Scope, row.ID)
	Filter["source.revision"] = revision
	Filter["source.key"] = row.Key
	Filter["source.label"] = row.Label
	Filter["source.capacity"] = row.Capacity
	Filter["source.createdAt"] = row.CreatedAt
	Filter["source.expiresAt"] = bson.M{"$gt": time.Now()}
	result, err := s.Sources.ReplaceOne(ctx, compatibleFilter(Filter), EventDocument{StorageVersion: agent.TaskStorageVersion, ID: s.SourceID(row.Scope, row.ID), Subject: s.Subject, Conversation: s.Conversation, Source: row})
	return err == nil && result.MatchedCount == 1, err
}

// CollectEvents回收正文但保留序号与去重，源到期后才删除；读取正确性不依赖TTL执行时机。
func (s *EventStore) CollectEvents(ctx context.Context, now time.Time) error {
	Filter := bson.M{"subject": s.Subject, "conversation": s.Conversation, "source.expiresAt": bson.M{"$lte": now}}
	if _, err := s.Sources.DeleteMany(ctx, compatibleFilter(Filter)); err != nil {
		return err
	}
	Filter = bson.M{"subject": s.Subject, "conversation": s.Conversation, "source.expiresAt": bson.M{"$gt": now}, "source.records": bson.M{"$elemMatch": bson.M{"bodyExpiresAt": bson.M{"$lte": now}, "availability": bson.M{"$ne": "expired"}}}}
	_, err := s.Sources.UpdateMany(ctx, compatibleFilter(Filter), bson.M{"$unset": bson.M{"source.records.$[expired].payload": ""}, "$set": bson.M{"source.records.$[expired].availability": "expired"}, "$inc": bson.M{"source.revision": 1}}, options.UpdateMany().SetArrayFilters([]any{bson.M{"expired.bodyExpiresAt": bson.M{"$lte": now}, "expired.availability": bson.M{"$ne": "expired"}}}))
	return err
}
