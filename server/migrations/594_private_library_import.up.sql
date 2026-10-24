CREATE TABLE IF NOT EXISTS private_library_import (
    id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    owner_id UUID NOT NULL,
    attempt_id TEXT NOT NULL,
    task_id UUID NOT NULL,
    runtime_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    project_id UUID NOT NULL,
    preview_digest TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    transport_hash TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
