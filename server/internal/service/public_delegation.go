package service

import (
	"encoding/json"

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
