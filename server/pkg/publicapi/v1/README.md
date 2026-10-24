# Multica Public API v1

This package is the source of truth for the first versioned Public API slice:

- `openapi.yaml` defines the wire contract.
- `routes.go` is the capability ledger used by router and contract tests.
- `types.go` contains DTOs that are deliberately independent from App API
  responses and database models.
- `problem.go` defines the stable error envelope used before and after routing.
- `foundation.go` defines actor, credential, pagination, idempotency, revision,
  risk, audit, and rate-limit vocabulary shared by every future resource slice.

The Plugin API and member API expose the shared resource contract through
separate trust boundaries. The Plugin surface remains at `/v1`; its
installation/callback tokens, manifest scopes, and stricter limiter are
unchanged. The member surface is mounted at `/api/v1` and currently accepts
scoped member PATs only. Plugin context and storage are marked
`plugin_extension` and are not available through the member surface.

Both surfaces should call the same Go service and handler layer. The Plugin API
must not make an additional HTTP request to the user/PAT API.

Additive fields and endpoints remain in v1. A breaking wire change requires a
new major version and a migration window. New write operations should adopt the
shared `Idempotency-Key` and revision/ETag components rather than inventing
endpoint-specific concurrency rules.

## Foundation rules

- A scoped `mul_` PAT authenticates a member on `/api/v1`. Create one through
  the existing authenticated `POST /api/tokens` endpoint with a workspace and
  a `scopes` list, for example
  `{"name":"AO","workspace_id":"<workspace-uuid>","scopes":["issues:read","issues:write"]}`.
  Accepted scopes come from the capability ledger: `issues:read`,
  `issues:write`, `comments:read`, `comments:write`, `agents:read`, and
  `runtimes:read`, `delegations:read`, and `delegations:write`. A PAT created without
  scopes, including a legacy PAT, continues to work on the existing `/api`
  surface but is rejected on `/api/v1` with `scoped_token_required`. A scoped
  PAT is bound to one workspace, must present that workspace on each request,
  and is rejected on legacy `/api`, daemon, and realtime surfaces with
  `public_api_credential_only`.
- PAT creation accepts an interactive session JWT or an unscoped member PAT
  through the first-party management endpoint. Task tokens, Cloud PATs, Plugin
  credentials, and scoped PATs cannot issue PATs. The issuer must be a member
  of the requested workspace; scope grants are checked against the role policy
  in the capability ledger. The current issue, comment, agent and runtime scopes are available
  to `member`, `admin`, and `owner` roles. The `/api/cli-token` exchange accepts
  only an interactive session JWT with signed provenance; task tokens, Cloud
  PATs, Plugin credentials, PATs, and JWTs without that provenance cannot be
  exchanged into a session. `/api/auth/refresh` applies the same provenance
  check. Existing unmarked JWTs continue to authenticate regular `/api`
  requests until expiry, but cannot be refreshed or exchanged and require a
  fresh login for those operations.
- The member surface does not currently accept session JWTs or cookies, task
  tokens (`mat_`), Cloud PATs (`mcn_`), or Plugin tokens (`mpi_`, `mpc_`).
  These credentials are rejected before resource routing. OAuth access tokens
  are not currently issued by Multica; a future verifier can install the same
  typed member principal after validating token issuer, audience, identity, and
  scopes.
- Plugin installation/invocation credentials remain confined to `/v1` and
  authenticate either a member-bounded invocation or the installation itself.
  Authentication happens at the surface, before a shared resource service
  runs. Every operation also checks current workspace membership.
- Collection endpoints use opaque cursor pagination with `limit` (default 50,
  maximum 200) and return `next_cursor`. The migrated comment endpoint keeps
  its existing newest-200 compatibility window until its dedicated pagination
  slice lands; it is not a precedent for new collections.
- Create, trigger, replay, and retry operations must durably deduplicate an
  `Idempotency-Key` (maximum 255 bytes) before the operation is documented as
  idempotent. Merely accepting the header is insufficient.
- Mutable resources expose a revision and ETag. Conditional writes accept
  `If-Match`; stale writes return `revision_conflict`.
- The capability ledger states credential families, scope, risk, audit, and
  rate-limit policy. Member PATs have a separate `user_default` profile of 300
  requests per minute by default (`RATE_LIMIT_USER_PUBLIC_API` overrides it).
  Plugin requests continue to use their existing `plugin_strict` profile and
  `RATE_LIMIT_PLUGIN_API` setting even when the resource DTO and service are
  shared.
