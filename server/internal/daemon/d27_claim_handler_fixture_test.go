package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestD27FixtureRealClaimToOriginalValidator(t *testing.T) {
	dsn := os.Getenv("MULTICA_D27_FIXTURE_DSN")
	if dsn == "" {
		t.Skip("explicit owned D27 fixture DSN required")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "d27_") {
		t.Fatal("not an explicit loopback D27 fixture")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal("fixture pool configuration failed")
	}
	t.Cleanup(pool.Close)
	f := testutil.New(pool, "", "")
	user := f.User(t, "D27 synthetic owner", "d27-"+uuid.NewString()+"@example.invalid")
	workspace := f.Workspace(t, "D27 synthetic workspace", "d27-"+uuid.NewString())
	f.WorkspaceID = workspace
	f.UserID = user
	f.Member(t, workspace, user, "owner")
	hub := realtime.NewHub()
	go hub.Run()
	h := handler.New(db.NewBinding(pool), pool, hub, events.New(), service.NewEmailService(), nil, nil, analytics.NoopClient{}, handler.Config{})
	for _, ws := range []bool{false, true} {
		for _, refs := range []bool{false, true} {
			for _, restricted := range []bool{false, true} {
				for _, assigned := range []bool{false, true} {
					t.Run(fmt.Sprintf("ws=%t/refs=%t/restricted=%t/assigned=%t", ws, refs, restricted, assigned), func(t *testing.T) {
						daemonID := "d27-" + uuid.NewString()
						d := New(Config{DaemonID: daemonID, BundleReadOnly: restricted, BundleWorkspaceID: workspace, WorkspacesRoot: t.TempDir(), ServerBaseURL: "https://fixture.invalid"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
						caps := d.client.clientCapabilities(ws)
						if !refs {
							var selected []string
							for _, capability := range strings.Split(caps, ",") {
								if capability != "skill-bundles-v1" {
									selected = append(selected, capability)
								}
							}
							caps = strings.Join(selected, ",")
						}
						d.client.client.Transport = startTaskTransport(func(r *http.Request) (*http.Response, error) {
							if r.URL.Host != "fixture.invalid" {
								t.Fatal("unexpected fixture origin")
							}
							r.Header.Set("X-User-ID", user)
							r.Header.Set("X-Workspace-ID", workspace)
							// Select the legacy/current encoding independently of the restriction.
							r.Header.Set("X-Client-Capabilities", caps)
							w := httptest.NewRecorder()
							switch r.URL.Path {
							case "/api/daemon/register":
								h.DaemonRegister(w, r)
							case "/api/daemon/heartbeat":
								h.DaemonHeartbeat(w, r)
							case "/api/daemon/tasks/claim":
								h.ClaimTasksByRuntime(w, r)
							default:
								t.Fatalf("unexpected fixture route %s", r.URL.Path)
							}
							return w.Result(), nil
						})
						reg, err := d.client.Register(context.Background(), map[string]any{"workspace_id": workspace, "daemon_id": daemonID, "device_name": "synthetic", "runtimes": []map[string]string{{"name": "fixture", "type": "claude", "version": "test", "status": "online"}}})
						if err != nil || len(reg.Runtimes) != 1 {
							t.Fatalf("real registration failed: %v", err)
						}
						rt := reg.Runtimes[0].ID
						f.Cleanup(t, `DELETE FROM agent_runtime WHERE id=$1`, rt)
						var metadata []byte
						f.QueryRow(t, `SELECT metadata FROM agent_runtime WHERE id=$1`, rt).Scan(&metadata)
						var registered struct {
							Capabilities []string `json:"capabilities"`
						}
						if json.Unmarshal(metadata, &registered) != nil {
							t.Fatal("registration metadata invalid")
						}
						found := false
						for _, capability := range registered.Capabilities {
							found = found || capability == "restricted-brief-claims-v1"
						}
						if found != restricted {
							t.Fatal("registration restriction mismatch")
						}
						a := f.Agent(t, "D27 fixture profile", rt, testutil.Cols{"runtime_mode": "local", "runtime_config": testutil.Raw(`'{"local_directory_mode":"none","permission_mode":"read_only"}'::jsonb`)})
						if assigned {
							skill := f.Insert(t, "skill", testutil.Cols{"workspace_id": workspace, "name": "d27-" + uuid.NewString(), "created_by": user, "content": "synthetic assigned skill"})
							f.InsertNoID(t, "agent_skill", testutil.Cols{"agent_id": a, "skill_id": skill}, "agent_id=$1", a)
						}
						payload, _ := json.Marshal(map[string]any{"type": service.PublicDelegationContextType, "delegation_id": uuid.NewString(), "workspace_id": workspace, "task": map[string]string{"title": "Synthetic", "instructions": "Fixture only."}})
						f.Task(t, a, testutil.Cols{"runtime_id": rt, "context": payload, "originator_user_id": user, "accountable_user_id": user, "originator_source": "direct_human"})
						var tasks []*Task
						if ws {
							body, _ := json.Marshal(map[string]any{"daemon_id": daemonID, "runtime_ids": []string{rt}, "max_tasks": 1})
							status, raw, rpcErr := h.DaemonRPCHandler(context.Background(), daemonws.ClientIdentity{UserID: user, WorkspaceID: workspace, WorkspaceIDs: []string{workspace}, RuntimeIDs: []string{rt}, Capabilities: caps}, "tasks.claim", body)
							if rpcErr != nil || status != 200 {
								t.Fatalf("real WS claim: status=%d err=%v body=%s", status, rpcErr, raw)
							}
							var result struct {
								Tasks []*Task `json:"tasks"`
							}
							if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&result); err != nil {
								t.Fatal(err)
							}
							tasks = result.Tasks
						} else {
							tasks, err = d.client.ClaimTasks(context.Background(), daemonID, []string{rt}, 1)
							if err != nil {
								t.Fatal(err)
							}
						}
						if len(tasks) != 1 {
							t.Fatalf("real claim count=%d", len(tasks))
						}
						task := tasks[0]
						if task.DelegationID == "" || task.IssueID != "" || task.ChatSessionID != "" {
							t.Fatal("not a synthetic direct delegation")
						}
						count := len(task.Agent.Skills) + len(task.Agent.SkillRefs)
						if (!restricted || assigned) && count == 0 {
							t.Fatal("ordinary/assigned skills stripped")
						}
						if restricted {
							validationErr := d.validateBundleTask(*task, "claude")
							if assigned && (validationErr == nil || !strings.Contains(validationErr.Error(), "skills are unsupported")) {
								t.Fatalf("assigned skill guard not retained: %v", validationErr)
							}
							if !assigned && validationErr != nil {
								t.Fatalf("clean actual claim refused by original guard: %v", validationErr)
							}
						}
					})
				}
			}
		}
	}
}
