package daemon

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

type stalledKnowledgeFS struct {
	started chan struct{}
	release chan struct{}
	opens   atomic.Int32
}

func (f *stalledKnowledgeFS) Open(string) (fs.File, error) {
	f.opens.Add(1)
	return &stalledKnowledgeFile{filesystem: f}, nil
}

type stalledKnowledgeFile struct{ filesystem *stalledKnowledgeFS }

func (f *stalledKnowledgeFile) Read([]byte) (int, error) {
	f.filesystem.started <- struct{}{}
	<-f.filesystem.release // Simulate a filesystem read that ignores context cancellation.
	return 0, io.EOF
}
func (*stalledKnowledgeFile) Close() error               { return nil }
func (*stalledKnowledgeFile) Stat() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

type filesystemKnowledgeStore struct{ filesystem fs.FS }

func (s filesystemKnowledgeStore) Resolve(context.Context, knowledge.Scope, knowledge.Selection) (knowledge.Snapshot, error) {
	f, err := s.filesystem.Open("snapshot.json")
	if err != nil {
		return knowledge.Snapshot{}, err
	}
	defer f.Close()
	_, err = io.ReadAll(f)
	return knowledge.Snapshot{}, err
}

func TestKnowledgeResolutionStalledFilesystem(t *testing.T) {
	filesystem := &stalledKnowledgeFS{started: make(chan struct{}, maxKnowledgeResolutions+1), release: make(chan struct{})}
	defer close(filesystem.release)
	store := filesystemKnowledgeStore{filesystem: filesystem}
	var limiter knowledgeResolutionLimiter
	task := Task{WorkspaceID: "workspace1", KnowledgeManifest: &knowledge.Selection{WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "machine1"}}
	type result struct {
		snapshot *knowledge.Snapshot
		err      error
	}
	resolve := func(ctx context.Context) <-chan result {
		done := make(chan result, 1)
		go func() {
			snapshot, err := limiter.resolve(ctx, store, "https://server.test", "machine1", task)
			done <- result{snapshot: snapshot, err: err}
		}()
		return done
	}
	await := func(done <-chan result, want error) {
		t.Helper()
		select {
		case got := <-done:
			if got.snapshot != nil || !errors.Is(got.err, want) {
				t.Fatalf("resolution = %v, %v; want no snapshot and %v", got.snapshot, got.err, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("stalled filesystem held the preparation caller after cancellation")
		}
	}
	for i := 0; i < maxKnowledgeResolutions; i++ {
		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("task preparation cancelled")
		if i%2 == 1 {
			var stop context.CancelFunc
			ctx, stop = context.WithTimeout(ctx, time.Second)
			defer stop()
			cause = context.DeadlineExceeded
		}
		done := resolve(ctx)
		select {
		case <-filesystem.started:
		case <-time.After(2 * time.Second):
			cancel(context.Canceled)
			t.Fatal("filesystem read did not start")
		}
		if i%2 == 0 {
			cancel(cause)
		}
		await(done, cause)
		cancel(context.Canceled)
	}
	// All permits remain occupied by the abandoned reads. Further tasks must
	// still observe their deadlines without starting additional filesystem work.
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		await(resolve(ctx), context.DeadlineExceeded)
		cancel()
	}
	if got := filesystem.opens.Load(); got != maxKnowledgeResolutions {
		t.Fatalf("abandoned reads accumulated: %d opens, limit %d", got, maxKnowledgeResolutions)
	}
	// Let the blocked reads return and prove their permits can be used again.
	// Sending instead of closing keeps the deferred close safe on every path.
	for i := 0; i < maxKnowledgeResolutions; i++ {
		filesystem.release <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := resolve(ctx)
	select {
	case <-filesystem.started:
		filesystem.release <- struct{}{}
	case <-ctx.Done():
		t.Fatal("finished reads did not release their permits")
	}
	select {
	case got := <-done:
		if got.err != nil || got.snapshot == nil {
			t.Fatalf("resolution did not recover: %v", got.err)
		}
	case <-ctx.Done():
		t.Fatal("healthy resolution did not finish")
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	await(resolve(ctx), context.Canceled)
	if got := filesystem.opens.Load(); got != maxKnowledgeResolutions+1 {
		t.Fatalf("cancelled task started filesystem work: %d opens", got)
	}
}

func TestKnowledgeDaemonResolution(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(source, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	store := knowledge.LocalStore{Dir: filepath.Join(t.TempDir(), "store")}
	scope := knowledge.Scope{Server: "https://server.test", WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "machine1"}
	ref, err := store.Register(context.Background(), scope, knowledge.File, source, true, true)
	if err != nil {
		t.Fatal(err)
	}
	selection := knowledge.Selection{SchemaVersion: 1, SnapshotID: "snapshot1", WorkspaceID: scope.WorkspaceID, ProjectID: "project1", OwnerID: scope.OwnerID, DaemonID: scope.DaemonID, Bindings: []knowledge.Binding{{ID: "binding1", Kind: knowledge.File, Reference: ref}}}.Seal()
	task := Task{ID: "task1", WorkspaceID: scope.WorkspaceID, KnowledgeManifest: &selection}
	original, err := resolveTaskKnowledge(context.Background(), store, scope.Server, scope.DaemonID, task)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "private-state-path")
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTaskKnowledge(context.Background(), knowledge.LocalStore{Dir: blocked}, scope.Server, scope.DaemonID, task); err == nil || strings.Contains(err.Error(), blocked) {
		t.Fatalf("storage error disclosed a local path: %v", err)
	}

	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	restored, err := resolveTaskKnowledge(context.Background(), store, scope.Server, scope.DaemonID, task)
	if err != nil || restored.Digest != original.Digest {
		t.Fatalf("resume lost snapshot: %v", err)
	}
	if _, err := resolveTaskKnowledge(context.Background(), store, scope.Server, "other-machine", task); err == nil {
		t.Fatal("wrong machine accepted")
	}
	task.WorkspaceID = "foreign"
	if _, err := resolveTaskKnowledge(context.Background(), store, scope.Server, scope.DaemonID, task); err == nil {
		t.Fatal("foreign workspace accepted")
	}
	task.WorkspaceID = scope.WorkspaceID
	task.ProjectInstructions = strings.Repeat("a", knowledge.MaxBytes)
	if _, err := resolveTaskKnowledge(context.Background(), store, scope.Server, scope.DaemonID, task); err == nil {
		t.Fatal("combined context budget not enforced")
	}
	if got, err := resolveTaskKnowledge(context.Background(), store, scope.Server, scope.DaemonID, Task{}); err != nil || got != nil {
		t.Fatalf("existing tasks changed: %v", err)
	}
}

func TestKnowledgeStatusReservesCapacityForClaims(t *testing.T) {
	var limiter knowledgeResolutionLimiter
	filesystem := &stalledKnowledgeFS{started: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(filesystem.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := limiter.checkStatus(ctx, func(ctx context.Context) ([]knowledge.SourceStatus, error) {
			_, err := (filesystemKnowledgeStore{filesystem: filesystem}).Resolve(ctx, knowledge.Scope{}, knowledge.Selection{})
			return nil, err
		})
		done <- err
	}()
	select {
	case <-filesystem.started:
	case <-time.After(2 * time.Second):
		t.Fatal("status read did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("status caller did not time out: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled read held the status caller past its deadline")
	}

	// A timeout must not release the status slot: retries cannot occupy the
	// second shared permit while the first underlying read remains stalled.
	for i := 0; i < 3; i++ {
		_, err := limiter.checkStatus(context.Background(), func(context.Context) ([]knowledge.SourceStatus, error) {
			t.Error("busy status retry started another underlying read")
			return nil, nil
		})
		if !errors.Is(err, errKnowledgeStatusBusy) {
			t.Fatalf("status retry = %v; want busy", err)
		}
	}
	if got := len(limiter.slots); got != 1 {
		t.Fatalf("stalled status must retain exactly one shared permit, got %d", got)
	}
	store := filesystemKnowledgeStore{filesystem: fstest.MapFS{"snapshot.json": &fstest.MapFile{Data: []byte("snapshot")}}}
	task := Task{WorkspaceID: "workspace1", KnowledgeManifest: &knowledge.Selection{WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "machine1"}}
	claimCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if snapshot, err := limiter.resolve(claimCtx, store, "https://server.test", "machine1", task); err != nil || snapshot == nil {
		t.Fatalf("stalled status blocked a healthy claim: %v", err)
	}
	if got := filesystem.opens.Load(); got != 1 {
		t.Fatalf("status reads accumulated: %d", got)
	}

	filesystem.release <- struct{}{}
	// The slot is reusable only after the underlying read and its cleanup finish.
	deadline := time.After(2 * time.Second)
	for limiter.statusActive.Load() {
		select {
		case <-deadline:
			t.Fatal("finished status read did not release its slot")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := limiter.checkStatus(claimCtx, func(context.Context) ([]knowledge.SourceStatus, error) { return nil, nil }); err != nil {
		t.Fatalf("status check did not recover: %v", err)
	}
}

func TestKnowledgeStatusCancellationBeforeReadReleasesSlot(t *testing.T) {
	var limiter knowledgeResolutionLimiter
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := limiter.checkStatus(ctx, func(context.Context) ([]knowledge.SourceStatus, error) {
		t.Error("cancelled status check started a read")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || limiter.statusActive.Load() {
		t.Fatalf("cancelled status check retained its slot: %v", err)
	}
}
