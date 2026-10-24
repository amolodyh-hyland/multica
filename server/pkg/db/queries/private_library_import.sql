-- name: CreatePrivateLibraryImport :one
INSERT INTO private_library_import (id, workspace_id, owner_id, attempt_id, task_id, runtime_id, agent_id, project_id, preview_digest, content_hash, transport_hash, payload)
SELECT @id,@workspace_id,@owner_id,@attempt_id,t.id,@runtime_id,t.agent_id,@project_id,@preview_digest,@content_hash,@transport_hash,@payload FROM agent_task_queue t WHERE t.id = @task_id
ON CONFLICT (workspace_id, owner_id, attempt_id) DO NOTHING
RETURNING *;

-- name: GetPrivateLibraryImportByAttempt :one
SELECT * FROM private_library_import WHERE workspace_id = $1 AND owner_id = $2 AND attempt_id = $3;

-- name: GetPrivateLibraryImportForOwner :one
SELECT * FROM private_library_import WHERE id = $1 AND workspace_id = $2 AND owner_id = $3;

-- name: LockPrivateLibraryImportTask :one
SELECT t.* FROM agent_task_queue t
JOIN agent_runtime r ON r.id = t.runtime_id AND r.workspace_id = @workspace_id AND r.owner_id = @owner_id
JOIN agent a ON a.id = t.agent_id AND a.workspace_id = @workspace_id AND a.owner_id = @owner_id AND a.archived_at IS NULL AND a.kind = 'user' AND a.runtime_id = t.runtime_id
JOIN project p ON p.id = @project_id AND p.workspace_id = @workspace_id
LEFT JOIN issue i ON i.id = t.issue_id AND i.workspace_id = @workspace_id
LEFT JOIN chat_session c ON c.id = t.chat_session_id AND c.workspace_id = @workspace_id
WHERE t.id = @task_id AND t.runtime_id = @runtime_id
 AND (t.originator_user_id = @owner_id OR t.accountable_user_id = @owner_id)
 AND (CASE WHEN t.issue_id IS NOT NULL THEN i.project_id ELSE c.project_id END) = p.id
 AND t.status = 'queued' AND t.session_id IS NULL AND t.knowledge_manifest IS NULL AND t.private_library_manifest IS NULL
FOR UPDATE OF t
FOR SHARE OF r, a, p;

-- name: CountPrivateLibraryImportsForTask :one
SELECT count(*) FROM private_library_import WHERE workspace_id = $1 AND task_id = $2;

-- name: GetPrivateLibraryImportForTask :one
SELECT b.* FROM private_library_import b
JOIN agent_task_queue t ON t.id = b.task_id AND t.agent_id = b.agent_id AND t.runtime_id = b.runtime_id
JOIN agent_runtime r ON r.id = b.runtime_id AND r.workspace_id = b.workspace_id AND r.owner_id = b.owner_id
JOIN agent a ON a.id = b.agent_id AND a.workspace_id = b.workspace_id AND a.owner_id = b.owner_id AND a.runtime_id = r.id AND a.archived_at IS NULL AND a.kind = 'user'
JOIN member m ON m.workspace_id = b.workspace_id AND m.user_id = b.owner_id
WHERE b.id = @id AND b.workspace_id = @workspace_id AND b.task_id = @task_id AND b.agent_id = @agent_id AND b.owner_id = @owner_id
 AND t.status IN ('dispatched', 'waiting_local_directory', 'running');

-- name: DeletePrivateLibraryImportsForWorkspace :exec
DELETE FROM private_library_import WHERE workspace_id = $1;

-- name: ListPrivateLibraryImportsForTask :many
SELECT b.* FROM private_library_import b
JOIN agent_task_queue t ON t.id = b.task_id AND t.agent_id = b.agent_id AND t.runtime_id = b.runtime_id
JOIN agent_runtime r ON r.id = b.runtime_id AND r.workspace_id = b.workspace_id AND r.owner_id = b.owner_id
JOIN agent a ON a.id = b.agent_id AND a.workspace_id = b.workspace_id AND a.owner_id = b.owner_id AND a.runtime_id = r.id AND a.archived_at IS NULL AND a.kind = 'user'
JOIN member m ON m.workspace_id = b.workspace_id AND m.user_id = b.owner_id
WHERE b.workspace_id = @workspace_id AND b.task_id = @task_id AND b.owner_id = @owner_id
ORDER BY b.created_at, b.id
LIMIT 65;

-- name: PinTaskPrivateLibraryManifest :one
UPDATE agent_task_queue
SET private_library_manifest = COALESCE(private_library_manifest, sqlc.arg(manifest)::jsonb)
WHERE id = sqlc.arg(task_id) AND runtime_id = sqlc.arg(runtime_id) AND knowledge_manifest IS NOT NULL
RETURNING private_library_manifest;

-- name: GetSessionPrivateLibraryManifest :one
SELECT private_library_manifest FROM agent_task_queue
WHERE agent_id = $1 AND session_id = $2 AND knowledge_manifest IS NOT NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;
