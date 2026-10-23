# Project instructions

Projects can carry nullable `instructions` independently of `description`. A Project does not need a repository or an execution-directory resource to use instructions. Descriptions retain their existing summary, search and runtime-context behavior; no description is copied into instructions.

## API contract

`POST /api/projects` accepts an optional string or null `instructions`. `PUT /api/projects/{id}` accepts the same field: an omitted key preserves the stored value, null clears it, and a string replaces it. Updates use a field-presence flag in SQL, so unrelated concurrent updates cannot restore stale instructions. The update query also requires the selected workspace ID.

Project create, get, list, update, search and search-index responses include `instructions` as a nullable string. The shared client validates Project responses and defaults an absent field to null for older servers. Older clients can omit the field without changing existing instructions. Installed daemons that predate the field ignore it; instructions reach runtime context with an updated daemon.

The limit is **32,768 UTF-8 bytes**, enforced by the API, the shared editor and a database `octet_length` CHECK constraint. The API returns 400 for oversized values. Instructions are stored verbatim as text, not interpreted as commands, configuration or resource paths.

## Task and chat context

The existing claim resolver reads the Project using both Project ID and workspace ID. Issue tasks, Project chats, autopilot runs and quick-create tasks receive a separate optional `project_instructions` field only after that scoped read succeeds. A missing, deleted or foreign-workspace Project supplies no Project instructions. Transient read failures retain the existing claim failure behavior.

The daemon passes the claimed value into the prepared runtime brief and `.multica/project/resources.json`, including Projects with no resources. Runtime briefs label Project instructions separately from descriptions and retain the existing identity and permission restrictions. Preparing context only writes text/JSON data; it never evaluates instructions or imports execution configuration. Each claim resolves the current stored value, using the existing task preparation lifecycle.

## Migration and cleanup

`590_project_instructions` follows migration 589. It adds a nullable column and its size CHECK as `NOT VALID`, without scanning existing rows or backfilling descriptions. `591_project_instructions_validate` validates the CHECK separately under `SHARE UPDATE EXCLUSIVE`, allowing Project reads and writes during the scan. Rolling back 591 recreates the unvalidated CHECK while retaining enforcement on new writes; rolling back 590 removes the column. Neither migration creates an index or needs a concurrent-index cleanup hook; this classification is recorded beside the cleanup registry in `server/cmd/migrate/main.go`.

Instructions belong to the Project row, classified as deleted in the workspace deletion manifest. Project deletion removes instructions with that row and clears chat Project references using the existing transaction. Stale soft references cannot restore deleted instructions through the claim resolver. Prepared context files use the existing sidecar ownership manifest and cleanup lifecycle; no additional files or references are introduced.

## Verification

Coverage includes reversible migration DDL and an opt-in isolated Postgres migration test; create/read/list/search, byte limits, omission/null and concurrent partial updates; cross-workspace read/write denial; issue/chat claim propagation; runtime briefs and context JSON for repository-free Projects; Project deletion, stale references and sidecar cleanup. Frontend tests cover response defaults, malformed payloads and editing/clearing instructions separately from descriptions.

DB-backed handler tests need a migrated Postgres database. The migration round-trip test uses `MULTICA_MIGRATION_TEST_DATABASE_URL`. Without Postgres these checks are compile-only or skipped; unit tests do not establish database behavior.

Knowledge attachments use a separate typed selection and immutable text snapshot. See [Project knowledge references](project-knowledge.md) for ownership, bounded resolution, capability gating, and migration 592. Execution-directory resources retain their existing behavior.
