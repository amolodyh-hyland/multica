import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { nativeTarget, standardCapabilities, providerEnvironment, profileSettings, pilotProfile, standaloneProfile, nativeRestrictions, targets, validateServerInputs, validateStandardSource, verify } from './fork-daemon-bundle.mjs';
import { imageInputs, renderRecipe } from './fork-images.mjs';

const source = 'a'.repeat(40);
const identity = '11111111-1111-1111-1111-111111111111';
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const recipe = readFileSync(new URL('../docker-compose.selfhost.yml', import.meta.url), 'utf8');

test('server compatibility binds full source, tree and rendered default-off recipe', () => {
  const inputs = { ...imageInputs(source), version: 1, tree: 'tree', recipeSha256: hash(renderRecipe(recipe, imageInputs(source))) };
  validateServerInputs(inputs, source, 'tree', recipe);
  for (const [key, value] of [['source', 'b'.repeat(40)], ['tree', 'other'], ['recipeSha256', 'changed'], ['tag', 'latest']]) {
    assert.throws(() => validateServerInputs({ ...inputs, [key]: value }, source, 'tree', recipe), /mismatch/);
  }
  assert.throws(() => validateServerInputs(inputs, 'main', 'tree', recipe));
});

test('bundle uses a unique dedicated profile and disables updates, reload and cleanup', () => {
  const a = profileSettings(source);
  const b = profileSettings(source);
  assert.notEqual(a.profile, b.profile);
  assert.notEqual(a.profile, 'desktop-host');
  assert.equal(a.runtimePinned, true);
  assert.equal(a.maxConcurrentTasks, 1);
  assert.equal(a.disableAutoUpdate, true);
  assert.equal(a.disableAutoReload, true);
  assert.equal(a.gcEnabled, false);
  assert.equal(a.identityScope, 'profile');
  assert.equal(a.identityFile, 'bundle-daemon.id');
  assert.equal(a.migrateLegacyIdentities, false);
  assert.ok(a.preferredHealthPort > 19514 && a.preferredHealthPort < 20515);
  for (const target of ['darwin/arm64', 'darwin/amd64', 'linux/arm64', 'linux/amd64', 'windows/amd64']) assert.deepEqual(targets(target), [target]);
  for (const target of ['', 'darwin/x64', '../escape', 'linux/amd64,linux/amd64']) assert.throws(() => targets(target));
});

test('Windows host maps to the Go target without changing other hosts', () => {
  assert.equal(nativeTarget('win32', 'x64'), 'windows/amd64');
  assert.deepEqual(targets(nativeTarget('win32', 'x64')), ['windows/amd64']);
  assert.equal(nativeTarget('darwin', 'arm64'), 'darwin/arm64');
  assert.equal(nativeTarget('linux', 'x64'), 'linux/amd64');
});

test('standard builds refuse source revisions without isolated identity and verified control', () => {
  const config = readFileSync(new URL('../server/internal/daemon/config.go', import.meta.url), 'utf8');
  const health = readFileSync(new URL('../server/internal/daemon/health.go', import.meta.url), 'utf8');
  validateStandardSource(config, health);
  assert.throws(() => validateStandardSource(config.replaceAll('MULTICA_DAEMON_ISOLATED', 'OLD_IDENTITY'), health), /Selected source/);
  assert.throws(() => validateStandardSource(config, health.replaceAll('/shutdown/verified', '/shutdown')), /Selected source/);
});

