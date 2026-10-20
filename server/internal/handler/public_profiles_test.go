package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func publicProfilesRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequirePublicAPIWorkspaceMember(testHandler.Queries))
	r.With(middleware.RequirePublicAPIScope("agents:read")).Get("/agents", testHandler.ListPublicAgents)
	r.With(middleware.RequirePublicAPIScope("agents:read")).Get("/agents/{agent_id}", testHandler.GetPublicAgent)
	r.With(middleware.RequirePublicAPIScope("runtimes:read")).Get("/runtimes", testHandler.ListPublicRuntimes)
	r.With(middleware.RequirePublicAPIScope("runtimes:read")).Get("/runtimes/{runtime_id}", testHandler.GetPublicRuntime)
	return r
}

func callPublicProfiles(t *testing.T, router http.Handler, userID, boundWorkspace, requestedWorkspace, path string, scopes []string) *testutil.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Workspace-ID", requestedWorkspace)
	ctx := middleware.WithPublicAPIUserCredential(req.Context(), middleware.PublicAPIUserCredential{
		UserID: userID, WorkspaceID: boundWorkspace, Scopes: scopes, Credential: publicapiv1.CredentialPersonalAccess,
	})
	return testutil.Call(t, router.ServeHTTP, req.WithContext(ctx))
}

func assertPublicProfileRedaction(t *testing.T, data []byte, runtime bool) {
	t.Helper()
	if strings.Contains(string(data), "SECRET_SENTINEL") {
		t.Fatal("secret value escaped the projection")
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"instructions", "system_instructions", "conversation_starters", "runtime_config", "custom_args", "custom_env", "has_custom_env", "custom_env_key_count", "mcp_config", "composio_toolkit_allowlist", "metadata", "device_info", "daemon_id", "owner_id", "last_seen_at", "token", "credentials"} {
		if _, present := wire[field]; present {
			t.Errorf("private field %s is present", field)
		}
	}
	want := 10
	if runtime {
		want = 3
	}
	if len(wire) != want {
		t.Fatalf("unexpected fields: %s", data)
	}
}

