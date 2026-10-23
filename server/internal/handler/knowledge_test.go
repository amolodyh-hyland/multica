package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func knowledgeTestRef() knowledge.Reference {
	return knowledge.Reference{SchemaVersion: 1, OwnerID: "owner1", DaemonID: "machine1", ResolverID: "machine1", RootRef: "root1", RootGeneration: 1, AuthorityID: "mu_local_machine1", SourceID: "source1", SourceRevision: 1, EntryID: "entry1", ContentHash: knowledge.Sum([]byte("text")), Format: "knowledge-text-v1", EntryPoint: "notes.md"}
}

func TestKnowledgeValidatorAndExecutionResourceCompatibility(t *testing.T) {
	for _, kind := range []string{knowledge.File, knowledge.Folder, knowledge.Skill} {
		ref := knowledgeTestRef()
		if kind == knowledge.Skill {
			ref.Format = "native-skill-text-v1"
			ref.EntryPoint = "SKILL.md"
		}
		raw, _ := json.Marshal(ref)
		if _, err := validateAndNormalizeResourceRef(kind, raw); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		fields["local_path"] = "/tmp/private"
		raw, _ = json.Marshal(fields)
		if _, err := validateAndNormalizeResourceRef(kind, raw); err == nil {
			t.Fatal("server accepted a local path in portable metadata")
		}
	}
	for _, tc := range []struct{ kind, ref string }{{"github_repo", `{"url":"https://github.com/example/repo"}`}, {"local_directory", `{"local_path":"/work","daemon_id":"machine1","execution_mode":"in_place"}`}} {
		raw, err := validateAndNormalizeResourceRef(tc.kind, []byte(tc.ref))
		if err != nil {
			t.Fatal(err)
		}
		resources, repos := projectResourcesForClaim([]db.ProjectResource{{ResourceType: tc.kind, ResourceRef: raw}})
		if len(resources) != 1 || tc.kind == "github_repo" && len(repos) != 1 {
			t.Fatal("execution resource semantics changed")
		}
	}
	ref := knowledgeTestRef()
	raw, _ := json.Marshal(ref)
	resources, repos := projectResourcesForClaim([]db.ProjectResource{{ResourceType: knowledge.Folder, ResourceRef: raw}})
	if len(resources) != 0 || len(repos) != 0 {
		t.Fatal("knowledge used as execution resources")
	}
}

func TestKnowledgeClaimAuthorization(t *testing.T) {
	owner := parseUUID("00000000-0000-0000-0000-000000000001")
	workspace := parseUUID("00000000-0000-0000-0000-000000000002")
	ref := knowledgeTestRef()
	ref.OwnerID = uuidToString(owner)
	selection := knowledge.Selection{SchemaVersion: 1, SnapshotID: "snapshot1", WorkspaceID: uuidToString(workspace), ProjectID: "project1", OwnerID: ref.OwnerID, DaemonID: ref.DaemonID, Bindings: []knowledge.Binding{{ID: "binding1", Kind: knowledge.File, Reference: ref}}}.Seal()
	runtime := db.AgentRuntime{OwnerID: owner, WorkspaceID: workspace, DaemonID: pgtype.Text{String: ref.DaemonID, Valid: true}}
	if got := knowledgeClaimBlockReason(selection, runtime, true); got != "" {
		t.Fatal(got)
	}
	if got := knowledgeClaimBlockReason(selection, runtime, false); got == "" {
		t.Fatal("old daemon accepted")
	}
	for _, mutate := range []func(*db.AgentRuntime){func(r *db.AgentRuntime) { r.WorkspaceID = owner }, func(r *db.AgentRuntime) { r.OwnerID = workspace }, func(r *db.AgentRuntime) { r.DaemonID.String = "remote" }} {
		foreign := runtime
		mutate(&foreign)
		if knowledgeClaimBlockReason(selection, foreign, true) == "" {
			t.Fatal("foreign claim accepted")
		}
	}
}

