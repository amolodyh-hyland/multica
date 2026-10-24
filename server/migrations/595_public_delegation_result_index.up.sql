CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS public_delegation_result_identity_idx ON public_delegation_result (workspace_id, delegation_id, revision);
