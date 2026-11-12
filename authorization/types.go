// Package authorization 提供有限目标、固定操作的持久授权，不负责业务编排或执行。
package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	MaxTargets     = 10000
	MaxPlanBytes   = 1 << 20
	MaxResultBytes = 64 << 10
	MaxRecords     = 128
	Pending        = "pending"
	Active         = "active"
	Revoked        = "revoked"
	Claimed        = "claimed"
	Completed      = "completed"
	Unknown        = "unknown"
)

var (
	ErrInvalid  = errors.New("authorization: invalid request")
	ErrNotFound = errors.New("authorization: not found")
	ErrConflict = errors.New("authorization: conflict")
	ErrDenied   = errors.New("authorization: denied")
	ErrNoMatch  = errors.New("authorization: no matching grant")
)

// Binding 的主体、会话和权限代次由宿主提供；Goal 保留批准时任务边界。
type Binding struct {
	Owner        string `json:"owner" bson:"owner"`
	Conversation string `json:"conversation" bson:"conversation"`
	Capability   string `json:"capability" bson:"capability"`
	Goal         string `json:"goal,omitempty" bson:"goal,omitempty"`
}

// Valid 检查可信绑定的必需字段与持久化长度边界。
func (b Binding) Valid() bool {
	return b.Owner != "" && b.Conversation != "" && b.Capability != "" && len(b.Owner) <= 512 && len(b.Conversation) <= 512 && len(b.Capability) <= 512 && len(b.Goal) <= 512
}

// SamePrincipal 仅供按明确授权 ID 查阅、批准及撤销；执行 claim 必须使用记录原 Goal。
func (b Binding) SamePrincipal(other Binding) bool {
	return b.Owner == other.Owner && b.Conversation == other.Conversation && b.Capability == other.Capability
}

// Plan 固定操作与全部非目标参数；有限目标集合不能在批准后追加。
type Plan struct {
	ExecutionEpoch  string          `json:"execution_epoch,omitempty" bson:"executionEpoch,omitempty"`
	Tool            string          `json:"tool" bson:"tool"`
	ContractVersion string          `json:"contract_version" bson:"contractVersion"`
	Arguments       json.RawMessage `json:"arguments" bson:"arguments"`
	TargetPointer   string          `json:"target_pointer" bson:"targetPointer"`
	Targets         []string        `json:"targets" bson:"targets"`
}

// Record 保存不可变授权内容、原始绑定、有效期和当前撤销状态。
type Record struct {
	ID        string    `json:"id" bson:"_id"`
	Binding   Binding   `json:"binding" bson:"binding"`
	Plan      Plan      `json:"plan" bson:"plan"`
	Digest    string    `json:"digest" bson:"digest"`
	State     string    `json:"state" bson:"state"`
	CreatedAt time.Time `json:"created_at" bson:"createdAt"`
	UpdatedAt time.Time `json:"updated_at" bson:"updatedAt"`
	ExpiresAt time.Time `json:"expires_at" bson:"expiresAt"`
}

// ClaimRequest 由 Manager 验证参数后生成，存储层需再次原子核对原记录。
type ClaimRequest struct {
	ID                      string
	Binding                 Binding
	Digest, Key, Invocation string
	Now                     time.Time
}

// Claim 是目标的一次执行凭据；Fresh 只对首次领取返回 true。
type Claim struct {
	GrantID    string `json:"grant_id" bson:"grantID"`
	Key        string `json:"key" bson:"key"`
	Invocation string `json:"invocation" bson:"invocation"`
	State      string `json:"state" bson:"state"`
	Result     string `json:"result,omitempty" bson:"result,omitempty"`
	Fresh      bool   `json:"fresh" bson:"-"`
}

// Outcome 保存这次调用的实际回执；unknown 不允许自动重新执行。
type Outcome struct {
	State  string
	Result string
}

// Store 的 Activate/Claim/Revoke 必须原子排序。Claim 一旦创建就永久禁止重复执行，
// Finish 失败不释放 claim；结果可分开存储，但必须绑定相同 Invocation。
type Store interface {
	Create(context.Context, Record) error
	Get(context.Context, Binding, string) (Record, error)
	List(context.Context, Binding) ([]Record, error)
	Activate(context.Context, Binding, string, string, time.Time) (Record, error)
	Claim(context.Context, ClaimRequest) (Claim, error)
	Finish(context.Context, Binding, Claim, Outcome, time.Time) error
	Revoke(context.Context, Binding, string, time.Time) error
}
