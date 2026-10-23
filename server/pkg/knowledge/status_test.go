package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeStatusChecksCurrentMetadataWithoutChangingSnapshots(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	source := filepath.Join(testDirectory(t), "notes.md")
	const private = "PRIVATE SOURCE CONTENT MUST STAY LOCAL"
	writeText(t, source, private)
	ref, err := store.Register(context.Background(), testScope(), File, source, true, true)
	if err != nil {
		t.Fatal(err)
	}
	request := StatusRequest{ID: "status1", WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "machine1", Bindings: []Binding{{ID: "binding1", Kind: File, Reference: ref}}, Deadline: time.Now().Add(30 * time.Second)}
	check := func(want string) SourceStatus {
		t.Helper()
		results, err := store.CheckStatus(context.Background(), testScope(), request)
		if err != nil || len(results) != 1 || results[0].Status != want {
			t.Fatalf("status %q: %+v %v", want, results, err)
		}
		raw, _ := json.Marshal(results)
		if strings.Contains(string(raw), private) || strings.Contains(string(raw), source) || strings.Contains(string(raw), "\"content\"") {
			t.Fatalf("status disclosed content or path: %s", raw)
		}
		return results[0]
	}
	available := check("available")
	if available.ContentHash != ref.ContentHash || available.SizeBytes == nil || *available.SizeBytes != len(private) || available.Exists == nil || !*available.Exists || available.Type != "file" {
		t.Fatalf("metadata: %+v", available)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "snapshots")); !os.IsNotExist(err) {
		t.Fatal("status check created snapshot storage")
	}
	selection := selectionFor(ref, File, "snapshot1")
	snapshot, err := store.Resolve(context.Background(), testScope(), selection)
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, source, private+" updated")
	stale := check("stale")
	if stale.ContentHash == ref.ContentHash {
		t.Fatal("current hash was not inspected")
	}
	retained, err := store.Resolve(context.Background(), testScope(), selection)
	if err != nil || retained.Digest != snapshot.Digest || retained.Entries[0].Files[0].Content != private {
		t.Fatal("status changed the pinned snapshot")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	missing := check("missing")
	if missing.Exists == nil || *missing.Exists {
		t.Fatal("missing source metadata incorrect")
	}
}

func TestKnowledgeStatusAuthorizationAndBounds(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	path := filepath.Join(testDirectory(t), "notes.md")
	writeText(t, path, "text")
	ref, err := store.Register(context.Background(), testScope(), File, path, true, true)
	if err != nil {
		t.Fatal(err)
	}
	request := StatusRequest{ID: "status1", WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "machine1", Bindings: []Binding{{ID: "binding1", Kind: File, Reference: ref}}, Deadline: time.Now().Add(time.Second)}
	for _, change := range []func(*StatusRequest){
		func(r *StatusRequest) { r.WorkspaceID = "foreign" }, func(r *StatusRequest) { r.OwnerID = "other" }, func(r *StatusRequest) { r.DaemonID = "other" },
		func(r *StatusRequest) { r.Bindings = make([]Binding, MaxStatusBindings+1) },
	} {
		invalid := request
		change(&invalid)
		if _, err := store.CheckStatus(context.Background(), testScope(), invalid); err == nil {
			t.Fatal("invalid status authorization accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.CheckStatus(ctx, testScope(), request); err == nil {
		t.Fatal("cancelled inspection continued")
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	results, err := store.CheckStatus(context.Background(), testScope(), request)
	if err != nil || results[0].Status == "available" {
		t.Fatal("executable source accepted")
	}
}

func TestKnowledgeStatusOfflineAndOldDaemonFallback(t *testing.T) {
	bindings := []Binding{{ID: "resource1", Reference: Reference{ContentHash: Sum([]byte("pinned"))}}}
	offline := StatusFallback(bindings, false, true)
	if len(offline) != 1 || offline[0].Status != "unverified" || offline[0].Reason != "owner_offline" {
		t.Fatalf("offline: %+v", offline)
	}
	old := StatusFallback(bindings, true, false)
	if len(old) != 1 || old[0].Status != "incompatible" || old[0].Reason != "needs_newer_runner" {
		t.Fatalf("old: %+v", old)
	}
	if StatusFallback(bindings, true, true) != nil {
		t.Fatal("supported daemon incorrectly bypassed live check")
	}
}
