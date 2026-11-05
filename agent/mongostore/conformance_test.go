package mongostore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/agent/storetest"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// 每个 fixture 只操作随机测试数据库，清理不能依赖已取消的测试上下文。
func fixture(t *testing.T, env string) (*mongo.Database, agent.TaskConfig) {
	t.Helper()
	uri := os.Getenv(env)
	if uri == "" {
		t.Skip(env + " is not configured")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("ihtml_conformance_" + strings.ToLower(rand.Text()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Drop(ctx); err != nil {
			t.Error(err)
		}
		client.Disconnect(ctx)
	})
	tasks := &TaskStore{Tasks: db.Collection("tasks"), Waits: db.Collection("waits"), Subject: "subject", Conversation: "conversation"}
	results := &ResultStore{Manifests: db.Collection("results"), Chunks: db.Collection("chunks"), Subject: tasks.Subject, Conversation: tasks.Conversation}
	events := &EventStore{Sources: db.Collection("events"), Catalogs: db.Collection("catalogs"), Subject: tasks.Subject, Conversation: tasks.Conversation}
	contexts := &ContextStore{Collection: db.Collection("contexts"), Subject: tasks.Subject, Conversation: tasks.Conversation}
	observations := &ObservationStore{Collection: db.Collection("observations"), Subject: tasks.Subject, Conversation: tasks.Conversation}
	control := &ControlStore{Collection: db.Collection("control")}
	for _, ensure := range []func(context.Context) error{tasks.EnsureIndexes, results.EnsureIndexes, events.EnsureIndexes, contexts.EnsureIndexes, observations.EnsureIndexes, control.EnsureIndexes} {
		if err := ensure(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	return db, agent.TaskConfig{StorageMode: agent.TaskStorageDurable, Store: tasks, Results: results, Events: events, Contexts: contexts, Observations: observations, ControlStore: control}
}

func TestMongoConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) agent.TaskConfig { _, cfg := fixture(t, "ICLOUDER_TEST_MONGODB"); return cfg })
}

func TestMongoOutboxConformance(t *testing.T) {
	for _, embedded := range []string{"", "delivery"} {
		t.Run("layout_"+embedded, func(t *testing.T) {
			storetest.RunOutbox(t, func(t *testing.T) storetest.OutboxFixture {
				db, cfg := fixture(t, "ICLOUDER_TEST_MONGODB")
				p := agent.TaskEventPublisher{Store: cfg.Events}
				src, err := p.OpenSource(t.Context(), "scope", "source", "facts")
				if err != nil {
					t.Fatal(err)
				}
				s := &OutboxStore{Collection: db.Collection("outbox"), Scope: "scope", EmbeddedPath: embedded}
				if err = s.EnsureIndexes(t.Context()); err != nil {
					t.Fatal(err)
				}
				return storetest.OutboxFixture{Store: s, Scope: "scope", SourceID: src.ID, Publisher: p, Append: func(ctx context.Context, e agent.TaskOutboxEntry) error {
					var doc any = e
					if embedded != "" {
						fields, err := s.AtomicFields(e)
						if err != nil {
							return err
						}
						doc = fields
					}
					_, err := s.Collection.InsertOne(ctx, doc)
					return err
				}}
			})
		})
	}
}

