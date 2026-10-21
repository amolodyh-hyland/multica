CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS public_delegation_identity_idx ON public_delegation (workspace_id, requester_id, idempotency_key);