- Public API failures use an `application/problem+json` envelope with stable
  `code`, `status`, `detail`, `request_id`, and compatibility `error` fields.
  User-surface credential codes include `credential_required`,
  `unsupported_credential`, `invalid_token`, `scoped_token_required`, and
  `insufficient_scope`; PAT issuance also documents `issuer_required`,
  `pat_issuer_not_allowed`, `public_api_credential_only`,
  `workspace_id_required`, `workspace_binding_required`, and
  `scope_not_permitted_for_role`; CLI token exchange uses
  `cli_token_issuer_not_allowed`. Workspace isolation returns `not_found` for
  a workspace the member cannot access; limiter failures return `rate_limited`
  and `Retry-After`.
- Audit requirements use an explicit lifecycle (`planned` or `enforced`). The
  migrated issue/comment operations remain `planned` until a durable audit
  sink is wired. This surface does not claim that a durable audit write is
  implemented; the ledger does not imply that a boolean marker performs one.

## Rollout ledger

The migrated slice is Issue read/content update, Comment read/create, and safe
Agent/Runtime reads. Context
and Storage remain Plugin extensions; Hook and invocation delivery remain on
the bridge. Projects, Members, Issue search/create/transition, Agent writes, Squads,
Skills, Tasks/Runs, and Autopilots are subsequent vertical slices. Each slice
adds its Public contract first, then explicitly opts the safe Plugin subset into
the capability ledger.

## Profile and runtime reads

These operations are member-only; Plugin credentials do not gain access:

| Operation | Required scope | Response |
| --- | --- | --- |
| `GET /api/v1/agents` | `agents:read` | `agents` and optional `next_cursor` |
| `GET /api/v1/agents/{agent_id}` | `agents:read` | Safe profile |
| `GET /api/v1/runtimes` | `runtimes:read` | `runtimes` and optional `next_cursor` |
| `GET /api/v1/runtimes/{runtime_id}` | `runtimes:read` | Safe runtime |

Send `X-Workspace-ID` matching the PAT's workspace binding. Resource IDs are
stable UUIDs. Lists order by UUID, accept `limit` (1–200, default 50) and `cursor`,
and recheck current membership and visibility on every page. Cursors are scoped
to the actor, workspace, collection and filters; they never grant access. Agent
lists exclude archived profiles by default; `include_archived=true` includes
them. Both lists currently reuse the workspace inventory queries and filter in
memory before pagination; response sizes are bounded, inventory reads are not.

Profiles expose only `agent_id`, `workspace_id`, `name`, `runtime_id`,
`runtime_bound`, `permission_mode`, `can_invoke`, `invocation_targets`,
`archived`, and `availability`. Visibility follows the existing profile rules:
profile owners and workspace owners/admins can view private profiles, while
regular members need a workspace or matching member invocation target on a
`public_to` profile. Team targets remain inert. Workspace governance visibility
does not grant invocation: private profiles remain invocable only by their
profile owner. Archived profiles always report `can_invoke=false`.

`can_invoke` describes permission, independently of readiness or capacity; it is
not an admission guarantee. Availability is `online`, `unstable`, `offline`,
`unbound`, `unavailable`, or `archived`. Unbound profiles have `runtime_bound=false`
and an empty runtime ID. Missing or inconsistent runtime bindings report
`unavailable` and an empty runtime ID. A visible profile can reference a hidden
runtime in the same workspace: only its stable runtime ID and coarse health are
exposed, matching the existing profile presence boundary.

Runtime lists and detail reads consistently require an owned or explicitly
public runtime with a valid owner, with no workspace admin bypass. Runtime DTOs
contain only `runtime_id`, `workspace_id`, and coarse `availability`. Hidden
runtimes are omitted from runtime lists and return `not_found` on runtime detail
reads. Hidden profiles, missing resources and resources in another workspace
also return `not_found`; collection reads omit them. Scope denial returns
`insufficient_scope`. Every failure uses the stable problem envelope.

Neither DTO contains configuration, instructions, conversation starters, custom
arguments, environment data, integration allow-lists, MCP configuration,
timestamps, device/daemon details, tokens, or credentials, including for owners.
These reads do not enqueue work or enable delegation.

## Delegation admission (TK-MA61)

