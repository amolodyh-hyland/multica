CREATE TABLE public_delegation_result (
    delegation_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    requester_id uuid NOT NULL,
    revision text NOT NULL CHECK (revision ~ '^[a-f0-9]{64}$'),
    snapshot jsonb NOT NULL CHECK (octet_length(snapshot::text) <= 262144),
    created_at timestamptz NOT NULL DEFAULT now()
);
