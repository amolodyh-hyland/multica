package libraryimport

import (
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"testing"
	"time"
)

func TestPrivateSelectionPinsConsentAndBothHashes(t *testing.T) {
	request := testRequest(time.Now())
	payload := Payload{Entry: request.Preview.Entry, Content: request.Content}
	bundle := payload.Bundle("object1")
	item := SelectedImport{ObjectID: bundle.ID, PreviewDigest: request.Consent.PreviewDigest, TransportHash: bundle.Hash, Entry: payload.Entry}
	projectDigest, _ := ProjectContextDigest(request.Preview.Binding.ProjectID, "", "", "", knowledge.Sum([]byte("context")), nil)
	selection, err := (Selection{SchemaVersion: 1, SnapshotID: "snapshot1", TaskID: request.Preview.Binding.TaskID, Destination: request.Preview.Destination, Binding: request.Preview.Binding, PersonaID: "persona1", ContextDigest: knowledge.Sum([]byte("context")), ProjectContextDigest: projectDigest, Provider: "claude", AdapterVersion: TaskAdapterVersion, Imports: []SelectedImport{item}}).Seal()
	if err != nil || selection.Validate() != nil {
		t.Fatalf("invalid selection: %v", err)
	}
	object := Object{Receipt: Receipt{ObjectID: bundle.ID, AuthorityID: selection.Destination.ServerID, ApprovalRef: "private-import:" + bundle.ID, PreviewDigest: item.PreviewDigest, ContentHash: item.Entry.ContentHash, TransportHash: bundle.Hash, Status: "stored"}, Selection: request, Bundle: bundle}
	if err := object.Verify(selection, item); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Selection){func(s *Selection) { s.Binding.TaskID = "other" }, func(s *Selection) { s.Provider = "codex" }, func(s *Selection) { s.ContextDigest = knowledge.Sum([]byte("new")) }} {
		changed := selection
		change(&changed)
		if changed.Validate() == nil {
			t.Fatal("mutated selection accepted")
		}
	}
	object.Bundle.Content += "changed"
	if object.Verify(selection, item) == nil {
		t.Fatal("changed transport accepted")
	}
	object.Bundle = bundle
	object.Selection.Preview.Binding.TaskID = "other"
	if object.Verify(selection, item) == nil {
		t.Fatal("another task accepted")
	}
}

func TestPrivateProjectRevisionIncludesGuidanceAndCanonicalResources(t *testing.T) {
	first, err := ProjectContextDigest("project1", "title", "description", "original", "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := ProjectContextDigest("project1", "title", "description", "original", "parent", []any{})
	if err != nil || empty != first {
		t.Fatal("empty wire representation changed context identity")
	}
	changed, err := ProjectContextDigest("project1", "title", "description", "changed", "parent", nil)
	if err != nil || changed == first {
		t.Fatal("Project instructions changed without a context revision")
	}
}
