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

## Standard Windows runner contract

For a standard Windows 11 x64 runner, use the explicit build mode:

```powershell
node scripts/fork-images.mjs prepare --source FULL_SHA --output C:\absolute\new\server-inputs
node scripts/fork-daemon-bundle.mjs build C:\absolute\new\server-inputs C:\absolute\new\runner-bundle windows/amd64 --mode standard
node scripts/fork-daemon-bundle.mjs verify C:\absolute\new\runner-bundle
```

The v2 manifest records `runnerMode: "standard"`, `profile.readOnly: false`, a new `standard-<source>-<uuid>` profile and its stable `profile.daemonID`. `capabilities.standardIsolatedIdentity` and `capabilities.authenticatedShutdown` must both be boolean `true` before spawning; absent/false/string values are unsupported. The profile string matches `^standard-[a-f0-9]{12}-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`; `daemonID` is a canonical UUID string equal to its UUID suffix. The binary is `multica-windows-amd64.exe`. Source/tree/archive/recipe and per-file checksums retain their existing meaning. Node's `win32` host name maps to Go's `windows` for the native tests, default target and version check. `version --output json` reports `os: "windows"`, `arch: "amd64"` and the embedded full source commit. The same executable runs the CLI and daemon. Builds archive committed source; uncommitted edits are not a source-pinned release. Standard builds reject archived sources lacking identity isolation and verified shutdown support, so an older binary cannot be given new capabilities merely by using a newer build script.

Default builds remain restricted and keep their original manifest/profile contract. A standard bundle's policy metadata explicitly says standard, native tools/native CWD skills and preserved managed configuration. Never apply the restricted templates to standard mode or change an existing restricted profile into a standard one. Standard `standalone-profile.json` has `bundle_read_only: false`; its `pilot-profile.json` uses `local_directory_mode: "in_place"`, with no nested read-only permission or empty MCP ceiling. Apply it only to a separately chosen owned standard execution profile. Choose an existing Project-bound local directory to expose native repository guidance and skills. This is ordinary same-user provider access, not a host filesystem sandbox or a new workspace/folder admission filter. Optional routing/context changes from other proposals are outside this contract.

Use a separate private Windows profile, metadata root, control token and saved process record. Native CLI state is under the Windows `os.UserHomeDir()` result (normally `%USERPROFILE%`) at `.multica\profiles\<profile>\config.json`, with daemon logs/locks/port/shutdown-result state in that profile directory. It is not an AppData or WSL home. Preserve the real Windows user/provider environment and company-managed settings. Windows permissions require private owner/DACLs; `chmod(0600)` alone does not establish privacy. Parent workspace helpers must validate profile/root/config ACLs and the selected repository binding before activation.

For standard startup, use the generated profile and `--daemon-id <profile.daemonID>` with `MULTICA_DAEMON_ISOLATED=true`. This opt-in requires a named standard profile and an explicit canonical nonzero UUID; it suppresses all legacy identity discovery/migration/aliases and never reads or creates a machine or restricted identity. Persist the UUID in the private workspace state and reuse it only for this profile. Ordinary profiles without this opt-in keep machine identity and migration behavior. Restricted profiles reject this opt-in and retain their separate `bundle-daemon.id` rules.

Set `MULTICA_DAEMON_PINNED=true`, `MULTICA_DAEMON_READ_ONLY=false`, `MULTICA_DAEMON_AUTO_UPDATE=false`, `MULTICA_DAEMON_AUTO_RELOAD=false`, `MULTICA_GC_ENABLED=false`, and use one concurrent task. Set the exact approved server origin and selected workspace with the existing profile/config APIs. A standard CLI workspace default is not a membership/discovery allowlist. Require both installed discovery and actual registered Claude/Codex runtime IDs from this separate daemon before calling it ready; extra installed providers do not prove registration ownership. Do not reuse Mac runtime/profile IDs. The Linux server remains containers through Docker Desktop's WSL2 engine; no native Windows database/server port is introduced.

