package ihtml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileStoreProcessLockWaitHonorsContext(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fileStoreProcessGate <- struct{}{}
	defer func() { <-fileStoreProcessGate }()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = store.KVSet(ctx, "blocked", "key", json.RawMessage(`true`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for process lock returned %v, want context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("context cancellation took too long: %s", elapsed)
	}
	if _, statErr := os.Stat(store.fileName("blocked")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled lock wait wrote data: %v", statErr)
	}
}

func TestFileStoreCASAcrossIndependentHandles(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	stores := []*FileStore{first, second}

	const writers = 32
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			state := UserState{
				Items: []Item{{ID: fmt.Sprintf("item-%02d", i), Type: ItemHTML, Content: "<p/>"}},
				KV:    map[string]json.RawMessage{},
			}
			results <- stores[i%len(stores)].ReplaceUserStateVersioned(context.Background(), "u", state, 0)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected CAS error: %v", err)
		}
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("same expected admitted successes=%d conflicts=%d", successes, conflicts)
	}
	state, err := second.UserState(context.Background(), "u")
	if err != nil || state.Version != 1 || len(state.Items) != 1 {
		t.Fatalf("final state is not the single CAS winner: %+v %v", state, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("unique temporary files leaked: %v %v", leftovers, err)
	}
}

func TestSyncDirPropagatesOpenErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := syncDir(missing); err == nil {
		t.Fatal("syncDir must propagate errors opening the directory")
	}
}

func TestFileStoreDataFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits")
	}
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceItems(context.Background(), "private", []Item{{
		ID: "secret", Type: ItemHTML, Content: "<p>private</p>",
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.fileName("private"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("store data file permissions = %04o, want 0600", got)
	}
}

func TestFileStoreReadsAndMigratesLegacyKVBytes(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const user = "legacy-kv"
	legacyValue := json.RawMessage(`{ "ok" : "<tag>&" }`)
	legacyDisk := []byte(`{"items":[],"kv":{"old":{ "ok" : "<tag>&" }},"version":7}`)
	if err := os.WriteFile(store.fileName(user), legacyDisk, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.KVGet(context.Background(), user, "old")
	if err != nil || !bytes.Equal(got, legacyValue) {
		t.Fatalf("read legacy KV bytes = %q, want %q: %v", got, legacyValue, err)
	}

	newValue := json.RawMessage(" { \"html\" : \"<script>&\" } \n")
	if err := store.KVSet(context.Background(), user, "new", newValue); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]json.RawMessage{"old": legacyValue, "new": newValue} {
		got, err := store.KVGet(context.Background(), user, key)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("migrated KV %q = %q, want %q: %v", key, got, want, err)
		}
	}

	raw, err := os.ReadFile(store.fileName(user))
	if err != nil {
		t.Fatal(err)
	}
	var disk fileUserDataDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.FormatVersion != fileStoreFormatVersion || len(disk.LegacyKV) != 2 ||
		!bytes.Equal(disk.KVBytes["old"], legacyValue) || !bytes.Equal(disk.KVBytes["new"], newValue) {
		t.Fatalf("legacy file was not migrated to byte-preserving v%d: %+v", fileStoreFormatVersion, disk)
	}
	// Simulate a rollback reader that knows only the historical `kv` field.
	// It need not preserve whitespace, but it must retain every JSON value.
	var rollback struct {
		KV map[string]json.RawMessage `json:"kv"`
	}
	if err := json.Unmarshal(raw, &rollback); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]json.RawMessage{"old": legacyValue, "new": newValue} {
		var gotValue, wantValue any
		if err := json.Unmarshal(rollback.KV[key], &gotValue); err != nil {
			t.Fatalf("rollback reader lost %q: %v", key, err)
		}
		if err := json.Unmarshal(want, &wantValue); err != nil {
			t.Fatal(err)
		}
		gotJSON, _ := json.Marshal(gotValue)
		wantJSON, _ := json.Marshal(wantValue)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("rollback reader changed %q: got %s want %s", key, gotJSON, wantJSON)
		}
	}
}

// TestFileStoreCASAcrossProcesses executes this test binary twice so the result
// depends on the OS lock rather than the shared in-process mutex.
func TestFileStoreCASAcrossProcesses(t *testing.T) {
	if os.Getenv("IHTML_FILESTORE_CAS_HELPER") == "1" {
		runFileStoreCASProcessHelper(t)
		return
	}

	dir := t.TempDir()
	startName := filepath.Join(dir, "start")
	type child struct {
		cmd *exec.Cmd
		out bytes.Buffer
	}
	children := make([]*child, 2)
	for i := range children {
		readyName := filepath.Join(dir, fmt.Sprintf("ready-%d", i))
		cmd := exec.Command(os.Args[0], "-test.run=^TestFileStoreCASAcrossProcesses$")
		cmd.Env = append(os.Environ(),
			"IHTML_FILESTORE_CAS_HELPER=1",
			"IHTML_FILESTORE_CAS_DIR="+dir,
			"IHTML_FILESTORE_CAS_READY="+readyName,
			"IHTML_FILESTORE_CAS_START="+startName,
			fmt.Sprintf("IHTML_FILESTORE_CAS_ID=%d", i),
		)
		children[i] = &child{cmd: cmd}
		cmd.Stdout, cmd.Stderr = &children[i].out, &children[i].out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(10 * time.Second)
	for i := range children {
		readyName := filepath.Join(dir, fmt.Sprintf("ready-%d", i))
		for {
			if _, err := os.Stat(readyName); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("child %d did not become ready", i)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := os.WriteFile(startName, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}

	successes, conflicts := 0, 0
	for i, child := range children {
		if err := child.cmd.Wait(); err != nil {
			t.Fatalf("child %d failed: %v\n%s", i, err, child.out.String())
		}
		output := child.out.String()
		switch {
		case strings.Contains(output, "IHTML_RESULT=success"):
			successes++
		case strings.Contains(output, "IHTML_RESULT=conflict"):
			conflicts++
		default:
			t.Fatalf("child %d returned no result marker:\n%s", i, output)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("cross-process CAS results: successes=%d conflicts=%d", successes, conflicts)
	}
}

func runFileStoreCASProcessHelper(t *testing.T) {
	dir := os.Getenv("IHTML_FILESTORE_CAS_DIR")
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.UserState(context.Background(), "u")
	if err != nil || state.Version != 0 {
		t.Fatalf("helper initial state: %+v %v", state, err)
	}
	if err := os.WriteFile(os.Getenv("IHTML_FILESTORE_CAS_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("IHTML_FILESTORE_CAS_START")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for parent barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	state.Items = []Item{{ID: "child-" + os.Getenv("IHTML_FILESTORE_CAS_ID"), Type: ItemHTML, Content: "<p/>"}}
	err = store.ReplaceUserStateVersioned(context.Background(), "u", state, 0)
	switch {
	case err == nil:
		fmt.Println("IHTML_RESULT=success")
	case errors.Is(err, ErrVersionConflict):
		fmt.Println("IHTML_RESULT=conflict")
	default:
		t.Fatal(err)
	}
}