func TestPublicProfilesVisibilityAndReadiness(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Public profiles", "public-profiles-"+uuid.NewString())
	ownerID := dbfx.User(t, "Profile owner", uuid.NewString()+"@multica.test")
	memberID := dbfx.User(t, "Picker member", uuid.NewString()+"@multica.test")
	dbfx.Member(t, workspaceID, testUserID, "owner")
	dbfx.Member(t, workspaceID, ownerID, "member")
	dbfx.Member(t, workspaceID, memberID, "member")
	fx := testutil.New(testPool, workspaceID, ownerID)
	privateRuntime := fx.Runtime(t, "SECRET_SENTINEL machine", testutil.Cols{
		"device_info": "SECRET_SENTINEL device", "metadata": testutil.Raw(`'{"token":"SECRET_SENTINEL"}'::jsonb`),
	})
	publicRuntime := fx.Runtime(t, "Public machine", testutil.Cols{"visibility": "public"})
	ownerlessRuntime := fx.Runtime(t, "Ownerless machine", testutil.Cols{"visibility": "public", "owner_id": nil})
	private := fx.Agent(t, "Private profile", privateRuntime, testutil.Cols{
		"instructions": "SECRET_SENTINEL prompt", "custom_env": testutil.Raw(`'{"KEY":"SECRET_SENTINEL"}'::jsonb`),
		"runtime_config": testutil.Raw(`'{"gateway":{"token":"SECRET_SENTINEL"}}'::jsonb`),
		"custom_args":    testutil.Raw(`'["SECRET_SENTINEL"]'::jsonb`),
		"mcp_config":     testutil.Raw(`'{"token":"SECRET_SENTINEL"}'::jsonb`),
	})
	shared := fx.Agent(t, "Shared hidden runtime", privateRuntime, testutil.Cols{"permission_mode": "public_to"})
	targeted := fx.Agent(t, "Member target", publicRuntime, testutil.Cols{"permission_mode": "public_to"})
	teamOnly := fx.Agent(t, "Inert team", publicRuntime, testutil.Cols{"permission_mode": "public_to"})
	archived := fx.Agent(t, "Archived", privateRuntime, testutil.Cols{"archived_at": testutil.Raw("now()"), "permission_mode": "public_to"})
	unbound := fx.Agent(t, "Unbound", "", testutil.Cols{"permission_mode": "public_to"})
	builder := fx.Agent(t, "Hidden carrier", privateRuntime, testutil.Cols{"kind": "system", "system_key": "agent_builder:" + uuid.NewString()})
	for _, id := range []string{shared, archived, unbound} {
		fx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": id, "target_type": "workspace", "target_id": workspaceID})
	}
	fx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": targeted, "target_type": "member", "target_id": memberID})
	fx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": teamOnly, "target_type": "team", "target_id": uuid.NewString()})
	router := publicProfilesRouter()
	scopes := []string{"agents:read", "runtimes:read"}
	for _, actor := range []struct {
		name, id      string
		visible       []string
		invokePrivate bool
	}{
		{"profile owner", ownerID, []string{private, shared, targeted, teamOnly, unbound}, true},
		{"member", memberID, []string{shared, targeted, unbound}, false},
		{"workspace owner", testUserID, []string{private, shared, targeted, teamOnly, unbound}, false},
	} {
		t.Run(actor.name, func(t *testing.T) {
			var list publicapiv1.AgentListResponse
			callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/agents", scopes).Want(200).JSON(&list)
			if len(list.Agents) != len(actor.visible) {
				t.Fatalf("profiles = %+v, want IDs %v", list.Agents, actor.visible)
			}
			for _, a := range list.Agents {
				if !slices.Contains(actor.visible, a.AgentID) || a.WorkspaceID != workspaceID {
					t.Fatalf("unexpected profile %+v", a)
				}
				var detail publicapiv1.Agent
				response := callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/agents/"+a.AgentID, scopes).Want(200)
				response.JSON(&detail)
				if detail.AgentID != a.AgentID || detail.CanInvoke != a.CanInvoke || detail.Availability != a.Availability {
					t.Fatalf("list/detail disagree: %+v / %+v", a, detail)
				}
				assertPublicProfileRedaction(t, response.Body.Bytes(), false)
				switch a.AgentID {
				case private:
					if a.CanInvoke != actor.invokePrivate || len(a.InvocationTargets) != 0 {
						t.Fatalf("private permission = %+v", a)
					}
				case shared:
					if !a.CanInvoke || a.RuntimeID != privateRuntime || a.Availability != "online" || len(a.InvocationTargets) != 1 {
						t.Fatalf("hidden runtime reference = %+v", a)
					}
				case targeted:
					if a.CanInvoke != (actor.id == memberID || actor.id == ownerID) || len(a.InvocationTargets) != 1 || a.InvocationTargets[0].TargetID == nil || *a.InvocationTargets[0].TargetID != memberID {
						t.Fatalf("target permission = %+v", a)
					}
				case teamOnly:
					if a.CanInvoke != (actor.id == ownerID) {
						t.Fatalf("team target widened invocation: %+v", a)
					}
				case unbound:
					if a.RuntimeBound || a.RuntimeID != "" || a.Availability != "unbound" {
						t.Fatalf("unbound = %+v", a)
					}
				}
			}
			for _, id := range []string{private, teamOnly, builder} {
				if !slices.Contains(actor.visible, id) {
					callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/agents/"+id, scopes).Want(404)
				}
			}
			var all publicapiv1.AgentListResponse
			callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/agents?include_archived=true", scopes).Want(200).JSON(&all)
			if len(all.Agents) != len(actor.visible)+1 {
				t.Fatalf("archived list = %+v", all)
			}
			var a publicapiv1.Agent
			callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/agents/"+archived, scopes).Want(200).JSON(&a)
			if !a.Archived || a.CanInvoke || a.Availability != "archived" {
				t.Fatalf("archived = %+v", a)
			}
			var runtimes publicapiv1.RuntimeListResponse
			callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/runtimes", scopes).Want(200).JSON(&runtimes)
			wantCount := 1
			if actor.id == ownerID {
				wantCount = 2
			}
			if len(runtimes.Runtimes) != wantCount {
				t.Fatalf("runtime visibility = %+v", runtimes)
			}
			for _, runtime := range runtimes.Runtimes {
				response := callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/runtimes/"+runtime.RuntimeID, scopes).Want(200)
				assertPublicProfileRedaction(t, response.Body.Bytes(), true)
			}
			if actor.id != ownerID {
				callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/runtimes/"+privateRuntime, scopes).Want(404)
			}
			callPublicProfiles(t, router, actor.id, workspaceID, workspaceID, "/runtimes/"+ownerlessRuntime, scopes).Want(404)
		})
	}
	fx.Exec(t, `UPDATE agent_runtime SET status='offline', last_seen_at=now()-interval '10 minutes' WHERE id=$1`, privateRuntime)
	var offline publicapiv1.Agent
	callPublicProfiles(t, router, memberID, workspaceID, workspaceID, "/agents/"+shared, scopes).Want(200).JSON(&offline)
	if offline.Availability != "offline" || !offline.CanInvoke {
		t.Fatalf("permission/health conflated: %+v", offline)
	}
	fx.Exec(t, `DELETE FROM agent_invocation_target WHERE agent_id=$1`, shared)
	callPublicProfiles(t, router, memberID, workspaceID, workspaceID, "/agents/"+shared, scopes).Want(404)
}

