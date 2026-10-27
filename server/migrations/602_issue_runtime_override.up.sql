CREATE TABLE issue_runtime_override (
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    runtime_id UUID NOT NULL,
    updated_by UUID NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE agent_task_queue ADD COLUMN runtime_override_id UUID;
