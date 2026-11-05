package mongostore

import (
	"context"
	"time"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ObservationDocument struct {
	StorageVersion        int    `bson:"storageVersion,omitempty"`
	ID                    string `bson:"_id"`
	Subject               string `bson:"subject"`
	Conversation          string `bson:"conversation"`
	agent.TaskObservation `bson:",inline"`
}

// ObservationStore 只接收脱敏关联元数据，固定宿主身份；不保存参数、结果、事件正文或检查点。
type ObservationStore struct {
	Collection            *mongo.Collection
	Subject, Conversation string
}

func (s *ObservationStore) Filter(Scope string) bson.M {
	return bson.M{"subject": s.Subject, "conversation": s.Conversation, "scope": Scope, "expiresAt": bson.M{"$gt": time.Now()}}
}

// AppendObservation 由稳定观测ID去重；重复投递不能累计成第二次业务执行。
func (s *ObservationStore) AppendObservation(ctx context.Context, event agent.TaskObservation) (bool, error) {
	if err := agent.ValidateTaskObservation(event); err != nil {
		return false, err
	}
	id := digest(encode([]string{s.Subject, s.Conversation, event.Scope, event.ID}))
	_, err := s.Collection.InsertOne(ctx, ObservationDocument{StorageVersion: agent.TaskStorageVersion, ID: id, Subject: s.Subject, Conversation: s.Conversation, TaskObservation: event})
	if mongo.IsDuplicateKeyError(err) {
		var previous ObservationDocument
		if e := decodeVersioned(s.Collection.FindOne(ctx, bson.M{"_id": id, "subject": s.Subject, "conversation": s.Conversation, "scope": event.Scope}), &previous); e != nil {
			return false, e
		}
		if agent.TaskObservationIdentity(previous.TaskObservation) != agent.TaskObservationIdentity(event) {
			return false, agent.ErrTaskConflict
		}
		return false, nil
	}
	return err == nil, err
}

// ListObservations 的游标属于当前查询快照，筛选只接受白名单字段，不接收任意Mongo表达式。
func (s *ObservationStore) ListObservations(ctx context.Context, Scope string, q agent.TaskObservationQuery) ([]agent.TaskObservation, error) {
	if Scope == "" || q.Limit < 1 || q.Limit > 1024 {
		return nil, agent.ErrTaskConflict
	}
	Filter := s.Filter(Scope)
	if q.After != "" {
		Filter["id"] = bson.M{"$gt": q.After}
	}
	for key, value := range map[string]string{"taskID": q.TaskID, "waitID": q.WaitID, "traceID": q.TraceID, "kind": q.Kind, "contextID": q.ContextID, "sourceID": q.SourceID, "subscriptionID": q.SubscriptionID, "eventID": q.EventID} {
		if value != "" {
			if len(value) > 256 {
				return nil, agent.ErrTaskConflict
			}
			Filter[key] = value
		}
	}
	period := bson.M{}
	if !q.From.IsZero() {
		period["$gte"] = q.From
	}
	if !q.To.IsZero() {
		period["$lt"] = q.To
	}
	if len(period) > 0 {
		Filter["at"] = period
	}
	cursor, err := s.Collection.Find(ctx, Filter, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}).SetLimit(int64(q.Limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	records := []agent.TaskObservation{}
	for cursor.Next(ctx) {
		var doc ObservationDocument
		if err = decodeVersioned(cursor, &doc); err != nil {
			return nil, err
		}
		records = append(records, doc.TaskObservation)
	}
	return records, cursor.Err()
}

// CollectObservations 只回收当前宿主命名空间；TTL另外兜底，不依赖准时执行。
func (s *ObservationStore) CollectObservations(ctx context.Context, now time.Time) error {
	_, err := s.Collection.DeleteMany(ctx, compatibleFilter(bson.M{"subject": s.Subject, "conversation": s.Conversation, "expiresAt": bson.M{"$lte": now}}))
	return err
}
