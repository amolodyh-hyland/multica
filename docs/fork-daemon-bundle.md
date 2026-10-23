# Source-pinned standalone CLI and daemon

WI-FAC-02 supplies a native CLI that also runs the daemon. It does not replace the hosted executor or change the hosted facade module. Server images, CLI and daemon must identify the **same full fork commit**. Rebuild the WI-FAC-01 recipe at the final bundle source; an older recipe is incompatible even if its API happens to work.

## Preparation without activation

Run from this repository with Node and the Go version required by `server/go.mod`. Use an existing permitted module cache; this recipe disables module downloads and toolchain downloads. Populate missing public dependencies separately under applicable policy. Do not build or pull Docker images as part of this recipe.

Choose new absolute output directories outside the checkout. The server preparation below only archives source and renders a recipe; it runs no containers.

```sh
SOURCE=$(git rev-parse HEAD)
node scripts/fork-images.mjs prepare --source "$SOURCE" --output /absolute/new/server-inputs
GOCACHE=/absolute/writable/build-cache node scripts/fork-daemon-bundle.mjs build \
  /absolute/new/server-inputs /absolute/new/daemon-bundle \
  darwin/arm64,darwin/amd64,linux/arm64,linux/amd64,windows/amd64
node scripts/fork-daemon-bundle.mjs verify /absolute/new/daemon-bundle
```

The recipe builds an immutable Git archive rather than the working tree. Commit intended changes first. It embeds the full source SHA, version and source timestamp and records the Go toolchain, module hashes, build options, server source-manifest hash, recipe hash, binary size and SHA-256. `SHA256SUMS` includes the guide and both manifests. The `source/` directory is build material, not an installed runtime. Hashes detect accidental changes; they do not authenticate an untrusted publisher. Obtain the manifest and its hash through the approved handoff. Binaries are unsigned and not notarized.

Verify before copying an individual platform binary to a private, versioned directory. On macOS use `shasum -a 256 -c SHA256SUMS` from the bundle directory; on Linux use `sha256sum -c SHA256SUMS`. On Windows use `Get-FileHash -Algorithm SHA256` and compare every listed file, or run the Node verifier. The Windows binary has an `.exe` suffix. Cross-compilation does not prove runtime behavior on that operating system.

Do not copy over an installed binary or enable boot autostart. Keep the prior bundle for rollback. `multica version --output json` is a local read; its `commit` must equal the server source and bundle manifest. `go version -m BINARY` shows build/module metadata. Do not use `update` on a pinned bundle.

## Verified native restrictions

The dedicated daemon profile persists `bundle_read_only=true`. That ceiling forces the source pin, one concurrent task, disabled update/reload/cleanup and one explicitly selected workspace. An environment value of `false` cannot relax the persisted ceiling. Ordinary profiles keep their existing behavior. The bundle generates a separate stable UUID in its own profile root as `bundle-daemon.id`; it never adopts the machine-wide `~/.multica/daemon.id`, migrates hostname/legacy aliases or accepts daemon-ID overrides. Ordinary migration ignores the bundle filename. Do not copy, delete or reuse this identity for a second daemon. A corrupt identity blocks startup instead of silently rotating it.

