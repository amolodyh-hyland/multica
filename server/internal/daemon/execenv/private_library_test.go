package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
)

func privateProjectionSnapshot(t *testing.T, kind, content string) libraryimport.TaskSnapshot {
	t.Helper()
	entryPoint, format := "SKILL.md", "native-skill-text-v1"
	if kind == "rule" {
		entryPoint, format = "rule.md", "plain-rule-v1"
	}
	entry := libraryimport.Entry{SchemaVersion: 1, EntryID: "entry1", SourceID: "source1", Kind: kind, DisplayName: "Friendly display label", Format: format, EntryPoint: entryPoint, Files: []libraryimport.File{{Path: entryPoint, Mode: "text", SizeBytes: int64(len(content)), SHA256: knowledge.Sum([]byte(content))}}, Requirements: []string{}, Provenance: libraryimport.Provenance{OriginAuthorityID: "origin1", OriginSourceID: "source1", OriginEntryID: "entry1", OriginKind: "machine_local", ResolverID: "resolver1", SourceRevision: 1, AdapterVersion: "1", Verification: "observed"}}
	var err error
	entry.ContentHash, err = knowledge.ContentHash(kind, format, entryPoint, []knowledge.TextFile{{Path: entryPoint, Mode: "text", SizeBytes: len(content), SHA256: entry.Files[0].SHA256, Content: content}})
	if err != nil {
		t.Fatal(err)
	}
	payload := libraryimport.Payload{Entry: entry, Content: map[string]string{entryPoint: content}}
	bundle := payload.Bundle("object1")
	projectDigest, err := libraryimport.ProjectContextDigest("project1", "", "", "", knowledge.Sum([]byte("context")), nil)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := (libraryimport.Selection{SchemaVersion: 1, SnapshotID: "snapshot1", TaskID: "task1", Destination: libraryimport.Destination{ServerID: "server1", Origin: "https://example.test", WorkspaceID: "workspace1", OwnerID: "owner1", Audience: "owner_and_explicit_task"}, Binding: libraryimport.Binding{ProjectID: "project1", TaskID: "task1", RuntimeID: "runtime1"}, PersonaID: "persona1", ContextDigest: knowledge.Sum([]byte("context")), ProjectContextDigest: projectDigest, Provider: "claude", AdapterVersion: libraryimport.TaskAdapterVersion, Imports: []libraryimport.SelectedImport{{ObjectID: bundle.ID, PreviewDigest: knowledge.Sum([]byte("consent")), TransportHash: bundle.Hash, Entry: entry}}}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	return libraryimport.TaskSnapshot{Selection: selected, Payloads: []libraryimport.Payload{payload}}
}

func TestPrivateLibraryStagingPreservesBytesAndReturnsActualBinding(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: review\ndescription: Review selected files\n---\nReview exactly these files.\r\n"
	snapshot := privateProjectionSnapshot(t, "skill", content)
	manifest := &sidecarManifest{}
	bindings, err := stagePrivateLibrary(dir, "claude", snapshot, manifest)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("stage: %+v, %v", bindings, err)
	}
	binding := bindings[0]
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Provider readiness remains unavailable even after exact materialization.
	if binding.NativeName != "review" || binding.Path != filepath.Join(canonical, ".claude", "skills", "review", "SKILL.md") || binding.Generation != manifest.Generation || binding.Status == "ready" || binding.Invocation != "unsupported" {
		t.Fatalf("incorrect binding: %+v", binding)
	}
	data, err := os.ReadFile(binding.Path)
	if err != nil || string(data) != content {
		t.Fatalf("imported bytes rewritten: %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(binding.Path)
		if err != nil || info.Mode().Perm() != 0400 {
			t.Fatalf("mutable materialization: %v, %v", info, err)
		}
	}
	if err := rollBackPreparedSidecars(*manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binding.Path); !os.IsNotExist(err) {
		t.Fatal("owned materialization was not reclaimed")
	}
}

func TestPrivateLibraryRejectsNativeCollisionsAndExecutableSemantics(t *testing.T) {
	valid := "---\nname: review\ndescription: Review changes\n---\nText only.\n"
	for _, scenario := range []string{"skill collision", "command collision", "missing frontmatter", "hooks", "preprocessing", "unsupported provider"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			content := valid
			provider := "claude"
			switch scenario {
			case "skill collision":
				if err := os.MkdirAll(filepath.Join(dir, ".claude", "skills", "review"), 0700); err != nil {
					t.Fatal(err)
				}
			case "command collision":
				commands := filepath.Join(dir, ".claude", "commands")
				if err := os.MkdirAll(commands, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(commands, "review.md"), []byte("user"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing frontmatter":
				content = "Text only."
			case "hooks":
				content = strings.Replace(valid, "description:", "hooks: {}\ndescription:", 1)
			case "preprocessing":
				content += "!`touch must-not-execute`"
			case "unsupported provider":
				provider = "codex"
			}
			manifest := &sidecarManifest{}
			if _, err := stagePrivateLibrary(dir, provider, privateProjectionSnapshot(t, "skill", content), manifest); err == nil {
				t.Fatal("unsafe native projection accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "review-multica", "SKILL.md")); !os.IsNotExist(err) {
				t.Fatal("collision silently renamed immutable frontmatter")
			}
		})
	}
}

func TestPrivateLibraryContextUsesReturnedBinding(t *testing.T) {
	snapshot := privateProjectionSnapshot(t, "rule", "Exact rule bytes.\r\n")
	bindings, err := stagePrivateLibrary(t.TempDir(), "claude", snapshot, &sidecarManifest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := TaskContextForEnv{ProjectID: "project1", PrivateLibrary: &snapshot, PrivateLibraryBindings: bindings}
	var brief strings.Builder
	writeProjectContext(&brief, ctx)
	if strings.Contains(brief.String(), "Exact rule bytes") || !strings.Contains(brief.String(), "native_conformance_missing") {
		t.Fatal("unready rule became active context")
	}
	// Formatting an already verified binding uses the returned native identity.
	ctx.PrivateLibraryBindings = []libraryimport.RuntimeBinding{{ObjectID: "object1", EntryID: "entry1", Status: "ready", NativeName: "actual-name", Path: "/owned/actual-name/SKILL.md", Invocation: "native_skill", SnapshotID: "snapshot1"}}
	brief.Reset()
	writeProjectContext(&brief, ctx)
	if !strings.Contains(brief.String(), "actual-name") || !strings.Contains(brief.String(), "/owned/actual-name/SKILL.md") || strings.Contains(brief.String(), "friendly-display-label") {
		t.Fatal("listing inferred an unallocated display slug")
	}
	ctx.PrivateLibraryBindings[0].Invocation = "context_only"
	brief.Reset()
	writeProjectContext(&brief, ctx)
	if !strings.Contains(brief.String(), "Exact rule bytes.\r\n") {
		t.Fatal("rule context changed approved bytes")
	}
}

func TestPrivateLibraryPreparationRefusesBeforeMutationWithoutEvidence(t *testing.T) {
	snapshot := privateProjectionSnapshot(t, "rule", "Private rule.\n")
	root := filepath.Join(t.TempDir(), "untouched")
	task := TaskContextForEnv{PrivateLibrary: &snapshot, PrivateProviderVersion: "2.1.292"}
	if _, err := Prepare(PrepareParams{WorkspacesRoot: root, WorkspaceID: "workspace1", TaskID: "task1", Provider: "claude", Task: task}, testLogger()); err == nil || err.Error() != "native_conformance_missing" {
		t.Fatalf("native loading enabled without evidence: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("unsupported preparation mutated storage")
	}
	workDir := t.TempDir()
	path := filepath.Join(workDir, "user.txt")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if Reuse(ReuseParams{WorkDir: workDir, Provider: "claude", Task: task}, testLogger()) != nil {
		t.Fatal("unsupported provider reused environment")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatal("refused reuse changed user file")
	}
}

func TestPrivateLibraryDoesNotConvertNativeScopedRules(t *testing.T) {
	snapshot := privateProjectionSnapshot(t, "rule", "---\npaths: [src/**]\n---\nScoped guidance.\n")
	if _, err := stagePrivateLibrary(t.TempDir(), "claude", snapshot, &sidecarManifest{}); err == nil || err.Error() != "unsupported_rule_format" {
		t.Fatalf("native matching semantics were discarded: %v", err)
	}
}
