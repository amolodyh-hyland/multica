package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func (h *Handler) CreatePublicDelegation(w http.ResponseWriter, r *http.Request) {
	member, ok := publicReadMember(w, r, "delegations:write")
	if !ok {
		return
	}
	if !featureflags.PublicDelegationsV1Enabled(r.Context(), h.FeatureFlags) {
		publicReadNotFound(w, r)
		return
	}
	key := r.Header.Get(publicapiv1.HeaderIdempotencyKey)
	if strings.TrimSpace(key) == "" || len(key) > publicapiv1.MaxIdempotencyBytes {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "Idempotency-Key must contain 1 to 255 bytes")
		return
	}
	var input publicapiv1.CreateDelegationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid delegation payload")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "expected one delegation payload")
		return
	}
	hash, err := input.PayloadHash()
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, input.AgentID, "agent_id")
	if !ok {
		return
	}
	runtimeID, ok := parseUUIDOrBadRequest(w, input.ExpectedRuntimeID, "expected_runtime_id")
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := bindingtx.Begin(ctx, h.TxStarter)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err := q.LockWorkspaceForChatSessionCreate(ctx, member.WorkspaceID); err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	member, err = q.LockPublicDelegationMember(ctx, db.LockPublicDelegationMemberParams{WorkspaceID: member.WorkspaceID, UserID: member.UserID})
	if err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	source, _ := json.Marshal(input.Source)
	accepted, err := q.CreatePublicDelegation(ctx, db.CreatePublicDelegationParams{
		ID: dbid.NewV7(), WorkspaceID: member.WorkspaceID, RequesterID: member.UserID,
		IdempotencyKey: key, PayloadHash: hash, AgentID: agentID,
		RuntimeID: runtimeID, TaskID: dbid.NewV7(), Source: source,
	})
	replay := errors.Is(err, pgx.ErrNoRows)
	if replay {
		accepted, err = q.GetPublicDelegationByKey(ctx, db.GetPublicDelegationByKeyParams{WorkspaceID: member.WorkspaceID, RequesterID: member.UserID, IdempotencyKey: key})
		if err != nil {
			publicReadFailure(w, r)
			return
		}
		if accepted.PayloadHash != hash {
			publicapiv1.WriteProblem(w, r, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already accepted with a different payload")
			return
		}
		task, err := q.GetAgentTask(ctx, accepted.TaskID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, publicDelegationProjection(accepted, "unavailable", ""))
			return
		}
		if err != nil {
			publicReadFailure(w, r)
			return
		}
		writeJSON(w, http.StatusOK, publicDelegationProjection(accepted, task.Status, task.FailureReason.String))
		return
	}
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	// Check the confirmed binding before reading caller-supplied runtime IDs.
	// Revalidate under runtime-before-agent locks to match runtime teardown.
	preview, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: accepted.AgentID, WorkspaceID: member.WorkspaceID})
	if err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	previewTargets, err := q.ListAgentInvocationTargets(ctx, preview.ID)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	if !requirePublicDelegationInvocation(w, r, preview, previewTargets, member) {
		return
	}
	if preview.RuntimeID != accepted.RuntimeID {
		publicDelegationBindingConflict(w, r)
		return
	}
	runtime, err := q.LockPublicDelegationRuntime(ctx, db.LockPublicDelegationRuntimeParams{ID: accepted.RuntimeID, WorkspaceID: member.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		publicDelegationBindingConflict(w, r)
		return
	}
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	agent, err := q.LockAgentForAutopilotAssignment(ctx, db.LockAgentForAutopilotAssignmentParams{ID: accepted.AgentID, WorkspaceID: member.WorkspaceID})
	if err != nil {
		publicDelegationReadError(w, r, err)
		return
	}
	targets, err := q.LockPublicDelegationInvocationTargets(ctx, agent.ID)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	if !requirePublicDelegationInvocation(w, r, agent, targets, member) {
		return
	}
	if agent.RuntimeID != accepted.RuntimeID || !runtime.OwnerID.Valid ||
		(runtime.Visibility == "private" && agent.OwnerID != runtime.OwnerID) {
		publicDelegationBindingConflict(w, r)
		return
	}
	verdict, err := service.AgentReadiness(ctx, service.RuntimeLookup{Queries: q}, agent)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	if !verdict.Ready() || deriveAgentRuntimeAvailability(runtime, time.Now()) != "online" {
		publicapiv1.WriteProblem(w, r, http.StatusConflict, "target_unavailable", "the profile runtime is not ready")
		return
	}
	if !runtimeHasCapability(runtime.Metadata, protocol.DaemonCapabilityPublicDelegationV1) {
		publicapiv1.WriteProblem(w, r, http.StatusConflict, "runtime_unsupported", "the runtime does not support delegation tasks")
		return
	}
	taskContext, _ := json.Marshal(service.PublicDelegationContext{
		Type: service.PublicDelegationContextType, DelegationID: uuidToString(accepted.ID),
		WorkspaceID: uuidToString(member.WorkspaceID), Task: input.Task,
	})
	task, err := q.CreatePublicDelegationTask(ctx, db.CreatePublicDelegationTaskParams{
		ID: accepted.TaskID, AgentID: agent.ID, RuntimeID: runtime.ID,
		Context: taskContext, RequesterID: member.UserID, TriggerSummary: pgtype.Text{String: input.Task.Title, Valid: true},
	})
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		publicReadFailure(w, r)
		return
	}
	writeJSON(w, http.StatusAccepted, publicDelegationProjection(accepted, task.Status, task.FailureReason.String))
	if h.TaskService != nil {
		h.TaskService.NotifyTaskEnqueued(ctx, task)
	}
}

func publicDelegationReadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		publicReadNotFound(w, r)
	} else {
		publicReadFailure(w, r)
	}
}

func publicDelegationProjection(d db.PublicDelegation, status, reason string) publicapiv1.Delegation {
	status, reason, terminal := publicapiv1.DelegationState(status, reason)
	return publicapiv1.Delegation{
		DelegationID: uuidToString(d.ID), TaskID: uuidToString(d.TaskID), WorkspaceID: uuidToString(d.WorkspaceID),
		AgentID: uuidToString(d.AgentID), RuntimeID: uuidToString(d.RuntimeID),
		Status: status, FailureReason: reason, Terminal: terminal,
		Revision:  publicapiv1.DelegationRevision(uuidToString(d.TaskID), status, reason),
		CreatedAt: d.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

func publicDelegationMemberCanInvoke(agent db.Agent, targets []db.AgentInvocationTarget, member db.Member) bool {
	return member.UserID.Valid && agent.WorkspaceID == member.WorkspaceID && agent.Kind == "user" &&
		(agent.OwnerID == member.UserID || (agent.PermissionMode == permissionModePublicTo && memberHitsInvocationTargets(targets, uuidToString(member.UserID))))
}

func requirePublicDelegationInvocation(w http.ResponseWriter, r *http.Request, agent db.Agent, targets []db.AgentInvocationTarget, member db.Member) bool {
	if !memberAllowedToViewAgent(agent, targets, uuidToString(member.UserID), member.Role) {
		publicReadNotFound(w, r)
		return false
	}
	if !publicDelegationMemberCanInvoke(agent, targets, member) {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "invocation_not_allowed", "invocation is not allowed")
		return false
	}
	return true
}

func publicDelegationBindingConflict(w http.ResponseWriter, r *http.Request) {
	publicapiv1.WriteProblem(w, r, http.StatusConflict, "runtime_binding_changed", "the confirmed runtime binding is unavailable; review and confirm a new request")
}
