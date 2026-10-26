-- name: GetIssueRuntimeOverride :one
SELECT o.* FROM issue_runtime_override o
JOIN issue i ON i.id=o.issue_id AND i.workspace_id=o.workspace_id
WHERE o.issue_id = @issue_id AND o.workspace_id = @workspace_id
  AND i.assignee_type='agent' AND i.assignee_id=o.agent_id;

-- name: SetIssueRuntimeOverride :exec
INSERT INTO issue_runtime_override(workspace_id,issue_id,agent_id,runtime_id,updated_by)
VALUES (@workspace_id,@issue_id,@agent_id,@runtime_id,@updated_by)
ON CONFLICT(issue_id) DO UPDATE SET
    agent_id=EXCLUDED.agent_id, runtime_id=EXCLUDED.runtime_id,
    updated_by=EXCLUDED.updated_by, updated_at=now();

-- name: DeleteIssueRuntimeOverride :exec
DELETE FROM issue_runtime_override WHERE issue_id = @issue_id AND workspace_id = @workspace_id;

-- name: DeleteIssueRuntimeOverridesByRuntime :exec
DELETE FROM issue_runtime_override WHERE runtime_id = $1;

-- name: CountExecutingIssueAgentTasks :one
SELECT count(*) FROM agent_task_queue
WHERE issue_id = @issue_id AND agent_id = @agent_id
  AND status IN ('dispatched','running','waiting_local_directory');

-- name: MovePendingIssueAgentTasksToRuntime :many
UPDATE agent_task_queue
SET runtime_id = @runtime_id, runtime_override_id=sqlc.narg('runtime_override_id'),
    session_id=NULL, work_dir=NULL, force_fresh_session=TRUE, native_resume_disabled=TRUE
WHERE issue_id = @issue_id AND agent_id = @agent_id AND status IN ('queued','deferred')
RETURNING *;

-- name: ResolveIssueTaskRuntime :one
SELECT COALESCE(issue_runtime_for_run(a.id,@issue_id),(SELECT binding.runtime_id FROM agent binding WHERE binding.id = a.id FOR SHARE))::uuid AS runtime_id
FROM agent a WHERE a.id = @agent_id;

-- name: HasPendingPublicDelegationForIssueAgent :one
SELECT EXISTS (
 SELECT 1 FROM public_delegation d JOIN agent_task_queue t ON t.id = d.task_id
 WHERE t.issue_id = @issue_id AND t.agent_id = @agent_id
 AND t.status IN ('queued','deferred')
);

-- name: LockIssueForRuntimeSwitch :one
-- Wakeup and edit transactions can already hold the issue before enqueuing.
-- Never wait on them while holding the profile lock they need for enqueue.
SELECT * FROM issue WHERE id = @issue_id AND workspace_id = @workspace_id FOR UPDATE NOWAIT;
