package daemon

import (
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDelegationPromptExecutesBrief(t *testing.T) {
	task := Task{DelegationID: "delegation", DelegationTitle: "Review transaction", DelegationInstructions: "Check the admission race without changing issues."}
	prompt := BuildPrompt(task, "codex")
	for _, want := range []string{task.DelegationID, task.DelegationTitle, task.DelegationInstructions} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	for _, forbidden := range []string{"multica issue create", "multica issue get", "Your assigned issue ID"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("delegation inherited issue semantics: %s", prompt)
		}
	}
	if !slices.Contains(daemonCommonCapabilities(), protocol.DaemonCapabilityPublicDelegationV1) {
		t.Fatal("delegation support not advertised")
	}
}