test('standard mode has a separate identity and normal tools while restricted defaults stay unchanged', (t) => {
  const standard = profileSettings(source, identity, 'standard');
  assert.notEqual(standard.profile, profileSettings(source, identity).profile);
  assert.equal(standard.readOnly, false);
  assert.equal(standard.daemonID, identity);
  assert.equal(standard.isolated, true);
  assert.equal(standard.identityFile, null);
  assert.equal(standard.migrateLegacyIdentities, false);
  assert.equal(standaloneProfile('standard').bundle_read_only, false);
  assert.equal(nativeRestrictions('standard').mode, 'standard');
  assert.equal(nativeRestrictions('standard').managedConfiguration, 'preserved');
  assert.doesNotMatch(JSON.stringify(nativeRestrictions('standard')), /read.only|restricted/);
  assert.deepEqual(pilotProfile('standard'), { runtime_config: { local_directory_mode: 'in_place' }, custom_args: [], custom_env: {} });
  const f = fixture(t, 'standard');
  assert.equal(verify(f.dir).runnerMode, 'standard');
  assert.deepEqual(verify(f.dir).capabilities, { standardIsolatedIdentity: true, authenticatedShutdown: true });
  f.manifest.capabilities.authenticatedShutdown = false;
  f.save();
  assert.throws(() => verify(f.dir), /capabilities/);
  f.manifest.capabilities.authenticatedShutdown = true;
  f.manifest.runnerMode = 'restricted';
  f.save();
  assert.throws(() => verify(f.dir), /Invalid/);
  assert.throws(() => profileSettings(source, identity, 'other'), /mode/);
});

test('standard provider environment covers every current probe including OMP', () => {
  const map = providerEnvironment();
  assert.equal(map.claude, 'MULTICA_CLAUDE_PATH');
  assert.equal(map.codex, 'MULTICA_CODEX_PATH');
  assert.equal(map.cursor, 'MULTICA_CURSOR_PATH');
  assert.equal(map.copilot, 'MULTICA_COPILOT_PATH');
  assert.equal(map.omp, 'MULTICA_OMP_PATH');
  assert.equal(Object.keys(map).length, readFileSync(new URL('agent-cli-command-names.txt', import.meta.url), 'utf8').trim().split('\n').length);
  assert.equal(new Set(Object.values(map)).size, Object.keys(map).length);
});

function fixture(t, mode = 'restricted') {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'multica-bundle-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const file = 'multica-windows-amd64.exe';
  const bytes = Buffer.from('fixture binary');
  const server = { source, tree: 'tree', archiveSha256: 'archive', recipeSha256: 'recipe' };
  const serverBytes = JSON.stringify(server);
  const manifest = {
    version: 2, ...server, serverSourceManifestSha256: hash(serverBytes), serverRecipeSha256: server.recipeSha256,
    ...(mode === 'standard' ? { runnerMode: mode, capabilities: standardCapabilities(), providerEnvironment: providerEnvironment() } : {}),
    profile: profileSettings(source, identity, mode),
    nativeRestrictions: nativeRestrictions(mode),
    binaries: [{ target: 'windows/amd64', file, bytes: bytes.length, sha256: hash(bytes) }],
  };
  const files = { [file]: bytes, 'server-source-manifest.json': serverBytes, 'install-and-start.md': 'fixture guide', 'fork-server-images.md': 'server guide', 'pilot-profile.json': JSON.stringify(pilotProfile(mode)), 'standalone-profile.json': JSON.stringify(standaloneProfile(mode)), 'bundle-manifest.json': JSON.stringify(manifest) };
  const save = () => {
    files['bundle-manifest.json'] = JSON.stringify(manifest);
    for (const [name, content] of Object.entries(files)) writeFileSync(path.join(dir, name), content);
    writeFileSync(path.join(dir, 'SHA256SUMS'), Object.entries(files).map(([name, content]) => `${hash(content)}  ${name}\n`).join(''));
  };
  save();
  return { dir, manifest, file, save };
}

