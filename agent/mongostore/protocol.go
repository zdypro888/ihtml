package mongostore

import (
	"context"
	"encoding/json"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// decodeVersioned 在解码业务结构前检查顶层存储版本，防止旧结构丢弃未知新字段后覆盖它们。
func decodeVersioned(reader interface{ Decode(any) error }, dst any) error {
	var raw bson.Raw
	if err := reader.Decode(&raw); err != nil {
		return err
	}
	value := raw.Lookup("storageVersion")
	if n, ok := value.AsInt64OK(); ok {
		if err := agent.TaskProtocolVersion(int(n)); err != nil {
			return err
		}
	} else if value.Type != 0 {
		return agent.ErrTaskStorageVersion
	}
	return bson.Unmarshal(raw, dst)
}
func compatibleFilter(f bson.M) bson.M {
	return versionFilter(f, "storageVersion")
}
func versionFilter(f bson.M, field string) bson.M {
	out := bson.M{}
	for k, v := range f {
		out[k] = v
	}
	// 缺失版本仅兼容迁移前格式；其他格式均不能被当前写入者修改。
	guard := bson.M{"$or": bson.A{bson.M{field: bson.M{"$exists": false}}, bson.M{field: bson.M{"$in": bson.A{0, agent.TaskStorageVersion}}}}}
	if prior, ok := out["$and"]; ok {
		out["$and"] = bson.A{bson.M{"$and": prior}, guard}
	} else {
		out["$and"] = bson.A{guard}
	}
	return out
}
func storeHealth(ctx context.Context, collections ...*mongo.Collection) error {
	for _, c := range collections {
		if c == nil {
			return agent.ErrTaskStorageConfiguration
		}
		if err := c.Database().RunCommand(ctx, bson.D{{Key: "ping", Value: 1}}).Err(); err != nil {
			return agent.ErrTaskStorageUnavailable
		}
		n, err := c.CountDocuments(ctx, bson.M{"storageVersion": bson.M{"$gt": agent.TaskStorageVersion}}, options.Count().SetLimit(1))
		if err != nil {
			return agent.ErrTaskStorageUnavailable
		}
		if n > 0 {
			return agent.ErrTaskStorageVersion
		}
	}
	return nil
}
func (s *TaskStore) TaskStoreHealth(ctx context.Context) error {
	return storeHealth(ctx, s.Tasks, s.Waits)
}
func (s *ResultStore) TaskStoreHealth(ctx context.Context) error {
	return storeHealth(ctx, s.Manifests, s.Chunks)
}
func (s *ContextStore) TaskStoreHealth(ctx context.Context) error {
	return storeHealth(ctx, s.Collection)
}
func (s *EventStore) TaskStoreHealth(ctx context.Context) error {
	return storeHealth(ctx, s.Sources, s.Catalogs)
}
func (s *ControlStore) TaskStoreHealth(ctx context.Context) error {
	return storeHealth(ctx, s.Collection)
}
func (s *ObservationStore) TaskStoreHealth(ctx context.Context) error {
	return storeHealth(ctx, s.Collection)
}
func (s *OutboxStore) TaskStoreHealth(ctx context.Context) error {
	if !s.valid() {
		return agent.ErrTaskStorageConfiguration
	}
	if err := storeHealth(ctx, s.Collection); err != nil {
		return err
	}
	n, err := s.Collection.CountDocuments(ctx, bson.M{s.field("storageVersion"): bson.M{"$gt": agent.TaskStorageVersion}}, options.Count().SetLimit(1))
	if err != nil {
		return agent.ErrTaskStorageUnavailable
	}
	if n > 0 {
		return agent.ErrTaskStorageVersion
	}
	return nil
}

// MigrateStorage 显式升级本库独占集合，需宿主先停止旧写入者；绝不在读取或构造时自动执行。
// strict=true 使用 Mongo validator 阻止缺少版本的旧二进制重新写入，不覆盖既有 validator。
func MigrateStorage(ctx context.Context, collections []*mongo.Collection, strict bool) error {
	for _, c := range collections {
		if c == nil {
			return agent.ErrTaskStorageConfiguration
		}
		n, err := c.CountDocuments(ctx, bson.M{"storageVersion": bson.M{"$gt": agent.TaskStorageVersion}}, options.Count().SetLimit(1))
		if err != nil {
			return err
		}
		if n > 0 {
			return agent.ErrTaskStorageVersion
		}
		if _, err = c.UpdateMany(ctx, bson.M{"$or": bson.A{bson.M{"storageVersion": bson.M{"$exists": false}}, bson.M{"storageVersion": 0}}}, bson.M{"$set": bson.M{"storageVersion": agent.TaskStorageVersion}}); err != nil {
			return err
		}
		if !strict {
			continue
		}
		cursor, err := c.Database().ListCollections(ctx, bson.M{"name": c.Name()})
		if err != nil {
			return err
		}
		var metadata struct {
			Options struct {
				Validator bson.M `bson:"validator"`
			} `bson:"options"`
		}
		if cursor.Next(ctx) {
			err = cursor.Decode(&metadata)
		}
		if err == nil {
			err = cursor.Err()
		}
		cursor.Close(ctx)
		if err != nil {
			return err
		}
		guard := bson.M{"$jsonSchema": bson.M{"bsonType": "object", "required": bson.A{"storageVersion"}, "properties": bson.M{"storageVersion": bson.M{"enum": bson.A{agent.TaskStorageVersion}}}}}
		validator := guard
		if len(metadata.Options.Validator) > 0 && !hasValidatorGuard(metadata.Options.Validator, guard) {
			validator = bson.M{"$and": bson.A{metadata.Options.Validator, guard}}
		} else if len(metadata.Options.Validator) > 0 {
			validator = metadata.Options.Validator
		}
		err = c.Database().RunCommand(ctx, bson.D{{Key: "collMod", Value: c.Name()}, {Key: "validator", Value: validator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// hasValidatorGuard 只沿合取检查既有约束，保证重复迁移不无限嵌套，也不把析取中的版本检查当成强制约束。
func hasValidatorGuard(validator, guard bson.M) bool {
	normalize := func(v bson.M) any {
		raw, _ := bson.MarshalExtJSON(v, true, false)
		var out any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	want := encode(normalize(guard))
	var contains func(any) bool
	contains = func(v any) bool {
		if encode(v) == want {
			return true
		}
		if m, ok := v.(map[string]any); ok {
			if rows, ok := m["$and"].([]any); ok {
				for _, row := range rows {
					if contains(row) {
						return true
					}
				}
			}
		}
		return false
	}
	return contains(normalize(validator))
}