func TestPublicProfilesScopeWorkspaceAndPagination(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Public read isolation", "public-isolation-"+uuid.NewString())
	otherWorkspace := dbfx.Workspace(t, "Other public reads", "public-other-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "member")
	dbfx.Member(t, otherWorkspace, testUserID, "member")
	fx := testutil.New(testPool, workspaceID, testUserID)
	other := testutil.New(testPool, otherWorkspace, testUserID)
	runtimeID := fx.Runtime(t, "Own runtime")
	foreignRuntime := other.Runtime(t, "Foreign runtime")
	foreignAgent := other.Agent(t, "Foreign profile", foreignRuntime)
	ids := []string{fx.Agent(t, "One", runtimeID), fx.Agent(t, "Two", runtimeID), fx.Agent(t, "Three", runtimeID)}
	slices.Sort(ids)
	router := publicProfilesRouter()
	scopes := []string{"agents:read", "runtimes:read"}
	for _, path := range []string{"/agents", "/agents/" + ids[0], "/runtimes", "/runtimes/" + runtimeID} {
		var problem publicapiv1.Problem
		callPublicProfiles(t, router, testUserID, workspaceID, workspaceID, path, []string{"issues:read"}).Want(403).JSON(&problem)
		if problem.Code != "insufficient_scope" {
			t.Fatalf("scope failure = %+v", problem)
		}
		callPublicProfiles(t, router, testUserID, workspaceID, otherWorkspace, path, scopes).Want(404)
	}
	callPublicProfiles(t, router, testUserID, workspaceID, workspaceID, "/agents", []string{"runtimes:read"}).Want(403)
	callPublicProfiles(t, router, testUserID, workspaceID, workspaceID, "/runtimes", []string{"agents:read"}).Want(403)
	for _, path := range []string{"/agents/" + foreignAgent, "/runtimes/" + foreignRuntime, "/agents/invalid", "/runtimes/invalid"} {
		callPublicProfiles(t, router, testUserID, workspaceID, workspaceID, path, scopes).Want(404)
	}
	seen := []string{}
	cursor := ""
	firstCursor := ""
	for range ids {
		var page publicapiv1.AgentListResponse
		callPublicProfiles(t, router, testUserID, workspaceID, workspaceID, "/agents?limit=1&cursor="+url.QueryEscape(cursor), scopes).Want(200).JSON(&page)
		if len(page.Agents) != 1 {
			t.Fatalf("page = %+v", page)
		}
		seen = append(seen, page.Agents[0].AgentID)
		cursor = page.NextCursor
		if firstCursor == "" {
			firstCursor = cursor
		}
	}
	if !slices.Equal(seen, ids) || cursor != "" {
		t.Fatalf("pagination got %v, cursor=%q, want %v", seen, cursor, ids)
	}
	for _, path := range []string{"/agents?limit=0", "/agents?limit=201", "/agents?cursor=bad", "/agents?include_archived=bad", "/runtimes?cursor=" + firstCursor, "/agents?include_archived=true&cursor=" + firstCursor} {
		callPublicProfiles(t, router, testUserID, workspaceID, workspaceID, path, scopes).Want(400)
	}
	callPublicProfiles(t, router, testUserID, otherWorkspace, otherWorkspace, "/agents?cursor="+firstCursor, scopes).Want(400)
	otherUser := dbfx.User(t, "Cursor isolation", uuid.NewString()+"@multica.test")
	dbfx.Member(t, workspaceID, otherUser, "member")
	callPublicProfiles(t, router, otherUser, workspaceID, workspaceID, "/agents?cursor="+firstCursor, scopes).Want(400)
	fx.Exec(t, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, workspaceID, testUserID)
	callPublicProfiles(t, router, testUserID, workspaceID, workspaceID, "/agents", scopes).Want(404)
}

func TestPublicProfilesDoNotProjectForeignOrMissingRuntimeBindings(t *testing.T) {
	workspaceID := parseUUID("00000000-0000-0000-0000-000000000001")
	otherWorkspaceID := parseUUID("00000000-0000-0000-0000-000000000002")
	runtimeID := parseUUID("00000000-0000-0000-0000-000000000003")
	agent := db.Agent{ID: parseUUID("00000000-0000-0000-0000-000000000004"), WorkspaceID: workspaceID, RuntimeID: runtimeID, PermissionMode: "private"}
	for _, runtime := range []db.AgentRuntime{{}, {ID: runtimeID, WorkspaceID: otherWorkspaceID, Status: "online"}} {
		projection := publicProfileProjection(agent, nil, db.Member{WorkspaceID: workspaceID}, runtime, time.Now())
		if projection.RuntimeID != "" || projection.Availability != "unavailable" || !projection.RuntimeBound || projection.CanInvoke {
			t.Fatalf("unsafe binding projection: %+v", projection)
		}
	}
}
