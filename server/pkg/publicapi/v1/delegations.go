package publicapiv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

type DelegationSource struct {
	System    string `json:"system"`
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
}

type DelegationTask struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
}

type CreateDelegationRequest struct {
	AgentID           string           `json:"agent_id"`
	ExpectedRuntimeID string           `json:"expected_runtime_id"`
	Source            DelegationSource `json:"source"`
	Task              DelegationTask   `json:"task"`
}

func (r *CreateDelegationRequest) PayloadHash() (string, error) {
	agentID, err := uuid.Parse(r.AgentID)
	if err != nil || agentID == uuid.Nil {
		return "", errors.New("agent_id must be a nonzero UUID")
	}
	runtimeID, err := uuid.Parse(r.ExpectedRuntimeID)
	if err != nil || runtimeID == uuid.Nil {
		return "", errors.New("expected_runtime_id must be a nonzero UUID")
	}
	r.AgentID, r.ExpectedRuntimeID = agentID.String(), runtimeID.String()
	if strings.TrimSpace(r.Source.System) == "" || len(r.Source.System) > 64 ||
		strings.TrimSpace(r.Source.ProjectID) == "" || len(r.Source.ProjectID) > 255 ||
		strings.TrimSpace(r.Source.TaskID) == "" || len(r.Source.TaskID) > 255 ||
		strings.TrimSpace(r.Task.Title) == "" || len(r.Task.Title) > 512 ||
		strings.TrimSpace(r.Task.Instructions) == "" || len(r.Task.Instructions) > 65536 {
		return "", errors.New("source identity and task title/instructions are required and must fit documented limits")
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

type Delegation struct {
	DelegationID string `json:"delegation_id"`
	TaskID       string `json:"task_id"`
	WorkspaceID  string `json:"workspace_id"`
	AgentID      string `json:"agent_id"`
	RuntimeID    string `json:"runtime_id"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
}
