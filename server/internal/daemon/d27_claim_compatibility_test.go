package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// This server transport is a synthetic compatibility peer, not a DB handler.
// It uses the real builtin and bundle builders to retain the original failure.
func d27SyntheticClaim(t *testing.T, restricted, refs bool, assigned []service.AgentSkillData) (*Daemon, *Task) {
	t.Helper()
	d := New(Config{DaemonID: "daemon", BundleReadOnly: restricted, BundleWorkspaceID: "workspace", WorkspacesRoot: t.TempDir(), ServerBaseURL: "https://fixture.invalid"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "fixture.invalid" {
			t.Fatalf("unexpected synthetic request: %s", r.URL)
		}
		if r.URL.Path == "/api/daemon/register" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"runtimes":[{"id":"runtime","provider":"claude"}]}`)), Header: make(http.Header)}, nil
		}
		if r.URL.Path == "/api/daemon/heartbeat" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"runtime_id":"runtime","status":"ok","server_capabilities":["restricted-brief-claims-v1"]}`)), Header: make(http.Header)}, nil
		}
		if r.URL.Path != "/api/daemon/tasks/claim" {
			t.Fatalf("unexpected synthetic request: %s", r.URL)
		}
		skills := append([]service.AgentSkillData(nil), assigned...)
		if !strings.Contains(r.Header.Get("X-Client-Capabilities"), "restricted-brief-claims-v1") {
			skills = append(skills, (&service.TaskService{}).BuiltinSkills("", false)...)
		}
		task := bundleTestTask()
		task.RuntimeID = "runtime"
		wire, err := json.Marshal(task.Agent)
		if err != nil {
			t.Fatal(err)
		}
		var expanded map[string]any
		if err := json.Unmarshal(wire, &expanded); err != nil {
			t.Fatal(err)
		}
		if refs {
			_, bundles := service.BuildAgentSkillBundles(skills)
			expanded["skill_refs"] = bundles
		} else {
			expanded["skills"] = skills
		}
		wire, err = json.Marshal(expanded)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(wire, task.Agent); err != nil {
			t.Fatal(err)
		}
		wire, err = json.Marshal(map[string]any{"tasks": []Task{task}})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(wire))), Header: make(http.Header)}, nil
	})
	if _, err := d.client.Register(context.Background(), map[string]any{"daemon_id": "daemon"}); err != nil {
		t.Fatal(err)
	}
	tasks, err := d.client.ClaimTasks(context.Background(), "daemon", []string{"runtime"}, 1)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("synthetic claim: %v tasks=%d", err, len(tasks))
	}
	return d, tasks[0]
}

func TestD27RestrictedClaimImplicitBuiltin(t *testing.T) {
	for _, refs := range []bool{false, true} {
		name := "inline"
		if refs {
			name = "refs"
		}
		t.Run(name, func(t *testing.T) {
			d, task := d27SyntheticClaim(t, true, refs, nil)
			if err := d.validateBundleTask(*task, "claude"); err != nil {
				t.Fatalf("clean restricted claim carries server-added skills (inline=%d refs=%d): %v", len(task.Agent.Skills), len(task.Agent.SkillRefs), err)
			}
		})
	}
}

func TestD27ClaimSkillMatrix(t *testing.T) {
	for _, refs := range []bool{false, true} {
		for _, restricted := range []bool{false, true} {
			for _, attached := range []bool{false, true} {
				t.Run(fmt.Sprintf("refs=%t/restricted=%t/attached=%t", refs, restricted, attached), func(t *testing.T) {
					var skills []service.AgentSkillData
					if attached {
						skills = []service.AgentSkillData{{ID: "assigned", Name: "assigned", Content: "synthetic skill"}}
					}
					d, task := d27SyntheticClaim(t, restricted, refs, skills)
					count := len(task.Agent.Skills) + len(task.Agent.SkillRefs)
					if restricted && !attached && count != 0 {
						t.Fatal("implicit builtin retained")
					}
					if (!restricted || attached) && count == 0 {
						t.Fatal("required skills stripped")
					}
					if restricted && attached && d.validateBundleTask(*task, "claude") == nil {
						t.Fatal("assigned skill accepted")
					}
				})
			}
		}
	}
}

