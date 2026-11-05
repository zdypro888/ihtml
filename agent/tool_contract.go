package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/go-openapi/jsonpointer"
	"strings"
)

// ToolContract 仅声明能力，不替 AI 选择并行、等待、重试或业务步骤。
// 零值表示未承诺；声明幂等不等于运行时获得自动重放许可。
type ToolContract struct {
	Version          string         `json:"version,omitempty"`
	IdempotencyScope string         `json:"idempotency_scope,omitempty"`
	Cancellation     string         `json:"cancellation,omitempty"`
	OutputSchema     map[string]any `json:"output_schema,omitempty"`
	ResultInputs     bool           `json:"result_inputs,omitempty"`
	ResourceFencing  bool           `json:"resource_fencing,omitempty"`
}

// TaskResultInput 固定不可变结果及可选 RFC6901 JSON Pointer，不接受其他作用域或文件路径。
type TaskResultInput struct {
	ID      string `json:"id"`
	SHA256  string `json:"sha256"`
	Pointer string `json:"pointer,omitempty"`
}

type taskResultReaderKey struct{}

// TaskResultInputReader 由当前可信运行时提供，不能用模型参数替换权限边界。
type TaskResultInputReader func(context.Context, TaskResultInput) ([]byte, error)

// ReadTaskResultInput 供显式支持引用的 Handler 使用；引用原样参加原权限/确认/幂等校验。
// 不把大正文展开到工具参数，也不绕过原 Handler 的目标权限检查。
func ReadTaskResultInput(ctx context.Context, ref TaskResultInput) ([]byte, error) {
	reader, ok := ctx.Value(taskResultReaderKey{}).(TaskResultInputReader)
	if !ok || reader == nil {
		return nil, ErrTaskNotFound
	}
	return reader(ctx, ref)
}

// ResultInputContext 绑定本轮仍可访问的源工具集合；调用者必须来自可信宿主。
func (r *TaskRuntime) ResultInputContext(ctx context.Context, scope string, tools []Tool) context.Context {
	allowed := make(map[string]bool, len(tools))
	for _, tool := range tools {
		allowed[tool.Name] = true
	}
	return context.WithValue(ctx, taskResultReaderKey{}, TaskResultInputReader(func(ctx context.Context, ref TaskResultInput) ([]byte, error) {
		return r.ResolveResultInput(ctx, scope, ref, allowed)
	}))
}

// ResolveResultInput 先核对当前源权限与摘要，再复用受控分段读取，不重新执行源工具。
func (r *TaskRuntime) ResolveResultInput(ctx context.Context, scope string, ref TaskResultInput, allowed map[string]bool) ([]byte, error) {
	if len(ref.SHA256) != 64 || len(ref.Pointer) > 2048 {
		return nil, ErrTaskConflict
	}
	body := make([]byte, 0)
	for offset := int64(0); ; {
		page, err := r.ReadResult(ctx, scope, ref.ID, offset, 64<<10, allowed)
		if err != nil {
			return nil, err
		}
		if page.Result.SHA256 != ref.SHA256 || page.Result.Encoding != "utf-8" {
			return nil, ErrTaskConflict
		}
		body = append(body, []byte(page.Text)...)
		if len(body) > r.cfg.MaxStoredResultBytes {
			return nil, ErrTaskResultRange
		}
		if page.EOF {
			break
		}
		offset = page.NextOffset
	}
	if taskBodySHA(body) != ref.SHA256 {
		return nil, ErrTaskResultUnavailable
	}
	if ref.Pointer == "" {
		return body, nil
	}
	// JSON Pointer 仅选择已有数据，不执行表达式或模型代码。
	return selectJSONPointer(body, ref.Pointer)
}

var errResultPointer = errors.New("agent: invalid result pointer")

func selectJSONPointer(body []byte, pointer string) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, errResultPointer
	}
	return selectJSONValue(value, pointer)
}

func taskBodySHA(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

// selectJSONValue 复用 JSON Pointer 标准库，不自建路径解析规则。
func selectJSONValue(value any, pointer string) ([]byte, error) {
	p, err := jsonpointer.New(pointer)
	if err != nil {
		return nil, errResultPointer
	}
	selected, _, err := p.Get(value)
	if err != nil {
		return nil, errResultPointer
	}
	return json.Marshal(selected)
}

func taskToolDigest(tool Tool, args json.RawMessage) string {
	contract, _ := json.Marshal(tool.Contract)
	// 零契约保留既有账本指纹，升级后仍能查询原事实；显式新版本不能借用旧参数身份。
	if string(contract) == "{}" {
		return taskHash([]any{tool.Name, args})
	}
	return taskHash([]any{tool.Name, args, tool.Contract})
}

// TaskResultInputSchema 供支持引用的工具直接复用，不需要各接口重复定义句柄协议。
func TaskResultInputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "pointer": map[string]any{"type": "string", "maxLength": 2048}}, "required": []string{"id", "sha256"}, "additionalProperties": false}
}
