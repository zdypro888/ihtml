package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"golang.org/x/time/rate"
)

// ErrTaskPolicyConflict 表示宿主配置版本不一致，AI 不能通过重试或修改额度修复。
var ErrTaskPolicyConflict = errors.New("agent: resource policy conflict")

// TaskResourcePolicy 使用宿主规范化的资源键，令牌桶算法复用 x/time/rate。
// Version 必须随策略变更单调递增；旧实例不能以旧配置扩大新策略。
type TaskResourcePolicy struct {
	Key           string  `json:"key" yaml:"key"`
	RatePerSecond float64 `json:"rate_per_second" yaml:"rate_per_second"`
	Burst         int     `json:"burst" yaml:"burst"`
	Version       int64   `json:"version" yaml:"version"`
}

// TaskQoSConfig 只提供可信资源策略，具体业务编排交由 AI。
type TaskQoSConfig struct{ Policies []TaskResourcePolicy }

// TaskResourceRequest 是工具声明的协作资源；IfVersion 由实际提供者核验，运行时不伪造业务版本。
type TaskResourceRequest struct {
	Key       string `json:"key" bson:"key"`
	Shared    bool   `json:"shared" bson:"shared"`
	IfVersion string `json:"if_version,omitempty" bson:"ifVersion,omitempty"`
}

// TaskResourceGrant 将本次租约的条件版本与 fencing 交给实际提供者校验。
type TaskResourceGrant struct {
	TaskResourceRequest
	Fence int64 `json:"fence" bson:"fence"`
}
type taskResourceGrantsKey struct{}

// TaskResourceGrants 返回本次执行的租约与单调 fencing；真正的陈旧写入防护需要提供者校验。
func TaskResourceGrants(ctx context.Context) []TaskResourceGrant {
	v, _ := ctx.Value(taskResourceGrantsKey{}).([]TaskResourceGrant)
	return append([]TaskResourceGrant(nil), v...)
}

type taskCapacityPolicy struct {
	Version int64
	Limit   TaskBudgetLimit
}
type taskQoSState struct {
	Capacities map[string]taskCapacityPolicy `json:"capacities"`
	Policies   map[string]TaskResourcePolicy `json:"policies"`
	EmptyAt    map[string]time.Time          `json:"empty_at"`
	Cooldown   map[string]time.Time          `json:"cooldown"`
	Fence      int64                         `json:"fence"`
}