func d27AckClient(t *testing.T) (*Client, *time.Time) {
	t.Helper()
	d := New(Config{DaemonID: "daemon", BundleReadOnly: true, WorkspacesRoot: t.TempDir(), ServerBaseURL: "https://fixture.invalid"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	d.client.restricted.now = func() time.Time { return now }
	d.client.restricted.runtimes = map[string]bool{"runtime": true}
	return d.client, &now
}
func d27Ack() *HeartbeatResponse {
	return &HeartbeatResponse{RuntimeID: "runtime", Status: "ok", ServerCapabilities: []string{protocol.DaemonCapabilityRestrictedBriefClaimsV1}}
}

func TestD27AckFreshnessAndTransportBinding(t *testing.T) {
	c, now := d27AckClient(t)
	httpCapture := c.restrictedCapture(false)
	if c.requireRestrictedAck(httpCapture, []string{"runtime"}) == nil {
		t.Fatal("missing ACK allowed")
	}
	if !c.acceptRestrictedAck(httpCapture, "runtime", d27Ack()) {
		t.Fatal("fresh ACK rejected")
	}
	if c.requireRestrictedAck(httpCapture, []string{"foreign"}) == nil {
		t.Fatal("foreign runtime allowed")
	}
	*now = now.Add(c.restricted.freshness)
	if c.requireRestrictedAck(httpCapture, []string{"runtime"}) == nil {
		t.Fatal("stale ACK allowed")
	}
	wsCapture := c.beginRestrictedWS()
	if c.requireRestrictedAck(httpCapture, []string{"runtime"}) == nil {
		t.Fatal("old HTTP epoch allowed")
	}
	if !c.acceptRestrictedAck(wsCapture, "runtime", d27Ack()) {
		t.Fatal("WS ACK rejected")
	}
	if c.requireRestrictedAck(c.restrictedCapture(false), []string{"runtime"}) == nil {
		t.Fatal("WS ACK authorizes HTTP")
	}
	retired := false
	if !c.bindRestrictedWS(wsCapture, func() { retired = true }) {
		t.Fatal("bind failed")
	}
	c.resetRestrictedRegistration()
	c.restricted.runtimes = map[string]bool{"runtime": true}
	if !retired {
		t.Fatal("reused UUID registration failed to retire WS")
	}
	if c.acceptRestrictedAck(wsCapture, "runtime", d27Ack()) {
		t.Fatal("delayed ACK relabeled")
	}
	c.endRestrictedWS(wsCapture)
	if c.requireRestrictedAck(c.restrictedCapture(true), []string{"runtime"}) == nil {
		t.Fatal("re-registration inherited ACK")
	}
}

func TestD27ObservedMissingAckBlocksClaim(t *testing.T) {
	c, _ := d27AckClient(t)
	claims := 0
	c.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/daemon/tasks/claim" {
			claims++
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"runtime_id":"runtime","status":"ok"}`))}, nil
	})
	if _, err := c.ClaimTasks(context.Background(), "daemon", []string{"runtime"}, 1); !errors.Is(err, errRestrictedClaimProtocol) {
		t.Fatalf("missing ACK: %v", err)
	}
	if claims != 0 {
		t.Fatal("observed incompatible server admitted claim")
	}
}

func TestD27CachedAckThenOldServerGuard(t *testing.T) {
	c, _ := d27AckClient(t)
	c.acceptRestrictedAck(c.restrictedCapture(false), "runtime", d27Ack())
	claims := 0
	task := bundleTestTask()
	task.Agent.Skills = []SkillData{{Name: "multica-platform", Content: "synthetic implicit builtin"}}
	c.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/daemon/tasks/claim" {
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		claims++
		body, _ := json.Marshal(map[string]any{"tasks": []Task{task}})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	tasks, err := c.ClaimTasks(context.Background(), "daemon", []string{"runtime"}, 1)
	if err != nil || len(tasks) != 1 || claims != 1 {
		t.Fatalf("cached ACK limitation not reproduced: %v", err)
	}
	d := &Daemon{cfg: Config{BundleReadOnly: true, BundleWorkspaceID: "workspace"}}
	if d.validateBundleTask(*tasks[0], "claude") == nil {
		t.Fatal("old-server skill payload passed pre-provider guard")
	}
}

func TestD27SingleClaimDropsRetiredRegistrationResponse(t *testing.T) {
	c, _ := d27AckClient(t)
	c.SetToken("mdt_synthetic_never_valid")
	c.acceptRestrictedAck(c.restrictedCapture(false), "runtime", d27Ack())
	c.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		c.resetRestrictedRegistration()
		body, _ := json.Marshal(map[string]any{"task": bundleTestTask()})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	task, err := c.ClaimTask(context.Background(), "runtime")
	if task != nil || !errors.Is(err, errRestrictedClaimProtocol) {
		t.Fatalf("retired claim delivered: taskPresent=%t err=%v", task != nil, err)
	}
}

func TestD27ProducerOnlyConfiguredRestriction(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		d := New(Config{BundleReadOnly: restricted, DaemonID: "daemon", WorkspacesRoot: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		r := httptest.NewRequest(http.MethodPost, "https://fixture.invalid", nil)
		d.client.setIdentityHeaders(r)
		expected := daemonHTTPClientCapabilities()
		if restricted {
			expected += "," + protocol.DaemonCapabilityRestrictedBriefClaimsV1
		}
		if r.Header.Get("X-Client-Capabilities") != expected {
			t.Fatal("HTTP capability producer drift")
		}
		expected = daemonClientCapabilities()
		if restricted {
			expected += "," + protocol.DaemonCapabilityRestrictedBriefClaimsV1
		}
		if d.client.clientCapabilities(true) != expected {
			t.Fatal("WS capability producer drift")
		}
	}
}

func TestD27AckInvalidationMatrix(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-runtime", "status", "unknown-cap", "gone", "origin"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := d27AckClient(t)
			capture := c.restrictedCapture(false)
			c.acceptRestrictedAck(capture, "runtime", d27Ack())
			ack := d27Ack()
			switch mode {
			case "missing":
				ack.ServerCapabilities = nil
			case "wrong-runtime":
				ack.RuntimeID = "foreign"
			case "status":
				ack.Status = "unknown"
			case "unknown-cap":
				ack.ServerCapabilities = []string{"restricted-brief-claims-v2"}
			case "gone":
				ack.RuntimeGone = true
			case "origin":
				c.baseURL = "https://replacement.invalid"
			}
			c.acceptRestrictedAck(capture, "runtime", ack)
			if c.requireRestrictedAck(c.restrictedCapture(false), []string{"runtime"}) == nil {
				t.Fatal("invalid ACK retained readiness")
			}
		})
	}
}

func TestD27DelayedHTTPHeartbeatCannotActivateNewRegistration(t *testing.T) {
	c, _ := d27AckClient(t)
	c.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
		c.resetRestrictedRegistration()
		c.restricted.runtimes = map[string]bool{"runtime": true}
		b, _ := json.Marshal(d27Ack())
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}, nil
	})
	if _, err := c.SendHeartbeat(context.Background(), "runtime"); err != nil {
		t.Fatal(err)
	}
	if c.requireRestrictedAck(c.restrictedCapture(false), []string{"runtime"}) == nil {
		t.Fatal("late HTTP ACK relabeled")
	}
}

func TestD27WSClaimRequiresExactTransportAck(t *testing.T) {
	c, _ := d27AckClient(t)
	capture := c.beginRestrictedWS()
	frame := func(daemonID, runtimeID string) []byte {
		b, _ := json.Marshal(map[string]any{"daemon_id": daemonID, "runtime_ids": []string{runtimeID}})
		p, _ := json.Marshal(protocol.RPCRequestPayload{Method: "tasks.claim", Body: b})
		f, _ := json.Marshal(protocol.Message{Payload: p})
		return f
	}
	if c.allowRestrictedWSFrame(capture, frame("daemon", "runtime")) == nil {
		t.Fatal("unacknowledged WS claim allowed")
	}
	c.acceptRestrictedAck(capture, "runtime", d27Ack())
	if err := c.allowRestrictedWSFrame(capture, frame("daemon", "runtime")); err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{frame("foreign", "runtime"), frame("daemon", "foreign"), []byte("malformed")} {
		if c.allowRestrictedWSFrame(capture, body) == nil {
			t.Fatal("invalid WS frame allowed")
		}
	}
	c.endRestrictedWS(capture)
	if c.allowRestrictedWSFrame(capture, frame("daemon", "runtime")) == nil {
		t.Fatal("retired WS frame allowed")
	}
	if _, err := c.claimTasksLegacy(context.Background(), []string{"runtime"}, 1); !errors.Is(err, errRestrictedClaimProtocol) {
		t.Fatal("restricted legacy fallback enabled")
	}
}

func TestD27ActualWSHandshakeAndReregistrationRetirement(t *testing.T) {
	upgraded := make(chan string, 1)
	peerDone := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(peerDone)
		upgraded <- r.Header.Get("X-Client-Capabilities")
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	d := New(Config{DaemonID: "daemon", BundleReadOnly: true, ServerBaseURL: srv.URL, WorkspacesRoot: t.TempDir(), HeartbeatInterval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.client.restricted.runtimes = map[string]bool{"runtime": true}
	changed, unsubscribe := d.runtimeSet.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wakes := make(chan taskWakeup, 4)
	done := make(chan error, 1)
	go func() { _, err := d.runTaskWakeupConnection(ctx, []string{"runtime"}, wakes, changed); done <- err }()
	select {
	case caps := <-upgraded:
		if !strings.Contains(caps, protocol.DaemonCapabilityRestrictedBriefClaimsV1) {
			t.Fatal("WS handshake omitted restriction")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WS handshake timeout")
	}
	select {
	case <-wakes:
	case <-time.After(3 * time.Second):
		t.Fatal("WS bind timeout")
	}
	old := d.client.restrictedCapture(true)
	d.client.resetRestrictedRegistration()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("re-registration failed to retire connection")
	}
	select {
	case <-peerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("old peer still open")
	}
	d.client.restricted.runtimes = map[string]bool{"runtime": true}
	if d.client.acceptRestrictedAck(old, "runtime", d27Ack()) {
		t.Fatal("old connection ACK activated reused UUID")
	}
}

func TestD27PollerRejectsBufferedWSCompletionAfterInvalidation(t *testing.T) {
	for _, restricted := range []bool{true, false} {
		for _, mode := range []string{"retired", "missing", "stale", "fresh"} {
			t.Run(fmt.Sprintf("restricted=%t/%s", restricted, mode), func(t *testing.T) {
				d := New(Config{DaemonID: "daemon", BundleReadOnly: restricted, BundleWorkspaceID: "workspace", ServerBaseURL: "https://fixture.invalid", WorkspacesRoot: t.TempDir(), MaxConcurrentTasks: 1, PollInterval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
				d.workspaces["workspace"] = &workspaceState{workspaceID: "workspace", runtimeIDs: []string{"runtime"}}
				d.runtimeIndex["runtime"] = Runtime{ID: "runtime", Provider: "claude"}
				d.cancelPollInterval = time.Hour
				now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
				if restricted {
					d.client.restricted.now = func() time.Time { return now }
					d.client.restricted.runtimes = map[string]bool{"runtime": true}
				}
				capture := d.client.beginRestrictedWS()
				d.client.acceptRestrictedAck(capture, "runtime", d27Ack())
				task := bundleTestTask()
				if restricted {
					if err := d.validateBundleTask(task, "claude"); err != nil {
						t.Fatal(err)
					}
				}
				var httpCalls, runnerCalls atomic.Int32
				d.client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
					httpCalls.Add(1)
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				})
				releaseRunner := make(chan struct{})
				runnerStarted := make(chan struct{}, 1)
				d.runner = taskRunnerFunc(func(ctx context.Context, _ Task, _ string, _ int, _ *slog.Logger) (TaskResult, error) {
					runnerCalls.Add(1)
					runnerStarted <- struct{}{}
					<-releaseRunner
					return TaskResult{Status: "completed"}, nil
				})
				buffered := make(chan struct{})
				generation := d.wsRPC.attach(func(frame []byte) (*wsOutbound, error) {
					if err := d.client.allowRestrictedWSFrame(capture, frame); err != nil {
						return nil, err
					}
					var message protocol.Message
					var request protocol.RPCRequestPayload
					if json.Unmarshal(frame, &message) != nil || json.Unmarshal(message.Payload, &request) != nil {
						return nil, errors.New("invalid synthetic frame")
					}
					body, _ := json.Marshal(map[string]any{"tasks": []Task{task}})
					item := &wsOutbound{data: frame}
					item.beginWrite()
					d.wsRPC.deliver(protocol.RPCResponsePayload{RequestID: request.RequestID, Status: 200, Body: body})
					// Invalidate only after success is buffered, before Call can receive it.
					switch mode {
					case "retired":
						d.client.endRestrictedWS(capture)
						d.wsRPC.attach(nil)
					case "missing":
						ack := d27Ack()
						ack.ServerCapabilities = nil
						d.client.acceptRestrictedAck(capture, "runtime", ack)
					case "stale":
						now = now.Add(31 * time.Second)
					}
					close(buffered)
					return item, nil
				})
				d.wsRPC.markRPCV1Supported(generation)
				ctx, cancel := context.WithCancel(context.Background())
				var taskWG sync.WaitGroup
				done := make(chan struct{})
				sem := newTaskSlotSemaphore(1)
				go func() { defer close(done); d.runBatchPoller(ctx, ctx, sem, make(chan struct{}, 1), &taskWG) }()
				defer func() { cancel(); <-done; close(releaseRunner); taskWG.Wait() }()
				select {
				case <-buffered:
				case <-time.After(3 * time.Second):
					t.Fatal("RPC response was not buffered")
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					d.claimMu.Lock()
					inFlight := d.claimsInFlight
					d.claimMu.Unlock()
					if inFlight == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("poller did not finish claim cycle")
					}
					time.Sleep(time.Millisecond)
				}
				blocked := restricted && mode != "fresh"
				if !blocked || d.activeTasks.Load() != 0 {
					select {
					case <-runnerStarted:
					case <-time.After(3 * time.Second):
						t.Fatal("dispatch control did not enter synthetic runner")
					}
				}
				if blocked && (d.activeTasks.Load() != 0 || runnerCalls.Load() != 0) {
					t.Fatalf("invalidated WS completion dispatched: active=%d runnerCalls=%d", d.activeTasks.Load(), runnerCalls.Load())
				}
				if !blocked && runnerCalls.Load() != 1 {
					t.Fatal("fresh/standard dispatch changed")
				}
				if httpCalls.Load() != 0 {
					t.Fatalf("unexpected task callback or HTTP fallback before dispatch decision: %d", httpCalls.Load())
				}
				if blocked && len(sem) != 1 {
					t.Fatal("rejected completion retained execution slot")
				}
			})
		}
	}
}
