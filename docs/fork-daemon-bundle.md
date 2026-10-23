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

## Native execution restriction gate

**This bundle's activation is held.** A dedicated daemon profile isolates configuration, PID/log files and workspace names. It does not enforce filesystem, network, process, credential or repository access restrictions.

At this source, the native backend inherits its sandbox configuration and automatically approves command/file requests, including additional network/filesystem permission requests. Another native backend explicitly selects a permission-bypass mode. These are observed implementation behaviors, not a read-only execution guarantee. A read-only brief, `max_concurrent_tasks=1`, a scratch directory or a loopback server cannot supply that guarantee. The package verifier deliberately keeps `nativeRestrictions.verified=false` and `activation=held`; no CLI capability string is added to suggest otherwise.

Before any real profile is activated, an operator must provide separately reviewed evidence for the selected native executable and effective configuration: attempted writes outside the approved scratch root, network/tool escalation, access to foreign repositories and credentials, and child-process/background escape must be denied by mechanism. Use fake/disposable fixtures first; do not invoke installed native executables or provider accounts for this verification without separate approval. Unknown or unsupported restrictions block the real pilot. Managed policy must also approve the exact runtime/registration and effective configuration; user-editable labels are insufficient. If restrictions cannot be demonstrated, request a revised execution design. Do not compensate by changing credentials, host or harness after an enforced refusal.

## Human installation and start, after the hold is resolved

These steps describe a later human operation. Building the bundle authorizes none of them.

1. Approve server activation first under the [fork server guide](fork-server-images.md). Preserve its fixed-project ownership/takeover gates, backups and restore procedure. Confirm server images actually running match the bundle's source SHA and that the delegated API flag is enabled only under the pilot decision. Official Cloud is not compatible.
2. Read `bundle-manifest.json`. Use its newly generated `profile.profile` and `profile.preferredHealthPort`. Inventory existing daemon profiles and active runtimes, including the older hosted executor, without stopping them. Choose a new profile if its name is already present. Prove no existing daemon uses the selected profile, root or port. Hash-derived health ports can collide: inspect the candidate port before start; do not assume a new name means a free port. Do not stop or reuse an unrelated listener.
3. Choose a new private absolute workspace root and create it with mode 0700. It must not overlap another runtime root or repository/worktree. The daemon discovers every workspace accessible to its signed-in member; the CLI default workspace is not an execution allowlist. For this bundle use a dedicated selfhost member with access only to the approved scratch workspace, and verify that membership before start. If that cannot be arranged within existing account/policy authority, hold activation pending a reviewed workspace-scoping control. Use one scratch workspace and one owned direct execution profile, with no repository binding, automation, squad or board trigger. The older hosted executor must not host this pilot profile or claim the same work. Resolve ambiguous ownership before continuing; do not launch a duplicate executor to test it.
4. Confirm the installed AO application's build/version with its normal About view, without restarting it. Record it in the pilot handoff and verify that it contains the merged delegation client. If not, prepare a separately authorized AO application build from the chosen AO source; the human installs it later. Never repin the hosted facade to make this standalone bundle usable. The installed AO version was not inspected during bundle preparation.
5. In the human's terminal, set the absolute binary, profile, root and exact selfhost API origin. Clear inherited task identity and unrelated `MULTICA_*` configuration before configuring this profile. Preserve required managed controls. Obtain supported sign-in using the human's own account; do not copy another daemon's token, inspect the Keychain or extract application credentials. A scoped delegation PAT belongs to the AO client and is separate from daemon sign-in.

```sh
BINARY=/absolute/versioned/multica-darwin-arm64
PROFILE=the-exact-profile-from-bundle-manifest
TASK_ROOT=/absolute/new/private/scratch-root
API_ORIGIN=http://127.0.0.1:the-approved-backend-port

"$BINARY" --profile "$PROFILE" config set server_url "$API_ORIGIN"
"$BINARY" --profile "$PROFILE" config set workspaces_root "$TASK_ROOT"
"$BINARY" --profile "$PROFILE" config set disable_auto_update true
"$BINARY" --profile "$PROFILE" config set disable_auto_reload true
"$BINARY" --profile "$PROFILE" config set max_concurrent_tasks 1
"$BINARY" --profile "$PROFILE" login
```

Login saves credentials and discovers workspace membership; it does not authorize starting a daemon. A dedicated CLI profile is different from the server's owned direct execution profile; record both IDs. Never select a shared/default/hosted profile. Review effective native executable/configuration and the restriction proof before registering any runtime.

6. After separate human approval for daemon activation, run it attended in the foreground. Do not use `restart`, a background start, the old hosted supervisor or autostart. The source pin disables periodic self-update, binary-change reload and server-requested replacement. Reuse these environment settings on every later start; the pin is not a persisted CLI setting.

```sh
MULTICA_DAEMON_PINNED=true \
MULTICA_DAEMON_AUTO_UPDATE=false \
MULTICA_DAEMON_AUTO_RELOAD=false \
MULTICA_GC_ENABLED=false \
"$BINARY" --profile "$PROFILE" --server-url "$API_ORIGIN" daemon start \
  --foreground --workspaces-root "$TASK_ROOT" --max-concurrent-tasks 1 \
  --no-auto-update --no-auto-reload
```

The same native binary runs CLI and daemon. Health binds loopback. If it reports a fallback port, stop this new foreground process and resolve the collision before any task. Record the actual health port and inspect only that exact loopback endpoint. Health must identify the expected profile, full `cli_commit`, `runtime_pinned=true`, empty `launched_by`, unique daemon ID, server origin and zero unexpected tasks. Check the workspace root, daemon profile state directory and the claimed server runtime/owned direct profile binding. Do not infer identity or locality from availability alone. Do not call AO's live daemon on port 3001 for these checks.

7. Confirm registration advertises the implemented `public-delegation-v1` capability using server registration evidence. Safe public runtime availability alone does not attest capability, location or sandbox policy. Disposable verification covers actual client headers, fenced claim/start, complete, and cancellation acknowledgement after the runner settles; it makes no real provider call and does not prove deployed-server or native-harness restrictions.
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
go test ./internal/daemon -run 'TestBundle|TestDelegation|TestStartTask|TestHandleTask_AcksCancel' -count=1
go test -race ./internal/daemon -run 'TestBundle|TestDelegation|TestStartTask|TestHandleTask_AcksCancel' -count=1
go test ./pkg/agent -run 'TestCodexHandleServerRequest|TestCodexPermissionsApprovalResponse|TestBuildCodexArgsExtraArgs' -count=1
```

From the repository root: `node --test scripts/fork-daemon-bundle.test.mjs scripts/fork-images.test.mjs`, `bash scripts/selfhost-config.test.sh`, and `bash scripts/makefile-build.test.sh`. Native permission tests confirm the activation hold's cause; passing them does not make the runtime restrictive. No frontend code is changed by this recipe. Database-backed real-handler tests and actual installed-harness verification require separate disposable prerequisites/approval and must be reported as unrun when unavailable.
