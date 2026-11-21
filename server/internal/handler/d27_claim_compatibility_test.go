package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const d27Restriction = protocol.DaemonCapabilityRestrictedBriefClaimsV1

func d27Runtime(t *testing.T, restricted bool) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	agentID, rt, daemonID := createRuntimeGuardAgent(t, ctx)
	dbfx.Exec(t, `DELETE FROM agent WHERE id=$1`, agentID)
	a, i := createClaimReclaimAgentAndIssue(t, ctx, rt, "D27 synthetic claim")
	caps := `{"capabilities":["platform-skill-v1","skill-bundles-v1"]}`
	if restricted {
		caps = `{"capabilities":["restricted-brief-claims-v1","platform-skill-v1","skill-bundles-v1"]}`
	}
	dbfx.Exec(t, `UPDATE agent_runtime SET metadata=$2 WHERE id=$1`, rt, []byte(caps))
	_ = seedQueuedIssueTask(t, ctx, a, rt, i)
	return a, rt, daemonID
}

func TestD27ActualClaimSkillMatrix(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, refs := range []bool{false, true} {
			for _, restricted := range []bool{false, true} {
				for _, assigned := range []bool{false, true} {
					t.Run(fmt.Sprintf("ws=%t/refs=%t/restricted=%t/assigned=%t", ws, refs, restricted, assigned), func(t *testing.T) {
						a, rt, daemonID := d27Runtime(t, restricted)
						if assigned {
							skill := dbfx.Insert(t, "skill", testutil.Cols{"workspace_id": testWorkspaceID, "name": "d27-" + uuid.NewString(), "content": "synthetic assigned skill", "created_by": testUserID})
							dbfx.Exec(t, `INSERT INTO agent_skill(agent_id,skill_id) VALUES($1,$2)`, a, skill)
							dbfx.Cleanup(t, `DELETE FROM agent_skill WHERE agent_id=$1`, a)
						}
						caps := "platform-skill-v1"
						if refs {
							caps += ",skill-bundles-v1"
						}
						if restricted {
							caps += "," + d27Restriction
						}
						body := map[string]any{"daemon_id": daemonID, "runtime_ids": []string{rt}, "max_tasks": 1}
						var raw []byte
						if ws {
							b, _ := json.Marshal(body)
							status, data, err := testHandler.DaemonRPCHandler(context.Background(), daemonws.ClientIdentity{UserID: testUserID, WorkspaceID: testWorkspaceID, WorkspaceIDs: []string{testWorkspaceID}, RuntimeIDs: []string{rt}, Capabilities: caps}, "tasks.claim", b)
							if err != nil || status != 200 {
								t.Fatalf("WS claim %d %v: %s", status, err, data)
							}
							raw = data
						} else {
							req := newOwnerDaemonRequest("POST", "/api/daemon/tasks/claim", body)
							req.Header.Set("X-Client-Capabilities", caps)
							w := testutil.Call(t, testHandler.ClaimTasksByRuntime, req).Want(200)
							raw = w.Body.Bytes()
						}
						var result struct {
							Tasks []AgentTaskResponse `json:"tasks"`
						}
						if err := json.Unmarshal(raw, &result); err != nil || len(result.Tasks) != 1 {
							t.Fatalf("claim decode/count: %v %s", err, raw)
						}
						payload := result.Tasks[0]
						count := len(payload.Agent.Skills) + len(payload.Agent.SkillRefs)
						if restricted && !assigned && count != 0 {
							t.Fatalf("implicit skills=%d", count)
						}
						if (!restricted || assigned) && count == 0 {
							t.Fatal("skills were stripped")
						}
						if refs && len(payload.Agent.Skills) != 0 {
							t.Fatal("wrong inline encoding")
						}
						if !refs && len(payload.Agent.SkillRefs) != 0 {
							t.Fatal("wrong refs encoding")
						}
						if payload.AuthToken == "" {
							t.Fatal("delivery token absent")
						}
					})
				}
			}
		}
	}
}

