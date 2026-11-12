package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// TaskFailure 是可公开的失败事实；它不授予重试、业务执行或新的工具权限。
// cause 只在进程内供 errors.Is/As 核对，不能持久化或发送原错误文本。
type TaskFailure struct {
	Code           string         `json:"code" bson:"code"`
	ExecutionState string         `json:"execution_state" bson:"executionState"`
	Message        string         `json:"message" bson:"message"`
	Admitted       bool           `json:"admitted" bson:"admitted"`
	AdmissionState string         `json:"admission_state" bson:"admissionState"`
	ReceiptID      string         `json:"receipt_id,omitempty" bson:"receiptID,omitempty"`
	ResultRef      *TaskResultRef `json:"result_ref,omitempty" bson:"resultRef,omitempty"`
	RetryAfterMS   int64          `json:"retry_after_ms,omitempty" bson:"retryAfterMS,omitempty"`
	// Public 仅保存宿主显式公开的原因，独立于框架 Code 与执行事实。
	Public *PublicErrorDetails `json:"public,omitempty" bson:"public,omitempty"`
	cause  error
}

// Error 仅提供固定公开说明，禁止把底层凭据、地址或响应原文串入模型错误。
func (e *TaskFailure) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap 保留服务端错误判定能力；序列化不会包含这个原因。
func (e *TaskFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
func cloneTaskFailure(f *TaskFailure) *TaskFailure {
	if f == nil {
		return nil
	}
	out := *f
	out.cause = nil
	if f.Public != nil {
		public := *f.Public
		out.Public = &public
	}
	if f.ResultRef != nil {
		ref := *f.ResultRef
		out.ResultRef = &ref
	}
	return &out
}

// TaskFailureFrom 在已知执行阶段分类错误。未知工具错误不会被猜成未执行或可安全重试。
func TaskFailureFrom(err error, execution string, admitted bool, receipt string, result *TaskResultRef) *TaskFailure {
	if err == nil {
		return nil
	}
	var existing *TaskFailure
	if errors.As(err, &existing) && existing != nil {
		out := normalizedTaskFailure(existing)
		var public *publicError
		if errors.As(err, &public) && public != nil && public.details != nil {
			out.Public = normalizedPublicError(public.details)
		}
		if admitted {
			out.Admitted = true
			out.AdmissionState = "admitted"
			if execution == "unknown" || out.ExecutionState != "unknown" {
				out.ExecutionState = execution
			}
			if receipt != "" {
				out.ReceiptID = receipt
			}
			if result != nil {
				ref := *result
				out.ResultRef = &ref
			}
		} else {
			if out.ReceiptID == "" {
				out.ReceiptID = receipt
			}
			if out.ResultRef == nil && result != nil {
				ref := *result
				out.ResultRef = &ref
			}
		}
		out = normalizedTaskFailure(out)
		out.cause = err
		return out
	}
	f := &TaskFailure{Code: "operation_failed", ExecutionState: execution, Admitted: admitted, ReceiptID: receipt, Message: "操作未能完成；请核对当前状态和原回执。", cause: err}
	var public *publicError
	if errors.As(err, &public) && public != nil {
		f.Public = normalizedPublicError(public.details)
	}
	if admitted {
		f.AdmissionState = "admitted"
	} else {
		f.AdmissionState = "not_admitted"
	}
	if result != nil {
		ref := *result
		f.ResultRef = &ref
	}
	var budget *TaskBudgetError
	switch {
	case errors.Is(err, ErrTaskEventClosed):
		f.Code = "event_source_closed"
	case errors.Is(err, ErrTaskContract):
		f.Code = "contract_violation"
	case errors.Is(err, ErrTaskStorageVersion), errors.Is(err, ErrTaskStorageConfiguration):
		f.Code = "storage_protocol_error"
	case errors.Is(err, ErrTaskDraining):
		f.Code = "runtime_draining"
	case errors.Is(err, ErrTaskStorageUnavailable):
		f.Code = "storage_unavailable"
		f.Message = "持久存储当前无法确认，请按原记录核对；不自动重放业务。"
	case errors.As(err, &budget):
		f.Code = "budget_exhausted"
		if strings.HasPrefix(budget.Kind, "goal_") {
			f.Code = "goal_budget_exhausted"
		}
		if budget.Kind == "goal_quote_required" {
			f.Code = "model_quote_required"
		}
		f.RetryAfterMS = budget.RetryAfterMS
		f.Message = "当前共享资源不足，调用尚未受理。"
	case errors.Is(err, ErrTaskContextCapacity), errors.Is(err, ErrTaskEventCapacity):
		f.Code = "capacity_exhausted"
		f.Message = "当前持久资源容量不足，新的登记尚未受理。"
	case errors.Is(err, ErrTaskContextInvalid):
		f.Code = "invalid_context"
		f.Message = "长期任务上下文不符合通用保存协议。"
	case errors.Is(err, ErrTaskEventCursor):
		f.Code = "invalid_cursor"
		f.Message = "事件游标无效，请先读取当前授权源的游标事实。"
	case errors.Is(err, ErrTaskStopped):
		f.Code = "scope_stopped"
		f.Message = "原指令所属代次已停止；新的执行须来自新的明确请求或批准。"
	case errors.Is(err, ErrTaskWakeBudget):
		f.Code = "wake_budget_exhausted"
		f.Message = "当前目标自动恢复预算已用完，未完成事实仍保留。"
	case errors.Is(err, ErrTaskNotFound):
		f.Code = "record_unavailable"
		f.Message = "当前授权范围内无法取得该记录；这不证明原业务未执行。"
	case errors.Is(err, ErrTaskPolicyConflict):
		f.Code = "policy_conflict"
	case errors.Is(err, ErrTaskConflict):
		f.Code = "revision_conflict"
		f.Message = "记录已并发更新；请读取当前版本后再判断。"
	case errors.Is(err, ErrTaskResultExpired):
		f.Code = "result_expired"
		f.Message = "原结果正文已过保留期，执行事实仍须按原记录核对。"
	case errors.Is(err, ErrTaskResultRange):
		f.Code = "invalid_range"
		f.Message = "结果字节范围无效，请使用返回的游标和范围。"
	case errors.Is(err, ErrTaskResultUnavailable):
		f.Code = "result_unavailable"
		f.Message = "结果正文当前不可用，请核对元数据与原执行事实。"
	case errors.Is(err, context.DeadlineExceeded):
		f.Code = "deadline_exceeded"
		f.Message = "操作期限已到，请核对原回执，不据此重复提交业务。"
	case errors.Is(err, context.Canceled):
		f.Code = "cancelled"
		f.Message = "当前操作已停止，已发生的业务影响仍须核对。"
	default:
		if admitted && execution == "unknown" {
			f.Code = "execution_unknown"
			f.Message = "调用可能已执行但结果未确认；请核对原回执，不自动重放。"
		} else if admitted && execution == "handler_returned" {
			f.Code = "handler_failed"
			f.Message = "工具已返回失败，原业务状态请按回执判断。"
		}
	}
	f = normalizedTaskFailure(f)
	f.cause = err
	return f
}

// TaskFailureJSON 使用通用错误信封；not_admitted 只用于确知尚未接纳的情况。
func TaskFailureJSON(err error, execution string, admitted bool, receipt string, result *TaskResultRef) (string, bool) {
	if err == nil {
		return "", false
	}
	f := TaskFailureFrom(err, execution, admitted, receipt, result)
	status := "operation_failed"
	if f.AdmissionState == "not_admitted" && !f.Admitted && f.ExecutionState == "not_started" {
		status = "not_admitted"
	}
	raw, _ := json.Marshal(struct {
		Status string `json:"status"`
		*TaskFailure
	}{status, f})
	return string(raw), true
}
func failureForRecord(record TaskRecord) *TaskFailure {
	if record.Info.Failure != nil {
		return normalizedTaskFailure(record.Info.Failure)
	}
	if record.Info.Error == "" {
		return nil
	}
	state := "not_started"
	if record.Started {
		state = "handler_returned"
	}
	if record.Info.State == "unknown" {
		state = "unknown"
	}
	return TaskFailureFrom(errors.New("record failure"), state, true, record.ID, record.Info.ResultRef)
}

// normalizedTaskFailure 将持久/宿主结构约束为公开协议，不允许错误文本作为旁路泄露原始响应。
func normalizedTaskFailure(f *TaskFailure) *TaskFailure {
	out := cloneTaskFailure(f)
	if out == nil {
		return nil
	}
	messages := map[string]string{
		"event_source_closed":    "原事件源已关闭或归档；请读取源状态和后继元数据，不猜造新来源。",
		"contract_violation":     "工具契约校验失败；请核对执行状态与原回执，不据此重执行业务。",
		"storage_protocol_error": "持久组件装配或存储格式不兼容，运行时拒绝降级写入。",
		"runtime_draining":       "运行时正在排空退出，未接纳新的执行。",
		"goal_budget_exhausted":  "原目标累计额度或总期限已用完；已提交事实保留，不自动重执行业务。",
		"model_quote_required":   "宿主未提供可靠的模型费用/token上界，受硬额度约束的调用未开始。",
		"policy_conflict":        "共享资源策略版本不一致，需要宿主更新配置，不能由模型扩大额度。",
		"budget_exhausted":       "当前共享资源不足，调用尚未受理。",
		"capacity_exhausted":     "当前持久资源容量不足，新的登记尚未受理。",
		"invalid_context":        "长期任务上下文不符合通用保存协议。",
		"invalid_cursor":         "事件游标无效，请先读取当前授权源的游标事实。",
		"scope_stopped":          "原指令所属代次已停止；新的执行须来自新的明确请求或批准。",
		"wake_budget_exhausted":  "当前目标自动恢复预算已用完，未完成事实仍保留。",
		"record_unavailable":     "当前授权范围内无法取得该记录；这不证明原业务未执行。",
		"revision_conflict":      "记录已并发更新；请读取当前版本后再判断。",
		"result_expired":         "原结果正文已过保留期，执行事实仍须按原记录核对。",
		"invalid_range":          "结果字节范围无效，请使用返回的游标和范围。",
		"result_unavailable":     "结果正文当前不可用，请核对元数据与原执行事实。",
		"deadline_exceeded":      "操作期限已到，请核对原回执，不据此重复提交业务。",
		"cancelled":              "当前操作已停止，已发生的业务影响仍须核对。",
		"execution_unknown":      "调用可能已执行但结果未确认；请核对原回执，不自动重放。",
		"handler_failed":         "工具已返回失败，原业务状态请按回执判断。",
		"operation_failed":       "操作未能完成；请核对当前状态和原回执。",
		"storage_unavailable":    "持久存储当前无法确认，请按原记录核对；不自动重放业务。",
	}
	message, ok := messages[out.Code]
	if !ok {
		out.Code = "operation_failed"
		message = messages[out.Code]
	}
	out.Message = message
	if out.ExecutionState != "not_started" && out.ExecutionState != "running" && out.ExecutionState != "handler_returned" && out.ExecutionState != "unknown" {
		if out.Admitted {
			out.ExecutionState = "unknown"
		} else {
			out.ExecutionState = "not_started"
		}
	}
	if out.AdmissionState != "admitted" && out.AdmissionState != "not_admitted" && out.AdmissionState != "unknown" {
		if out.Admitted {
			out.AdmissionState = "admitted"
		} else {
			out.AdmissionState = "not_admitted"
		}
	}
	if out.Admitted && out.AdmissionState == "not_admitted" {
		out.AdmissionState = "admitted"
	}
	if out.ExecutionState == "unknown" {
		out.Message = "当前执行结果尚未确认；请核对原回执，不自动重放。"
	} else if out.Admitted && out.ExecutionState == "handler_returned" && (out.Code == "budget_exhausted" || out.Code == "capacity_exhausted") {
		out.Message = "原工具已返回资源拒绝；原调用已受理，业务状态请按回执核对。"
	}
	if out.Admitted && out.ExecutionState == "not_started" && (out.Code == "budget_exhausted" || out.Code == "capacity_exhausted") {
		out.Message = "调用已登记，但资源准入未通过，Handler尚未开始；请按原记录核对。"
	}
	out.Public = normalizedPublicError(out.Public)
	if out.Public != nil {
		out.Message = out.Public.Message
		if out.ExecutionState == "unknown" {
			// 已知失败原因不等于确定业务没有发生；不能把 unknown 改写成可安全重试。
			out.Message += " 当前执行结果尚未确认；请核对原回执，不自动重放。"
		}
	}
	out.RetryAfterMS = max(0, min(out.RetryAfterMS, int64(86400000)))
	return out
}

// TaskStorageFailure 用于明确的持久读写边界；错误说明不包含数据库地址或原驱动文本。
// 写入应答丢失时由调用者保留unknown和回执ID，不能据此重复业务执行。
func TaskStorageFailure(err error, execution string, admitted bool, receipt string, result *TaskResultRef) *TaskFailure {
	f := TaskFailureFrom(err, execution, admitted, receipt, result)
	if f == nil {
		return nil
	}
	if f.Code == "operation_failed" || f.Code == "execution_unknown" || f.Code == "handler_failed" {
		f.Code = "storage_unavailable"
		f.Message = "持久存储当前无法确认，请按原记录核对；不自动重放业务。"
	}
	return f
}

// ErrTaskStorageUnavailable 表示存储边界未能确认，不暴露驱动、地址或响应原文。
var ErrTaskStorageUnavailable = errors.New("agent: persistent storage unavailable")

type taskStorageError struct{ cause error }

func (e *taskStorageError) Error() string        { return ErrTaskStorageUnavailable.Error() }
func (e *taskStorageError) Unwrap() error        { return e.cause }
func (e *taskStorageError) Is(target error) bool { return target == ErrTaskStorageUnavailable }

// WrapTaskStorage 由明确持久边界调用；已知语义错误保持原判定，其他错误以固定公开类型标记。
func WrapTaskStorage(err error) error {
	if err == nil {
		return nil
	}
	var budget *TaskBudgetError
	var failure *TaskFailure
	if errors.Is(err, ErrTaskEventClosed) || errors.Is(err, ErrTaskContract) || errors.Is(err, ErrTaskStorageVersion) || errors.Is(err, ErrTaskStorageConfiguration) || errors.Is(err, ErrTaskDraining) || errors.As(err, &budget) || errors.As(err, &failure) || errors.Is(err, ErrTaskWakeBudget) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTaskStorageUnavailable) || errors.Is(err, ErrTaskNotFound) || errors.Is(err, ErrTaskConflict) || errors.Is(err, ErrTaskPolicyConflict) || errors.Is(err, ErrTaskResultExpired) || errors.Is(err, ErrTaskResultRange) || errors.Is(err, ErrTaskResultUnavailable) || errors.Is(err, ErrTaskContextCapacity) || errors.Is(err, ErrTaskContextInvalid) || errors.Is(err, ErrTaskEventCapacity) || errors.Is(err, ErrTaskEventCursor) || errors.Is(err, ErrTaskStopped) {
		return err
	}
	return &taskStorageError{err}
}

// TaskStorageCommitFailure 用于提交应答丢失：可能已经落库，必须查询原ID或重投同一幂等键核对。
func TaskStorageCommitFailure(err error, execution, receipt string, result *TaskResultRef) *TaskFailure {
	f := TaskStorageFailure(WrapTaskStorage(err), execution, true, receipt, result)
	if f != nil {
		f.AdmissionState = "unknown"
	}
	return f
}