// 新写入者不读取/覆盖未来格式；维护清理与嵌入式领取同样必须遵守版本屏障。
func TestMongoVersionMigrationAndFencing(t *testing.T) {
	db, cfg := fixture(t, "ICLOUDER_TEST_MONGODB")
	store := cfg.Store.(*TaskStore)
	now := time.Now()
	row := agent.TaskRecord{ID: "future", Scope: "scope", Key: "key", Digest: "digest", StorageVersion: 2, Revision: 1, Info: agent.TaskInfo{State: "completed"}, ExpiresAt: now.Add(-time.Hour)}
	if _, _, err := store.CreateTask(t.Context(), row); !errors.Is(err, agent.ErrTaskStorageVersion) {
		t.Fatal(err)
	}
	if _, err := store.Tasks.InsertOne(t.Context(), TaskDocument{row, store.Subject, store.Conversation}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTask(t.Context(), "scope", row.ID); !errors.Is(err, agent.ErrTaskStorageVersion) {
		t.Fatal(err)
	}
	row.StorageVersion = 1
	row.Revision = 2
	if ok, err := store.ReplaceTask(t.Context(), row, 1); err != nil || ok {
		t.Fatal("overwrote future format", ok, err)
	}
	if err := store.Collect(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Tasks.CountDocuments(t.Context(), bson.M{"_id": "future", "storageVersion": 2}); n != 1 {
		t.Fatal("collector touched future record")
	}
	control := cfg.ControlStore.(*ControlStore)
	if _, err := control.Collection.InsertOne(t.Context(), agent.TaskControlState{Key: "future", Revision: 1, StorageVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if ok, err := control.CompareControl(t.Context(), agent.TaskControlState{Key: "future", Revision: 2}, 1); err != nil || ok {
		t.Fatal(ok, err)
	}
	legacy := db.Collection("legacy")
	if _, err := legacy.InsertOne(t.Context(), bson.M{"_id": "old", "business": "preserved"}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStorage(t.Context(), []*mongo.Collection{legacy}, true); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStorage(t.Context(), []*mongo.Collection{legacy}, true); err != nil {
		t.Fatal("migration not repeatable", err)
	}
	cursor, err := db.ListCollections(t.Context(), bson.M{"name": legacy.Name()})
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Options struct {
			Validator bson.M `bson:"validator"`
		} `bson:"options"`
	}
	if !cursor.Next(t.Context()) {
		t.Fatal("missing collection metadata")
	}
	if err = cursor.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	cursor.Close(t.Context())
	if _, nested := spec.Options.Validator["$and"]; nested {
		t.Fatal("migration repeatedly nested the same validator")
	}
	if _, err := legacy.InsertOne(t.Context(), bson.M{"_id": "old-writer"}); err == nil {
		t.Fatal("old writer passed strict migration")
	}
	if _, err := legacy.InsertOne(t.Context(), bson.M{"_id": "new-writer", "storageVersion": 1}); err != nil {
		t.Fatal(err)
	}
	var migrated bson.M
	if err := legacy.FindOne(t.Context(), bson.M{"_id": "old"}).Decode(&migrated); err != nil || migrated["business"] != "preserved" {
		t.Fatal(migrated, err)
	}
	if err := MigrateStorage(t.Context(), []*mongo.Collection{store.Tasks}, false); !errors.Is(err, agent.ErrTaskStorageVersion) {
		t.Fatal(err)
	}
	for _, embedded := range []string{"", "delivery"} {
		s := &OutboxStore{Collection: db.Collection("version_outbox_" + embedded), Scope: "scope", EmbeddedPath: embedded}
		e, _ := agent.NewTaskOutboxEntry("scope", "source", "key", "test", json.RawMessage(`{}`), time.Hour)
		e.StorageVersion = 2
		var doc any = e
		if embedded != "" {
			doc = bson.M{"delivery": e}
		}
		if _, err := s.Collection.InsertOne(t.Context(), doc); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.ClaimOutbox(t.Context(), "worker", time.Minute); err != nil || ok {
			t.Fatal("claimed future format", ok, err)
		}
	}
}

type unavailableEvents struct {
	agent.TaskEventStore
	source string
}

func (s unavailableEvents) GetEventSource(ctx context.Context, scope, id string) (agent.TaskEventSource, error) {
	if id == s.source {
		return agent.TaskEventSource{}, agent.ErrTaskStorageUnavailable
	}
	return s.TaskEventStore.GetEventSource(ctx, scope, id)
}

func TestMongoOutboxBackoffDeadletterAndRequeue(t *testing.T) {
	db, cfg := fixture(t, "ICLOUDER_TEST_MONGODB")
	p := agent.TaskEventPublisher{Store: cfg.Events}
	bad, _ := p.OpenSource(t.Context(), "scope", "bad", "bad")
	good, _ := p.OpenSource(t.Context(), "scope", "good", "good")
	for _, embedded := range []string{"", "delivery"} {
		t.Run("layout_"+embedded, func(t *testing.T) {
			s := &OutboxStore{Collection: db.Collection("outbox_" + embedded), Scope: "scope", EmbeddedPath: embedded}
			if err := s.EnsureIndexes(t.Context()); err != nil {
				t.Fatal(err)
			}
			appendEntry := func(e agent.TaskOutboxEntry) {
				t.Helper()
				var doc any = e
				if embedded != "" {
					fields, err := s.AtomicFields(e)
					if err != nil {
						t.Fatal(err)
					}
					doc = fields
				}
				if _, err := s.Collection.InsertOne(t.Context(), doc); err != nil {
					t.Fatal(err)
				}
			}
			one, _ := agent.NewTaskOutboxEntry("scope", bad.ID, "bad-"+embedded, "fact", json.RawMessage(`{"private":"payload"}`), time.Hour)
			one.CreatedAt = one.CreatedAt.Add(-time.Minute)
			appendEntry(one)
			two, _ := agent.NewTaskOutboxEntry("scope", good.ID, "good-"+embedded, "fact", json.RawMessage(`{"id":1}`), time.Hour)
			appendEntry(two)
			faulty := p
			faulty.Store = unavailableEvents{cfg.Events, bad.ID}
			if ok, err := agent.DeliverTaskOutbox(t.Context(), s, faulty, "worker"); !ok || !errors.Is(err, agent.ErrTaskStorageUnavailable) {
				t.Fatal(ok, err)
			}
			if ok, err := agent.DeliverTaskOutbox(t.Context(), s, p, "worker"); !ok || err != nil {
				t.Fatal("failed item starved next fact", ok, err)
			}
			list, err := s.ListOutbox(t.Context(), "", 8)
			if err != nil || len(list) != 2 {
				t.Fatal(list, err)
			}
			var failed agent.TaskOutboxEntry
			for _, e := range list {
				if len(e.Event.Payload) != 0 {
					t.Fatal("diagnostic payload leak")
				}
				if e.ID == one.ID {
					failed = e
				}
			}
			if failed.Attempts != 1 || failed.Dead || !failed.RetryAt.After(time.Now()) || failed.FailureCode != "storage_unavailable" {
				t.Fatal(failed)
			}
			if err = s.RequeueOutbox(t.Context(), failed.ID, failed.Revision-1); !errors.Is(err, agent.ErrTaskConflict) {
				t.Fatal(err)
			}
			if err = s.RequeueOutbox(t.Context(), failed.ID, failed.Revision); err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := s.ClaimOutbox(t.Context(), "new-worker", time.Minute)
			if err != nil || !ok || string(claimed.Event.Payload) != string(one.Event.Payload) {
				t.Fatal(claimed, ok, err)
			}
			stale := claimed
			stale.Owner = "stale"
			if err = s.AckOutbox(t.Context(), stale); !errors.Is(err, agent.ErrTaskConflict) {
				t.Fatal(err)
			}
			if err = s.RequeueOutbox(t.Context(), claimed.ID, claimed.Revision); !errors.Is(err, agent.ErrTaskConflict) {
				t.Fatal("stole active lease", err)
			}
			if err = s.AckOutbox(t.Context(), claimed); err != nil {
				t.Fatal(err)
			}
			expired, _ := agent.NewTaskOutboxEntry("scope", good.ID, "expired-"+embedded, "fact", json.RawMessage(`{}`), time.Hour)
			appendEntry(expired)
			_, err = s.Collection.UpdateOne(t.Context(), bson.M{s.field("_id"): expired.ID}, bson.M{"$set": bson.M{s.field("expiresAt"): time.Now().Add(-time.Minute)}})
			if err != nil {
				t.Fatal(err)
			}
			if ok, err = agent.DeliverTaskOutbox(t.Context(), s, p, "worker"); !ok || !errors.Is(err, agent.ErrTaskResultExpired) {
				t.Fatal(ok, err)
			}
			list, err = s.ListOutbox(t.Context(), "", 8)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range list {
				if e.ID == expired.ID {
					if !e.Dead || e.Attempts != 1 {
						t.Fatal(e)
					}
					if err = s.RequeueOutbox(t.Context(), e.ID, e.Revision); !errors.Is(err, agent.ErrTaskConflict) {
						t.Fatal("extended expired fact", err)
					}
				}
			}
		})
	}
}

// 真实事务验收独立使用 replica set；同一业务写入与 outbox 插入一起提交或一起撤销。
func TestMongoNativeTransactionOutbox(t *testing.T) {
	db, cfg := fixture(t, "IHTML_TEST_MONGODB_REPLICA")
	p := agent.TaskEventPublisher{Store: cfg.Events}
	src, err := p.OpenSource(t.Context(), "scope", "source", "facts")
	if err != nil {
		t.Fatal(err)
	}
	s := &OutboxStore{Collection: db.Collection("outbox"), Scope: "scope"}
	if err = s.EnsureIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	business := db.Collection("business")
	if err = db.CreateCollection(t.Context(), business.Name()); err != nil {
		t.Fatal(err)
	}
	e, _ := agent.NewTaskOutboxEntry("scope", src.ID, "commit", "fact", json.RawMessage(`{"id":1}`), time.Hour)
	if err = s.AppendInTransaction(t.Context(), e); !errors.Is(err, agent.ErrTaskConflict) {
		t.Fatal("accepted nontransaction append", err)
	}
	session, err := db.Client().StartSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.EndSession(context.Background())
	abort := errors.New("test abort")
	for _, rollback := range []bool{true, false} {
		_, err = session.WithTransaction(t.Context(), func(ctx context.Context) (any, error) {
			if _, err := business.InsertOne(ctx, bson.M{"_id": "business"}); err != nil {
				return nil, err
			}
			if err := s.AppendInTransaction(ctx, e); err != nil {
				return nil, err
			}
			if rollback {
				return nil, abort
			}
			return nil, nil
		})
		if rollback && !errors.Is(err, abort) || !rollback && err != nil {
			t.Fatal(err)
		}
		want := int64(1)
		if rollback {
			want = 0
		}
		for _, c := range []*mongo.Collection{business, s.Collection} {
			if n, e := c.CountDocuments(t.Context(), bson.M{}); e != nil || n != want {
				t.Fatal(c.Name(), n, e)
			}
		}
	}
	for range 2 {
		ok, err := agent.DeliverTaskOutbox(t.Context(), s, p, "worker")
		if err != nil {
			t.Fatal(ok, err)
		}
	}
	source, err := cfg.Events.GetEventSource(t.Context(), "scope", src.ID)
	if err != nil || source.Head != 1 {
		t.Fatal(source.Head, err)
	}
}

// 多发布者分段运行超过单源容量；预留允许空洞，但不能重复序号、超填或静默丢事实。
func TestMongoEventSegmentsAcrossConcurrentPublishers(t *testing.T) {
	_, cfg := fixture(t, "ICLOUDER_TEST_MONGODB")
	p := agent.TaskEventPublisher{Store: cfg.Events, MaxRecords: 16}
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for n := range 64 {
				key := fmt.Sprintf("worker-%d-item-%d", worker, n)
				src, err := agent.AllocateEventSegment(t.Context(), cfg.ControlStore, "shared", "scope", "long-run", "facts", p)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err = p.Publish(t.Context(), "scope", src.ID, agent.TaskExternalEvent{Key: key, Type: "fact", Payload: json.RawMessage(`{"ok":true}`)}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	rows, err := cfg.Events.ListEventSources(t.Context(), "scope", "", 128)
	if err != nil || len(rows) != 32 {
		t.Fatal(len(rows), err)
	}
	var total uint64
	for _, src := range rows {
		if src.Head != 16 {
			t.Fatal("source was overfilled or lost facts", src.Head)
		}
		total += src.Head
	}
	if total != 512 {
		t.Fatal(total)
	}
}
