-- name: CreatePublicDelegation :one
INSERT INTO public_delegation (id, workspace_id, requester_id, idempotency_key, payload_hash, agent_id, runtime_id, task_id, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (workspace_id, requester_id, idempotency_key) DO NOTHING
RETURNING *;

-- name: GetPublicDelegationByKey :one
SELECT * FROM public_delegation
WHERE workspace_id = $1 AND requester_id = $2 AND idempotency_key = $3;

-- name: LockPublicDelegationMember :one
SELECT * FROM member WHERE workspace_id = $1 AND user_id = $2 FOR SHARE;

-- name: LockPublicDelegationRuntime :one
SELECT * FROM agent_runtime WHERE id = $1 AND workspace_id = $2 FOR SHARE;

-- name: DeletePublicDelegationsForWorkspace :exec
DELETE FROM public_delegation WHERE workspace_id = $1;

-- name: CreatePublicDelegationTask :one
INSERT INTO agent_task_queue (
    id, agent_id, runtime_id, status, priority, context,
    originator_user_id, accountable_user_id, originator_source, max_attempts,
    trigger_summary
)
SELECT @id, @agent_id, @runtime_id, 'queued', 0, @context,
    @requester_id::uuid, @requester_id::uuid, 'direct_human', 1, @trigger_summary
WHERE lock_task_owner_rows(@agent_id, NULL, @runtime_id)
RETURNING *;
