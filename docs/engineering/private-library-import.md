# Private library imports

TK-MA40 milestone 1 introduces private storage, consent, and task access. It does not activate provider loading. No import writes `skill`, `skill_file`, or an agent's catalog bindings. Existing catalog import permissions remain unchanged.

The portable entry field names, content framing, canonical JSON, and golden vectors are pinned to the shared-library v1 model at revision `3b1cba3cd` (registry change #49). `server/pkg/libraryimport/testdata/entry-v1.json` is the portable manifest fixture. The adapter reuses `knowledge.ContentHash` for exact declarative text and `skillbundle.BuildManifest` for the separate immutable transport hash. Destination object IDs are allocated independently of source entry IDs. Origin authority/source/entry identity is retained; source provenance never grants destination access or execution permission.

## Consent and transfer

Consumers build a local `libraryimport.Preview`, display its destination server ID/origin, Workspace, owner, audience, entry identity/hash, every relative file path/hash/byte count, total bytes, exclusions, Project, optional persona tuple, runtime and exact task. Only after human confirmation of that exact digest may they call `Confirm` and `Upload`. Registration, inspection, and source load approval do not confirm transfer. No automatic uploader or background inventory disclosure is installed.

`Upload` freezes and validates the request locally, makes a metadata-free authenticated capabilities request, and checks the complete destination before POST. It disables redirects and checks that the receipt binds a valid destination object ID to the transport hash derived from the exact uploaded bytes. Cancellation, absent/expired consent, unavailable private storage, and destination changes prevent upload. Receipts expire after 15 minutes and authorize one attempt. Changing any disclosed field requires confirmation again. The request has a 16 MiB serialized limit and the existing lower 1 MiB/file, 8 MiB/entry and 256-file limits. Unsupported kinds, formats, execution requirements, ambiguous JSON, case-folded aliases, unsafe paths and changed bytes are rejected as a whole. Managed bundles fail with `policy_unavailable`; no managed-policy resolver is implied.

The server must have an explicitly configured stable `MULTICA_PRIVATE_IMPORT_AUTHORITY_ID`, a canonical `MULTICA_PUBLIC_URL` (HTTPS, or HTTP loopback for local use), and all storage migrations/indexes ready. Otherwise capabilities reports `private_import_unsupported` and POST rejects before reading a body. Identity is configuration-owned, never derived from request host headers. Clients must preflight; direct POST cannot retract bytes a caller has already sent.

## API and transaction

- `GET /api/private-library/capabilities`: human membership, no file metadata in the request.
- `POST /api/private-library/imports`: exact local consent receipt, immutable payload, and explicit binding; returns a storage receipt.
- `GET /api/private-library/imports/{id}`: owner-only complete object, including its retained selection and the existing bundle wire shape.
- `GET /api/private-library/tasks/{taskId}/imports/{id}`: existing task token for that exact invocation, with middleware-stamped task, persona, owner and Workspace identity. Broad daemon credentials do not grant private access. Other members, Workspace administrators, other tasks, broad daemon tokens and other Workspaces receive no private content.

New imports require a queued issue/chat task without an established session or knowledge snapshot, matching the explicit Project. The human must own its runtime and native persona and be its recorded originator or accountable human. An optional persona reference is `(serverId, workspaceId, personaId)` and must match the task's native agent ID; a daemon profile is never a persona ID. Other task origins fail closed in this milestone. An offline runtime may retain an inert import; this does not make it runnable.

A transaction fences Workspace deletion, locks membership and the task/binding rows, enforces the combined 64-binding limit, and commits the complete private object once. Duplicate `(Workspace, owner, attempt)` requests return the original receipt when their complete preview digest matches, including after dispatch. A changed digest conflicts. Failed/interrupted transactions publish no object. Reads return the retained bytes and verify both hashes; they never rescan sources. Consent expiry does not erase historical objects or grant new uses. Workspace deletion removes these server-owned rows; no external file is touched.

## Remaining milestone

Storage receipts report `stored` and `unsupported_provider`. `approvalRef` acknowledges consent to store the exact digest under the destination authority; it is not a load/invocation grant. There is no publication endpoint, shared catalog fallback, Project-wide discovery, provider readiness, or reload in this milestone. Destination policy and load approval must be checked again before any future protected use.

Milestone 2 must integrate immutable claim/resume selection and private cache scope, rules through existing context, and the first supported provider projection with actual allocated binding metadata and pinned provider conformance. It must first replace sidecar rollback/reuse's path-only deletion authority with verified owner/generation, filesystem identity and baseline hashes. Edited, newly added, replaced, symlinked, or legacy-unverifiable files must remain with visible cleanup conflicts; only unchanged owned files and empty owned directories may be removed. No private payload currently reaches those cleanup or provider paths.

DB-backed API/transaction tests are compiled separately and require an isolated migrated Postgres to execute. No live server is necessary for manifest, consent, preflight, cancellation, redirect, and transport tests.
