package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestBundlePinnedConfig(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("SHELL", "/nonexistent")
	t.Setenv("MULTICA_DAEMON_PINNED", "true")
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "true")
	t.Setenv("MULTICA_DAEMON_AUTO_RELOAD", "true")
	cfg, err := LoadConfig(Overrides{Profile: "bundle-test", WorkspacesRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RuntimePinned || cfg.AutoUpdateEnabled || cfg.AutoReloadEnabled {
		t.Fatalf("pin did not override update/reload: %+v", cfg)
	}
	t.Setenv("MULTICA_DAEMON_PINNED", "invalid")
	if _, err := LoadConfig(Overrides{WorkspacesRoot: t.TempDir()}); err == nil {
		t.Fatal("invalid pin accepted")
	}
}

func TestBundlePinnedRejectsServerUpdate(t *testing.T) {
	client := NewClient("https://fixture.invalid")
	var calls int
	client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/daemon/runtimes/runtime/update/update/result" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["status"] != "failed" || !strings.Contains(fmt.Sprint(body["error"]), "source-pinned") {
			t.Errorf("report = %#v", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})
	d := &Daemon{cfg: Config{RuntimePinned: true}, client: client, logger: slog.Default()}
	d.runUpdateFn = func(string) (string, error) { t.Fatal("pinned runtime tried to update"); return "", nil }
	d.handleUpdate(context.Background(), "runtime", &PendingUpdate{ID: "update", TargetVersion: "latest"})
	if calls != 1 || d.updating.Load() || d.pauseClaims {
		t.Fatalf("calls=%d updating=%t pauseClaims=%t", calls, d.updating.Load(), d.pauseClaims)
	}
}

func TestBundleDelegationLifecycle(t *testing.T) {
	for _, cancelTask := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelTask), func(t *testing.T) {
			var mu sync.Mutex
			state := "queued"
			var quiescent atomic.Bool
			client := NewClient("https://fixture.invalid")
			client.SetToken("disposable-token")
			client.SetVersion("fork-fixture")
			client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Host != "fixture.invalid" || r.Header.Get("Authorization") != "Bearer disposable-token" {
					t.Errorf("unexpected destination/auth")
				}
				if !strings.Contains(r.Header.Get("X-Client-Capabilities"), protocol.DaemonCapabilityPublicDelegationV1) {
					t.Error("real client did not advertise delegation capability")
				}
				response := `{}`
				switch r.URL.Path {
				case "/api/daemon/tasks/claim":
					if state != "queued" {
						t.Errorf("claim from %s", state)
					}
					var body struct {
						RuntimeIDs []string `json:"runtime_ids"`
						MaxTasks   int      `json:"max_tasks"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.RuntimeIDs) != 1 || body.RuntimeIDs[0] != "runtime" || body.MaxTasks != 1 {
						t.Errorf("claim = %+v, error=%v", body, err)
					}
					state = "dispatched"
					response = `{"tasks":[{"id":"task","runtime_id":"runtime","start_claim_supported":true,"dispatched_at":"2026-10-23T00:00:00Z","delegation_id":"delegation","delegation_title":"Synthetic brief","delegation_instructions":"Inspect synthetic data","agent":{"name":"fixture"}}]}`
				case "/api/daemon/tasks/task/start":
					if state != "dispatched" {
						t.Errorf("start from %s", state)
					}
					var claim map[string]any
					if err := json.NewDecoder(r.Body).Decode(&claim); err != nil || claim["runtime_id"] != "runtime" || claim["dispatched_at"] != "2026-10-23T00:00:00Z" {
						t.Errorf("start fence = %#v, error=%v", claim, err)
					}
					state = "running"
				case "/api/daemon/tasks/task/status":
					if cancelTask {
						state = "cancelled"
					}
					response = fmt.Sprintf(`{"status":%q}`, state)
				case "/api/daemon/tasks/task/progress":
					if state != "running" {
						t.Errorf("progress outside running: %s", state)
					}
				case "/api/daemon/tasks/task/complete":
					if state != "running" || !quiescent.Load() {
						t.Errorf("complete before settlement: %s", state)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["output"] != "synthetic result" {
						t.Errorf("complete body=%#v error=%v", body, err)
					}
					state = "completed"
				case "/api/daemon/tasks/task/cancel-ack":
					if state != "cancelled" || !quiescent.Load() {
						t.Errorf("cancel acknowledgement before settlement: %s", state)
					}
					state = "cancel-acknowledged"
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response)), Header: make(http.Header)}, nil
			})
			ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			tasks, err := client.ClaimTasks(ctx, "bundle", []string{"runtime"}, 1)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("claim tasks=%v error=%v", tasks, err)
			}
			d := &Daemon{client: client, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workspaces: make(map[string]*workspaceState), runtimeIndex: map[string]Runtime{"runtime": {ID: "runtime", Provider: "fixture"}}, cancelPollInterval: time.Millisecond}
			d.runner = taskRunnerFunc(func(runCtx context.Context, task Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
				if _, err := client.StartTask(runCtx, task); err != nil {
					return TaskResult{}, err
				}
				if cancelTask {
					<-runCtx.Done()
				}
				quiescent.Store(true)
				return TaskResult{Status: "completed", Comment: "synthetic result"}, nil
			})
			d.handleTask(ctx, *tasks[0], 0)
			mu.Lock()
			defer mu.Unlock()
			want := "completed"
			if cancelTask {
				want = "cancel-acknowledged"
			}
			if state != want {
				t.Fatalf("state=%s want=%s", state, want)
			}
		})
	}
}

func TestBundleHealthReportsPin(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		d := &Daemon{cfg: Config{RuntimePinned: pinned, Profile: "bundle-profile", CLICommit: strings.Repeat("a", 40)}}
		rec := httptest.NewRecorder()
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["runtime_pinned"] != pinned || body["profile"] != "bundle-profile" || body["cli_commit"] != strings.Repeat("a", 40) {
			t.Fatalf("health identity = %#v", body)
		}
	}
}
