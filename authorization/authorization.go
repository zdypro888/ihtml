package authorization

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
)

// Manager 只验证授权边界和调用去重；业务权限检查、执行与任务调度由现有宿主完成。
type Manager struct{ store Store }

// New 绑定由宿主控制的持久存储，模型不能直接写入授权资料。
func New(store Store) *Manager { return &Manager{store: store} }
func hash(data []byte) string  { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }

// TargetKey 为目标生成稳定索引，不把原始目标用作数据库字段路径。
func TargetKey(target string) string { return hash([]byte(target)) }

// InvocationKey 把业务幂等标识固定到授权版本与目标，重连及新轮次保持不变。
func InvocationKey(record Record, key string) string {
	return hash([]byte(record.ID + "\x00" + record.Digest + "\x00" + key))
}

// NormalizePlan 冻结单个字符串叶子以外的全部参数；不能把数组、对象或任意谓词变成授权范围。
func NormalizePlan(in Plan) (Plan, error) {
	if strings.TrimSpace(in.Tool) == "" || len(in.Tool) > 256 || in.ContractVersion == "" || len(in.ContractVersion) > 512 || len(in.Arguments) > 64<<10 || len(in.Targets) == 0 || len(in.Targets) > MaxTargets {
		return Plan{}, ErrInvalid
	}
	tree, err := decode(in.Arguments)
	if err != nil {
		return Plan{}, err
	}
	if _, ok := tree.(map[string]any); !ok {
		return Plan{}, ErrInvalid
	}
	parts, err := pointer(in.TargetPointer)
	if err != nil {
		return Plan{}, err
	}
	if _, err = leaf(tree, parts, nil); err != nil {
		return Plan{}, err
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		return Plan{}, ErrInvalid
	}
	in.Arguments = raw
	seen := map[string]bool{}
	targets := make([]string, 0, len(in.Targets))
	for _, target := range in.Targets {
		if target == "" || len(target) > 4096 || seen[target] {
			return Plan{}, ErrInvalid
		}
		seen[target] = true
		targets = append(targets, target)
	}
	in.Targets = targets
	full, err := json.Marshal(in)
	if err != nil || len(full) > MaxPlanBytes {
		return Plan{}, ErrInvalid
	}
	return in, nil
}

// BuildArguments 仅将批准模板的目标叶子替换为清单内目标；其他字段保持原值。
func BuildArguments(plan Plan, target string) (json.RawMessage, error) {
	normalized, err := NormalizePlan(plan)
	if err != nil {
		return nil, err
	}
	found := false
	for _, v := range normalized.Targets {
		if v == target {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrDenied
	}
	tree, err := decode(normalized.Arguments)
	if err != nil {
		return nil, err
	}
	parts, _ := pointer(normalized.TargetPointer)
	if _, err = leaf(tree, parts, &target); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		return nil, ErrInvalid
	}
	return raw, nil
}
func recordDigest(r Record) string {
	raw, _ := json.Marshal(struct {
		ID        string
		Binding   Binding
		Plan      Plan
		ExpiresAt time.Time
	}{r.ID, r.Binding, r.Plan, r.ExpiresAt.UTC()})
	return hash(raw)
}

// ValidateRecord 可供持久适配器防止写入不完整、超界或被改写的授权记录。
func ValidateRecord(r Record) error {
	if r.ID == "" || len(r.ID) > 128 || !r.Binding.Valid() || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || !r.ExpiresAt.After(r.CreatedAt) || (r.State != Pending && r.State != Active && r.State != Revoked) {
		return ErrInvalid
	}
	p, err := NormalizePlan(r.Plan)
	if err != nil {
		return err
	}
	before, _ := json.Marshal(r.Plan)
	after, _ := json.Marshal(p)
	if !bytes.Equal(before, after) || r.Digest != recordDigest(r) {
		return ErrInvalid
	}
	return nil
}

