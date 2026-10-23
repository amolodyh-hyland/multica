package knowledge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testDirectory(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func testScope() Scope {
	return Scope{Server: "https://server.test", WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "machine1"}
}
func writeText(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func selectionFor(ref Reference, kind, id string) Selection {
	return Selection{SchemaVersion: 1, SnapshotID: id, WorkspaceID: "workspace1", ProjectID: "project1", OwnerID: "owner1", DaemonID: "machine1", Bindings: []Binding{{ID: "binding1", Kind: kind, Reference: ref}}}.Seal()
}

func TestKnowledgeSharedLibraryGoldenHash(t *testing.T) {
	text := "Review selected changes.\n"
	files := []TextFile{{Path: "rules/review.md", Mode: "text", SizeBytes: len(text), SHA256: Sum([]byte(text)), Content: text}}
	got, err := ContentHash("rule", "plain-rule-v1", "rules/review.md", files)
	if err != nil || got != "sha256:26eb5fd1696af1e0c83b85a2933f699fb93c286ba0ca4e0056f528e174cbb94d" {
		t.Fatalf("golden hash = %q, %v", got, err)
	}
	text = "Review selected changes carefully.\n"
	files[0].Content = text
	files[0].SHA256 = Sum([]byte(text))
	files[0].SizeBytes = len(text)
	got, err = ContentHash("rule", "plain-rule-v1", "rules/review.md", files)
	if err != nil || got != "sha256:d7108c9dd9c4b5ec1c67a81800dd1a9fb9f1e0fdf27499fff7f80873d18016e9" {
		t.Fatalf("changed golden = %q, %v", got, err)
	}
}

func TestKnowledgeReferenceValidation(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	p := filepath.Join(testDirectory(t), "notes.md")
	writeText(t, p, "notes")
	ref, err := store.Register(context.Background(), testScope(), File, p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ref)
	if _, err := DecodeReference(File, raw); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Reference){func(r *Reference) { r.SchemaVersion = 2 }, func(r *Reference) { r.ContentHash = "" }, func(r *Reference) { r.EntryID = r.SourceID }, func(r *Reference) { r.RootGeneration = 0 }, func(r *Reference) { r.ResolverID = "other" }, func(r *Reference) { r.EntryPoint = "../escape" }, func(r *Reference) { r.Format = "executable" }} {
		invalid := ref
		change(&invalid)
		raw, _ := json.Marshal(invalid)
		if _, err := DecodeReference(File, raw); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"schema_version":1,"local_path":"/tmp"}`, string(raw) + ` {}`} {
		if _, err := DecodeReference(File, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestKnowledgeSnapshotRetainsChangedAndDeletedSource(t *testing.T) {
	for _, kind := range []string{File, Folder, Skill} {
		t.Run(kind, func(t *testing.T) {
			store := LocalStore{Dir: testDirectory(t)}
			dir := testDirectory(t)
			p := filepath.Join(dir, "SKILL.md")
			writeText(t, p, "Original text.\n")
			source := dir
			if kind == File {
				source = p
			}
			ref, err := store.Register(context.Background(), testScope(), kind, source, true, true)
			if err != nil {
				t.Fatal(err)
			}
			selection := selectionFor(ref, kind, "snapshot1")
			snapshot, err := store.Resolve(context.Background(), testScope(), selection)
			if err != nil {
				t.Fatal(err)
			}
			writeText(t, p, "Changed text.\n")
			retained, err := store.Resolve(context.Background(), testScope(), selection)
			if err != nil || retained.Digest != snapshot.Digest {
				t.Fatalf("changed source rewrote snapshot: %v", err)
			}
			if _, err := store.Resolve(context.Background(), testScope(), selectionFor(ref, kind, "snapshot2")); err == nil {
				t.Fatal("changed source admitted for new session")
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			retained, err = store.Resolve(context.Background(), testScope(), selection)
			if err != nil || retained.Entries[0].Files[0].Content != "Original text.\n" {
				t.Fatalf("missing source lost snapshot: %v", err)
			}
			if _, err := store.Resolve(context.Background(), testScope(), selectionFor(ref, kind, "snapshot3")); err == nil {
				t.Fatal("deleted source admitted for new session")
			}
		})
	}
}

func TestKnowledgeScopeAndNoImplicitTransfer(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	p := filepath.Join(testDirectory(t), "notes.md")
	writeText(t, p, "private")
	if _, err := store.Register(context.Background(), testScope(), File, "/unreadable", false, false); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatal("inspection before approval")
	}
	ref, err := store.Register(context.Background(), testScope(), File, p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	selection := selectionFor(ref, File, "snapshot1")
	for _, scope := range []Scope{{Server: "https://foreign.test", WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "machine1"}, {Server: testScope().Server, WorkspaceID: "foreign", OwnerID: "owner1", DaemonID: "machine1"}, {Server: testScope().Server, WorkspaceID: "workspace1", OwnerID: "other", DaemonID: "machine1"}, {Server: testScope().Server, WorkspaceID: "workspace1", OwnerID: "owner1", DaemonID: "other"}} {
		if _, err := store.Resolve(context.Background(), scope, selection); err == nil {
			t.Fatalf("accepted foreign scope %+v", scope)
		}
	}
	remote := LocalStore{Dir: testDirectory(t)}
	if _, err := remote.Resolve(context.Background(), testScope(), selection); err == nil {
		t.Fatal("remote resolver silently copied a source")
	}
	raw, _ := json.Marshal(selection)
	if strings.Contains(string(raw), p) || strings.Contains(string(raw), "private") {
		t.Fatal("portable reference leaked local data")
	}
}

func TestKnowledgeLimitsAndUnsafeSources(t *testing.T) {
	for _, tc := range []string{"oversize", "executable", "symlink", "depth", "casefold", "nul", "binary"} {
		t.Run(tc, func(t *testing.T) {
			dir := testDirectory(t)
			store := LocalStore{Dir: testDirectory(t)}
			writeText(t, filepath.Join(dir, "SKILL.md"), "notes")
			switch tc {
			case "oversize":
				writeText(t, filepath.Join(dir, "large.md"), strings.Repeat("a", MaxFileBytes+1))
			case "executable":
				p := filepath.Join(dir, "run.sh")
				writeText(t, p, "touch /tmp/unwanted")
				if err := os.Chmod(p, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				p := filepath.Join(testDirectory(t), "outside.md")
				writeText(t, p, "secret")
				if err := os.Symlink(p, filepath.Join(dir, "escape")); err != nil {
					t.Skip(err)
				}
			case "depth":
				writeText(t, filepath.Join(dir, "a/b/c/d/e/deep.md"), "deep")
			case "casefold":
				writeText(t, filepath.Join(dir, "a.md"), "a")
				writeText(t, filepath.Join(dir, "A.md"), "b")
				entries, _ := os.ReadDir(dir)
				if len(entries) != 3 {
					t.Skip("filesystem folds case")
				}
			case "nul":
				writeText(t, filepath.Join(dir, "data.md"), "text\x00hidden")
			case "binary":
				writeText(t, filepath.Join(dir, "data.md"), "\xff")
			}
			if _, err := store.Register(context.Background(), testScope(), Skill, dir, true, true); err == nil {
				t.Fatal("unsafe source approved")
			}
		})
	}
	for _, p := range []string{"../a", "/a", "a//b", "a/./b", "a\\b", "a:b", "a/\x00b", "a/e\u0301.md"} {
		if ValidatePath(p) == nil {
			t.Fatalf("accepted unsafe path %q", p)
		}
	}
}

func TestKnowledgeConcurrentSnapshotAndTampering(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	p := filepath.Join(testDirectory(t), "notes.md")
	writeText(t, p, "notes")
	ref, err := store.Register(context.Background(), testScope(), File, p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	selection := selectionFor(ref, File, "snapshot1")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	digests := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			snapshot, err := store.Resolve(context.Background(), testScope(), selection)
			errs <- err
			digests <- snapshot.Digest
		})
	}
	wg.Wait()
	close(errs)
	close(digests)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for digest := range digests {
		if first == "" {
			first = digest
		}
		if first != digest {
			t.Fatal("concurrent snapshots diverged")
		}
	}
	target := filepath.Join(store.Dir, "snapshots", Sum([]byte(testScope().Server))[7:], "snapshot1.json")
	writeText(t, target, "{}")
	if _, err := store.Resolve(context.Background(), testScope(), selection); err == nil {
		t.Fatal("corrupt snapshot re-resolved live source")
	}
}

func TestKnowledgeRootReplacement(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	dir := testDirectory(t)
	p := filepath.Join(dir, "notes.md")
	writeText(t, p, "notes")
	ref, err := store.Register(context.Background(), testScope(), File, p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	writeText(t, p, "notes")
	if _, err := store.Resolve(context.Background(), testScope(), selectionFor(ref, File, "snapshot1")); err == nil {
		t.Fatal("same path and bytes accepted replacement root")
	}
}

func TestKnowledgeReapprovalPreservesPortableIdentity(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	dir := testDirectory(t)
	p := filepath.Join(dir, "SKILL.md")
	writeText(t, p, "first")
	first, err := store.Register(context.Background(), testScope(), Skill, dir, true, true)
	if err != nil {
		t.Fatal(err)
	}
	writeText(t, p, "second")
	next, err := store.Register(context.Background(), testScope(), Skill, dir, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.EntryID != next.EntryID || first.SourceID != next.SourceID || first.RootRef != next.RootRef || next.SourceRevision != first.SourceRevision+1 || first.ContentHash == next.ContentHash {
		t.Fatal("content edit changed identity or failed to advance revision")
	}
	if _, err := store.Resolve(context.Background(), testScope(), selectionFor(first, Skill, "old-selection-new-session")); err == nil {
		t.Fatal("new session used superseded approval")
	}
	if _, err := store.Resolve(context.Background(), testScope(), selectionFor(next, Skill, "new-selection")); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgePortablePathConflictAndSelectionLimit(t *testing.T) {
	content := "text"
	files := []TextFile{{Path: "SKILL.md", Mode: "text", SizeBytes: 4, SHA256: Sum([]byte(content)), Content: content}, {Path: "skill.md", Mode: "text", SizeBytes: 4, SHA256: Sum([]byte(content)), Content: content}}
	if _, err := ContentHash("skill", "native-skill-text-v1", "SKILL.md", files); err == nil {
		t.Fatal("case-fold collision accepted")
	}
	ref := Reference{SchemaVersion: 1, OwnerID: "owner1", DaemonID: "machine1", ResolverID: "machine1", RootRef: "root1", RootGeneration: 1, AuthorityID: "mu_local_machine1", SourceID: "source1", SourceRevision: 1, EntryID: "entry1", Format: "knowledge-text-v1", EntryPoint: "notes.md", ContentHash: Sum([]byte(content))}
	selection := selectionFor(ref, File, "snapshot1")
	for len(selection.Bindings) <= MaxBindings {
		selection.Bindings = append(selection.Bindings, Binding{ID: "duplicate", Kind: File, Reference: ref})
	}
	selection = selection.Seal()
	if selection.Validate() == nil {
		t.Fatal("over-limit selection accepted")
	}
}

func TestKnowledgeMissingRetainedSnapshotNeverRereadsSource(t *testing.T) {
	store := LocalStore{Dir: testDirectory(t)}
	p := filepath.Join(testDirectory(t), "notes.md")
	writeText(t, p, "original")
	ref, err := store.Register(context.Background(), testScope(), File, p, true, true)
	if err != nil {
		t.Fatal(err)
	}
	selection := selectionFor(ref, File, "snapshot1")
	if _, err := store.Resolve(context.Background(), testScope(), selection); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(store.Dir, "snapshots", Sum([]byte(testScope().Server))[7:], "snapshot1.json")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), testScope(), selection); err == nil || !strings.Contains(err.Error(), "retained snapshot is missing") {
		t.Fatalf("missing retained snapshot silently regenerated: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("live source was republished")
	}
}
