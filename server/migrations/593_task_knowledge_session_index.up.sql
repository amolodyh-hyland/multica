CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_knowledge_session ON agent_task_queue (agent_id, session_id, created_at DESC, id DESC) WHERE knowledge_manifest IS NOT NULL;
