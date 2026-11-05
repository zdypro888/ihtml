// Package storetest 提供持久适配器共用的协议验收，不编排任何业务接口。
package storetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/agent"
)

// Factory 必须为每个子测试提供独立命名空间；重建运行时须继续使用同一组真实存储。
type Factory func(*testing.T) agent.TaskConfig

// Run 验证接纳去重、重建、隔离、CAS、不可变结果、上下文与外部唤醒的一致语义。
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("InvocationAndRecreation", func(t *testing.T) {
		cfg := factory(t)
		cfg.Scope = "scope"
		var calls atomic.Int32
		tool := agent.Tool{Name: "arbitrary", Effect: agent.ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) {
			calls.Add(1)
			return `{"value":9007199254740993}`, nil
		}}
		r, err := agent.NewTaskRuntime(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		row, err := r.Start(t.Context(), "scope", "turn", "invocation", tool, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		row, err = r.AwaitResult(t.Context(), "scope", row.ID, map[string]bool{tool.Name: true})
		if err != nil || row.State != "completed" {
			t.Fatal(row, err)
		}
		r.Close()
		r, err = agent.NewTaskRuntime(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		again, err := r.Start(t.Context(), "scope", "turn", "invocation", tool, json.RawMessage(`{}`))
		if err != nil || again.ID != row.ID || calls.Load() != 1 {
			t.Fatal(again, err, calls.Load())
		}
		if _, err = r.Start(t.Context(), "scope", "turn", "invocation", tool, json.RawMessage(`{"changed":1}`)); !errors.Is(err, agent.ErrTaskConflict) {
			t.Fatal(err)
		}
		if _, err = cfg.Store.GetTask(t.Context(), "foreign", row.ID); !errors.Is(err, agent.ErrTaskNotFound) {
			t.Fatal("scope leak", err)
		}
		if _, err = cfg.Store.GetTask(t.Context(), "", row.ID); !errors.Is(err, agent.ErrTaskNotFound) {
			t.Fatal("empty scope expanded lookup", err)
		}
		b, err := r.ResolveResultInput(t.Context(), "scope", agent.TaskResultInput{ID: row.ID, SHA256: row.ResultRef.SHA256, Pointer: "/value"}, map[string]bool{tool.Name: true})
		if err != nil || string(b) != "9007199254740993" {
			t.Fatal(string(b), err)
		}
		rec, err := cfg.Store.GetTask(t.Context(), "scope", row.ID)
		if err != nil {
			t.Fatal(err)
		}
		old := rec.Revision
		rec.Revision++
		if ok, e := cfg.Store.ReplaceTask(t.Context(), rec, old); e != nil || !ok {
			t.Fatal(ok, e)
		}
		if ok, e := cfg.Store.ReplaceTask(t.Context(), rec, old); e != nil || ok {
			t.Fatal("stale writer", ok, e)
		}
	})
	t.Run("ResultImmutability", func(t *testing.T) {
		cfg := factory(t)
		now := time.Now().UTC()
		body := bytes.Repeat([]byte("binary\x00value"), 30000)
		sum := sha256.Sum256(body)
		row := agent.TaskResultRecord{Scope: "scope", TaskID: "immutable", Digest: "frozen", State: "completed", Started: true, Body: body, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Encoding: "base64", Availability: "available", CreatedAt: now, ExpiresAt: now.Add(time.Hour), LedgerExpiresAt: now.Add(24 * time.Hour)}
		if _, err := cfg.Results.PutResult(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.Results.PutResult(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		part, err := cfg.Results.ReadResult(t.Context(), "scope", row.TaskID, 255990, 77)
		if err != nil || !bytes.Equal(part, body[255990:256067]) {
			t.Fatal(err)
		}
		changed := row
		changed.Body = []byte("changed")
		changed.Size = 7
		sum = sha256.Sum256(changed.Body)
		changed.SHA256 = hex.EncodeToString(sum[:])
		if _, err = cfg.Results.PutResult(t.Context(), changed); !errors.Is(err, agent.ErrTaskConflict) {
			t.Fatal("mutable outcome", err)
		}
		if _, err = cfg.Results.ReadResult(t.Context(), "foreign", row.TaskID, 0, 1); !errors.Is(err, agent.ErrTaskNotFound) {
			t.Fatal(err)
		}
	})
	t.Run("ControlCAS", func(t *testing.T) {
		cfg := factory(t)
		row := agent.TaskControlState{Key: "isolated", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}
		ok, err := cfg.ControlStore.CompareControl(t.Context(), row, 0)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if ok, err = cfg.ControlStore.CompareControl(t.Context(), row, 0); err != nil || ok {
			t.Fatal(ok, err)
		}
		row.Revision++
		if ok, err = cfg.ControlStore.CompareControl(t.Context(), row, 1); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if ok, err = cfg.ControlStore.CompareControl(t.Context(), row, 1); err != nil || ok {
			t.Fatal("stale control CAS", ok, err)
		}
	})
	t.Run("ContextAndWake", func(t *testing.T) {
		cfg := factory(t)
		cfg.Scope = "scope"
		r, err := agent.NewTaskRuntime(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		doc := agent.TaskContextDocument{Goal: "public goal", State: "active"}
		record, err := r.SaveContext(t.Context(), "scope", "turn", "save", "", 0, doc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.SaveContext(t.Context(), "scope", "turn", "stale", record.ID, 0, doc); !errors.Is(err, agent.ErrTaskConflict) {
			t.Fatal(err)
		}
		p := r.EventPublisher()
		src, err := p.OpenSource(t.Context(), "scope", "source", "external facts")
		if err != nil {
			t.Fatal(err)
		}
		event := agent.TaskExternalEvent{Key: "event", Type: "changed", Payload: json.RawMessage(`{"value":1}`)}
		first, err := p.Publish(t.Context(), "scope", src.ID, event)
		if err != nil {
			t.Fatal(err)
		}
		again, err := p.Publish(t.Context(), "scope", src.ID, event)
		if err != nil || again.Seq != first.Seq {
			t.Fatal(again, err)
		}
		w, err := r.Suspend(t.Context(), "scope", "turn", "wait", agent.TaskWaitSpec{Timeout: time.Hour, Checkpoint: "facts", Events: []agent.TaskEventCondition{{SourceID: src.ID, After: 0}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
		r, err = agent.NewTaskRuntime(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		wake, err := r.ClaimWake(t.Context(), "scope", "consumer")
		if err != nil || wake == nil || wake.ID != w.ID {
			t.Fatal(wake, err)
		}
		if err = r.UpdateWake(t.Context(), "scope", wake.ID, "other", "ack"); !errors.Is(err, agent.ErrTaskConflict) {
			t.Fatal(err)
		}
		if err = r.UpdateWake(t.Context(), "scope", wake.ID, "consumer", "ack"); err != nil {
			t.Fatal(err)
		}
		if wake, err = r.ClaimWake(t.Context(), "scope", "consumer"); err != nil || wake != nil {
			t.Fatal("redelivered acknowledged wake", wake, err)
		}
	})
}
