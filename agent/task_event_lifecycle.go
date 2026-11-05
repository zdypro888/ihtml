package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

var ErrTaskEventClosed = errors.New("agent: event source closed")

// CloseSource 停止接收新事实，但同键重投仍可核对原指纹；后继必须是同作用域的真实源。
func (p TaskEventPublisher) CloseSource(ctx context.Context, scope, id string, expected int64, successor string) (TaskEventSource, error) {
	if successor != "" {
		next, err := p.Store.GetEventSource(ctx, scope, successor)
		if err != nil || next.ID == id || sourceIsClosed(next) {
			return TaskEventSource{}, ErrTaskConflict
		}
	}
	return p.editSource(ctx, scope, id, expected, func(s *TaskEventSource) error {
		if s.State == "archiving" || s.State == "archived" {
			return ErrTaskEventClosed
		}
		s.State = "closed"
		s.SuccessorID = successor
		return nil
	})
}

// RenewSource 只延长仍存活源及去重保留，不复活已过期正文或已关闭源。
func (p TaskEventPublisher) RenewSource(ctx context.Context, scope, id string, expected int64, until time.Time) (TaskEventSource, error) {
	p = p.defaults()
	if !until.After(time.Now()) || until.After(time.Now().Add(p.Retention)) {
		return TaskEventSource{}, ErrTaskConflict
	}
	return p.editSource(ctx, scope, id, expected, func(s *TaskEventSource) error {
		if sourceIsClosed(*s) {
			return ErrTaskEventClosed
		}
		if !s.ExpiresAt.After(time.Now()) {
			return ErrTaskNotFound
		}
		if until.After(s.ExpiresAt) {
			s.ExpiresAt = until
		}
		return nil
	})
}
func (p TaskEventPublisher) editSource(ctx context.Context, scope, id string, expected int64, fn func(*TaskEventSource) error) (TaskEventSource, error) {
	s, err := p.Store.GetEventSource(ctx, scope, id)
	if err != nil {
		return s, err
	}
	if s.Revision != expected {
		return s, ErrTaskConflict
	}
	if err = fn(&s); err != nil {
		return s, err
	}
	s.Revision++
	ok, err := p.Store.ReplaceEventSource(ctx, s, expected)
	if err == nil && !ok {
		err = ErrTaskConflict
	}
	return s, err
}

// ArchiveEventSource 在所有活跃等待均已交付/退出后回收关闭源正文，保留序号和去重指纹。
func (r *TaskRuntime) ArchiveEventSource(ctx context.Context, scope, id string, expected int64) (TaskEventSource, error) {
	if r.cfg.Scope != "" && r.cfg.Scope != scope {
		return TaskEventSource{}, ErrTaskNotFound
	}
	leave, err := r.enterAdmission()
	if err != nil {
		return TaskEventSource{}, err
	}
	defer leave()
	// 先持久冻结登记。即使归档租约过期或进程退出，新订阅也不能跨越正文回收屏障。
	source, err := r.cfg.Events.GetEventSource(ctx, scope, id)
	if err != nil {
		return source, err
	}
	if source.Revision != expected {
		return source, ErrTaskConflict
	}
	if source.State == "archived" {
		return source, nil
	}
	if source.State != "closed" && source.State != "archiving" {
		return source, ErrTaskConflict
	}
	if source.State == "closed" {
		source, err = r.EventPublisher().editSource(ctx, scope, id, expected, func(s *TaskEventSource) error { s.State = "archiving"; return nil })
		if err != nil {
			return source, err
		}
	}
	epoch, err := r.scopeEpoch(ctx, scope)
	if err != nil {
		return source, err
	}
	lease := rand.Text()
	until := time.Now().Add(time.Minute)
	claim := TaskResourceRequest{Key: "event-source/" + taskHash([]string{r.cfg.ControlNamespace, scope, id})}
	if err = r.acquireBudget(ctx, scope, epoch, lease, "maintenance", until, claim); err != nil {
		return source, err
	}
	defer r.releaseBudget(lease)
	if err = r.updateBudget(ctx, lease, "run", until); err != nil {
		return source, err
	}
	after := ""
	for {
		rows, err := r.cfg.Store.ListWaits(ctx, scope, TaskScan{After: after, Limit: 128, ActiveOnly: true})
		if err != nil {
			return source, err
		}
		for _, w := range rows {
			for _, c := range w.Spec.Events {
				if c.SourceID == id {
					return source, ErrTaskConflict
				}
			}
		}
		if len(rows) < 128 {
			break
		}
		after = rows[len(rows)-1].ID
	}
	return r.EventPublisher().editSource(ctx, scope, id, source.Revision, func(s *TaskEventSource) error {
		if s.State != "archiving" {
			return ErrTaskConflict
		}
		s.State = "archived"
		now := time.Now().UTC()
		for i := range s.Records {
			s.Records[i].Payload = nil
			s.Records[i].Availability = "archived"
			s.Records[i].BodyExpiresAt = minTaskTime(s.Records[i].BodyExpiresAt, now)
		}
		return nil
	})
}

func sourceIsClosed(s TaskEventSource) bool {
	return s.State == "closed" || s.State == "archiving" || s.State == "archived"
}

// AllocateEventSegment 用共享 CAS 预留源内发布位置，避免每个操作占一整个源、也避免并发填满同一段。
// 预留失败可留下空位，不会重执行业务；原 outbox 固定源句柄，重投不会换段。
func AllocateEventSegment(ctx context.Context, control TaskControlStore, namespace, scope, stream, label string, publisher TaskEventPublisher) (TaskEventSource, error) {
	if control == nil || scope == "" || stream == "" {
		return TaskEventSource{}, ErrTaskStorageConfiguration
	}
	p := publisher.defaults()
	key := "event-segment/" + taskHash([]string{namespace, scope, stream})
	var slot uint64
	for {
		row, err := control.LoadControl(ctx, key)
		if err == ErrTaskNotFound {
			row = TaskControlState{Key: key}
		} else if err != nil {
			return TaskEventSource{}, WrapTaskStorage(err)
		}
		if err = TaskProtocolVersion(row.StorageVersion); err != nil {
			return TaskEventSource{}, err
		}
		slot = 0
		if len(row.Payload) > 0 && json.Unmarshal(row.Payload, &slot) != nil {
			return TaskEventSource{}, ErrTaskConflict
		}
		if slot == ^uint64(0) {
			return TaskEventSource{}, ErrTaskEventCapacity
		}
		next := slot + 1
		row.Payload, _ = json.Marshal(next)
		old := row.Revision
		row.Revision++
		row.StorageVersion = TaskStorageVersion
		row.UpdatedAt = time.Now().UTC()
		row.ExpiresAt = row.UpdatedAt.Add(p.Retention)
		ok, err := control.CompareControl(ctx, row, old)
		if err != nil {
			return TaskEventSource{}, err
		}
		if ok {
			break
		}
		if err = ctx.Err(); err != nil {
			return TaskEventSource{}, err
		}
	}
	segment := slot / uint64(p.MaxRecords)
	source, err := p.OpenSource(ctx, scope, stream+"/"+strconv.FormatUint(segment, 10), label+" · "+strconv.FormatUint(segment+1, 10))
	if err == nil && sourceIsClosed(source) {
		err = ErrTaskEventClosed
	}
	return source, err
}