test('verification catches tampering, incomplete checksums and binary metadata mismatch', (t) => {
  const { dir, manifest, file, save } = fixture(t);
  assert.equal(verify(dir).source, source);
  writeFileSync(path.join(dir, file), 'changed');
  assert.throws(() => verify(dir), /Checksum mismatch/);
  save();
  writeFileSync(path.join(dir, 'SHA256SUMS'), readFileSync(path.join(dir, 'SHA256SUMS'), 'utf8').split('\n').slice(1).join('\n'));
  assert.throws(() => verify(dir), /Binary manifest mismatch/);
  save();
  manifest.binaries[0].file = '../escape';
  save();
  assert.throws(() => verify(dir), /Binary manifest mismatch/);
});

test('verification rejects false activation, server drift and symbolic links', (t) => {
  const { dir, manifest, file, save } = fixture(t);
  manifest.nativeRestrictions.activation = 'ready';
  save();
  assert.throws(() => verify(dir), /activation/);
  manifest.nativeRestrictions.activation = 'requires-human-approval';
  manifest.serverRecipeSha256 = 'wrong';
  save();
  assert.throws(() => verify(dir), /Server manifest/);
  manifest.serverRecipeSha256 = 'recipe';
  save();
  rmSync(path.join(dir, file));
  symlinkSync('install-and-start.md', path.join(dir, file));
  assert.throws(() => verify(dir), /Checksum mismatch/);
});

test('both generated profiles require read-only native execution without overrides', () => {
  const pilot = pilotProfile();
  assert.deepEqual(pilot.runtime_config, { local_directory_mode: 'none', permission_mode: 'read_only' });
  assert.deepEqual(pilot.custom_args, []);
  assert.deepEqual(pilot.custom_env, {});
  assert.deepEqual(pilot.mcp_config, { mcpServers: {} });
  assert.equal(standaloneProfile().bundle_read_only, true);
  assert.doesNotMatch(JSON.stringify([pilot, standaloneProfile(), nativeRestrictions()]), /bypassPermissions|danger-full-access|dangerously-skip|auto_approve/);
  assert.equal(nativeRestrictions().automaticApproval, false);
  assert.equal(nativeRestrictions().otherHarnesses, 'unsupported');
  assert.deepEqual(nativeRestrictions().supported.codex.platforms, ['darwin', 'linux']);
  assert.deepEqual(nativeRestrictions().unsupported.codex.platforms, ['windows']);
  assert.equal(nativeRestrictions().supported.codex.inheritedMcp, 'effective-config-refusal-before-thread-start');
  assert.equal(nativeRestrictions().supported.codex.managedConfiguration, 'never-overridden');
  assert.deepEqual(nativeRestrictions().supported.codex.configAttestation.methods, ['config/read', 'configRequirements/read']);
  assert.equal(nativeRestrictions().supported.codex.configAttestation.includeLayers, true);
  assert.equal(nativeRestrictions().supported.codex.minimumVersion, '0.144.6');
  assert.equal(nativeRestrictions().supported.codex.exactVersion, undefined);
  assert.equal(nativeRestrictions().supported.codex.nativeRecipe, undefined);
  assert.equal(nativeRestrictions().supported.codex.startupGuard, undefined);
  assert.deepEqual(nativeRestrictions().supported.codex.testedVersionRange, ['0.144.6', '0.144.6']);
  assert.equal(nativeRestrictions().supported.codex.startupFeatures, 'explicitly-disabled-in-config-and-argv-before-spawn');
  assert.equal(nativeRestrictions().supported.codex.stockVersions, 'stable >=0.144.6');
  assert.equal(nativeRestrictions().supported.codex.localManaged.action, 'presence-refusal-before-spawn');
  assert.deepEqual(nativeRestrictions().supported.codex.localManaged.macOS.keys, ['config_toml_base64', 'requirements_toml_base64']);
  assert.equal(nativeRestrictions().supported.codex.cloudResidualRisk.accepted, true);
  assert.equal(nativeRestrictions().supported.codex.cloudResidualRisk.startupWarning, true);
  assert.equal(nativeRestrictions().supported.codex.cloudResidualRisk.pluginWarmupsBeforeAttestation, 'possible');
});
