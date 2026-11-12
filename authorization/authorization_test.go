package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Manager, Record) {
	t.Helper()
	m := New(NewMemoryStore())
	r, err := m.Prepare(t.Context(), Binding{Owner: "owner", Conversation: "conversation", Capability: "cap", Goal: "goal"}, Plan{Tool: "single", ContractVersion: "contract-hash", Arguments: json.RawMessage(`{"path":{"id":"placeholder"},"body":{"usage":"live","nested":{"keep":9007199254740993}}}`), TargetPointer: "/path/id", Targets: []string{"one", "two"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return m, r
}
func activate(t *testing.T, m *Manager, r Record) {
	t.Helper()
	if _, err := m.Activate(t.Context(), r.Binding, r.ID, r.Digest); err != nil {
		t.Fatal(err)
	}
}
func TestScopeGrantDoesNotPrepareIndividualCalls(t *testing.T) {
	m := New(NewMemoryStore())
	targets := make([]string, 1000)
	for i := range targets {
		targets[i] = fmt.Sprint(i)
	}
	r, err := m.Prepare(t.Context(), Binding{Owner: "o", Conversation: "c", Capability: "p"}, Plan{Tool: "single", ContractVersion: "hash", Arguments: json.RawMessage(`{"id":"placeholder","set":1}`), TargetPointer: "/id", Targets: targets}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	args, err := BuildArguments(r.Plan, "999")
	if err != nil || string(args) != `{"id":"999","set":1}` {
		t.Fatal(string(args), err)
	}
	if _, err = m.Authorize(t.Context(), r.Binding, r.ID, "single", "hash", args); !errors.Is(err, ErrDenied) {
		t.Fatal("unapproved call permitted", err)
	}
	activate(t, m, r)
	claim, err := m.Authorize(t.Context(), r.Binding, r.ID, "single", "hash", args)
	if err != nil || !claim.Fresh {
		t.Fatal(claim, err)
	}
}
func TestScopeGrantRejectsChangesOutsideTarget(t *testing.T) {
	m, r := fixture(t)
	activate(t, m, r)
	for _, raw := range []string{`{"path":{"id":"three"},"body":{"usage":"live","nested":{"keep":9007199254740993}}}`, `{"path":{"id":"one"},"body":{"usage":"all","nested":{"keep":9007199254740993}}}`, `{"path":{"id":"one"},"body":{"usage":"live","nested":{"keep":9007199254740992}}}`} {
		if _, err := m.Authorize(t.Context(), r.Binding, r.ID, r.Plan.Tool, r.Plan.ContractVersion, json.RawMessage(raw)); err == nil {
			t.Fatal("changed scope authorized", raw)
		}
	}
	args, _ := BuildArguments(r.Plan, "one")
	for _, bind := range []Binding{{Owner: "other", Conversation: "conversation", Capability: "cap", Goal: "goal"}, {Owner: "owner", Conversation: "other", Capability: "cap", Goal: "goal"}, {Owner: "owner", Conversation: "conversation", Capability: "different", Goal: "goal"}, {Owner: "owner", Conversation: "conversation", Capability: "cap", Goal: "different"}} {
		if _, err := m.Authorize(t.Context(), bind, r.ID, r.Plan.Tool, r.Plan.ContractVersion, args); err == nil {
			t.Fatal("cross scope authorization")
		}
	}
	if _, err := m.Authorize(t.Context(), r.Binding, r.ID, r.Plan.Tool, "new-contract", args); err == nil {
		t.Fatal("new tool contract accepted")
	}
	if _, err := m.Activate(t.Context(), r.Binding, r.ID, "tampered"); err == nil {
		t.Fatal("changed digest approved")
	}
}
func TestScopeClaimsAreAtomicAndUnknownCannotRetry(t *testing.T) {
	m, r := fixture(t)
	activate(t, m, r)
	args, _ := BuildArguments(r.Plan, "one")
	var fresh atomic.Int32
	var wg sync.WaitGroup
	claims := make(chan Claim, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := m.Authorize(t.Context(), r.Binding, r.ID, r.Plan.Tool, r.Plan.ContractVersion, args)
			if err != nil {
				t.Error(err)
				return
			}
			if c.Fresh {
				fresh.Add(1)
			}
			claims <- c
		}()
	}
	wg.Wait()
	close(claims)
	if fresh.Load() != 1 {
		t.Fatal("multiple executions", fresh.Load())
	}
	var saved Claim
	for c := range claims {
		if saved.Invocation != "" && c.Invocation != saved.Invocation {
			t.Fatal("unstable invocation")
		}
		saved = c
	}
	if err := m.Finish(t.Context(), r.Binding, saved, Outcome{State: Unknown, Result: "original receipt uncertain"}); err != nil {
		t.Fatal(err)
	}
	again, err := m.Authorize(t.Context(), r.Binding, r.ID, r.Plan.Tool, r.Plan.ContractVersion, args)
	if err != nil || again.Fresh || again.State != Unknown || again.Result != "original receipt uncertain" {
		t.Fatal("unknown operation replayed", again, err)
	}
}
func TestScopeRevokeAndExpiryBlockNewClaimsButKeepReceipt(t *testing.T) {
	m, r := fixture(t)
	activate(t, m, r)
	args, _ := BuildArguments(r.Plan, "one")
	claim, err := m.Authorize(t.Context(), r.Binding, r.ID, r.Plan.Tool, r.Plan.ContractVersion, args)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Revoke(t.Context(), r.Binding, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Authorize(t.Context(), r.Binding, r.ID, r.Plan.Tool, r.Plan.ContractVersion, args); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = m.Finish(t.Context(), r.Binding, claim, Outcome{State: Completed, Result: "actual result"}); err != nil {
		t.Fatal("late result lost", err)
	}
	m2, r2 := fixture(t)
	activate(t, m2, r2)
	q := ClaimRequest{ID: r2.ID, Binding: r2.Binding, Digest: r2.Digest, Key: TargetKey("one"), Invocation: InvocationKey(r2, TargetKey("one")), Now: r2.ExpiresAt}
	if _, err = m2.store.Claim(t.Context(), q); !errors.Is(err, ErrDenied) {
		t.Fatal("expired grant permitted", err)
	}
}
func TestPlanPointerValidationAndCanonicalPrecision(t *testing.T) {
	base := Plan{Tool: "tool", ContractVersion: "v1", Arguments: json.RawMessage(`{"a/b":{"~id":"old"},"array":["old"]}`), TargetPointer: "/a~1b/~0id", Targets: []string{"new"}}
	raw, err := BuildArguments(base, "new")
	if err != nil || !strings.Contains(string(raw), `"~id":"new"`) {
		t.Fatal(string(raw), err)
	}
	base.TargetPointer = "/array/0"
	if _, err = BuildArguments(base, "new"); err != nil {
		t.Fatal(err)
	}
	for _, pointer := range []string{"", "/array/00", "/array/-", "/array", "/missing", "/a~2b/~0id"} {
		copy := base
		copy.TargetPointer = pointer
		if _, err := NormalizePlan(copy); err == nil {
			t.Fatal("invalid pointer", pointer)
		}
	}
	base.TargetPointer = "/id"
	for _, raw := range []string{`{"id":"one","id":"two"}`, `{"id":2}`, `{"id":{}}`, `{"id":"old"} {}`} {
		base.Arguments = json.RawMessage(raw)
		if _, err := NormalizePlan(base); err == nil {
			t.Fatal("ambiguous input accepted", raw)
		}
	}
}
func TestStoreDetachedAndCancellation(t *testing.T) {
	m, r := fixture(t)
	r.Plan.Targets[0] = "tampered"
	stored, err := m.Get(t.Context(), r.Binding, r.ID)
	if err != nil || stored.Plan.Targets[0] != "one" {
		t.Fatal("caller changed persisted plan", stored, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = m.Get(ctx, r.Binding, r.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	core := r.Binding
	core.Goal = ""
	if _, err = m.Get(t.Context(), core, r.ID); err != nil {
		t.Fatal("explicit grant cannot resume across turns", err)
	}
}

func TestEquivalentPlansIgnoreOnlyTargetExamplesAndOrdering(t *testing.T) {
	_, r := fixture(t)
	other := r.Plan
	other.Targets = []string{"two", "one"}
	other.Arguments = json.RawMessage(`{"path":{"id":"different"},"body":{"usage":"live","nested":{"keep":9007199254740993}}}`)
	if !EquivalentPlans(r.Plan, other) {
		t.Fatal("same precise authorization did not match")
	}
	other.ExecutionEpoch = "different"
	if EquivalentPlans(r.Plan, other) {
		t.Fatal("execution barrier bypassed")
	}
	other.ExecutionEpoch = r.Plan.ExecutionEpoch
	other.Arguments = json.RawMessage(`{"path":{"id":"different"},"body":{"usage":"upload","nested":{"keep":9007199254740993}}}`)
	if EquivalentPlans(r.Plan, other) {
		t.Fatal("different mutation reused authorization")
	}
}
