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
  `issues:write`, `comments:read`, and `comments:write`. A PAT created without
  scopes, including a legacy PAT, continues to work on the existing `/api`
  surface but is rejected on `/api/v1` with `scoped_token_required`. A scoped
  PAT is bound to one workspace, must present that workspace on each request,
  and is rejected on legacy `/api`, daemon, and realtime surfaces with
  `public_api_credential_only`.
- PAT creation accepts an interactive session JWT or an unscoped member PAT
  through the first-party management endpoint. Task tokens, Cloud PATs, Plugin
  credentials, and scoped PATs cannot issue PATs. The issuer must be a member
  of the requested workspace; scope grants are checked against the role policy
  in the capability ledger. The current issue and comment scopes are available
  to `member`, `admin`, and `owner` roles.
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
  `scope_not_permitted_for_role`. Workspace isolation returns `not_found` for
  a workspace the member cannot access; limiter failures return `rate_limited`
  and `Retry-After`.
- Audit requirements use an explicit lifecycle (`planned` or `enforced`). The
  migrated issue/comment operations remain `planned` until a durable audit
  sink is wired. This surface does not claim that a durable audit write is
  implemented; the ledger does not imply that a boolean marker performs one.

## Rollout ledger

The migrated slice is Issue read/content update and Comment read/create. Context
and Storage remain Plugin extensions; Hook and invocation delivery remain on
the bridge. Projects, Members, Issue search/create/transition, Agents, Squads,
Skills, Tasks/Runs, and Autopilots are subsequent vertical slices. Each slice
adds its Public contract first, then explicitly opts the safe Plugin subset into
the capability ledger.
