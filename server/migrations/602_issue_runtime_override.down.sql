ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS runtime_override_id;
DROP TABLE IF EXISTS issue_runtime_override;
