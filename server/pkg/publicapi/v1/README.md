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
  `runtimes:read`. A PAT created without
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