func readQoS(row *TaskControlState) (taskQoSState, error) {
	q := taskQoSState{Capacities: map[string]taskCapacityPolicy{}, Policies: map[string]TaskResourcePolicy{}, EmptyAt: map[string]time.Time{}, Cooldown: map[string]time.Time{}}
	if len(row.Payload) > 0 {
		if json.Unmarshal(row.Payload, &q) != nil {
			return q, ErrTaskConflict
		}
	}
	if q.Capacities == nil {
		q.Capacities = map[string]taskCapacityPolicy{}
	}
	if q.Policies == nil {
		q.Policies = map[string]TaskResourcePolicy{}
	}
	if q.EmptyAt == nil {
		q.EmptyAt = map[string]time.Time{}
	}
	if q.Cooldown == nil {
		q.Cooldown = map[string]time.Time{}
	}
	return q, nil
}
func (r *TaskRuntime) checkQoS(row *TaskControlState, lease *TaskBudgetLease) error {
	q, err := readQoS(row)
	if err != nil {
		return err
	}
	if len(q.Policies) > 4096 || len(q.Cooldown) > 4096 {
		return &TaskBudgetError{Kind: "policy_capacity"}
	}
	now := time.Now().UTC()
	for _, limit := range r.cfg.Budgets {
		if p, ok := q.Capacities[limit.Key]; !ok || p.Version != r.cfg.BudgetPolicyVersion || p.Limit != limit {
			return ErrTaskPolicyConflict
		}
	}
	for _, p := range r.cfg.QoS.Policies {
		if old, ok := q.Policies[p.Key]; !ok || old != p {
			return ErrTaskPolicyConflict
		}
	}
	// FIFO 只比较相交资源且先行项当前可执行，避免一个无关瓶颈阻塞整个池。
	for _, prior := range row.Leases {
		if prior.ID == lease.ID {
			break
		}
		if prior.Running || prior.Kind == "wait" {
			continue
		}
		intersects := false
		for _, key := range prior.Resources {
			if hasResource(*lease, key) {
				intersects = true
			}
		}
		for _, a := range prior.Claims {
			for _, b := range lease.Claims {
				if a.Key == b.Key && (!a.Shared || !b.Shared) {
					intersects = true
				}
			}
		}
		if !intersects {
			continue
		}
		eligible := true
		for _, key := range prior.Resources {
			if p, ok := q.Capacities[key]; ok {
				_, running, _ := budgetCounts(*row, key)
				if running >= p.Limit.MaxConcurrent {
					eligible = false
				}
			}
		}
		for _, key := range prior.Resources {
			if q.Cooldown[key].After(now) {
				eligible = false
			}
			if p, ok := q.Policies[key]; ok {
				limiter := rate.NewLimiter(rate.Limit(p.RatePerSecond), p.Burst)
				limiter.ReserveN(q.EmptyAt[key], p.Burst)
				if !limiter.AllowN(now, 1) {
					eligible = false
				}
			}
		}
		for _, active := range row.Leases {
			if !active.Running {
				continue
			}
			if claimsConflict(prior.Claims, active.Claims) {
				eligible = false
				break
			}
		}
		if eligible {
			return &TaskBudgetError{Kind: "resource_queue", RetryAfterMS: r.cfg.PollInterval.Milliseconds()}
		}
	}
	for _, active := range row.Leases {
		if active.ID != lease.ID && active.Running && claimsConflict(lease.Claims, active.Claims) {
			return &TaskBudgetError{Kind: "resource_busy", RetryAfterMS: r.cfg.PollInterval.Milliseconds()}
		}
	}
	// 先检验全部资源，令牌只随整个共享文档 CAS 一次提交。
	for _, key := range lease.Resources {
		if until := q.Cooldown[key]; until.After(now) {
			return &TaskBudgetError{Resource: key, Kind: "cooldown", RetryAfterMS: max(1, time.Until(until).Milliseconds())}
		}
		policy, ok := q.Policies[key]
		if !ok {
			continue
		}
		limiter := rate.NewLimiter(rate.Limit(policy.RatePerSecond), policy.Burst)
		empty := q.EmptyAt[key]
		if empty.IsZero() {
			empty = now.Add(-time.Duration(float64(policy.Burst) / policy.RatePerSecond * float64(time.Second)))
		}
		limiter.ReserveN(empty, policy.Burst)
		if !limiter.AllowN(now, 1) {
			return &TaskBudgetError{Resource: key, Kind: "rate", RetryAfterMS: max(1, int64(1000/policy.RatePerSecond))}
		}
		tokens := limiter.TokensAt(now)
		q.EmptyAt[key] = now.Add(-time.Duration(tokens / policy.RatePerSecond * float64(time.Second)))
	}
	if len(lease.Claims) > 0 {
		q.Fence++
		if q.Fence <= 0 {
			return ErrTaskConflict
		}
		for _, claim := range lease.Claims {
			lease.Grants = append(lease.Grants, TaskResourceGrant{TaskResourceRequest: claim, Fence: q.Fence})
		}
	}
	row.Payload, _ = json.Marshal(q)
	return nil
}
func claimsConflict(a, b []TaskResourceRequest) bool {
	for _, x := range a {
		for _, y := range b {
			if x.Key == y.Key && (!x.Shared || !y.Shared) {
				return true
			}
		}
	}
	return false
}

// CooldownResource 接收可信提供者的 Retry-After，不能由模型创建任意资源键。
func (r *TaskRuntime) CooldownResource(ctx context.Context, key string, until time.Time) error {
	known := false
	for _, p := range r.cfg.QoS.Policies {
		if p.Key == key {
			known = true
		}
	}
	if !known || until.After(time.Now().Add(24*time.Hour)) {
		return ErrTaskConflict
	}
	_, err := r.editControl(ctx, r.poolControlKey(), func(row *TaskControlState) error {
		q, err := readQoS(row)
		if err != nil {
			return err
		}
		if until.After(q.Cooldown[key]) {
			q.Cooldown[key] = until
		}
		row.Payload, _ = json.Marshal(q)
		return nil
	})
	return err
}
func (r *TaskRuntime) resourceContext(ctx context.Context, id string) (context.Context, error) {
	row, err := r.cfg.ControlStore.LoadControl(ctx, r.poolControlKey())
	if err != nil {
		return nil, err
	}
	for _, lease := range row.Leases {
		if lease.ID == id && lease.Owner == r.owner && lease.Running && lease.ExpiresAt.After(time.Now()) {
			return context.WithValue(ctx, taskResourceGrantsKey{}, append([]TaskResourceGrant(nil), lease.Grants...)), nil
		}
	}
	return nil, ErrTaskConflict
}