Both standard provider adapters accept a selected native Windows executable through `MULTICA_CLAUDE_PATH` and `MULTICA_CODEX_PATH`. Use absolute runnable `.exe` files from the approved native distributions. Discovery uses `exec.LookPath`; launch uses direct `exec.CommandContext`, so finding an npm `.cmd`/`.bat` shim does not prove it is launchable. This contract does not add a shell, change PowerShell execution policy, repair/install a provider or bypass company controls. Standard Claude keeps normal permission/tool behavior, and standard Codex retains ordinary task-home/config and native skill behavior. Windows task-home support already uses directory junctions and file-copy fallbacks when symlinks are unavailable. Provider processes are created suspended and assigned to an owned Job Object before resuming, so cancellation/cleanup owns descendants rather than signalling arbitrary PIDs. None of this proves a native Windows account/model execution smoke; that remains a separate check.

The standard manifest's `providerEnvironment` is a source-derived object mapping every discovered provider ID to its exact path override key. At this source it contains 29 entries, including `cursor: "MULTICA_CURSOR_PATH"`, `copilot: "MULTICA_COPILOT_PATH"` and the descriptor-derived `omp: "MULTICA_OMP_PATH"`. Select `claude` and `codex` using their approved native paths, then set every other mapped key to a verified nonexistent **absolute** path under the private standard root. Do this only in this daemon's environment; preserve user HOME, provider auth/config, proxies and company settings. There are no special disable booleans to infer. A missing override containing a slash/backslash returns immediately before login-shell and bundled-app fallback; nonempty CodeArts/ZCode overrides also prevent their extra fallback paths. Do not use an empty string or bare missing command, which can permit fallback. The regression puts every known command on a fixture PATH, selects only the two explicit providers and confirms an explicit missing selected path never falls back. Discovery reads files only; actual registration still requires version/protocol detection and the correct daemon runtime IDs. Custom runtime profiles remain a separately audited server configuration, not something this built-in probe map disables.

The standard version floors in `server/pkg/agent/version.go` are Claude `2.0.0` and Codex `0.100.0` (the app-server stdio protocol floor); these are source admission minima, not claims that those old versions were tested on Windows. Existing restriction-specific minima do not apply to standard mode. Use human-approved native versions and record their actual resolved path/version during the later Windows smoke. Build tools are Node >=22 (root `package.json`/`.nvmrc`) for the `.mjs` recipes, Git, native `tar` and a local Go >=1.26.9 toolchain satisfying `server/go.mod`; no new PowerShell version floor is imposed by these child scripts. The Mac cross-build used Go 1.27.1 and Node 26.13.0, with module/toolchain downloads disabled.

### Authenticated exact graceful stop

Before starting, the workspace creates a random 32-byte token as 64 lowercase hexadecimal characters (optionally one newline) in a new private regular file and sets `MULTICA_DAEMON_CONTROL_TOKEN_FILE` to its absolute path. Do not put the token in argv, logs or health responses. The daemon reads it once, rejects symlinks, replacements, invalid content or unprovable privacy, and never changes permissions. On Windows it checks the opened handle's owner and actual DACL: owner must be the current user, and access entries may name only that user, SYSTEM or Administrators; null/public/unknown ACLs refuse startup. On Unix the file must be user-owned with no group/other access. Parent helpers must protect the containing profile/root as well.

Health adds `verified_shutdown` (boolean), `started_at` (UTC RFC3339Nano string) and `runner_mode` (`standard` or `restricted`). `started_at` is the daemon health lifetime timestamp, **not** the OS process creation time. Require `verified_shutdown: true`, expected source, mode, profile, origin, daemon UUID, task counts and PID. Independently retain native process owner/SID, executable, argv/profile, CIM creation-time and listener-ownership checks, then recheck immediately before sending:

```http
POST /shutdown/verified
Authorization: Bearer <private-control-token>
Content-Type: application/json

{"pid":1234,"profile":"the-exact-profile","daemon_id":"the-exact-uuid","cli_commit":"the-full-40-character-source-sha","started_at":"the-exact-health-started_at"}
```

Every field must match that daemon instance. Missing/wrong authentication returns 401; malformed/unknown/trailing JSON returns 400; stale/missing identity returns 409. Success returns 200 with `{"status":"shutting down"}`, flushes the response, then cancels the same daemon context used by normal graceful shutdown, allowing runtime deregistration and task cleanup. No process signal or forced kill occurs in this API. An older daemon returns 404 for this route; an unconfigured daemon also returns 404 and reports `verified_shutdown: false`. Never fall back to `/shutdown`, `daemon stop`, SIGINT, taskkill or another profile on failure.

