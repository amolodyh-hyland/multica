package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type IssueRuntimeResponse struct {
	IssueID            string  `json:"issue_id"`
	AgentID            string  `json:"agent_id"`
	RuntimeID          *string `json:"runtime_id"`
	EffectiveRuntimeID *string `json:"effective_runtime_id"`
}

func (h *Handler) issueRuntimeAccess(w http.ResponseWriter, r *http.Request) (db.Issue, db.Agent, bool) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return db.Issue{}, db.Agent{}, false
	}
	if issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusConflict, "assign an agent before choosing a runtime")
		return db.Issue{}, db.Agent{}, false
	}
	profile, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "assigned agent not found")
		return db.Issue{}, db.Agent{}, false
	}
	if !h.canManageAgent(w, r, profile) {
		return db.Issue{}, db.Agent{}, false
	}
	return issue, profile, true
}

func (h *Handler) GetIssueRuntime(w http.ResponseWriter, r *http.Request) {
	issue, profile, ok := h.issueRuntimeAccess(w, r)
	if !ok {
		return
	}
	resp := IssueRuntimeResponse{IssueID: uuidToString(issue.ID), AgentID: uuidToString(profile.ID), EffectiveRuntimeID: uuidToPtr(profile.RuntimeID)}
	override, err := h.Queries.GetIssueRuntimeOverride(r.Context(), db.GetIssueRuntimeOverrideParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err == nil {
		resp.RuntimeID = uuidToPtr(override.RuntimeID)
		resp.EffectiveRuntimeID = resp.RuntimeID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load issue runtime")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) SetIssueRuntime(w http.ResponseWriter, r *http.Request) {
	issue, profile, ok := h.issueRuntimeAccess(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	if actorType != "member" {
		writeError(w, http.StatusForbidden, "only members can switch issue runtimes")
		return
	}
	var req struct {
		AgentID   string  `json:"agent_id"`
		RuntimeID *string `json:"runtime_id"`
	}
	fields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, present := fields["runtime_id"]; !present {
		writeError(w, http.StatusBadRequest, "runtime_id is required; use null for the profile default")
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	if agentID != profile.ID {
		writeError(w, http.StatusConflict, "assigned agent changed; reload before switching runtime")
		return
	}
	targetID := profile.RuntimeID
	overrideID := pgtype.UUID{}
	if req.RuntimeID != nil {
		overrideID, ok = parseUUIDOrBadRequest(w, *req.RuntimeID, "runtime_id")
		if !ok {
			return
		}
		targetID = overrideID
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start runtime change")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	if _, err := q.LockWorkspaceForChatSessionCreate(r.Context(), issue.WorkspaceID); err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if targetID.Valid {
		target, err := q.LockAgentRuntimeForAccessChange(r.Context(), targetID)
		if err != nil || target.WorkspaceID != issue.WorkspaceID {
			writeError(w, http.StatusBadRequest, "invalid runtime_id")
			return
		}
		member, ok := h.workspaceMember(w, r, uuidToString(issue.WorkspaceID))
		if !ok {
			return
		}
		if !canUseRuntimeForAgent(member, target) || (target.Visibility == "private" && target.OwnerID != profile.OwnerID) {
			writeError(w, http.StatusForbidden, "this runtime does not permit the assigned agent and member")
			return
		}
	}
	locked, err := q.GetAgentForUpdate(r.Context(), profile.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "assigned agent not found")
		return
	}
	if locked.OwnerID != profile.OwnerID || locked.RuntimeID != profile.RuntimeID || locked.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "assigned agent changed; reload before switching runtime")
		return
	}
	current, err := q.LockIssueForRuntimeSwitch(r.Context(), db.LockIssueForRuntimeSwitchParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		var lockErr *pgconn.PgError
		if errors.As(err, &lockErr) && lockErr.Code == "55P03" {
			writeError(w, http.StatusConflict, "issue is being updated; retry the runtime change")
		} else if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to lock issue")
		}
		return
	}
	if current.AssigneeType.String != "agent" || current.AssigneeID != profile.ID {
		writeError(w, http.StatusConflict, "assigned agent changed; reload before switching runtime")
		return
	}
	previous, err := q.GetIssueRuntimeOverride(r.Context(), db.GetIssueRuntimeOverrideParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load issue runtime")
		return
	}
	if previous.RuntimeID == overrideID {
		writeJSON(w, http.StatusOK, IssueRuntimeResponse{IssueID: uuidToString(issue.ID), AgentID: uuidToString(profile.ID), RuntimeID: uuidToPtr(overrideID), EffectiveRuntimeID: uuidToPtr(targetID)})
		return
	}
	if !targetID.Valid {
		writeError(w, http.StatusConflict, "bind a profile default runtime before clearing this issue override")
		return
	}
	executing, err := q.CountExecutingIssueAgentTasks(r.Context(), db.CountExecutingIssueAgentTasksParams{IssueID: issue.ID, AgentID: profile.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check executing tasks")
		return
	}
	if executing > 0 {
		writeError(w, http.StatusConflict, "finish or cancel active runs before switching runtime")
		return
	}
	pinned, err := q.HasPendingPublicDelegationForIssueAgent(r.Context(), db.HasPendingPublicDelegationForIssueAgentParams{IssueID: issue.ID, AgentID: profile.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check pending delegations")
		return
	}
	if pinned {
		writeError(w, http.StatusConflict, "finish or cancel pending delegations before switching their confirmed runtime")
		return
	}
	if overrideID.Valid {
		err = q.SetIssueRuntimeOverride(r.Context(), db.SetIssueRuntimeOverrideParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, AgentID: profile.ID, RuntimeID: overrideID, UpdatedBy: parseUUID(actorID)})
	} else {
		err = q.DeleteIssueRuntimeOverride(r.Context(), db.DeleteIssueRuntimeOverrideParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save issue runtime")
		return
	}
	tasks, err := q.MovePendingIssueAgentTasksToRuntime(r.Context(), db.MovePendingIssueAgentTasksToRuntimeParams{IssueID: issue.ID, AgentID: profile.ID, RuntimeID: targetID, RuntimeOverrideID: overrideID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to move pending runs")
		return
	}
	previousID := previous.RuntimeID
	if !previousID.Valid {
		previousID = profile.RuntimeID
	}
	ids := make([]string, len(tasks))
	for i, task := range tasks {
		ids[i] = uuidToString(task.ID)
	}
	fromProvider, toProvider := "", ""
	for _, entry := range []struct {
		id       pgtype.UUID
		provider *string
	}{{previousID, &fromProvider}, {targetID, &toProvider}} {
		if entry.id.Valid {
			rt, err := q.GetAgentRuntime(r.Context(), entry.id)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load runtime provider")
				return
			}
			*entry.provider = rt.Provider
		}
	}
	details, _ := json.Marshal(map[string]any{
		"agent_id":            uuidToString(profile.ID),
		"from_runtime_id":     uuidToPtr(previousID),
		"to_runtime_id":       uuidToPtr(targetID),
		"runtime_override_id": uuidToPtr(overrideID),
		"pending_task_ids":    ids,
		"from_provider":       fromProvider,
		"to_provider":         toProvider,
	})
	if _, err := q.CreateActivity(r.Context(), db.CreateActivityParams{
		ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, IssueID: issue.ID,
		ActorType: pgtype.Text{String: actorType, Valid: true}, ActorID: parseUUID(actorID),
		Action: "runtime_changed", Details: details,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record runtime change")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit runtime change")
		return
	}
	for _, task := range tasks {
		h.TaskService.NotifyTaskRuntimeChanged(r.Context(), task)
	}
	h.publish(protocol.EventIssueRuntimeChanged, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{"issue_id": uuidToString(issue.ID)})
	writeJSON(w, http.StatusOK, IssueRuntimeResponse{IssueID: uuidToString(issue.ID), AgentID: uuidToString(profile.ID), RuntimeID: uuidToPtr(overrideID), EffectiveRuntimeID: uuidToPtr(targetID)})
}
