-- name: CreatePrivateLibraryImport :one
INSERT INTO private_library_import (id, workspace_id, owner_id, attempt_id, task_id, runtime_id, agent_id, project_id, preview_digest, content_hash, transport_hash, payload)
SELECT @id,@workspace_id,@owner_id,@attempt_id,t.id,@runtime_id,t.agent_id,@project_id,@preview_digest,@content_hash,@transport_hash,@payload FROM agent_task_queue t WHERE t.id = @task_id
ON CONFLICT (workspace_id, owner_id, attempt_id) DO NOTHING
RETURNING *;

-- name: GetPrivateLibraryImportByAttempt :one
SELECT * FROM private_library_import WHERE workspace_id = $1 AND owner_id = $2 AND attempt_id = $3;

-- name: GetPrivateLibraryImportForOwner :one
SELECT * FROM private_library_import WHERE id = $1 AND workspace_id = $2 AND owner_id = $3 AND removed_at IS NULL;

-- name: LockPrivateLibraryImportRuntime :one
SELECT r.id FROM agent_runtime r
WHERE r.id = @runtime_id AND r.workspace_id = @workspace_id AND r.owner_id = @owner_id
FOR SHARE;

-- name: LockPrivateLibraryImportAgent :one
SELECT a.id FROM agent a
WHERE a.id = @agent_id AND a.workspace_id = @workspace_id AND a.owner_id = @owner_id
 AND a.runtime_id = @runtime_id AND a.archived_at IS NULL AND a.kind = 'user'
FOR SHARE;

-- name: LockPrivateLibraryImportProject :one
SELECT id FROM project WHERE id = @project_id AND workspace_id = @workspace_id
FOR SHARE;

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
FOR UPDATE OF t;

-- name: CountPrivateLibraryImportsForTask :one
SELECT count(*) FROM private_library_import WHERE workspace_id = $1 AND task_id = $2 AND removed_at IS NULL;

-- name: GetPrivateLibraryImportForTask :one
SELECT b.* FROM private_library_import b
JOIN agent_task_queue t ON t.id = b.task_id AND t.agent_id = b.agent_id AND t.runtime_id = b.runtime_id
JOIN agent_runtime r ON r.id = b.runtime_id AND r.workspace_id = b.workspace_id AND r.owner_id = b.owner_id
JOIN agent a ON a.id = b.agent_id AND a.workspace_id = b.workspace_id AND a.owner_id = b.owner_id AND a.runtime_id = r.id AND a.archived_at IS NULL AND a.kind = 'user'
JOIN member m ON m.workspace_id = b.workspace_id AND m.user_id = b.owner_id
JOIN project p ON p.id = b.project_id AND p.workspace_id = b.workspace_id
LEFT JOIN issue i ON i.id = t.issue_id AND i.workspace_id = b.workspace_id
LEFT JOIN chat_session c ON c.id = t.chat_session_id AND c.workspace_id = b.workspace_id
WHERE b.removed_at IS NULL AND b.id = @id AND b.workspace_id = @workspace_id AND b.task_id = @task_id AND b.agent_id = @agent_id AND b.owner_id = @owner_id
 AND (CASE WHEN t.issue_id IS NOT NULL THEN i.project_id ELSE c.project_id END) = b.project_id
 AND t.status IN ('dispatched', 'waiting_local_directory', 'running');

-- name: DeletePrivateLibraryImportsForWorkspace :exec
DELETE FROM private_library_import WHERE workspace_id = $1;