// Prepare 只持久保存待确认范围，不逐项调用业务工具，也不授予执行权限。
func (m *Manager) Prepare(ctx context.Context, binding Binding, plan Plan, expires time.Time) (Record, error) {
	if m == nil || m.store == nil || ctx == nil || !binding.Valid() {
		return Record{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	p, err := NormalizePlan(plan)
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	expires = expires.UTC().Truncate(time.Millisecond)
	if !expires.After(now) {
		return Record{}, ErrInvalid
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return Record{}, err
	}
	r := Record{ID: hex.EncodeToString(id[:]), Binding: binding, Plan: p, State: Pending, CreatedAt: now, UpdatedAt: now, ExpiresAt: expires}
	r.Digest = recordDigest(r)
	if err = m.store.Create(ctx, r); err != nil {
		return Record{}, err
	}
	return r, nil
}

// Get 按显式授权 ID 读取同一主体和会话记录，恢复原 Goal 而不借用新轮次 Goal。
func (m *Manager) Get(ctx context.Context, binding Binding, id string) (Record, error) {
	if m == nil || m.store == nil || ctx == nil || !binding.Valid() {
		return Record{}, ErrInvalid
	}
	r, err := m.store.Get(ctx, binding, id)
	if err != nil {
		return Record{}, err
	}
	if !r.Binding.SamePrincipal(binding) || ValidateRecord(r) != nil {
		return Record{}, ErrDenied
	}
	return r, nil
}

// List 完整返回当前主体与会话的授权，停止时不能因展示分页漏掉待撤销记录。
func (m *Manager) List(ctx context.Context, binding Binding) ([]Record, error) {
	if m == nil || m.store == nil || ctx == nil || !binding.Valid() {
		return nil, ErrInvalid
	}
	rows, err := m.store.List(ctx, binding)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if !r.Binding.SamePrincipal(binding) || ValidateRecord(r) != nil {
			return nil, ErrDenied
		}
	}
	return rows, nil
}

// Activate 只能由已验签的用户确认路径调用，不暴露为模型可直接执行的工具。
func (m *Manager) Activate(ctx context.Context, binding Binding, id, digest string) (Record, error) {
	r, err := m.Get(ctx, binding, id)
	if err != nil {
		return Record{}, err
	}
	if r.Digest != digest {
		return Record{}, ErrDenied
	}
	return m.store.Activate(ctx, binding, id, digest, time.Now().UTC())
}

// Authorize 使用完整规范参数匹配模板并原子领取目标。Fresh=false 时只能读原回执，不能再次执行。
func (m *Manager) Authorize(ctx context.Context, binding Binding, id, tool, contract string, args json.RawMessage) (Claim, error) {
	r, err := m.Get(ctx, binding, id)
	if err != nil {
		return Claim{}, err
	}
	if r.Binding != binding || r.Plan.Tool != tool || r.Plan.ContractVersion != contract {
		return Claim{}, ErrDenied
	}
	tree, err := decode(args)
	if err != nil {
		return Claim{}, err
	}
	parts, _ := pointer(r.Plan.TargetPointer)
	target, err := leaf(tree, parts, nil)
	if err != nil {
		return Claim{}, err
	}
	expected, err := BuildArguments(r.Plan, target)
	if err != nil {
		return Claim{}, err
	}
	actual, err := json.Marshal(tree)
	if err != nil || !bytes.Equal(actual, expected) {
		return Claim{}, ErrDenied
	}
	key := TargetKey(target)
	return m.store.Claim(ctx, ClaimRequest{ID: id, Binding: binding, Digest: r.Digest, Key: key, Invocation: InvocationKey(r, key), Now: time.Now().UTC()})
}

// ValidOutcome 保证结果有明确语义且大小有界，大结果应使用原任务账本中的引用。
func ValidOutcome(outcome Outcome) bool {
	return (outcome.State == Completed || outcome.State == Unknown) && len(outcome.Result) <= MaxResultBytes
}

// ValidateClaimRequest 用于存储层同一原子边界内校验有效期、授权状态、目标及原始任务绑定。
func ValidateClaimRequest(r Record, q ClaimRequest) error {
	if ValidateRecord(r) != nil || r.ID != q.ID || r.Binding != q.Binding || r.Digest != q.Digest || q.Now.IsZero() || r.State != Active || !r.ExpiresAt.After(q.Now) || q.Invocation != InvocationKey(r, q.Key) {
		return ErrDenied
	}
	for _, target := range r.Plan.Targets {
		if TargetKey(target) == q.Key {
			return nil
		}
	}
	return ErrDenied
}

// Finish 持久保存已经领取的调用回执；保存失败保持占用状态，不恢复重复执行资格。
func (m *Manager) Finish(ctx context.Context, binding Binding, claim Claim, outcome Outcome) error {
	if m == nil || m.store == nil || ctx == nil || !binding.Valid() || !ValidOutcome(outcome) {
		return ErrInvalid
	}
	return m.store.Finish(ctx, binding, claim, outcome, time.Now().UTC())
}

// Revoke 阻止之后的新目标领取，不伪造已接纳调用的取消或执行结果。
func (m *Manager) Revoke(ctx context.Context, binding Binding, id string) error {
	if m == nil || m.store == nil || ctx == nil || !binding.Valid() {
		return ErrInvalid
	}
	return m.store.Revoke(ctx, binding, id, time.Now().UTC())
}

func pointer(value string) ([]string, error) {
	if value == "" || len(value) > 1024 || value[0] != '/' {
		return nil, ErrInvalid
	}
	parts := strings.Split(value[1:], "/")
	if len(parts) > 32 {
		return nil, ErrInvalid
	}
	for i, p := range parts {
		var b strings.Builder
		for j := 0; j < len(p); j++ {
			if p[j] != '~' {
				b.WriteByte(p[j])
				continue
			}
			j++
			if j == len(p) || (p[j] != '0' && p[j] != '1') {
				return nil, ErrInvalid
			}
			if p[j] == '0' {
				b.WriteByte('~')
			} else {
				b.WriteByte('/')
			}
		}
		parts[i] = b.String()
	}
	return parts, nil
}
func leaf(tree any, parts []string, replacement *string) (string, error) {
	node := tree
	for i, key := range parts {
		last := i == len(parts)-1
		var child any
		switch current := node.(type) {
		case map[string]any:
			var ok bool
			child, ok = current[key]
			if !ok {
				return "", ErrInvalid
			}
			if last {
				v, ok := child.(string)
				if !ok {
					return "", ErrInvalid
				}
				if replacement != nil {
					current[key] = *replacement
				}
				return v, nil
			}
		case []any:
			n, err := strconv.Atoi(key)
			if err != nil || n < 0 || n >= len(current) || strconv.Itoa(n) != key {
				return "", ErrInvalid
			}
			child = current[n]
			if last {
				v, ok := child.(string)
				if !ok {
					return "", ErrInvalid
				}
				if replacement != nil {
					current[n] = *replacement
				}
				return v, nil
			}
		default:
			return "", ErrInvalid
		}
		node = child
	}
	return "", ErrInvalid
}

// decode 拒绝重复键及超深 JSON，数字按原始精度保存，避免规范化改变实际参数。
func decode(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > MaxPlanBytes {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := decodeValue(d, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return v, nil
}
func decodeValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalid
	}
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			s, ok := key.(string)
			if !ok {
				return nil, ErrInvalid
			}
			if _, exists := out[s]; exists {
				return nil, ErrInvalid
			}
			v, err := decodeValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[s] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrInvalid
		}
		return out, nil
	case '[':
		out := []any{}
		for d.More() {
			v, err := decodeValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrInvalid
		}
		return out, nil
	}
	return nil, ErrInvalid
}