`POST /api/v1/delegations` requires a workspace-bound member PAT with
`delegations:write`. The server-only `public_delegations_v1` flag defaults OFF;
`FF_PUBLIC_DELEGATIONS_V1=on` is an explicit operator rollout decision. This does
not enable unattended dispatch: AO obtains human confirmation before calling.
No issue, assignment, or profile copy is created.

Admission atomically persists the workspace/member/key identity, normalized
payload SHA-256, target/source IDs, and one queued task. The task carries the
approved title and instructions, not Quick Create issue-creation semantics.
The runtime must advertise `public-delegation-v1`, be online, have an owner,
and satisfy existing agent readiness/ownership checks. Invocation rights come
from the profile owner/allow-list; runtime inventory visibility alone grants
no invocation rights. Binding changes require a new preview and confirmation.

A new request returns 202 with `status: queued`, without reserving concurrency.
A retry returns 200 with the original IDs and current task status; a changed
payload with the same key returns 409 `idempotency_conflict`. Identity survives
PAT rotation and target changes. If execution history has been deleted, replay
returns the original IDs with `status: unavailable` and never recreates the run.
Keys are retained until workspace deletion.
Do not generate a new key after a lost response. New intent requires a new key.
OpenAPI defines payload limits and the stable conflict reasons. Audit remains
`planned`; the durable admission row is not a general audit sink.

Migrations 587–589 add the record and independent concurrent unique indexes.
Apply all three before enabling the flag. The down migration drops admission
history, so disable intake before any rollback and preserve records needed for
reconciliation. Status lookup and cancellation use the same admission records and task lifecycle (TK-MA62).

Permission changes replace the profile mode and invocation targets in one
transaction. Admission locks the profile and target rows through task commit;
delivery rechecks current membership and invocation rights under the same locks
in the task-token transaction. A revoked delegation is failed before credentials
or a task payload can be delivered. The runtime binding is compared before any
caller-supplied runtime is looked up. Mismatched, missing, or disallowed bindings
uniformly return `runtime_binding_changed` without disclosing runtime existence.


## Delegation status, reconciliation, and cancellation (TK-MA62)

All operations remain behind the same default-OFF `public_delegations_v1` flag,
accept scoped member PATs only, and require `X-Workspace-ID` matching the PAT.

| Operation | Scope | Behavior |
| --- | --- | --- |
| `GET /api/v1/delegations` | `delegations:read` | Caller-owned records, optional exact `idempotency_key` / `delegation_id` filters |
| `GET /api/v1/delegations/{delegation_id}` | `delegations:read` | Requester or current workspace owner/admin |
| `POST /api/v1/delegations/{delegation_id}/cancel` | `delegations:write` | Requester or current workspace owner/admin; idempotent |

Reconciliation never creates work. Lists remain caller-scoped even for admins;
a shared idempotency key cannot discover another member's submission. Both the
SQL query and response are bounded to `limit` (default 50, maximum 200), with
one lookahead row for UUID-ordered keyset pagination. Follow `next_cursor` until
omitted. A cursor binds the caller, workspace, collection and filters; each page
rechecks membership. Missing or inaccessible detail/cancel targets uniformly
return `not_found`; lists omit other callers' records. Invocation revocation
preserves the requester's ability to reconcile and stop accepted work; removal
from the workspace revokes these operations.

The safe DTO retains the accepted delegation/task/workspace/profile/runtime IDs
and creation timestamp, adding `terminal`, an opaque `revision`, and optional
coarse `failure_reason`. Status is one of `queued`, `deferred`, `dispatched`,
`waiting_local_directory`, `running`, `completed`, `failed`, `cancelled`, or
`unavailable`. Only completed/failed/cancelled confirm a terminal outcome.
Failure reasons are allow-listed public codes: `access_revoked`,
`target_unavailable`, `timeout`, `runtime_unavailable`, `execution_failed`,
`execution_cancelled`, `execution_unavailable`. Unknown internal failure strings
collapse to execution_failed/execution_cancelled; missing history or unknown
internal states project to unavailable. Results, free-form errors, context,
source identity, request key/hash, runtime configuration, paths, session IDs,
credentials and other execution internals never appear. No history retention
or task re-creation is introduced.

