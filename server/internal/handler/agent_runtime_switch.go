package handler

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func recordAgentRuntimeSwitch(ctx context.Context, q *db.Queries, before db.Agent, runtime db.AgentRuntime, actorType string, actorID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	tasks, err := q.MovePendingAgentTasksToRuntime(ctx, db.MovePendingAgentTasksToRuntimeParams{
		AgentID:   before.ID,
		RuntimeID: runtime.ID,
	})
	if err != nil {
		return nil, err
	}
	oldProvider := ""
	if before.RuntimeID.Valid {
		oldRuntime, err := q.GetAgentRuntime(ctx, before.RuntimeID)
		if err != nil {
			return nil, err
		}
		oldProvider = oldRuntime.Provider
	}
	taskIDs := make([]string, len(tasks))
	for i, task := range tasks {
		taskIDs[i] = uuidToString(task.ID)
	}
	details, _ := json.Marshal(map[string]any{
		"agent_id":         uuidToString(before.ID),
		"agent_name":       before.Name,
		"from_runtime_id":  uuidToString(before.RuntimeID),
		"to_runtime_id":    uuidToString(runtime.ID),
		"from_provider":    oldProvider,
		"to_provider":      runtime.Provider,
		"pending_task_ids": taskIDs,
	})
	_, err = q.CreateActivity(ctx, db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: before.WorkspaceID,
		ActorType:   pgtype.Text{String: actorType, Valid: true},
		ActorID:     actorID,
		Action:      "agent_runtime_changed",
		Details:     details,
	})
	return tasks, err
}