When token control is enabled, the original `/shutdown` refuses every request. While holding the profile lock and before publishing health/port/PID state, CLI startup atomically writes a sticky `daemon.control.json` containing version 1, PID, profile, daemon UUID, full CLI source and a startup-record timestamp; it contains no token. This private policy record persists after shutdown. Its timestamp records CLI policy publication and is not the health `started_at` or CIM process creation time. Current CLI stop/restart and every force fallback consult that durable record independently of caller environment and health availability, and recheck before signalling. A stale PID does not authorize legacy control; malformed, unreadable, non-file or foreign-profile evidence also refuses. An existing controlled profile cannot silently start without token control or change its daemon UUID. Ordinary profiles without this record retain existing no-token endpoint/lifecycle behavior. The token file is a local control credential, not a server login or delegation token. After success, the workspace must wait for the exact saved process to exit and confirm matching `daemon.shutdown.json` (`pid`, `cleanup_confirmed: true`), profile lock release and runtime/child settlement. A missing health listener alone is not completion. A failed timeout or unconfirmed cleanup remains a failure; do not remove live state or broadly kill processes.

Windows/amd64 cross-compilation and disposable protocol/config/control fixtures are feasible on a Mac. They do not execute Windows ACLs, Job Objects, provider launch/authentication, CWD/native skill discovery or end-to-end graceful cleanup on Windows. Keep those checks pending until a Windows host is available. Restricted Codex continues to refuse Windows, separately from standard Windows support.

## Verified restricted native behavior

The dedicated daemon profile persists `bundle_read_only=true`. That ceiling forces the source pin, one concurrent task, disabled update/reload/cleanup and one explicitly selected workspace. An environment value of `false` cannot relax the persisted ceiling. Ordinary profiles keep their existing behavior. The bundle generates a separate stable UUID in its own profile root as `bundle-daemon.id`; it never adopts the machine-wide `~/.multica/daemon.id`, migrates hostname/legacy aliases or accepts daemon-ID overrides. Ordinary migration ignores the bundle filename. Do not copy, delete or reuse this identity for a second daemon. A corrupt identity blocks startup instead of silently rotating it.

