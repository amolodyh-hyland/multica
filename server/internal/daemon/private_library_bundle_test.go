package daemon

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"testing"

	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
)

func TestPrivateLibraryBundleUsesExistingImmutableTransport(t *testing.T) {
	content := "---\nname: review\ndescription: Review selected files\n---\nReview exactly these files.\r\n"
	payload := libraryimport.Payload{Entry: libraryimport.Entry{SchemaVersion: 1, EntryID: "entry1", SourceID: "source1", Kind: "skill", DisplayName: "Review", Format: "native-skill-text-v1", EntryPoint: "SKILL.md", Files: []libraryimport.File{{Path: "SKILL.md", Mode: "text", SizeBytes: int64(len(content)), SHA256: knowledge.Sum([]byte(content))}, {Path: "notes/checks.md", Mode: "text", SizeBytes: 5, SHA256: knowledge.Sum([]byte("check"))}}, Provenance: libraryimport.Provenance{OriginAuthorityID: "ao_local", OriginSourceID: "source1", OriginEntryID: "entry1", OriginKind: "machine_local", ResolverID: "resolver1", SourceRevision: 1, AdapterVersion: "1", Verification: "observed"}}, Content: map[string]string{"SKILL.md": content, "notes/checks.md": "check"}}
	var files []knowledge.TextFile
	for _, f := range payload.Entry.Files {
		files = append(files, knowledge.TextFile{Path: f.Path, Mode: f.Mode, SizeBytes: int(f.SizeBytes), SHA256: f.SHA256, Content: payload.Content[f.Path]})
	}
	hash, err := knowledge.ContentHash(payload.Entry.Kind, payload.Entry.Format, payload.Entry.EntryPoint, files)
	if err != nil {
		t.Fatal(err)
	}
	payload.Entry.ContentHash = hash
	if err := payload.Verify(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload.Bundle("private_object1"))
	if err != nil {
		t.Fatal(err)
	}
	var bundle SkillData
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	ref := SkillRefData{ID: bundle.ID, Source: bundle.Source, Hash: bundle.Hash, SizeBytes: bundle.SizeBytes, FileCount: len(bundle.Files)}
	if bundle.Source != libraryimport.SourcePrivate || bundle.ID != "private_object1" || bundle.Hash == hash || bundle.Content != content || len(bundle.Files) != 1 || bundle.Files[0].Path != "notes/checks.md" || !validateSkillBundle(ref, bundle) {
		t.Fatalf("transport changed immutable selection: %+v", bundle)
	}
	bundle.Content += "changed"
	if validateSkillBundle(ref, bundle) {
		t.Fatal("cache verifier accepted changed imported content")
	}
}

func TestPrivateLibraryHasNoSharedCatalogFallback(t *testing.T) {
	resolver := &service.TaskService{}
	bundles, err := resolver.LoadRequestedAgentSkillBundles(context.Background(), pgtype.UUID{}, []service.AgentSkillBundleRef{{ID: "private_object1", Source: libraryimport.SourcePrivate}})
	if err != nil || len(bundles) != 0 {
		t.Fatalf("private request entered shared catalog resolution: bundles=%v err=%v", bundles, err)
	}
}
