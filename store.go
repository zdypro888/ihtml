package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
)

// ErrNotFound 表示请求的对象(KV 键/快照)不存在。
var ErrNotFound = errors.New("ihtml: not found")

// ErrVersionConflict 由 VersionedUserStateStore.ReplaceUserStateVersioned 返回,
// 表示存储中的用户状态版本已不等于调用方读取时的版本(通常是另一个副本先写入)。
// 调用方应重读状态、重放变更后重试。
var ErrVersionConflict = errors.New("ihtml: user state version conflict")

// Store 是持久化接口。实现只需要保证单个方法的原子性;
// "读-改-写"的并发合并在单副本内由上层(Handler)按用户加锁串行化。
// 多副本部署要求 Store 同时实现 VersionedUserStateStore(内置 Store 均已实现),
// 上层会用它做跨进程的条件写与冲突重试。
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

	// KVGet 读取用户级键值, 键不存在时返回 ErrNotFound。KVSet/KVGet
	// 必须逐字节保留有效 JSON（包括无意义空白与 HTML 字符），不得规范化、
	// compact 或重新编码 RawMessage。
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

	// EraseUser 清空该用户的业务数据(Item/KV/快照), 用于注销与导入前清场。
	// VersionedUserStateStore 必须额外保留一个不可见的递增版本墓碑。
	EraseUser(ctx context.Context, user string) error
}

// UserState 是 Store 中一个用户的完整持久化状态。它主要供需要跨 Items、KV、
// Revisions 原子更新的操作使用（例如导入、页面删除和带页面注册表的回滚）。
// 调用方和实现都必须把它当作值快照，不能保留或修改传入切片/RawMessage。
type UserState struct {
	Items     []Item
	KV        map[string]json.RawMessage
	Revisions []Revision // 新的在前

	// Version 是整个用户状态的乐观并发令牌，由实现 VersionedUserStateStore 的
	// Store 在 UserState() 时填充；不支持版本的 Store 保持为 0。调用方应视为
	// 不透明值：读到什么，条件写时就传什么。
	Version uint64
}

// UserStateStore 是 Store 的可选增强接口。内置 Store 均实现它；第三方旧 Store
// 无需立即修改，Service 会使用备份+补偿恢复，但不能提供与该接口同等的强原子性。
//
// ReplaceUserState 必须在单个原子操作中用 state 整体替换指定用户的数据。
type UserStateStore interface {
	UserState(ctx context.Context, user string) (UserState, error)
	ReplaceUserState(ctx context.Context, user string, state UserState) error
}

// VersionedUserStateStore 是 UserStateStore 的可选增强接口，为多副本部署提供
// 跨进程的乐观并发控制。内置 Store 均实现它。
//
// 注意: 上层在两个替换方法中优先调用 ReplaceUserStateVersioned。若你通过嵌入
// 内置 Store 做包装(审计/加密等)，Go 的方法提升会把版本化方法一并暴露——
// 必须同时覆写 ReplaceUserState 与 ReplaceUserStateVersioned，否则包装逻辑会被绕过。
//
// 实现要求：
//   - 用户状态版本从 0 开始（0 仅表示该用户从未被任何写操作触及）；
//   - 每个成功的写方法（ReplaceItems/KVSet/KVDelete/AddRevision/ReplaceUserState
//     等）都必须单调推进该用户的版本，使并发的整状态条件写能察觉任何交错写入；
//   - EraseUser 必须清空 Items/KV/Revisions，但仍单调推进并持久化版本。
//     不得把版本重置为 0 或复用旧值，否则 purge 前的过期 CAS 可通过
//     ABA 覆盖 purge 后重建的新状态。
type VersionedUserStateStore interface {
	UserStateStore
	// ReplaceUserStateVersioned 仅当该用户当前版本等于 expected 时原子替换完整
	// 状态并推进版本；否则返回 ErrVersionConflict 且不产生任何修改。
	ReplaceUserStateVersioned(ctx context.Context, user string, state UserState, expected uint64) error
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
		if items[i].Meta != nil {
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
		Version:   state.Version,
	}
	for key, value := range state.KV {
		out.KV[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