`pilot-profile.json` supplies the server execution profile's existing `runtime_config.local_directory_mode=none`, plus `runtime_config.permission_mode=read_only`, empty custom arguments/environment, and no MCP servers. The nested permission setting is the native execution contract for this dedicated daemon; the agent's top-level `permission_mode` remains its existing visibility/invocation authorization. Do not put `read_only` in that top-level field. Preserve appropriate private/direct ownership. Apply the template only to the new owned pilot profile, never a shared profile. The daemon rejects missing or incompatible settings before preparing a task. The `none` mode alone is not a write barrier; see [the upstream mode contract](https://github.com/multica-ai/multica/pull/9280).

Supported native harnesses:

- **Claude Code 2.1.259 or later:** native `--restricted`, plan permissions, `--permission-prompts none`, no built-in tools, explicit denial of MCP tools, empty strict MCP configuration, disabled hooks and no persisted/resumed session. Every SDK permission request receives `deny`. Custom arguments cannot reintroduce bypass. These switches are defined in the [native CLI reference](https://code.claude.com/docs/en/cli-reference).
- **Stock Codex stable 0.144.6 and later on macOS and Linux:** select an official stock executable. This bundle supplies no Codex binary or Codex build recipe. Earlier versions, custom/prerelease suffixes and Windows are refused. The exact source/protocol fixture baseline tested is **0.144.6 through 0.144.6**; later stable releases are permitted by the user's decision, subject to local preflight and effective-config attestation. Version-filter tests also cover 0.144.7, 0.145.0 and 1.0.0; they do not constitute runtime verification of those releases. No installed stock executable was launched for a live execution smoke test.

The task gets a fresh private `CODEX_HOME` with its restrictive `config.toml` written **before spawning**. The home, launch arguments and thread configuration explicitly disable Apps, plugins, remote plugins and sharing, skill MCP installation, tool suggestions, browser/computer controls, image generation, hooks, multi-agent/code-mode host execution, shell snapshots, mentions, guardian approval, goals, MCP/auth elicitation, workspace dependencies, remote compaction, remote control and realtime conversation. Every flag must be boolean `false` in `config/read`; missing flags are refused. Empty `mcp_servers` alone is insufficient because the [0.144.6 feature registry](https://github.com/openai/codex/blob/rust-v0.144.6/codex-rs/features/src/lib.rs) defaults several tool-producing features on. The same policy list drives the private home, startup arguments, thread settings and attestation; tests check their agreement.

**Pre-launch local policy refusal:** the [0.144.6 loader](https://github.com/openai/codex/blob/rust-v0.144.6/codex-rs/config/src/loader/mod.rs) and [legacy layer reader](https://github.com/openai/codex/blob/rust-v0.144.6/codex-rs/config/src/loader/layer_io.rs) read `/etc/codex/config.toml`, `/etc/codex/managed_config.toml` and `/etc/codex/requirements.toml` on macOS/Linux. Any presence, including empty files, disabled settings or dangling symlinks, refuses launch; unreadable paths also refuse. The private home must also have no `managed_config.toml` or `requirements.toml`. These home files are a conservative integrity check, not additional Unix loader locations. Internal `LoaderOverrides` are test/embedding hooks and are not exposed by the stock release CLI; custom launch/environment overrides are already refused. User config remains private; inherited profile selection, MCP/plugin entries and unsafe project/ancestor config are refused.

On macOS, the [managed preference reader](https://github.com/openai/codex/blob/rust-v0.144.6/codex-rs/config/src/loader/macos.rs) uses `CFPreferencesCopyAppValue` in domain `com.openai.codex` for exactly `config_toml_base64` and `requirements_toml_base64`. The runner checks those same effective preferences, including MDM/profile delivery, and refuses any value without decoding its contents. A fixed `/usr/bin/python3 -I -S` presence probe calls CoreFoundation through standard-library `ctypes`; macOS Codex requires this interpreter and module. Missing, failed, timed-out or unrecognized probe results refuse launch. This work queried only a nonexistent fixture domain to verify the API binding; it did not inspect this machine's Codex preferences. Do not delete or bypass managed controls to make a refused runtime start.

**Accepted residual risk:** cloud-delivered managed configuration for a company (enterprise or business) Codex account cannot be detected before launch, so plugin warmups might start before effective-config attestation refuses. Upstream [managed feature normalization](https://github.com/openai/codex/blob/rust-v0.144.6/codex-rs/core/src/config/managed_features.rs) can re-enable features despite local and launch settings, and [plugin warmups](https://github.com/openai/codex/blob/rust-v0.144.6/codex-rs/core-plugins/src/manager.rs) run before client requests. The user explicitly accepted this risk for stock Codex, using a company account on the work machine and a personal account at home. The runner does not inspect tokens to infer account type; it warns at every restricted daemon startup and before each Codex launch because it cannot rule the risk out. Attestation prevents a thread or task from starting after an unsafe response; it cannot undo network-side warmups that already occurred.

Before `thread/start`, the client requires empty effective MCP and plugin settings from `config/read` with all layers, and inspects `configRequirements/read`. The latter is necessary because `config/read` reports merged TOML, without materializing feature defaults or managed normalization. Missing/unknown layers, enabled features, conflicts and RPC errors/timeouts prevent thread creation and stop/reap the process. Windows, other harnesses and custom runtime commands remain unsupported. Restrictive settings remain mandatory even with the accepted cloud warmup risk.

The dedicated daemon accepts only fresh, direct, brief-only delegations. Repository/local-directory bindings, issue/chat/autopilot tasks, skills, external tools, custom launch/environment overrides and previous conversation/workdir reuse are refused. Live model discovery is unavailable for this profile: explicit model/effort/tier selections pass through without launching catalog probes outside task preflight. It neither advertises task supplementation nor retries a model turn automatically. Changing `none` to `in_place`/`worktree` cannot grant access. Native read-only settings do not constitute host-administrator containment. Present managed controls remain authoritative and must approve the runtime and effective configuration separately.

The Multica build recipe runs disposable configuration, protocol, full task-launch and lifecycle tests at the archived source before issuing a manifest. They prove generated configuration and transmitted requests/refusals, without invoking installed native executables or making provider calls. No live native-harness smoke or OS escape test was performed. Record that limitation; a managed deployment needing additional boundary proof remains blocked until its policy owner supplies it. Activation and any real task remain separate human decisions.

## Human installation and start, after approval

These steps describe a later human operation. Building the bundle authorizes none of them.

1. Approve server activation first under the [fork server guide](fork-server-images.md). Preserve its fixed-project ownership/takeover gates, backups and restore procedure. Confirm server images actually running match the bundle's source SHA and that the delegated API flag is enabled only under the pilot decision. Official Cloud is not compatible.
2. Read `bundle-manifest.json`. Use its newly generated `profile.profile` and `profile.preferredHealthPort`. Inventory existing daemon profiles and active runtimes, including the older hosted executor, without stopping them. Choose a new profile if its name is already present. Prove no existing daemon uses the selected profile, root or port. Hash-derived health ports can collide: inspect the candidate port before start; do not assume a new name means a free port. Do not stop or reuse an unrelated listener.
3. Choose a new private absolute workspace root and create it with mode 0700. It must not overlap another runtime root or repository/worktree. The restricted profile registers runtimes only in its explicit workspace. Confirm current membership and use the approved scratch workspace ID; an ordinary CLI default workspace without the restricted profile ceiling is not an execution allowlist. Use one scratch workspace and one owned direct execution profile, with no repository binding, automation, squad or board trigger. The older hosted executor must not host this pilot profile or claim the same work. Resolve ambiguous ownership before continuing; do not launch a duplicate executor to test it.
4. Confirm the installed AO application's build/version with its normal About view, without restarting it. Record it in the pilot handoff and verify that it contains the merged delegation client. If not, prepare a separately authorized AO application build from the chosen AO source; the human installs it later. Never repin the hosted facade to make this standalone bundle usable. The installed AO version was not inspected during bundle preparation.
5. In the human's terminal, set the absolute binary, profile, root and exact selfhost API origin. Clear inherited task identity and unrelated `MULTICA_*` configuration before configuring this profile. Preserve required managed controls. Obtain supported sign-in using the human's own account; do not copy another daemon's token, inspect the Keychain or extract application credentials. A scoped delegation PAT belongs to the AO client and is separate from daemon sign-in.

```sh
BINARY=/absolute/versioned/multica-darwin-arm64
CODEX_BINARY=/absolute/path/to/official/stock/codex
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

Login saves credentials and discovers workspace membership; it does not authorize starting a daemon. A dedicated CLI profile is different from the server's owned direct execution profile; record both IDs. Never select a shared/default/hosted profile. Apply `pilot-profile.json` to the owned direct execution profile through the existing profile editor or its supported update command (`--runtime-config` replaces that whole object). Keep its custom arguments/environment empty and MCP disabled. Confirm both supported native executable versions and the restriction evidence before registering any runtime. `"$CODEX_BINARY" --version` must report a stock stable `codex-cli 0.144.6` or later. Select that official executable using `MULTICA_CODEX_PATH` on this daemon only. Verify its provenance using the official distribution's checksums. Native harness updates remain a human operation; keep the chosen executable/version pinned during the pilot. On macOS, confirm `/usr/bin/python3` and standard `ctypes` are available for the required managed-preference preflight. Read the accepted cloud warmup risk above before activation. Version/help reads do not start a daemon or execution.

6. After separate human approval for daemon activation, run it attended in the foreground. Do not use `restart`, a background start, the old hosted supervisor or autostart. The persisted restricted profile also enforces the source pin, even on a later start without the environment flags. The source pin disables periodic self-update, binary-change reload and server-requested replacement.

```sh
MULTICA_CODEX_PATH="$CODEX_BINARY" \
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

From the repository root: `node --test scripts/fork-daemon-bundle.test.mjs scripts/fork-images.test.mjs`, `bash scripts/selfhost-config.test.sh`, and `bash scripts/makefile-build.test.sh`. Native fixture tests verify the generated restrictive launch/configuration, local managed-file/MDM presence refusal and effective-config denial responses. Actual stock executable versions and local policy absence remain activation preflight facts. No frontend code is changed by this recipe. Database-backed real-handler tests and actual installed-harness verification require separate disposable prerequisites/approval and must be reported as unrun when unavailable.