func seenResource(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}
func (r *TaskRuntime) configureQoS(row *TaskControlState) error {
	q, err := readQoS(row)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, limit := range r.cfg.Budgets {
		p, ok := q.Capacities[limit.Key]
		if ok && (p.Version > r.cfg.BudgetPolicyVersion || p.Version == r.cfg.BudgetPolicyVersion && p.Limit != limit) {
			return ErrTaskPolicyConflict
		}
		q.Capacities[limit.Key] = taskCapacityPolicy{Version: r.cfg.BudgetPolicyVersion, Limit: limit}
	}
	if len(q.Capacities) > 4096 {
		return &TaskBudgetError{Kind: "policy_capacity"}
	}
	for _, policy := range r.cfg.QoS.Policies {
		if policy.Key == "" || policy.Version < 1 || policy.RatePerSecond <= 0 || policy.Burst < 1 || policy.Burst > 100000 {
			return ErrTaskConflict
		}
		old, exists := q.Policies[policy.Key]
		if exists && (old.Version > policy.Version || old.Version == policy.Version && !reflect.DeepEqual(old, policy)) {
			return ErrTaskPolicyConflict
		}
		if !exists || old.Version < policy.Version {
			// 策略更换从空桶开始，不能通过滚动发布反复获得新的 burst。
			q.Policies[policy.Key] = policy
			q.EmptyAt[policy.Key] = now
		}
	}
	if len(q.Policies) > 4096 {
		return &TaskBudgetError{Kind: "policy_capacity"}
	}
	row.Payload, _ = json.Marshal(q)
	return nil
}

// TaskSchedulingView 是本身份可见资源的有效策略快照，不暴露其他任务、账户或租约。
type TaskSchedulingView struct {
	Key             string    `json:"key"`
	CapacityVersion int64     `json:"capacity_version"`
	RateVersion     int64     `json:"rate_version"`
	RatePerSecond   float64   `json:"rate_per_second"`
	Burst           int       `json:"burst"`
	CooldownUntil   time.Time `json:"cooldown_until,omitempty"`
}

// SchedulingBudget 让 AI 根据实际限流和冷却事实选择等待或其他工作，不替它安排业务。
func (r *TaskRuntime) SchedulingBudget(ctx context.Context) ([]TaskSchedulingView, error) {
	row, err := r.cfg.ControlStore.LoadControl(ctx, r.poolControlKey())
	if err != nil && !errors.Is(err, ErrTaskNotFound) {
		return nil, WrapTaskStorage(err)
	}
	q, err := readQoS(&row)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for _, p := range r.cfg.Budgets {
		keys = append(keys, p.Key)
	}
	for _, p := range r.cfg.QoS.Policies {
		if !seenResource(keys, p.Key) {
			keys = append(keys, p.Key)
		}
	}
	out := make([]TaskSchedulingView, 0, len(keys))
	for _, key := range keys {
		p := q.Policies[key]
		out = append(out, TaskSchedulingView{Key: key, CapacityVersion: q.Capacities[key].Version, RateVersion: p.Version, RatePerSecond: p.RatePerSecond, Burst: p.Burst, CooldownUntil: q.Cooldown[key]})
	}
	return out, nil
}

// ValidateTaskQoSConfig 供宿主启动配置和运行时共用，避免等到模型请求才发现非法策略。
func ValidateTaskQoSConfig(c TaskQoSConfig) error {
	if len(c.Policies) > 8 {
		return ErrTaskPolicyConflict
	}
	seen := map[string]bool{}
	for _, p := range c.Policies {
		if p.Key == "" || len(p.Key) > 256 || seen[p.Key] || p.Version < 1 || math.IsNaN(p.RatePerSecond) || math.IsInf(p.RatePerSecond, 0) || p.RatePerSecond <= 0 || p.RatePerSecond > 100000 || float64(p.Burst)/p.RatePerSecond > 365*24*3600 || p.Burst < 1 || p.Burst > 100000 {
			return ErrTaskPolicyConflict
		}
		seen[p.Key] = true
	}
	return nil
}
