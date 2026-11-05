package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const TaskStorageVersion = 1
const TaskStorageMemory = "memory"
const TaskStorageDurable = "durable"

var ErrTaskStorageConfiguration = errors.New("agent: incomplete durable storage")
var ErrTaskStorageVersion = errors.New("agent: incompatible storage protocol")
var ErrTaskContract = errors.New("agent: tool contract violation")
var ErrTaskDraining = errors.New("agent: runtime draining")

// TaskProtocolVersion 是存储读写协议，独立于记录 CAS 修订和工具版本。零版本仅用于显式兼容旧记录。
func TaskProtocolVersion(version int) error {
	if version < 0 || version > TaskStorageVersion {
		return ErrTaskStorageVersion
	}
	return nil
}

// TaskStoreHealth 由适配器执行轻量就绪检查，不能创建业务记录或隐式迁移。
type TaskStoreHealth interface{ TaskStoreHealth(context.Context) error }

// TaskReadiness 返回实际装配结果；持久模式缺少任何权威存储时拒绝启动。
type TaskReadiness struct {
	Mode            string            `json:"mode"`
	ProtocolVersion int               `json:"protocol_version"`
	Components      map[string]string `json:"components"`
}

func validateTaskStorage(c TaskConfig) (TaskReadiness, error) {
	mode := c.StorageMode
	if mode == "" {
		mode = TaskStorageMemory
	}
	report := TaskReadiness{Mode: mode, ProtocolVersion: TaskStorageVersion, Components: map[string]string{}}
	if mode != TaskStorageMemory && mode != TaskStorageDurable {
		return report, ErrTaskStorageConfiguration
	}
	var missing []string
	for _, item := range []struct {
		name    string
		present bool
	}{{"tasks", c.Store != nil}, {"results", c.Results != nil}, {"control", c.ControlStore != nil}, {"contexts", c.Contexts != nil}, {"events", c.Events != nil}, {"observations", c.Observations != nil}} {
		if item.present {
			report.Components[item.name] = "configured"
		} else {
			report.Components[item.name] = "memory_default"
			if mode == TaskStorageDurable {
				report.Components[item.name] = "missing"
				missing = append(missing, item.name)
			}
		}
	}
	if c.Outbox != nil {
		report.Components["outbox"] = "configured"
	}
	if len(missing) > 0 {
		return report, fmt.Errorf("%w: %s", ErrTaskStorageConfiguration, strings.Join(missing, ", "))
	}
	return report, nil
}

// Readiness 查询已注入适配器健康状态；默认不把数据库地址或驱动错误返回给调用者。
func (r *TaskRuntime) Readiness(ctx context.Context) (TaskReadiness, error) {
	report, err := validateTaskStorage(r.cfg)
	if err != nil {
		return report, err
	}
	for name, store := range map[string]any{"tasks": r.cfg.Store, "results": r.cfg.Results, "control": r.cfg.ControlStore, "contexts": r.cfg.Contexts, "events": r.cfg.Events, "observations": r.cfg.Observations, "outbox": r.cfg.Outbox} {
		if health, ok := store.(TaskStoreHealth); ok {
			query, stop := context.WithTimeout(ctx, 2*time.Second)
			e := health.TaskStoreHealth(query)
			stop()
			if e != nil {
				report.Components[name] = "unavailable"
				publicErr := ErrTaskStorageUnavailable
				if errors.Is(e, ErrTaskStorageVersion) {
					report.Components[name] = "incompatible"
					publicErr = ErrTaskStorageVersion
				}
				err = errors.Join(err, publicErr)
			} else {
				report.Components[name] = "ready"
			}
		}
	}
	return report, err
}

// CheckTaskReadiness 只检查装配和存储，不启动维护、投递或业务执行 goroutine。
func CheckTaskReadiness(ctx context.Context, c TaskConfig) (TaskReadiness, error) {
	return (&TaskRuntime{cfg: c}).Readiness(ctx)
}