Detail and cancel responses set `Cache-Control: no-store` and `ETag`. The ETag
is a quoted content hash of task identity and projected status/failure; revision
is the unquoted value. It is not a sortable sequence number. Concurrent status
responses can arrive out of order; consumers must retain observed terminal
outcomes and reconcile ambiguous results using stable IDs. Cancellation accepts
optional `If-Match` (the quoted ETag, or `*`). The member/role decision is
rechecked under workspace/member locks in the cancellation transaction, and the
ETag is compared under the task row lock; stale values return 409
`revision_conflict` without mutation. A request without If-Match is convergent:
repeating it returns the current authoritative outcome without creating work.

Cancellation reuses `TaskService.CancelTaskWithResult`, including actor receipt,
terminal settlement, scheduler notification and the existing cancellation event
to the runtime. The task status becomes cancelled atomically before the stop
broadcast; runtime process teardown can finish afterward. A completion race
preserves whichever terminal transition committed first. Terminal outcomes
cannot be replaced by cancellation. Unknown network outcomes require another
status read; a local request alone never proves that remote cancellation won.
No additional migrations, indexes, tables, or deletion-manifest entries are
needed. The DB lifecycle/race tests in `internal/handler/public_delegation_operations_test.go`
require an isolated migrated test database; never run them against a live workspace.

## Delegation cross-repository verification

`testdata/multica-delegation-v1.json` is identical to AO's
`test/contracts/multica-delegation-v1.json`. Update both copies together. The shared
scenarios cover discovery, confirmed submission, exact idempotent retry, status
reconciliation, policy denial and cancellation. AO owns native confirmation and
managed-policy denial before any POST; Multica independently enforces scopes,
workspace membership, invocation rights and runtime compatibility.

From `server/`, the offline contract, scope and default-off flag tests need no
PostgreSQL, Docker or running server:

```sh
AO_CONTRACT_FIXTURE=/absolute/path/to/agent-orchestrator/test/contracts/multica-delegation-v1.json \
  go test ./pkg/publicapi/v1 ./internal/middleware ./internal/featureflags
```

The peer path is optional; supplying it also checks that the copies are identical.
The test verifies production DTOs, registered methods/scopes, payload identity,
lifecycle/revision projection and scope denial. Transactional idempotency is also
covered by the existing handler tests and this optional real-handler workflow:

```sh
DATABASE_URL='postgres://USER:PASSWORD@HOST:PORT/THROWAWAY_DB?sslmode=disable' \
  MULTICA_RUN_DELEGATION_CONTRACT=1 \
  go test ./internal/handler -run '^TestDelegationSharedContractHandlers$' -count=1 -v
```

Use an explicitly provisioned, migrated throwaway database, never a running
application's database. No HTTP server is necessary. The handler workflow checks
profile discovery, admission/replay/conflict, lookup after a lost response, safe
status projection, denied writes, cancellation races and one execution per key.
The handler package exits with “Skipping tests” when its database is unavailable;
that output does not verify these scenarios. No migration is required.

AO's rollout setting defaults off and can only be enabled through a
native-confirmed Settings action. The preference is saved across desktop
restarts; missing, invalid or unreadable settings default off. Each admission and
cancellation still requires its own approval. The server's `public_delegations_v1` flag remains
independent and default off. These checks do not enable a pilot or unattended
submission.

### Bounded specialist result (WI-FAC-08)

`GET /api/v1/delegations/{delegation_id}/result` is a separate member operation
with `delegations:results:read`. The original requester must still be a workspace
member. It requires the exact terminal status ETag in `If-Match`; optional
`X-Result-Revision` pins a previously previewed snapshot. Status DTOs stay unchanged.
Only final `result.output` is eligible, at most 32768 UTF-8 bytes. Empty, missing,
nonterminal, unsafe or oversized output fails closed without returning raw errors.
The export filter refuses recognizable credentials, paths, URLs and control data;
it cannot prove that arbitrary prose contains no confidential information. The
operator remains responsible for approving the text scope and reviewing content.
No environment, history, files, repository bindings or download links are projected.

The result carries terminal status/revision/time, producer identities, SHA-256 of
exact text bytes, byte count and a versioned snapshot revision. The revision hashes
compact JSON in DTO field order with an empty revision field. A result is untrusted
data, including any instructions inside it. Obtain consent before retrieval, preview
plain text, then obtain separate consent before import. Import must revalidate the
snapshot; no execution, continuation or remote upload is authorized.
The paired fixture is `testdata/multica-delegation-result-v1.json`; set
`AO_RESULT_CONTRACT_FIXTURE` to the AO peer when testing this package.
