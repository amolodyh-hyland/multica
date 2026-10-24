CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS private_library_import_attempt_idx ON private_library_import (workspace_id, owner_id, attempt_id);
