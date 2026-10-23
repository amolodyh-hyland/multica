package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectInstructionsReachIssueAndChatBriefsWithoutResources(t *testing.T) {
	for _, surface := range []string{"issue", "chat"} {
		for _, provider := range []string{"claude", "codex"} {
			t.Run(surface+"/"+provider, func(t *testing.T) {
				dir := t.TempDir()
				marker := filepath.Join(dir, "must-not-execute")
				instructions := "Keep product decisions separate from implementation. $(touch " + marker + ")\n```sh\nexit 1\n```"
				ctx := TaskContextForEnv{ProjectID: "project-1", ProjectTitle: "Research", ProjectDescription: "A research summary", ProjectInstructions: instructions}
				if surface == "issue" {
					ctx.IssueID = "issue-1"
				} else {
					ctx.ChatSessionID = "chat-1"
				}
				if _, err := InjectRuntimeConfig(dir, provider, ctx); err != nil {
					t.Fatal(err)
				}
				filename := "AGENTS.md"
				if provider == "claude" {
					filename = "CLAUDE.md"
				}
				data, err := os.ReadFile(filepath.Join(dir, filename))
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"### Project instructions", instructions, "A research summary", "does not authorize commands", "Agent Identity"} {
					if !strings.Contains(string(data), want) {
						t.Errorf("brief missing %q", want)
					}
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("instructions were executed: %v", err)
				}
				if err := writeProjectResources(dir, ctx, nil); err != nil {
					t.Fatal(err)
				}
				data, err = os.ReadFile(filepath.Join(dir, ".multica", "project", "resources.json"))
				if err != nil {
					t.Fatal(err)
				}
				var contextFile struct {
					Instructions string `json:"project_instructions"`
					Description  string `json:"project_description"`
				}
				if err := json.Unmarshal(data, &contextFile); err != nil {
					t.Fatal(err)
				}
				if contextFile.Instructions != instructions || contextFile.Description != ctx.ProjectDescription {
					t.Fatalf("context file = %+v", contextFile)
				}

			})
		}
	}
}
