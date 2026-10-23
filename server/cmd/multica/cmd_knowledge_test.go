package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestKnowledgeCLIRequiresHumanContextAndApproval(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("allow-read", false, "")
	cmd.Flags().Bool("approve-context", false, "")
	for _, key := range []string{"MULTICA_TASK_ID", "MULTICA_AGENT_ID", "MULTICA_DAEMON_PORT"} {
		t.Setenv(key, "")
	}
	if err := knowledgeRegisterCmd.RunE(cmd, []string{"file", "/source-never-read"}); err == nil || !strings.Contains(err.Error(), "--allow-read") {
		t.Fatalf("inspection proceeded without approval: %v", err)
	}
	t.Setenv("MULTICA_TASK_ID", "task1")
	if err := knowledgeRegisterCmd.RunE(cmd, []string{"file", "/source-never-read"}); err == nil || !strings.Contains(err.Error(), "inside a task") {
		t.Fatalf("task registered knowledge: %v", err)
	}
}
