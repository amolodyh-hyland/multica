package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

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
