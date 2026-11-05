package agent

import (
	"context"
	"errors"
)

// TaskContextVersion 固定登记时观察到的长期工作上下文版本，引用本身不赋予执行授权。
// 恢复后由AI读取当前版本核对差异，不把旧引用伪装成最新事实或隐藏推理。
type TaskContextVersion struct {
	ID       string `json:"id" bson:"id"`
	Revision int64  `json:"revision" bson:"revision"`
}

func (r *TaskRuntime) validateContextVersions(ctx context.Context, scope string, refs []TaskContextVersion) error {
	if len(refs) > 16 {
		return ErrTaskContextInvalid
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.ID == "" || len(ref.ID) > 128 || ref.Revision < 1 || seen[ref.ID] {
			return ErrTaskContextInvalid
		}
		seen[ref.ID] = true
		record, err := r.ReadContext(ctx, scope, ref.ID)
		if err != nil {
			return err
		}
		if record.Deleted || record.Revision != ref.Revision {
			return ErrTaskConflict
		}
	}
	return nil
}

// MaxTaskWakeEnvelopeBytes 限制内部恢复事实的JSON传输；正文另有配额，用户输入不能取得此内部额度。
const MaxTaskWakeEnvelopeBytes = 128 << 10

// TaskContextMatch 记录认领时的引用状态，不把新版本等同于原目标，也不自动决定后续业务。
type TaskContextMatch struct {
	ID               string `json:"id" bson:"id"`
	ExpectedRevision int64  `json:"expected_revision" bson:"expectedRevision"`
	CurrentRevision  int64  `json:"current_revision,omitempty" bson:"currentRevision,omitempty"`
	State            string `json:"state" bson:"state"`
}

func (r *TaskRuntime) matchContextVersions(ctx context.Context, scope string, refs []TaskContextVersion) []TaskContextMatch {
	rows := make([]TaskContextMatch, 0, len(refs))
	for _, ref := range refs {
		match := TaskContextMatch{ID: ref.ID, ExpectedRevision: ref.Revision, State: "unconfirmed"}
		current, err := r.ReadContext(ctx, scope, ref.ID)
		if err == nil {
			match.CurrentRevision = current.Revision
			switch {
			case current.Deleted:
				match.State = "deleted"
			case current.Revision != ref.Revision:
				match.State = "version_changed"
			default:
				match.State = "available"
			}
		} else if errors.Is(err, ErrTaskNotFound) {
			match.State = "unavailable"
		}
		rows = append(rows, match)
	}
	return rows
}
