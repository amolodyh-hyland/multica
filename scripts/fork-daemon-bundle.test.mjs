import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { profileSettings, pilotProfile, standaloneProfile, nativeRestrictions, targets, validateServerInputs, verify } from './fork-daemon-bundle.mjs';
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

function fixture(t) {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'multica-bundle-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const file = 'multica-darwin-arm64';
  const bytes = Buffer.from('fixture binary');
  const server = { source, tree: 'tree', archiveSha256: 'archive', recipeSha256: 'recipe' };
  const serverBytes = JSON.stringify(server);
  const manifest = {
    version: 2, ...server, serverSourceManifestSha256: hash(serverBytes), serverRecipeSha256: server.recipeSha256,
    profile: profileSettings(source, identity),
    nativeRestrictions: nativeRestrictions(),
    binaries: [{ target: 'darwin/arm64', file, bytes: bytes.length, sha256: hash(bytes) }],
  };
  const files = { [file]: bytes, 'server-source-manifest.json': serverBytes, 'install-and-start.md': 'fixture guide', 'fork-server-images.md': 'server guide', 'pilot-profile.json': JSON.stringify(pilotProfile()), 'standalone-profile.json': JSON.stringify(standaloneProfile()), 'bundle-manifest.json': JSON.stringify(manifest) };
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
  assert.deepEqual(nativeRestrictions().supported.codex.platforms, ['linux']);
  assert.equal(nativeRestrictions().supported.codex.inheritedMcp, 'refuse-before-launch');
  assert.equal(nativeRestrictions().supported.codex.managedConfiguration, 'never-overridden');
});
