CREATE TABLE public_delegation (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    requester_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 255),
    payload_hash text NOT NULL CHECK (length(payload_hash) = 64),
    agent_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    task_id uuid NOT NULL,
    source jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