-- name: ListPrivateLibraryImportsForTask :many
SELECT b.* FROM private_library_import b
JOIN agent_task_queue t ON t.id = b.task_id AND t.agent_id = b.agent_id AND t.runtime_id = b.runtime_id
JOIN agent_runtime r ON r.id = b.runtime_id AND r.workspace_id = b.workspace_id AND r.owner_id = b.owner_id
JOIN agent a ON a.id = b.agent_id AND a.workspace_id = b.workspace_id AND a.owner_id = b.owner_id AND a.runtime_id = r.id AND a.archived_at IS NULL AND a.kind = 'user'
JOIN member m ON m.workspace_id = b.workspace_id AND m.user_id = b.owner_id
JOIN project p ON p.id = b.project_id AND p.workspace_id = b.workspace_id
LEFT JOIN issue i ON i.id = t.issue_id AND i.workspace_id = b.workspace_id
LEFT JOIN chat_session c ON c.id = t.chat_session_id AND c.workspace_id = b.workspace_id
WHERE b.removed_at IS NULL AND b.workspace_id = @workspace_id AND b.task_id = @task_id AND b.owner_id = @owner_id
 AND (CASE WHEN t.issue_id IS NOT NULL THEN i.project_id ELSE c.project_id END) = b.project_id
ORDER BY b.created_at, b.id
LIMIT 65;

-- name: PinTaskPrivateLibraryManifest :one
UPDATE agent_task_queue
SET private_library_manifest = COALESCE(private_library_manifest, sqlc.arg(manifest)::jsonb)
WHERE id = sqlc.arg(task_id) AND runtime_id = sqlc.arg(runtime_id) AND knowledge_manifest IS NOT NULL
RETURNING private_library_manifest;

-- name: GetSessionPrivateLibraryManifest :one
SELECT (CASE WHEN octet_length(private_library_manifest::text) > sqlc.arg(max_selection_bytes)::integer THEN '{}'::jsonb ELSE private_library_manifest END)::jsonb AS private_library_manifest FROM agent_task_queue
WHERE agent_id = @agent_id AND session_id = @session_id AND knowledge_manifest IS NOT NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: PrivateLibraryTaskBudget :one
SELECT count(*)::bigint AS import_count,
 COALESCE(sum((payload #>> '{preview,totalBytes}')::bigint),0)::bigint AS total_bytes,
 COALESCE(sum(jsonb_array_length(payload #> '{preview,entry,files}')),0)::bigint AS total_files,
 COALESCE(sum(octet_length(payload::text)),0)::bigint AS serialized_payload_bytes,
 -- Go JSON may escape each UTF-8 byte as six ASCII bytes; include each selected envelope.
 COALESCE(sum(6::bigint * octet_length((payload #> '{preview,entry}')::text) + 1024),0)::bigint AS selection_bytes_upper_bound
FROM private_library_import WHERE workspace_id = $1 AND task_id = $2 AND removed_at IS NULL;

-- name: RemovePrivateLibraryImportForOwner :one
UPDATE private_library_import b SET payload = '{}'::jsonb, removed_at = now()
WHERE b.id = @id AND b.workspace_id = @workspace_id AND b.owner_id = @owner_id
 AND b.preview_digest = @preview_digest AND b.removed_at IS NULL
 AND EXISTS (SELECT 1 FROM member m WHERE m.workspace_id = @workspace_id AND m.user_id = @owner_id)
RETURNING b.id;

-- name: ListPrivateLibraryImportTaskIDsForIssue :many
SELECT id FROM agent_task_queue WHERE issue_id = $1;

-- name: DeleteUnreferencedPrivateLibraryImportsForTasks :execrows
DELETE FROM private_library_import b
WHERE b.workspace_id = @workspace_id AND b.task_id = ANY(@task_ids::uuid[])
 AND NOT EXISTS (SELECT 1 FROM agent_task_queue t WHERE t.id = b.task_id);

-- name: PruneUnreferencedPrivateLibraryImports :execrows
WITH batch AS (
 SELECT b.id FROM private_library_import b
 WHERE NOT EXISTS (SELECT 1 FROM agent_task_queue t WHERE t.id = b.task_id)
 ORDER BY b.id LIMIT @batch_size
 FOR UPDATE OF b SKIP LOCKED
)
DELETE FROM private_library_import WHERE id IN (SELECT id FROM batch);
