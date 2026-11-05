package mongostore

import (
	"context"
	"errors"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ControlStore 只实现通用控制文档的 CAS；租约、预算和停止代次由共享运行时解释。
// 使用全局根数据库，使不同会话、租户及实例共享同一预算，不套用每会话账本过滤器。
type ControlStore struct {
	Collection *mongo.Collection
}

func (s *ControlStore) LoadControl(ctx context.Context, key string) (agent.TaskControlState, error) {
	if key == "" {
		return agent.TaskControlState{}, agent.ErrTaskNotFound
	}
	var state agent.TaskControlState
	err := decodeVersioned(s.Collection.FindOne(ctx, bson.M{"_id": key}), &state)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return state, err
}

func (s *ControlStore) CompareControl(ctx context.Context, state agent.TaskControlState, expected int64) (bool, error) {
	if err := agent.TaskProtocolVersion(state.StorageVersion); err != nil {
		return false, err
	}
	state.StorageVersion = agent.TaskStorageVersion
	if state.Key == "" || expected < 0 || state.Revision != expected+1 {
		return false, agent.ErrTaskConflict
	}
	if expected == 0 {
		_, err := s.Collection.InsertOne(ctx, state)
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return err == nil, err
	}
	result, err := s.Collection.ReplaceOne(ctx, compatibleFilter(bson.M{"_id": state.Key, "revision": expected}), state)
	return err == nil && result.MatchedCount == 1, err
}
