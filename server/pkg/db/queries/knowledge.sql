-- name: PinTaskKnowledgeManifest :one
UPDATE agent_task_queue
SET knowledge_manifest = COALESCE(knowledge_manifest, sqlc.arg(manifest)::jsonb)
WHERE id = sqlc.arg(task_id) AND runtime_id = sqlc.arg(runtime_id)
RETURNING knowledge_manifest;

-- name: GetSessionKnowledgeManifest :one
SELECT knowledge_manifest FROM agent_task_queue
WHERE agent_id = $1 AND session_id = $2 AND knowledge_manifest IS NOT NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;
