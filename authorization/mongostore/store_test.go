package mongostore

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/authorization"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func fixture(t *testing.T) *Store {
	t.Helper()
	uri := os.Getenv("ICLOUDER_TEST_MONGODB")
	if uri == "" {
		t.Skip("ICLOUDER_TEST_MONGODB not configured")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("ihtml_authorization_" + strings.ToLower(rand.Text()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Drop(ctx); err != nil {
			t.Error(err)
		}
		client.Disconnect(ctx)
	})
	s := &Store{Grants: db.Collection("grants"), Results: db.Collection("results")}
	if err = s.EnsureIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}
func prepared(t *testing.T, s *Store) authorization.Record {
	t.Helper()
	r, err := authorization.New(s).Prepare(t.Context(), authorization.Binding{Owner: "owner", Conversation: "conversation", Capability: "version", Goal: "goal"}, authorization.Plan{Tool: "operation", ContractVersion: "contract-v1", Arguments: []byte(`{"target":"first","enabled":true}`), TargetPointer: "/target", Targets: []string{"first", "second"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func request(r authorization.Record, target string) authorization.ClaimRequest {
	k := authorization.TargetKey(target)
	return authorization.ClaimRequest{ID: r.ID, Binding: r.Binding, Digest: r.Digest, Key: k, Invocation: authorization.InvocationKey(r, k), Now: time.Now()}
}
func activate(t *testing.T, s *Store, r authorization.Record) authorization.Record {
	t.Helper()
	r, err := s.Activate(t.Context(), r.Binding, r.ID, r.Digest, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMongoAuthorizationClaimsOnceAcrossInstances(t *testing.T) {
	s := fixture(t)
	r := activate(t, s, prepared(t, s))
	other := &Store{Grants: s.Grants, Results: s.Results}
	var fresh atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Go(func() {
			c, err := other.Claim(t.Context(), request(r, "first"))
			if err != nil {
				t.Error(err)
				return
			}
			if c.Fresh {
				fresh.Add(1)
			}
		})
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatal("duplicate admission", fresh.Load())
	}
	claim, err := s.Claim(t.Context(), request(r, "first"))
	if err != nil || claim.Fresh || claim.State != authorization.Claimed {
		t.Fatal(claim, err)
	}
	out := authorization.Outcome{State: authorization.Completed, Result: `{"result":"accepted"}`}
	if err = s.Finish(t.Context(), r.Binding, claim, out, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := other.Claim(t.Context(), request(r, "first"))
	if err != nil || got.Fresh || got.Result != out.Result || got.State != authorization.Completed {
		t.Fatal(got, err)
	}
	if err = s.Finish(t.Context(), r.Binding, claim, out, time.Now()); err != nil {
		t.Fatal("idempotent receipt", err)
	}
	if err = s.Finish(t.Context(), r.Binding, claim, authorization.Outcome{State: authorization.Unknown}, time.Now()); !errors.Is(err, authorization.ErrConflict) {
		t.Fatal("receipt overwritten", err)
	}
}
func TestMongoAuthorizationRevocationAndBinding(t *testing.T) {
	s := fixture(t)
	r := prepared(t, s)
	if _, err := s.Claim(t.Context(), request(r, "first")); !errors.Is(err, authorization.ErrDenied) {
		t.Fatal("unapproved claim", err)
	}
	r = activate(t, s, r)
	other := r.Binding
	other.Capability = "changed"
	if _, err := s.Get(t.Context(), other, r.ID); !errors.Is(err, authorization.ErrNotFound) {
		t.Fatal("cross-capability read", err)
	}
	q := request(r, "first")
	q.Binding.Goal = "different"
	if _, err := s.Claim(t.Context(), q); !errors.Is(err, authorization.ErrDenied) {
		t.Fatal("goal changed", err)
	}
	q = request(r, "outside")
	if _, err := s.Claim(t.Context(), q); !errors.Is(err, authorization.ErrDenied) {
		t.Fatal("target escaped", err)
	}
	claim, err := s.Claim(t.Context(), request(r, "first"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Revoke(t.Context(), r.Binding, r.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(t.Context(), request(r, "second")); !errors.Is(err, authorization.ErrDenied) {
		t.Fatal("revoked grant executed", err)
	}
	if _, err = s.Activate(t.Context(), r.Binding, r.ID, r.Digest, time.Now()); !errors.Is(err, authorization.ErrDenied) {
		t.Fatal("revoked grant reactivated", err)
	}
	if err = s.Finish(t.Context(), r.Binding, claim, authorization.Outcome{State: authorization.Unknown, Result: "provider result unavailable"}, time.Now()); err != nil {
		t.Fatal("inflight receipt lost", err)
	}
}
func TestMongoAuthorizationUnknownReceiptCannotReleaseClaim(t *testing.T) {
	s := fixture(t)
	r := activate(t, s, prepared(t, s))
	claim, err := s.Claim(t.Context(), request(r, "first"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(t.Context(), r.Binding, claim, authorization.Outcome{State: authorization.Completed, Result: strings.Repeat("x", authorization.MaxResultBytes+1)}, time.Now()); !errors.Is(err, authorization.ErrInvalid) {
		t.Fatal(err)
	}
	got, err := s.Claim(t.Context(), request(r, "first"))
	if err != nil || got.Fresh || got.State != authorization.Claimed {
		t.Fatal("failed persistence released admission", got, err)
	}
	q := request(r, "second")
	q.Now = r.ExpiresAt.Add(time.Second)
	if _, err = s.Claim(t.Context(), q); !errors.Is(err, authorization.ErrDenied) {
		t.Fatal("expired grant admitted", err)
	}
}
func TestMongoAuthorizationListDoesNotTruncateRevocationScope(t *testing.T) {
	s := fixture(t)
	r := prepared(t, s)
	// 历史记录数量不是批准窗口；完整枚举仍必须可撤销。
	for i := 0; i < authorization.MaxRecords+2; i++ {
		clone := r
		clone.ID = rand.Text()
		_, err := s.Grants.InsertOne(t.Context(), grantDocument{ID: key(clone.Binding, clone.ID), Version: version, Principal: principal(clone.Binding), Record: clone, DeleteAt: clone.ExpiresAt.Add(receiptRetention)})
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.List(t.Context(), r.Binding)
	if err != nil || len(rows) != authorization.MaxRecords+3 {
		t.Fatal("list truncated", len(rows), err)
	}
	other := r.Binding
	other.Owner = "different"
	rows, err = s.List(t.Context(), other)
	if err != nil || len(rows) != 0 {
		t.Fatal("list leaked principal", rows, err)
	}
}
func TestMongoAuthorizationRejectsFutureStorageVersion(t *testing.T) {
	s := fixture(t)
	r := prepared(t, s)
	_, err := s.Grants.UpdateOne(t.Context(), filter(r.Binding, r.ID), bson.M{"$set": bson.M{"storageVersion": version + 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Activate(t.Context(), r.Binding, r.ID, r.Digest, time.Now()); !errors.Is(err, authorization.ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.Revoke(t.Context(), r.Binding, r.ID, time.Now()); !errors.Is(err, authorization.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestMongoActiveHistoryDoesNotLimitNewApprovals(t *testing.T) {
	s := fixture(t)
	for i := 0; i < authorization.MaxRecords+1; i++ {
		activate(t, s, prepared(t, s))
	}
	if r := prepared(t, s); r.State != authorization.Pending {
		t.Fatal("history consumed pending capacity", r.State)
	}
}
