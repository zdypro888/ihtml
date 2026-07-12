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
// user 是 UserResolver 解析出的用户标识, 实现应按它隔离数据。所有返回的
// slice/map/RawMessage 必须是调用方可修改的值快照；实现不得在返回后继续修改
// 它们。所有传入的 slice/map/RawMessage 也必须在方法返回前复制或持久化，不能
// 保留并随后修改调用方内存。
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

// UserState 是 Store 中一个用户的完整持久化状态。它主要供需要跨 Items、KV、
// Revisions 原子更新的操作使用（例如导入、页面删除和带页面注册表的回滚）。
// 调用方和实现都必须把它当作值快照，不能保留或修改传入切片/RawMessage。
type UserState struct {
	Items     []Item
	KV        map[string]json.RawMessage
	Revisions []Revision // 新的在前
}

// UserStateStore 是 Store 的可选增强接口。内置 Store 均实现它；第三方旧 Store
// 无需立即修改，Service 会使用备份+补偿恢复，但不能提供与该接口同等的强原子性。
//
// ReplaceUserState 必须在单个原子操作中用 state 整体替换指定用户的数据。
type UserStateStore interface {
	UserState(ctx context.Context, user string) (UserState, error)
	ReplaceUserState(ctx context.Context, user string, state UserState) error
}

// RevisionStore 是 Store 的可选增强接口，用于读取完整快照（包括较新契约中的
// 页面注册表）。旧 Store 只有 RevisionItems 时仍可读取旧版仅含 Item 的快照。
type RevisionStore interface {
	Revision(ctx context.Context, user, id string) (Revision, error)
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

func cloneRevisions(revisions []Revision) []Revision {
	out := make([]Revision, len(revisions))
	copy(out, revisions)
	for i := range out {
		out[i].Items = cloneItems(revisions[i].Items)
		if revisions[i].Pages != nil {
			out[i].Pages = append([]Page{}, revisions[i].Pages...)
		}
		out[i].Templates = cloneTemplates(revisions[i].Templates)
	}
	return out
}

func cloneUserState(state UserState) UserState {
	out := UserState{
		Items:     cloneItems(state.Items),
		KV:        make(map[string]json.RawMessage, len(state.KV)),
		Revisions: cloneRevisions(state.Revisions),
	}
	for key, value := range state.KV {
		out.KV[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
