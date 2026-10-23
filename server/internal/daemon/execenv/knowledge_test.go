package execenv

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

func TestKnowledgeReuseRequiresProjection(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		name := "Reuse"
		if isolated {
			name = "ReuseIsolated"
		}
		t.Run(name, func(t *testing.T) {
			for _, obstruction := range []string{"collision", "symlink", "blocked-directory", "none", "legacy-resource"} {
				t.Run(obstruction, func(t *testing.T) {
					work := filepath.Join(t.TempDir(), "work")
					project := filepath.Join(work, ".multica", "project")
					if err := os.MkdirAll(project, 0700); err != nil {
						t.Fatal(err)
					}
					outside := filepath.Join(t.TempDir(), "resources.json")
					if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
					target := filepath.Join(project, "resources.json")
					switch obstruction {
					case "collision", "legacy-resource":
						if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
							t.Fatal(err)
						}
					case "symlink":
						if err := os.Symlink(outside, target); err != nil {
							t.Skip(err)
						}
					case "blocked-directory":
						if err := os.Remove(project); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(project, []byte("keep"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					task := TaskContextForEnv{ProjectID: "project1", KnowledgeSnapshot: &knowledge.Snapshot{Selection: knowledge.Selection{SnapshotID: "snapshot1"}, Digest: "digest1"}}
					if obstruction == "legacy-resource" {
						task.KnowledgeSnapshot = nil
					}
					params := ReuseParams{WorkDir: work, Provider: "claude", Task: task}
					logger := slog.New(slog.NewTextHandler(io.Discard, nil))
					var env *Environment
					if isolated {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						var err error
						env, err = ReuseIsolated(ctx, preparationHelperTestCommand(), params, logger)
						if err != nil {
							t.Fatal(err)
						}
					} else {
						env = Reuse(params, logger)
					}
					wantUsable := obstruction == "none" || obstruction == "legacy-resource"
					if (env != nil) != wantUsable {
						t.Fatalf("usable environment = %v, want %v", env != nil, wantUsable)
					}
					if obstruction == "none" {
						raw, err := os.ReadFile(target)
						if err != nil || !strings.Contains(string(raw), "snapshot1") || !strings.Contains(string(raw), "digest1") {
							t.Fatalf("missing verified knowledge: %s, %v", raw, err)
						}
					}
					if obstruction == "collision" || obstruction == "legacy-resource" {
						raw, err := os.ReadFile(target)
						if err != nil || string(raw) != "keep" {
							t.Fatalf("occupied context changed: %s, %v", raw, err)
						}
					}
					raw, err := os.ReadFile(outside)
					if err != nil || string(raw) != "keep" {
						t.Fatalf("outside file changed: %s, %v", raw, err)
					}
				})
			}
		})
	}
}

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
