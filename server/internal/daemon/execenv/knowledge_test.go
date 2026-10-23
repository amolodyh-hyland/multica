package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

func TestKnowledgeContextIsDataOnly(t *testing.T) {
	dir := t.TempDir()
	snapshot := knowledge.Snapshot{Selection: knowledge.Selection{SnapshotID: "snapshot1"}, Digest: "digest1", Entries: []knowledge.Entry{{Files: []knowledge.TextFile{{Path: "SKILL.md", Content: "$(touch /tmp/must-not-execute)"}}}}}
	ctx := TaskContextForEnv{ProjectID: "project1", ProjectTitle: "Research", KnowledgeSnapshot: &snapshot}
	if err := writeProjectResources(dir, ctx, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".multica", "project", "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ".multica", "project", "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("knowledge context permissions = %v", info.Mode())
	}

	for _, expected := range []string{"snapshot1", "digest1", "$(touch /tmp/must-not-execute)", `"resources": []`} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("missing %s", expected)
		}
	}
	for _, p := range []string{".claude", ".agents", "SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Fatalf("knowledge installed as executable skill: %s", p)
		}
	}
	ctx.ProjectID = ""
	var brief strings.Builder
	writeProjectContext(&brief, ctx)
	if !strings.Contains(brief.String(), "snapshot1") {
		t.Fatal("retained knowledge disappeared from the brief after Project deletion")
	}

	if err := writeProjectResources(dir, ctx, &sidecarManifest{}); err == nil {
		t.Fatal("occupied context path silently ignored")
	}
}

func TestKnowledgeContextRejectsSymlinkProjection(t *testing.T) {
	for _, target := range []string{".multica", ".multica/project", ".multica/project/resources.json"} {
		t.Run(target, func(t *testing.T) {
			work := t.TempDir()
			outside := t.TempDir()
			victim := filepath.Join(outside, "resources.json")
			if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(work, target)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			destination := outside
			if strings.HasSuffix(target, "resources.json") {
				destination = victim
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Skip(err)
			}
			ctx := TaskContextForEnv{KnowledgeSnapshot: &knowledge.Snapshot{}}
			if err := writeProjectResources(work, ctx, nil); err == nil {
				t.Fatal("symlink projection accepted")
			}
			raw, err := os.ReadFile(victim)
			if err != nil || string(raw) != "keep" {
				t.Fatalf("outside file changed: %s, %v", raw, err)
			}
		})
	}
}
