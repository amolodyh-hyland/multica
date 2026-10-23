package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func bundleTestTask() Task {
	return Task{ID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", DelegationID: "delegation", Agent: &AgentData{
		RuntimeConfig: json.RawMessage(`{"local_directory_mode":"none","permission_mode":"read_only"}`),
	}}
}

func TestBundleReadOnlyTaskPolicy(t *testing.T) {
	d := &Daemon{cfg: Config{BundleReadOnly: true, BundleWorkspaceID: "workspace"}}
	for _, provider := range []string{"claude", "codex"} {
		if bundleProviderSupported(provider) {
			if err := d.validateBundleTask(bundleTestTask(), provider); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Task)
	}{
		{"missing configuration", func(task *Task) { task.Agent.RuntimeConfig = nil }},
		{"bypass", func(task *Task) {
			task.Agent.RuntimeConfig = json.RawMessage(`{"local_directory_mode":"none","permission_mode":"bypassPermissions"}`)
		}},
		{"worktree", func(task *Task) {
			task.Agent.RuntimeConfig = json.RawMessage(`{"local_directory_mode":"worktree","permission_mode":"read_only"}`)
		}},
		{"foreign workspace", func(task *Task) { task.WorkspaceID = "other" }},
		{"repository", func(task *Task) { task.Repos = []RepoData{{URL: "https://fixture.invalid/repository"}} }},
		{"local directory binding", func(task *Task) { task.ProjectResources = []ProjectResourceData{{ResourceType: "local_directory"}} }},
		{"continue", func(task *Task) { task.PriorSessionID = "old" }},
		{"prior directory", func(task *Task) { task.PriorWorkDir = "/outside" }},
		{"custom arguments", func(task *Task) { task.Agent.CustomArgs = []string{"--dangerously-skip-permissions"} }},
		{"custom environment", func(task *Task) { task.Agent.CustomEnv = map[string]string{"CONFIG": "unsafe"} }},
		{"MCP", func(task *Task) { task.Agent.McpConfig = json.RawMessage(`{"mcpServers":{"unsafe":{"command":"sh"}}}`) }},
		{"ordinary task", func(task *Task) { task.DelegationID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := bundleTestTask()
			tc.mutate(&task)
			if err := d.validateBundleTask(task, "claude"); err == nil {
				t.Fatal("unsafe task accepted")
			}
		})
	}
	task := bundleTestTask()
	if _, err := d.runTask(context.Background(), task, "other", 0, nil); err == nil {
		t.Fatal("unsupported provider reached preparation")
	}
}

func TestBundleReadOnlyPersistedCeiling(t *testing.T) {
	stageFakeAgent(t)
	redirectTestHome(t, t.TempDir())
	t.Setenv("MULTICA_DAEMON_ID", "")
	t.Setenv("SHELL", "/nonexistent")
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{BundleReadOnly: true, WorkspaceID: "workspace"}, "restricted"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_DAEMON_READ_ONLY", "false")
	t.Setenv("MULTICA_DAEMON_PINNED", "false")
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "true")
	t.Setenv("MULTICA_DAEMON_AUTO_RELOAD", "true")
	cfg, err := LoadConfig(Overrides{Profile: "restricted", WorkspacesRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.BundleReadOnly || !cfg.RuntimePinned || cfg.BundleWorkspaceID != "workspace" || cfg.AutoUpdateEnabled || cfg.AutoReloadEnabled || cfg.MaxConcurrentTasks != 1 || cfg.GCEnabled {
		t.Fatal("persisted profile ceiling was relaxed")
	}
}

func TestBundleReadOnlyWorkspaceDiscovery(t *testing.T) {
	client := NewClient("https://fixture.invalid")
	calls := 0
	client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/workspaces") {
			t.Errorf("unexpected registration: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[{"id":"workspace","name":"Scratch"},{"id":"foreign","name":"Foreign"}]`))}, nil
	})
	d := New(Config{BundleReadOnly: true, BundleWorkspaceID: "workspace", WorkspacesRoot: t.TempDir()}, slog.Default())
	d.client = client
	d.workspaces["workspace"] = &workspaceState{runtimeIDs: []string{"runtime"}}
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(d.workspaces) != 1 {
		t.Fatal("discovered a foreign workspace")
	}
}

func TestBundleReadOnlyRunTaskNative(t *testing.T) {
	d, argsFile, cleanup := newLeaderReuseTestDaemon(t)
	defer cleanup()
	d.cfg.BundleReadOnly = true
	d.cfg.RuntimePinned = true
	d.cfg.Profile = "bundle-fixture"
	d.cfg.BundleWorkspaceID = "ws-leader"
	d.agentVersions = map[string]string{"claude": "2.1.259"}
	task := leaderReuseTestTask("restricted-task")
	task.IssueID = ""
	task.IsLeaderTask = false
	task.DelegationID = "delegation"
	task.Agent.RuntimeConfig = json.RawMessage(`{"local_directory_mode":"none","permission_mode":"read_only"}`)
	result, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("result=%+v", result)
	}
	bytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(bytes)
	if strings.Contains(args, "bypassPermissions") || strings.Contains(args, "dangerously-skip") || !strings.Contains(args, "--restricted\n") || !strings.Contains(args, "--permission-prompts\nnone\n") || !strings.Contains(args, "--tools\n\n") {
		t.Fatalf("unsafe effective launch:\n%s", args)
	}
}
