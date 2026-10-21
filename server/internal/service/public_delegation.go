package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

const PublicDelegationContextType = "public_delegation"

type PublicDelegationContext struct {
	Type         string                     `json:"type"`
	DelegationID string                     `json:"delegation_id"`
	WorkspaceID  string                     `json:"workspace_id"`
	Task         publicapiv1.DelegationTask `json:"task"`
}

func ParsePublicDelegationContext(task db.AgentTaskQueue) (PublicDelegationContext, bool) {
	var payload PublicDelegationContext
	if task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid ||
		json.Unmarshal(task.Context, &payload) != nil || payload.Type != PublicDelegationContextType ||
		payload.DelegationID == "" || payload.WorkspaceID == "" || payload.Task.Instructions == "" {
		return PublicDelegationContext{}, false
	}
	return payload, true
}

var ErrPublicDelegationNotFound = errors.New("delegation not found")
var ErrPublicDelegationExecutionUnavailable = errors.New("delegation execution unavailable")

var ErrPublicDelegationRevision = errors.New("delegation revision changed")

func (s *TaskService) CancelPublicDelegation(ctx context.Context, d db.PublicDelegation, userID pgtype.UUID, ifMatch string) (*CancelTaskResult, error) {
	if s.TxStarter == nil {
		return nil, errors.New("delegation cancellation requires a transaction")
	}
	return s.CancelTaskWithResult(ctx, d.TaskID, CancelTaskOptions{
		ClientSupportsDraftRestore: true,
		UserInitiated:              true,
		CancelledBy:                TaskCancellationActor{Type: "member", ID: userID},
		authorize: func(ctx context.Context, q *db.Queries) error {
			if _, err := q.LockWorkspaceForChatSessionCreate(ctx, d.WorkspaceID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrPublicDelegationNotFound
				}
				return fmt.Errorf("authorize delegation cancellation: %w", err)
			}
			member, err := q.LockPublicDelegationMember(ctx, db.LockPublicDelegationMemberParams{WorkspaceID: d.WorkspaceID, UserID: userID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrPublicDelegationNotFound
				}
				return fmt.Errorf("authorize delegation cancellation: %w", err)
			}
			accepted, err := q.GetPublicDelegationForActor(ctx, db.GetPublicDelegationForActorParams{
				ID: d.ID, WorkspaceID: d.WorkspaceID, RequesterID: userID, IsAdmin: member.Role == "admin" || member.Role == "owner",
			})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrPublicDelegationNotFound
				}
				return fmt.Errorf("authorize delegation cancellation: %w", err)
			}
			if accepted.TaskID != d.TaskID {
				return ErrPublicDelegationNotFound
			}
			task, err := q.LockPublicDelegationTask(ctx, db.LockPublicDelegationTaskParams{DelegationID: d.ID, WorkspaceID: d.WorkspaceID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrPublicDelegationExecutionUnavailable
				}
				return fmt.Errorf("authorize delegation cancellation: %w", err)
			}
			status, reason, _ := publicapiv1.DelegationState(task.Status, task.FailureReason.String)
			if ifMatch != "" && ifMatch != "*" && ifMatch != "\""+publicapiv1.DelegationRevision(util.UUIDToString(d.TaskID), status, reason)+"\"" {
				return ErrPublicDelegationRevision
			}
			return nil
		},
	})
}