`pilot-profile.json` supplies the server execution profile's existing `runtime_config.local_directory_mode=none`, plus `runtime_config.permission_mode=read_only`, empty custom arguments/environment, and no MCP servers. The nested permission setting is the native execution contract for this dedicated daemon; the agent's top-level `permission_mode` remains its existing visibility/invocation authorization. Do not put `read_only` in that top-level field. Preserve appropriate private/direct ownership. Apply the template only to the new owned pilot profile, never a shared profile. The daemon rejects missing or incompatible settings before preparing a task. The `none` mode alone is not a write barrier; see [the upstream mode contract](https://github.com/multica-ai/multica/pull/9280).

Supported native harnesses:

- **Claude Code 2.1.259 or later:** native `--restricted`, plan permissions, `--permission-prompts none`, no built-in tools, explicit denial of MCP tools, empty strict MCP configuration, disabled hooks and no persisted/resumed session. Every SDK permission request receives `deny`. Custom arguments cannot reintroduce bypass. These switches are defined in the [native CLI reference](https://code.claude.com/docs/en/cli-reference).
- **Codex app-server 0.144.6 or later, on macOS/Linux:** a fresh task-local native home contains `sandbox_mode="read-only"`, `approval_policy="never"`, disabled shell/unified execution/web search and no MCP servers. User config, plugins, skills and prior sessions are not copied. Local system, managed, requirements, task-home and ancestor project files are checked before process launch. This file check is only a preflight: empty overlays do not delete inherited entries, and files alone cannot cover cloud fragments or macOS managed preferences.

After the stdio app-server initializes, the daemon requests `config/read` with `includeLayers=true` and the exact scratch cwd, before sending `thread/start`. The [native config manager](https://github.com/openai/codex/blob/main/codex-rs/app-server/src/config_manager_service.rs) resolves cloud/enterprise fragments and macOS managed preferences and applies managed requirements to the returned effective settings. The daemon requires an explicitly empty MCP map, read-only/never, disabled web/shell/unified execution, complete known layer metadata and no MCP/app/plugin entries in any returned layer. Even disabled MCP entries are refused. Missing or malformed configuration, unknown layers, conflicting managed requirements, RPC errors and timeouts terminate this app-server without creating a thread or turn. There is no config-write RPC, automatic retry or broader fallback. Managed settings are never rewritten to pass inspection.

Both macOS and Linux Codex use this same effective-config gate. macOS previously lacked support because a file audit cannot inspect forced MDM preferences; native inspection resolves that limitation without separate preference access. The native API must supply the required fields and layer data; a version number alone is insufficient. Thread startup explicitly selects read-only/never; each turn requests restricted reads rooted at its scratch cwd, without network permission. Command/file approval, permission grants, elicitation and unknown host requests are refused. See the [app-server sandbox contract](https://learn.chatgpt.com/docs/app-server#sandbox-read-access-readonlyaccess). Do not downgrade these restrictions to accommodate a peer.

Other harnesses, custom runtime commands and the Windows Codex sandbox are unsupported for this bundle. Only supported built-in harnesses are registered. Do not remove system or managed controls to make a refused harness run. Check the actual approved executable's version before activation. Windows CLI cross-builds verify packaging and do not attest Windows sandbox support.

The dedicated daemon accepts only fresh, direct, brief-only delegations. Repository/local-directory bindings, issue/chat/autopilot tasks, skills, external tools, custom launch/environment overrides and previous conversation/workdir reuse are refused. It neither advertises task supplementation nor retries a model turn automatically. Changing `none` to `in_place`/`worktree` cannot grant access. Native read-only settings do not constitute host-administrator containment or managed-policy attestation. Present managed controls remain authoritative and must approve the runtime and effective configuration separately.

The build recipe runs disposable configuration, protocol, full task-launch and lifecycle tests at the archived source before issuing a manifest. They prove generated configuration and transmitted requests/refusals, without invoking installed native executables or making provider calls. No live native-harness smoke or OS escape test was performed. Record that limitation; a managed deployment needing additional boundary proof remains blocked until its policy owner supplies it. Activation and any real task remain separate human decisions.

## Human installation and start, after approval

These steps describe a later human operation. Building the bundle authorizes none of them.

1. Approve server activation first under the [fork server guide](fork-server-images.md). Preserve its fixed-project ownership/takeover gates, backups and restore procedure. Confirm server images actually running match the bundle's source SHA and that the delegated API flag is enabled only under the pilot decision. Official Cloud is not compatible.
2. Read `bundle-manifest.json`. Use its newly generated `profile.profile` and `profile.preferredHealthPort`. Inventory existing daemon profiles and active runtimes, including the older hosted executor, without stopping them. Choose a new profile if its name is already present. Prove no existing daemon uses the selected profile, root or port. Hash-derived health ports can collide: inspect the candidate port before start; do not assume a new name means a free port. Do not stop or reuse an unrelated listener.
3. Choose a new private absolute workspace root and create it with mode 0700. It must not overlap another runtime root or repository/worktree. The restricted profile registers runtimes only in its explicit workspace. Confirm current membership and use the approved scratch workspace ID; an ordinary CLI default workspace without the restricted profile ceiling is not an execution allowlist. Use one scratch workspace and one owned direct execution profile, with no repository binding, automation, squad or board trigger. The older hosted executor must not host this pilot profile or claim the same work. Resolve ambiguous ownership before continuing; do not launch a duplicate executor to test it.
4. Confirm the installed AO application's build/version with its normal About view, without restarting it. Record it in the pilot handoff and verify that it contains the merged delegation client. If not, prepare a separately authorized AO application build from the chosen AO source; the human installs it later. Never repin the hosted facade to make this standalone bundle usable. The installed AO version was not inspected during bundle preparation.
5. In the human's terminal, set the absolute binary, profile, root and exact selfhost API origin. Clear inherited task identity and unrelated `MULTICA_*` configuration before configuring this profile. Preserve required managed controls. Obtain supported sign-in using the human's own account; do not copy another daemon's token, inspect the Keychain or extract application credentials. A scoped delegation PAT belongs to the AO client and is separate from daemon sign-in.

```sh
BINARY=/absolute/versioned/multica-darwin-arm64
PROFILE=the-exact-profile-from-bundle-manifest
TASK_ROOT=/absolute/new/private/scratch-root
API_ORIGIN=http://127.0.0.1:the-approved-backend-port
WORKSPACE_ID=the-approved-scratch-workspace-uuid

"$BINARY" --profile "$PROFILE" config set server_url "$API_ORIGIN"
"$BINARY" --profile "$PROFILE" config set workspaces_root "$TASK_ROOT"
"$BINARY" --profile "$PROFILE" config set disable_auto_update true
"$BINARY" --profile "$PROFILE" config set disable_auto_reload true
"$BINARY" --profile "$PROFILE" config set max_concurrent_tasks 1
"$BINARY" --profile "$PROFILE" login
"$BINARY" --profile "$PROFILE" config set workspace_id "$WORKSPACE_ID"
"$BINARY" --profile "$PROFILE" config set bundle_read_only true
```

Login saves credentials and discovers workspace membership; it does not authorize starting a daemon. A dedicated CLI profile is different from the server's owned direct execution profile; record both IDs. Never select a shared/default/hosted profile. Apply `pilot-profile.json` to the owned direct execution profile through the existing profile editor or its supported update command (`--runtime-config` replaces that whole object). Keep its custom arguments/environment empty and MCP disabled. Confirm both supported native executable versions and the generated restriction evidence before registering any runtime.

6. After separate human approval for daemon activation, run it attended in the foreground. Do not use `restart`, a background start, the old hosted supervisor or autostart. The persisted restricted profile also enforces the source pin, even on a later start without the environment flags. The source pin disables periodic self-update, binary-change reload and server-requested replacement.

```sh
MULTICA_DAEMON_PINNED=true \
MULTICA_DAEMON_READ_ONLY=true \
MULTICA_DAEMON_WORKSPACE_ID="$WORKSPACE_ID" \
MULTICA_DAEMON_AUTO_UPDATE=false \
MULTICA_DAEMON_AUTO_RELOAD=false \
MULTICA_GC_ENABLED=false \
"$BINARY" --profile "$PROFILE" --server-url "$API_ORIGIN" daemon start \
  --foreground --workspaces-root "$TASK_ROOT" --max-concurrent-tasks 1 \
  --no-auto-update --no-auto-reload
```

The same native binary runs CLI and daemon. Health binds loopback. If it reports a fallback port, stop this new foreground process and resolve the collision before any task. Record the actual health port and inspect only that exact loopback endpoint. Health must identify the expected profile, full `cli_commit`, `runtime_pinned=true`, `bundle_read_only=true`, the exact `bundle_workspace_id`, empty `launched_by`, unique daemon ID, server origin and zero unexpected tasks. Check the workspace root, daemon profile state directory and the claimed server runtime/owned direct profile binding. Do not infer identity or locality from availability alone. Do not call AO's live daemon on port 3001 for these checks.

7. Confirm registration advertises the implemented `public-delegation-v1` capability using server registration evidence. Safe public runtime availability alone does not attest capability, location or sandbox policy. Disposable verification covers actual client headers, fenced claim/start, complete, and cancellation acknowledgement after the runner settles; it makes no real provider call. Native configuration/permission tests verify the restricted launch contract; they do not prove deployed-server identity or a live OS boundary.
8. Keep the runtime idle until a separately approved pilot: one brief-only synthetic task, exact workspace/profile/runtime, attended 15-minute window and explicitly accepted usage. No repository access or result-driven commands. Each continuation needs per-turn human approval; no automatic resume or steer. Keep at most two executing factory stations and one heavy job, including observed outside work. This daemon's one-task setting is not a global admission controller or spending cap.

## Stop, rollback and evidence

The human stops this new foreground daemon with Ctrl-C only after reconciling exact tasks. If cancellation is chosen, use the supported exact-ID cancellation flow with its separate approval, then wait for authoritative outcome **and** process/transcript finalization. A cancelled row or lost heartbeat does not prove quiescence. Preserve uncertain tasks and their workspace root; never start a replacement writer on timeout.

For rollback, stop only the bundle's owned process after settlement; keep its private profile and scratch root for diagnosis. Select an approved matching server/runtime pair before restarting anything. Do not replace a binary underneath a running pinned daemon. Never stop the hosted runtime, modify the Compose project, delete state or change the installed application incidentally. Cleanup and installation remain separate human actions.

Retain bundle/source/server manifests and checksums, actual server image identities, full binary revision, installed AO version, profile/root/port, server workspace/direct-profile/runtime IDs, native restriction proof, policy outcome, exact task IDs, terminal outcome and process-settlement evidence. Do not include tokens, raw environments or credential stores.

## Focused verification

From `server/`, with an isolated home and cached dependencies:

```sh
go build ./cmd/multica ./cmd/server ./cmd/migrate
go vet ./internal/daemon/... ./cmd/multica/... ./pkg/agent/...
go test ./internal/daemon -run 'TestBundle|TestReadOnly|TestDelegation|TestStartTask|TestHandleTask_AcksCancel' -count=1
go test -race ./internal/daemon -run 'TestBundle|TestReadOnly|TestDelegation|TestStartTask|TestHandleTask_AcksCancel' -count=1
go test ./cmd/multica -run 'TestBundleProfileIsolation|TestBuildDaemonStartArgs|TestApplyConfigSet' -count=1
go test ./pkg/agent ./internal/daemon/execenv -run 'TestReadOnly|TestCodexHandleServerRequest|TestCodexPermissionsApprovalResponse|TestBuildCodexArgsExtraArgs' -count=1
```

From the repository root: `node --test scripts/fork-daemon-bundle.test.mjs scripts/fork-images.test.mjs`, `bash scripts/selfhost-config.test.sh`, and `bash scripts/makefile-build.test.sh`. Native fixture tests verify the generated restrictive launch/configuration and denial responses. Actual native executable versions and managed compatibility remain activation preflight facts. No frontend code is changed by this recipe. Database-backed real-handler tests and actual installed-harness verification require separate disposable prerequisites/approval and must be reported as unrun when unavailable.
