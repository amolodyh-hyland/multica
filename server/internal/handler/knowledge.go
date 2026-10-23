package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func (h *Handler) requireKnowledgeReferenceOwner(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, kind string, raw []byte) bool {
	if !knowledge.IsType(kind) {
		return true
	}
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "knowledge references must be bound by their human owner")
		return false
	}
	ref, err := knowledge.DecodeReference(kind, raw)
	if err != nil {
		writeError(w, 400, "invalid knowledge reference")
		return false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return false
	}
	if ref.OwnerID != userID {
		writeError(w, http.StatusForbidden, "knowledge reference belongs to another owner")
		return false
	}
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, 500, "failed to authorize knowledge resolver")
		return false
	}
	for _, rt := range runtimes {
		if rt.DaemonID.String == ref.DaemonID && uuidToString(rt.OwnerID) == userID {
			return true
		}
	}
	writeError(w, http.StatusForbidden, "knowledge resolver is not owned in this workspace")
	return false
}

func knowledgeClaimBlockReason(selection knowledge.Selection, runtime db.AgentRuntime, hasCapability bool) string {
	if !hasCapability {
		return "Knowledge references require an updated daemon advertising knowledge-context-v1."
	}
	if err := selection.Validate(); err != nil {
		return "Knowledge reference manifest is invalid."
	}
	if selection.WorkspaceID != uuidToString(runtime.WorkspaceID) || selection.OwnerID != uuidToString(runtime.OwnerID) || selection.DaemonID != runtime.DaemonID.String {
		return "Knowledge references are unavailable on this owner machine; explicit import is required for a remote runtime."
	}
	return ""
}

func (h *Handler) pinClaimKnowledge(ctx context.Context, task *db.AgentTaskQueue, runtime db.AgentRuntime, resp *AgentTaskResponse) (*knowledge.Selection, error) {
	var candidate knowledge.Selection
	raw := task.KnowledgeManifest
	if len(raw) == 0 && resp.PriorSessionID != "" {
		retained, err := h.Queries.GetSessionKnowledgeManifest(ctx, db.GetSessionKnowledgeManifestParams{AgentID: task.AgentID, SessionID: pgtype.Text{String: resp.PriorSessionID, Valid: true}})
		if err == nil {
			raw = retained
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return nil, err
		}
	} else {
		if len(resp.ProjectKnowledge) == 0 && (!runtime.OwnerID.Valid || !knowledge.ValidID(runtime.DaemonID.String)) {
			return nil, nil
		}
		candidate = knowledge.Selection{SchemaVersion: 1, SnapshotID: uuidToString(task.ID), WorkspaceID: resp.WorkspaceID, ProjectID: resp.ProjectID, OwnerID: uuidToString(runtime.OwnerID), DaemonID: runtime.DaemonID.String, Bindings: resp.ProjectKnowledge}.Seal()
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	pinned, err := h.Queries.PinTaskKnowledgeManifest(ctx, db.PinTaskKnowledgeManifestParams{TaskID: task.ID, RuntimeID: task.RuntimeID, Manifest: raw})
	if err != nil {
		return nil, err
	}
	var selection knowledge.Selection
	if err := json.Unmarshal(pinned, &selection); err != nil {
		return nil, err
	}
	return &selection, nil
}

func (h *Handler) prepareClaimKnowledge(r *http.Request, task *db.AgentTaskQueue, runtime db.AgentRuntime, resp *AgentTaskResponse) *claimBuildFailure {
	selection, err := h.pinClaimKnowledge(r.Context(), task, runtime, resp)
	if err != nil {
		return h.rejectClaimSourceLoad(r.Context(), task, err, "knowledge", uuidToString(task.ID))
	}
	if selection == nil {
		return nil
	}
	if err := selection.Validate(); err != nil {
		return h.failClaimedTaskBeforeLaunch(r.Context(), task, "Knowledge reference manifest is invalid.", taskfailure.ReasonInvalidTaskIdentity, "knowledge_invalid", http.StatusUnprocessableEntity, "Knowledge reference manifest is invalid.")
	}
	if len(selection.Bindings) == 0 {
		return nil
	}
	reason := knowledgeClaimBlockReason(*selection, runtime, requestHasClientCapability(r, protocol.DaemonCapabilityKnowledgeV1))
	if reason == "" {
		_, err = h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: runtime.OwnerID, WorkspaceID: runtime.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			reason = "Knowledge reference owner is no longer a workspace member."
		} else if err != nil {
			return h.rejectClaimSourceLoad(r.Context(), task, err, "knowledge", selection.SnapshotID)
		}
	}
	if reason != "" {
		return h.failClaimedTaskBeforeLaunch(r.Context(), task, reason, taskfailure.ReasonInvalidTaskIdentity, "knowledge_unavailable", http.StatusUnprocessableEntity, reason)
	}
	resp.KnowledgeManifest = selection
	return nil
}
