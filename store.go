package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
)

// ErrNotFound 表示请求的对象(KV 键/快照)不存在。
var ErrNotFound = errors.New("ihtml: not found")

// Store 是持久化接口。实现只需要保证单个方法的原子性;
// "读-改-写"的并发合并由上层(Handler)按用户加锁串行化。
//
// user 是 UserResolver 解析出的用户标识, 实现应按它隔离数据。
type Store interface {
	// Items 返回该用户的全部 Item(顺序不限, 上层会按 Order 排序)。
	// 用户不存在时返回空切片而不是错误。
	Items(ctx context.Context, user string) ([]Item, error)
	// ReplaceItems 用 items 整体替换该用户的 Item 全集。
	ReplaceItems(ctx context.Context, user string, items []Item) error

	// KVGet 读取用户级键值, 键不存在时返回 ErrNotFound。
	KVGet(ctx context.Context, user, key string) (json.RawMessage, error)
	KVSet(ctx context.Context, user, key string, value json.RawMessage) error
	// KVDelete 删除键, 键不存在时不报错。
	KVDelete(ctx context.Context, user, key string) error

	// KVKeys 返回该用户的全部 KV 键(含保留键), 顺序不限。
	KVKeys(ctx context.Context, user string) ([]string, error)

	// Revisions 返回该用户的快照元信息, 新的在前。
	Revisions(ctx context.Context, user string) ([]RevisionInfo, error)
	// RevisionItems 返回指定快照的 Item 全集, 不存在时返回 ErrNotFound。
	RevisionItems(ctx context.Context, user, id string) ([]Item, error)
	// AddRevision 追加一条快照。keep 是保留上限, 实现应删除最旧的超额快照。
	AddRevision(ctx context.Context, user string, rev Revision, keep int) error

	// EraseUser 删除该用户的全部数据(Item/KV/快照), 用于注销与导入前清场。
	EraseUser(ctx context.Context, user string) error
}

func cloneItems(items []Item) []Item {
	out := make([]Item, len(items))
	copy(out, items)
	for i := range out {
		if len(items[i].Meta) > 0 {
			meta := make(map[string]string, len(items[i].Meta))
			maps.Copy(meta, items[i].Meta)
			out[i].Meta = meta
		}
	}
	return out
}
