package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
)

func privateDaemonFixture(t *testing.T, origin string) (Task, libraryimport.Object) {
	t.Helper()
	text := "Review selected changes.\n"
	entry := libraryimport.Entry{SchemaVersion: 1, EntryID: "entry1", SourceID: "source1", Kind: "rule", DisplayName: "Review", Format: "plain-rule-v1", EntryPoint: "rule.md", Files: []libraryimport.File{{Path: "rule.md", Mode: "text", SizeBytes: int64(len(text)), SHA256: knowledge.Sum([]byte(text))}}, Requirements: []string{}, Provenance: libraryimport.Provenance{OriginAuthorityID: "source_authority", OriginSourceID: "source1", OriginEntryID: "entry1", OriginKind: "machine_local", ResolverID: "resolver1", SourceRevision: 1, AdapterVersion: "1", Verification: "observed"}}
	hash, err := knowledge.ContentHash(entry.Kind, entry.Format, entry.EntryPoint, []knowledge.TextFile{{Path: entry.EntryPoint, Mode: "text", SizeBytes: len(text), SHA256: entry.Files[0].SHA256, Content: text}})
	if err != nil {
		t.Fatal(err)
	}
	entry.ContentHash = hash
	destination := libraryimport.Destination{ServerID: "server1", Origin: origin, WorkspaceID: "workspace1", OwnerID: "owner1", Audience: "owner_and_explicit_task"}
	binding := libraryimport.Binding{ProjectID: "project1", TaskID: "task1", RuntimeID: "runtime1"}
	preview := libraryimport.Preview{SchemaVersion: 1, AttemptID: "attempt1", Operation: "private_import", SourceResolverID: "resolver1", Destination: destination, Binding: binding, Entry: entry, TotalBytes: int64(len(text)), Excluded: []string{}}
	digest, err := preview.Digest()
	if err != nil {
		t.Fatal(err)
	}
	consent, err := libraryimport.Confirm(preview, digest, "owner1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	request := libraryimport.Request{Preview: preview, Consent: consent, Content: map[string]string{entry.EntryPoint: text}}
	bundle := (libraryimport.Payload{Entry: entry, Content: request.Content}).Bundle("object1")
	parent := (knowledge.Selection{SchemaVersion: 1, SnapshotID: "snapshot1", WorkspaceID: "workspace1", ProjectID: "project1", OwnerID: "owner1", DaemonID: "daemon1", Bindings: []knowledge.Binding{}}).Seal()
	item := libraryimport.SelectedImport{ObjectID: bundle.ID, PreviewDigest: digest, TransportHash: bundle.Hash, Entry: entry}
	selected, err := (libraryimport.Selection{SchemaVersion: 1, SnapshotID: "snapshot1", TaskID: "task1", Destination: destination, Binding: binding, PersonaID: "persona1", ContextDigest: parent.Digest, Provider: "claude", AdapterVersion: libraryimport.TaskAdapterVersion, Imports: []libraryimport.SelectedImport{item}}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	object := libraryimport.Object{Receipt: libraryimport.Receipt{ObjectID: bundle.ID, AuthorityID: destination.ServerID, ApprovalRef: "private-import:" + bundle.ID, PreviewDigest: digest, ContentHash: entry.ContentHash, TransportHash: bundle.Hash, Status: "stored", ReasonCode: "unsupported_provider"}, Selection: request, Bundle: bundle}
	return Task{ID: "task1", AgentID: "persona1", RuntimeID: "runtime1", WorkspaceID: "workspace1", ProjectID: "project1", AuthToken: "mat_exact_task", KnowledgeManifest: &parent, PrivateLibraryManifest: &selected}, object
}

func TestPrivateLibraryCacheReauthorizesExactTaskAndFailsOffline(t *testing.T) {
	task, object := privateDaemonFixture(t, "https://example.test")
	gets, heads := 0, 0
	allowed, offline := true, false
	client := NewClient("https://example.test")
	client.token = "pat_must_not_be_used"
	client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		if offline {
			return nil, errors.New("offline")
		}
		if r.Header.Get("Authorization") != "Bearer mat_exact_task" || r.Header.Get("X-Workspace-ID") != "workspace1" || r.URL.Path != "/api/private-library/tasks/task1/imports/object1" {
			t.Error("private request lost exact task scope")
		}
		status := http.StatusOK
		if !allowed {
			status = http.StatusForbidden
		}
		if r.Method == http.MethodHead {
			heads++
		} else {
			gets++
		}
		raw, _ := json.Marshal(object)
		return &http.Response{StatusCode: status, Header: http.Header{"X-Private-Import-Preview-Digest": {object.Receipt.PreviewDigest}, "X-Private-Import-Content-Hash": {object.Receipt.ContentHash}, "X-Private-Import-Transport-Hash": {object.Receipt.TransportHash}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	})
	cache := NewPrivateSkillBundleCache(filepath.Join(t.TempDir(), "private"))
	for i := 0; i < 2; i++ {
		payloads, err := resolvePrivateLibrary(context.Background(), client, cache, task, "claude", "2.1.292")
		if err != nil || len(payloads) != 1 || payloads[0].Content["rule.md"] != "Review selected changes.\n" {
			t.Fatalf("resolve: %v, %v", payloads, err)
		}
	}
	if gets != 1 || heads != 1 {
		t.Fatalf("GET=%d HEAD=%d", gets, heads)
	}
	if _, err := resolvePrivateLibrary(context.Background(), client, cache, task, "claude", "new-version"); err != nil {
		t.Fatal(err)
	}
	if gets != 2 {
		t.Fatal("provider version reused another cache scope")
	}
	allowed = false
	if _, err := resolvePrivateLibrary(context.Background(), client, cache, task, "claude", "2.1.292"); err == nil {
		t.Fatal("cache bypassed access denial")
	}
	offline = true
	if _, err := resolvePrivateLibrary(context.Background(), client, cache, task, "claude", "2.1.292"); err == nil {
		t.Fatal("offline cache granted access")
	}
	if runtime.GOOS != "windows" {
		entries, err := os.ReadDir(cache.root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("private cache permissions: %v, %v", info, err)
			}
		}
	}
}

func TestPrivateLibraryRejectsIdentityChangesBeforeFetch(t *testing.T) {
	calls := 0
	client := NewClient("https://example.test")
	client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected request") })
	for _, change := range []func(*Task){func(task *Task) { task.WorkspaceID = "other" }, func(task *Task) { task.AgentID = "other" }, func(task *Task) { task.RuntimeID = "other" }, func(task *Task) { task.ProjectID = "other" }, func(task *Task) { task.ID = "other" }, func(task *Task) { task.AuthToken = "pat_no_task" }, func(task *Task) { task.KnowledgeManifest.Digest = knowledge.Sum([]byte("other")) }} {
		task, _ := privateDaemonFixture(t, "https://example.test")
		change(&task)
		if _, err := resolvePrivateLibrary(context.Background(), client, NewPrivateSkillBundleCache(t.TempDir()), task, "claude", "version1"); err == nil {
			t.Fatal("changed task identity accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("metadata disclosed before identity validation: %d", calls)
	}
}

func TestPrivateLibraryLimitsIncludeExistingContext(t *testing.T) {
	task, object := privateDaemonFixture(t, "https://example.test")
	task.ProjectInstructions = strings.Repeat("x", knowledge.MaxBytes)
	payload := libraryimport.Payload{Entry: object.Selection.Preview.Entry, Content: object.Selection.Content}
	if privateLibraryByteLimit(task, nil, []libraryimport.Payload{payload}) == nil {
		t.Fatal("combined runtime limit bypassed")
	}
}
