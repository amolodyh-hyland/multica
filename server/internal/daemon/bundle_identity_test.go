package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestBundleIdentityDoesNotRecoverForeignTasks(t *testing.T) {
	stageFakeAgent(t)
	home := t.TempDir()
	redirectTestHome(t, home)
	t.Setenv("SHELL", "/nonexistent")
	t.Setenv("MULTICA_DAEMON_ID", "")
	const foreignID = "11111111-1111-1111-1111-111111111111"
	root := filepath.Join(home, ".multica")
	if err := os.MkdirAll(filepath.Join(root, "profiles", "old"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "daemon.id"), filepath.Join(root, "profiles", "old", "daemon.id")} {
		if err := os.WriteFile(path, []byte(foreignID), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{BundleReadOnly: true, WorkspaceID: "workspace"}, "bundle"); err != nil {
		t.Fatal(err)
	}
	// A leftover old-style ID in this profile must not be promoted either.
	if err := os.WriteFile(filepath.Join(root, "profiles", "bundle", "daemon.id"), []byte(foreignID), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(Overrides{Profile: "bundle", WorkspacesRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DaemonID == foreignID || len(cfg.LegacyDaemonIDs) != 0 {
		t.Fatalf("unsafe identity/aliases: %s %v", cfg.DaemonID, cfg.LegacyDaemonIDs)
	}
	again, err := LoadConfig(Overrides{Profile: "bundle", WorkspacesRoot: cfg.WorkspacesRoot})
	if err != nil || again.DaemonID != cfg.DaemonID {
		t.Fatalf("identity not stable: %s %v", again.DaemonID, err)
	}
	legacy, err := LegacyDaemonUUIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range legacy {
		if id == cfg.DaemonID {
			t.Fatal("ordinary daemon would adopt bundle identity")
		}
	}

	// Same backend, workspace and provider: mimic the real upsert key and
	// startup recovery. A shared UUID or migration alias would fail foreign work.
	newBatchFixture(t)
	cfg.Agents = map[string]AgentEntry{"claude": {Path: "/fake/claude"}}
	d := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	foreignTask := "running"
	var recovered []string
	d.client = NewClient("https://fixture.invalid")
	d.client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		response := `{}`
		switch r.URL.Path {
		case "/api/daemon/workspaces":
			response = `[{"id":"workspace","name":"Scratch"}]`
		case "/api/daemon/register":
			var req struct {
				DaemonID  string              `json:"daemon_id"`
				Legacy    []string            `json:"legacy_daemon_ids"`
				Workspace string              `json:"workspace_id"`
				Runtimes  []map[string]string `json:"runtimes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			runtimeID := "bundle-runtime"
			if req.DaemonID == foreignID {
				runtimeID = "foreign-runtime"
			}
			for _, id := range req.Legacy {
				if id == foreignID {
					runtimeID = "foreign-runtime"
				}
			}
			if req.Workspace != "workspace" || len(req.Runtimes) != 1 || req.Runtimes[0]["type"] != "claude" {
				t.Errorf("unexpected registration: %+v", req)
			}
			response = fmt.Sprintf(`{"runtimes":[{"id":%q,"provider":"claude","status":"online"}]}`, runtimeID)
		case "/api/daemon/runtimes/bundle-runtime/recover-orphans":
			recovered = append(recovered, "bundle-runtime")
		case "/api/daemon/runtimes/foreign-runtime/recover-orphans":
			foreignTask = "failed"
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	if err := d.syncWorkspacesFromAPI(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if foreignTask != "running" || len(recovered) != 1 || len(d.allRuntimeIDs()) != 1 || d.allRuntimeIDs()[0] != "bundle-runtime" {
		t.Fatalf("foreign task=%s recovered=%v runtimes=%v", foreignTask, recovered, d.allRuntimeIDs())
	}
	data, err := os.ReadFile(filepath.Join(root, "daemon.id"))
	if err != nil || string(data) != foreignID {
		t.Fatal("machine identity changed")
	}
	t.Setenv("MULTICA_DAEMON_ID", foreignID)
	if _, err := LoadConfig(Overrides{Profile: "bundle"}); err == nil {
		t.Fatal("accepted foreign identity override")
	}
	t.Setenv("MULTICA_DAEMON_ID", "")
	if _, err := LoadConfig(Overrides{Profile: "bundle", DaemonID: foreignID}); err == nil {
		t.Fatal("accepted explicit identity override")
	}
}

func TestBundleIdentityConcurrentAndCorrupt(t *testing.T) {
	redirectTestHome(t, t.TempDir())
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for range 20 {
		wg.Go(func() {
			id, err := ensureBundleDaemonID("bundle")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent startup replaced identity")
		}
	}
	other, err := ensureBundleDaemonID("other")
	if err != nil || other == first {
		t.Fatal("distinct profiles collided")
	}
	dir, err := cli.ProfileDir("bundle")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, bundleDaemonIDFileName)
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBundleDaemonID("bundle"); err == nil {
		t.Fatal("silently rotated corrupt identity")
	}
}

func TestBundleProbeExclusionsConcurrentWithFailures(t *testing.T) {
	fx := newBatchFixture(t)
	d := fx.daemon
	d.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	d.cfg.BundleReadOnly = true
	d.cfg.Agents = map[string]AgentEntry{"claude": {Path: "/fake/claude"}, "codex": {Path: "/fake/codex"}}
	for i := range 300 {
		d.cfg.Agents[fmt.Sprintf("excluded-%d", i)] = AgentEntry{Path: "/fake/unsupported"}
	}
	fx.setProbeVersion("0.0.1") // supported probes and exclusions both write skipped
	for range 100 {
		d.detectBuiltinRuntimes(context.Background())
		skipped := d.skippedAgentsSnapshot()
		if len(skipped) != len(d.cfg.Agents) {
			t.Fatalf("incomplete exclusions: %d", len(skipped))
		}
	}
}