func TestD27AdmissionRejectsModeSkew(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, mode := range []string{"absent", "unknown", "duplicate", "wrong-daemon", "foreign-owner"} {
			t.Run(fmt.Sprintf("ws=%t/%s", ws, mode), func(t *testing.T) {
				_, rt, daemonID := d27Runtime(t, true)
				caps := d27Restriction
				switch mode {
				case "absent":
					caps = ""
				case "unknown":
					caps = "restricted-brief-claims-v2"
				case "duplicate":
					caps += "," + caps
				case "wrong-daemon":
					daemonID = "foreign"
				case "foreign-owner":
					owner := dbfx.User(t, "D27 foreign", "d27-"+uuid.NewString()+"@example.invalid")
					dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=$2 WHERE id=$1`, rt, owner)
				}
				body := map[string]any{"daemon_id": daemonID, "runtime_ids": []string{rt}, "max_tasks": 1}
				status := 0
				var raw []byte
				var before string
				dbfx.QueryRow(t, `SELECT jsonb_build_object('task',to_jsonb(q),'assignee_id',i.assignee_id,'assignee_type',i.assignee_type)::text FROM agent_task_queue q JOIN issue i ON i.id=q.issue_id WHERE q.runtime_id=$1`, rt).Scan(&before)
				if ws {
					b, _ := json.Marshal(body)
					status, raw, _ = testHandler.DaemonRPCHandler(context.Background(), daemonws.ClientIdentity{UserID: testUserID, WorkspaceID: testWorkspaceID, WorkspaceIDs: []string{testWorkspaceID}, RuntimeIDs: []string{rt}, Capabilities: caps}, "tasks.claim", b)
				} else {
					r := newOwnerDaemonRequest("POST", "/api/daemon/tasks/claim", body)
					r.Header.Set("X-Client-Capabilities", caps)
					w := httptest.NewRecorder()
					testHandler.ClaimTasksByRuntime(w, r)
					status = w.Code
					raw = w.Body.Bytes()
				}
				if mode == "wrong-daemon" || mode == "foreign-owner" {
					var result struct {
						Tasks []json.RawMessage `json:"tasks"`
					}
					if status != http.StatusOK || json.Unmarshal(raw, &result) != nil || result.Tasks == nil || len(result.Tasks) != 0 {
						t.Fatalf("expected empty successful unauthorized poll: status=%d body=%s", status, raw)
					}
				} else if status < 400 {
					t.Fatalf("invalid contract admitted: %d", status)
				}
				var after string
				dbfx.QueryRow(t, `SELECT jsonb_build_object('task',to_jsonb(q),'assignee_id',i.assignee_id,'assignee_type',i.assignee_type)::text FROM agent_task_queue q JOIN issue i ON i.id=q.issue_id WHERE q.runtime_id=$1`, rt).Scan(&after)
				if before != after {
					t.Fatal("unauthorized poll changed task or issue assignment")
				}
				var tokens int
				dbfx.QueryRow(t, `SELECT count(*) FROM task_token token JOIN agent_task_queue q ON q.id=token.task_id WHERE q.runtime_id=$1`, rt).Scan(&tokens)
				if tokens != 0 {
					t.Fatal("unauthorized poll minted token")
				}
				var dispatched int
				dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE runtime_id=$1 AND status!='queued'`, rt).Scan(&dispatched)
				if dispatched != 0 {
					t.Fatal("invalid admission changed task")
				}
			})
		}
	}
}

