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
	DelegationID  string `json:"delegation_id"`
	TaskID        string `json:"task_id"`
	WorkspaceID   string `json:"workspace_id"`
	AgentID       string `json:"agent_id"`
	RuntimeID     string `json:"runtime_id"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	FailureReason string `json:"failure_reason,omitempty"`
	Terminal      bool   `json:"terminal"`
	Revision      string `json:"revision"`
}

type DelegationListResponse struct {
	Delegations []Delegation `json:"delegations"`
	PageInfo
}

func DelegationState(status, reason string) (string, string, bool) {
	switch status {
	case "queued", "deferred", "dispatched", "waiting_local_directory", "running":
		return status, "", false
	case "completed":
		return status, "", true
	case "failed", "cancelled":
		failure := ""
		switch reason {
		case "error_delegation_invocation_not_allowed", "runtime_access_denied", "runtime_access_revoked":
			failure = "access_revoked"
		case "invalid_task_identity":
			failure = "target_unavailable"
		case "queued_expired", "timeout", "runtime_cli_timeout":
			failure = "timeout"
		case "runtime_offline", "runtime_reconnect_timeout", "runtime_recovery":
			failure = "runtime_unavailable"
		default:
			if status == "failed" {
				failure = "execution_failed"
			} else if reason != "" {
				failure = "execution_cancelled"
			}
		}
		return status, failure, true
	default:
		return "unavailable", "execution_unavailable", false
	}
}

func DelegationRevision(taskID, status, failureReason string) string {
	sum := sha256.Sum256([]byte(taskID + "\x00" + status + "\x00" + failureReason))
	return hex.EncodeToString(sum[:])
}
