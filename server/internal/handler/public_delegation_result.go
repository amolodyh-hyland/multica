package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func (h *Handler) GetPublicDelegationResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	member, ok := h.publicDelegationMember(w, r, "delegations:results:read")
	if !ok {
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "delegation_id"))
	if err != nil {
		publicReadNotFound(w, r)
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err = q.LockWorkspaceForChatSessionCreate(ctx, member.WorkspaceID); err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	if _, err = q.LockPublicDelegationMember(ctx, db.LockPublicDelegationMemberParams{WorkspaceID: member.WorkspaceID, UserID: member.UserID}); err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	d, err := q.GetPublicDelegationForActor(ctx, db.GetPublicDelegationForActorParams{ID: id, WorkspaceID: member.WorkspaceID, RequesterID: member.UserID, IsAdmin: false})
	if err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	task, err := q.LockPublicDelegationTask(ctx, db.LockPublicDelegationTaskParams{DelegationID: d.ID, WorkspaceID: d.WorkspaceID})
	if err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	status := publicDelegationProjection(d, task.Status, task.FailureReason.String)
	if !status.Terminal {
		publicapiv1.WriteProblem(w, r, 409, "result_unavailable", "terminal text result unavailable")
		return
	}
	if r.Header.Get("If-Match") != "\""+status.Revision+"\"" {
		publicapiv1.WriteProblem(w, r, 409, "revision_conflict", "terminal snapshot changed; preview again")
		return
	}
	var output struct {
		Output string `json:"output"`
	}
	if !task.CompletedAt.Valid || len(task.Result) > 262144 || json.Unmarshal(task.Result, &output) != nil {
		publicapiv1.WriteProblem(w, r, 409, "result_unavailable", "terminal text result unavailable")
		return
	}
	result := publicapiv1.DelegationResult{DelegationID: status.DelegationID, TaskID: status.TaskID, WorkspaceID: status.WorkspaceID, AgentID: status.AgentID, RuntimeID: status.RuntimeID, Status: status.Status, TerminalRevision: status.Revision, CompletedAt: task.CompletedAt.Time.UTC().Format(time.RFC3339Nano), Text: output.Output}
	if result.Seal() != nil {
		publicapiv1.WriteProblem(w, r, 409, "result_unavailable", "terminal text result unavailable")
		return
	}
	if expected := r.Header.Get(publicapiv1.HeaderResultRevision); expected != "" && expected != result.Revision {
		publicapiv1.WriteProblem(w, r, 409, "revision_conflict", "result changed; preview again")
		return
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	if err = q.RecordPublicDelegationResult(ctx, db.RecordPublicDelegationResultParams{DelegationID: d.ID, WorkspaceID: d.WorkspaceID, RequesterID: member.UserID, Revision: result.Revision, Snapshot: snapshot}); err != nil {
		publicReadFailure(w, r)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		publicReadFailure(w, r)
		return
	}
	w.Header().Set("ETag", "\""+result.Revision+"\"")
	writeJSON(w, http.StatusOK, result)
}