func TestD27FinalLockedRuntimeFence(t *testing.T) {
	for _, change := range []string{"owner", "daemon", "mode"} {
		t.Run(change, func(t *testing.T) {
			_, rt, daemonID := d27Runtime(t, true)
			r := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/tasks/claim", nil)
			r.Header.Set("X-Client-Capabilities", d27Restriction)
			runtime, err := testHandler.Queries.GetAgentRuntimeForWorkspace(context.Background(), db.GetAgentRuntimeForWorkspaceParams{ID: parseUUID(rt), WorkspaceID: parseUUID(testWorkspaceID)})
			if err != nil {
				t.Fatal(err)
			}
			policy, err := claimRestrictionForRuntime(r, runtime, daemonID)
			if err != nil {
				t.Fatal(err)
			}
			task, err := testHandler.TaskService.ClaimTaskForRuntime(context.Background(), runtime.ID)
			if err != nil || task == nil {
				t.Fatalf("claim %v", err)
			}
			resp, ids, snapshot, _, _, failure := testHandler.buildClaimedTaskResponse(r, task, runtime, rt, testWorkspaceID, policy)
			if failure != nil {
				t.Fatalf("build: %+v", failure)
			}
			switch change {
			case "owner":
				owner := dbfx.User(t, "D27 replacement", "d27-new-"+uuid.NewString()+"@example.invalid")
				dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=$2 WHERE id=$1`, rt, owner)
			case "daemon":
				dbfx.Exec(t, `UPDATE agent_runtime SET daemon_id='replacement' WHERE id=$1`, rt)
			case "mode":
				dbfx.Exec(t, `UPDATE agent_runtime SET metadata='{}' WHERE id=$1`, rt)
			}
			_, failure, err = testHandler.finalizeClaimDelivery(context.Background(), task, runtime, rt, testWorkspaceID, &resp, db.CreateTaskTokenParams{ID: dbid.NewV7(), TokenHash: auth.HashToken("synthetic-never-delivered"), TaskID: task.ID, AgentID: task.AgentID, WorkspaceID: runtime.WorkspaceID, UserID: runtime.OwnerID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}, ids, false, snapshot, policy)
			if err != nil || failure == nil || failure.outcome != "error_restricted_claim_protocol" {
				t.Fatalf("fence err=%v failure=%+v", err, failure)
			}
			var tokens int
			dbfx.QueryRow(t, `SELECT count(*) FROM task_token WHERE task_id=$1`, task.ID).Scan(&tokens)
			if tokens != 0 {
				t.Fatal("fence minted token")
			}
		})
	}
}

func TestD27HTTPHeartbeatAckProjection(t *testing.T) {
	for _, mode := range []string{"restricted", "absent", "unknown", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			_, runtimeID, _ := createRuntimeGuardAgent(t, context.Background())
			r := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/heartbeat", map[string]any{"runtime_id": runtimeID, "supports_batch_import": true})
			switch mode {
			case "restricted", "unauthorized":
				r.Header.Set("X-Client-Capabilities", d27Restriction)
			case "unknown":
				r.Header.Set("X-Client-Capabilities", "restricted-brief-claims-v2")
			}
			if mode == "unauthorized" {
				r.Header.Set("X-User-ID", uuid.NewString())
			}
			w := httptest.NewRecorder()
			testHandler.DaemonHeartbeat(w, r)
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			_, hasRuntime := body["runtime_id"]
			_, hasCaps := body["server_capabilities"]
			if mode == "unauthorized" {
				if w.Code < 400 || hasRuntime || hasCaps {
					t.Fatalf("unauthorized ACK status=%d runtime=%t caps=%t", w.Code, hasRuntime, hasCaps)
				}
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("heartbeat status=%d", w.Code)
			}
			if mode != "restricted" {
				if hasRuntime || hasCaps {
					t.Fatal("ordinary HTTP shape changed")
				}
				var status string
				_ = json.Unmarshal(body["status"], &status)
				if status != "ok" {
					t.Fatal("ordinary status changed")
				}
				return
			}
			var actualID string
			var caps []string
			if json.Unmarshal(body["runtime_id"], &actualID) != nil || actualID != runtimeID {
				t.Fatal("negotiated ACK lacks exact authenticated runtime")
			}
			if json.Unmarshal(body["server_capabilities"], &caps) != nil {
				t.Fatal("negotiated ACK lacks server capabilities")
			}
			found := false
			for _, capability := range caps {
				found = found || capability == d27Restriction
			}
			if !found {
				t.Fatal("negotiated support missing")
			}
		})
	}
}