func TestKnowledgeAPIAndPinnedClaim(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(testRuntimeID))
	if err != nil {
		t.Fatal(err)
	}
	originalOwner, originalDaemon := runtime.OwnerID, runtime.DaemonID
	t.Cleanup(func() {
		dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=$2,daemon_id=$3 WHERE id=$1`, testRuntimeID, originalOwner, originalDaemon)
	})
	dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=$2,daemon_id='knowledge-machine' WHERE id=$1`, testRuntimeID, testUserID)
	runtime.OwnerID = parseUUID(testUserID)
	runtime.DaemonID = pgtype.Text{String: "knowledge-machine", Valid: true}
	projectID := dbfx.Project(t, "Knowledge project")
	ref := knowledgeTestRef()
	ref.OwnerID = testUserID
	ref.DaemonID = runtime.DaemonID.String
	ref.ResolverID = ref.DaemonID
	var saved ProjectResourceResponse
	req := withURLParam(newRequest("POST", "/api/projects/"+projectID+"/resources", map[string]any{"resource_type": knowledge.File, "resource_ref": ref}), "id", projectID)
	testutil.Call(t, testHandler.CreateProjectResource, req).Want(http.StatusCreated).JSON(&saved)
	for _, actor := range []string{"task_token", "cloud_pat"} {
		machineRequest := withURLParam(newRequest("POST", "/", map[string]any{"resource_type": knowledge.File, "resource_ref": ref}), "id", projectID)
		machineRequest.Header.Set("X-Actor-Source", actor)
		testutil.Call(t, testHandler.CreateProjectResource, machineRequest).Want(http.StatusForbidden)
	}

	foreign := ref
	foreign.OwnerID = "00000000-0000-0000-0000-000000000099"
	testutil.Call(t, testHandler.CreateProjectResource, withURLParam(newRequest("POST", "/", map[string]any{"resource_type": knowledge.File, "resource_ref": foreign}), "id", projectID)).Want(http.StatusForbidden)
	foreign = ref
	foreign.DaemonID = "another-machine"
	foreign.ResolverID = foreign.DaemonID
	testutil.Call(t, testHandler.CreateProjectResource, withURLParam(newRequest("POST", "/", map[string]any{"resource_type": knowledge.File, "resource_ref": foreign}), "id", projectID)).Want(http.StatusForbidden)
	resolved, err := testHandler.resolveClaimProjectContext(ctx, parseUUID(projectID), parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Knowledge) != 1 || len(resolved.Resources) != 0 {
		t.Fatalf("wrong context partition %+v", resolved)
	}
	agentID := dbfx.Agent(t, "Knowledge agent", testRuntimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID})
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	resp := AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: projectID, ProjectKnowledge: resolved.Knowledge}
	selection, err := testHandler.pinClaimKnowledge(ctx, &task, runtime, &resp)
	if err != nil {
		t.Fatal(err)
	}
	if selection == nil || selection.Validate() != nil {
		t.Fatalf("invalid selection %+v", selection)
	}
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityKnowledgeV1)
	if failure := testHandler.prepareClaimKnowledge(req, &task, runtime, &resp); failure != nil {
		t.Fatalf("claim failed: %+v", failure)
	}
	if resp.KnowledgeManifest == nil {
		t.Fatal("manifest absent from claim")
	}
	resp.ProjectKnowledge[0].Reference.ContentHash = knowledge.Sum([]byte("changed"))
	again, err := testHandler.pinClaimKnowledge(ctx, &task, runtime, &resp)
	if err != nil || again.Digest != selection.Digest {
		t.Fatalf("claim changed pinned identity: %v", err)
	}
	dbfx.Exec(t, `DELETE FROM project_resource WHERE id=$1`, saved.ID)
	resp.ProjectKnowledge = nil
	again, err = testHandler.pinClaimKnowledge(ctx, &task, runtime, &resp)
	if err != nil || again.Digest != selection.Digest {
		t.Fatalf("deletion lost pinned identity: %v", err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for range 4 {
		wg.Go(func() { _, err := testHandler.pinClaimKnowledge(ctx, &task, runtime, &resp); failures <- err })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	foreignWorkspace, foreignProject := foreignWorkspaceWithProject(t, "knowledge-foreign", "KNF")
	raw, _ := json.Marshal(ref)
	dbfx.Insert(t, "project_resource", testutil.Cols{"project_id": projectID, "workspace_id": foreignWorkspace, "resource_type": knowledge.File, "resource_ref": string(raw)})
	same, err := testHandler.resolveClaimProjectContext(ctx, parseUUID(projectID), parseUUID(testWorkspaceID))
	if err != nil || len(same.Knowledge) != 0 {
		t.Fatalf("foreign resource leaked: %+v %v", same, err)
	}
	same, err = testHandler.resolveClaimProjectContext(ctx, parseUUID(foreignProject), parseUUID(testWorkspaceID))
	if err != nil || len(same.Knowledge) != 0 {
		t.Fatal("foreign project leaked knowledge")
	}
}

func TestKnowledgeOldDaemonClaimFailsVisiblyAndResumeStaysPinned(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(testRuntimeID))
	if err != nil {
		t.Fatal(err)
	}
	runtime.OwnerID = parseUUID(testUserID)
	runtime.DaemonID = pgtype.Text{String: "knowledge-machine", Valid: true}
	agentID := dbfx.Agent(t, "Knowledge refusal", testRuntimeID)
	ref := knowledgeTestRef()
	ref.OwnerID = testUserID
	ref.DaemonID = runtime.DaemonID.String
	ref.ResolverID = ref.DaemonID
	projectID := dbfx.Project(t, "Pinned knowledge")
	for _, mode := range []string{"old-daemon", "remote-machine"} {
		t.Run(mode, func(t *testing.T) {
			taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID, "status": "dispatched"})
			task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
			if err != nil {
				t.Fatal(err)
			}
			selectionRef := ref
			if mode == "remote-machine" {
				selectionRef.DaemonID = "remote-machine"
				selectionRef.ResolverID = selectionRef.DaemonID
			}
			resp := AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: projectID, ProjectKnowledge: []knowledge.Binding{{ID: "binding1", Kind: knowledge.File, Reference: selectionRef}}}
			req := httptest.NewRequest("POST", "/", nil)
			if mode != "old-daemon" {
				req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityKnowledgeV1)
			}
			failure := testHandler.prepareClaimKnowledge(req, &task, runtime, &resp)
			if failure == nil || failure.status != http.StatusUnprocessableEntity || !failure.settled {
				t.Fatalf("unavailable claim = %+v", failure)
			}
			stored, err := testHandler.Queries.GetAgentTask(ctx, task.ID)
			if err != nil || stored.Status != "failed" || !stored.Error.Valid || stored.Error.String == "" {
				t.Fatalf("refusal not visible: %+v %v", stored, err)
			}
		})
	}
	firstID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID, "status": "completed", "session_id": "knowledge-session"})
	first, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(firstID))
	if err != nil {
		t.Fatal(err)
	}
	initial := AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: projectID}
	empty, err := testHandler.pinClaimKnowledge(ctx, &first, runtime, &initial)
	if err != nil || empty == nil || len(empty.Bindings) != 0 {
		t.Fatalf("empty selection not pinned: %v", err)
	}
	nextID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": testRuntimeID})
	next, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(nextID))
	if err != nil {
		t.Fatal(err)
	}
	changed := AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: projectID, PriorSessionID: "knowledge-session", ProjectKnowledge: []knowledge.Binding{{ID: "binding1", Kind: knowledge.File, Reference: ref}}}
	retained, err := testHandler.pinClaimKnowledge(ctx, &next, runtime, &changed)
	if err != nil || retained.Digest != empty.Digest || len(retained.Bindings) != 0 {
		t.Fatalf("resumed session adopted new attachments: %+v %v", retained, err)
	}
}
