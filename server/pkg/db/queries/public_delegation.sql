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

-- name: LockPublicDelegationInvocationTargets :many
SELECT * FROM agent_invocation_target
WHERE agent_id = $1
ORDER BY target_type, created_at, id
FOR SHARE;

-- name: GetPublicDelegationForActor :one
SELECT * FROM public_delegation
WHERE id = @id AND workspace_id = @workspace_id
  AND (requester_id = @requester_id OR @is_admin::boolean);

-- name: ListPublicDelegationStatuses :many
SELECT sqlc.embed(d), COALESCE(t.status, 'unavailable')::text AS status,
       COALESCE(t.failure_reason, '')::text AS failure_reason
FROM public_delegation d
LEFT JOIN agent_task_queue t ON t.id = d.task_id AND t.agent_id = d.agent_id AND t.runtime_id = d.runtime_id
WHERE d.workspace_id = @workspace_id AND d.requester_id = @requester_id
  AND (sqlc.narg('idempotency_key')::text IS NULL OR d.idempotency_key = sqlc.narg('idempotency_key'))
  AND (sqlc.narg('delegation_id')::uuid IS NULL OR d.id = sqlc.narg('delegation_id'))
  AND (sqlc.narg('after_id')::uuid IS NULL OR d.id > sqlc.narg('after_id'))
ORDER BY d.id
LIMIT @page_limit;

-- name: LockPublicDelegationTask :one
SELECT t.* FROM agent_task_queue t
JOIN public_delegation d ON d.task_id = t.id AND d.agent_id = t.agent_id AND d.runtime_id = t.runtime_id
WHERE d.id = @delegation_id AND d.workspace_id = @workspace_id
FOR UPDATE OF t;

-- name: GetPublicDelegationStatusForActor :one
SELECT sqlc.embed(d), COALESCE(t.status, 'unavailable')::text AS status,
       COALESCE(t.failure_reason, '')::text AS failure_reason
FROM public_delegation d
LEFT JOIN agent_task_queue t ON t.id = d.task_id AND t.agent_id = d.agent_id AND t.runtime_id = d.runtime_id
WHERE d.id = @id AND d.workspace_id = @workspace_id
  AND (d.requester_id = @requester_id OR @is_admin::boolean);

-- name: RecordPublicDelegationResult :exec
INSERT INTO public_delegation_result (delegation_id, workspace_id, requester_id, revision, snapshot)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, delegation_id, revision) DO NOTHING;

-- name: DeletePublicDelegationResultsForWorkspace :exec
DELETE FROM public_delegation_result WHERE workspace_id = $1;
