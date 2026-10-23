import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync, existsSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { disabledFeatures, upstream, patchSources, build } from './fork-codex-bundle.mjs';

const read = (file) => readFileSync(new URL(file, import.meta.url), 'utf8');
test('native policy covers the same explicit disables as home, argv and attestation', () => {
  const go = read('../server/pkg/agent/read_only.go');
  const array = go.split('var readOnlyCodexDisabledFeatures = [...]string{')[1].split('\n}')[0];
  const features = [...array.matchAll(/"([a-z_0-9]+)"/g)].map((m) => m[1]);
  assert.deepEqual(disabledFeatures(), features);
  assert.ok(features.includes('apps') && features.includes('plugins') && features.includes('remote_plugin'));
  assert.match(go, /"--wi-fac-02-read-only"/);
  assert.equal(upstream.version, '0.144.6-wi-fac-02.1');
  assert.match(upstream.archiveSha256, /^[0-9a-f]{64}$/);
});

test('pinned native warmup fixture has no work outside the plugins_enabled guard', () => {
  const fixture = read('./codex-bundle/upstream-plugin-startup.rs');
  const body = fixture.slice(fixture.indexOf('    ) {') + 7).trim();
  assert.ok(body.startsWith('if config.plugins_enabled {'));
  // The entire method body is this single conditional. Reject any future
  // warmup inserted before/after it, including unguarded spawn calls.
  let depth = 0;
  let close = -1;
  const open = body.indexOf('{');
  for (let i = open; i < body.length; i++) {
    if (body[i] === '{') depth++;
    if (body[i] === '}' && --depth === 0) { close = i; break; }
  }
  assert.equal(body.slice(close + 1).trim(), '}');
  for (const operation of ['start_curated_repo_sync', 'plugins-marketplace-auto-upgrade', 'maybe_start_remote_plugin_caches_refresh', 'maybe_start_remote_installed_plugin_bundle_sync', 'fetch_and_cache_global_remote_plugin_catalog', 'featured_plugin_ids_for_config']) {
    assert.ok(body.slice(open + 1, close).includes(operation), operation);
  }
});

test('managed normalization fixture proves why explicit startup flags require a native refusal', () => {
  const fixture = read('./codex-bundle/upstream-managed-normalization.rs');
  assert.match(fixture, /for \(feature, enabled\) in pinned_features \{\s*candidate\.set_enabled\(\*feature, \*enabled\);/);
  assert.match(fixture, /candidate\.normalize_dependencies\(\);/);
  assert.throws(() => patchSources({}), /Pinned native source mismatch/);
});

test('native source patch installs the guard on every resolved load before plugin startup', { skip: !process.env.CODEX_BUNDLE_SOURCE }, () => {
  const files = Object.fromEntries(Object.keys(upstream.files).map((file) => [file, readFileSync(path.join(process.env.CODEX_BUNDLE_SOURCE, file))]));
  const patches = patchSources(files);
  const manager = patches['codex-rs/app-server/src/config_manager.rs'];
  assert.equal((manager.match(/self.validate_read_only_bundle\(&config\)\?;/g) || []).length, 3);
  assert.match(manager, /apply_runtime_feature_enablement\(&mut config\);[\s\S]*?validate_read_only_bundle\(&config\)\?;/);
  assert.match(manager, /feature_for_key\(key\).map\(\|feature\| config.features.enabled\(feature\)\)/);
  assert.match(manager, /if !config.mcp_servers.get\(\).is_empty\(\) \{/);
  const lib = patches['codex-rs/app-server/src/lib.rs'];
  assert.ok(lib.indexOf('.with_read_only_bundle(runtime_options.read_only_bundle)') < lib.indexOf('let (mut config, should_run_personality_migration)'));
  assert.ok(lib.indexOf('let (mut config, should_run_personality_migration)') < lib.indexOf('MessageProcessor::new'));
  assert.match(lib, /if runtime_options.read_only_bundle \{\s*return Err\(err\);\s*\}\s*warn!\(error = %err, "Failed to preload config for cloud config bundle"\)/);
  assert.match(lib, /if strict_config \|\| runtime_options.read_only_bundle \{\s*return Err\(err\);/);
  const cli = patches['codex-rs/cli/src/main.rs'];
  assert.match(cli, /strict_config = wi_fac_02_read_only \|\| app_server_strict_config \|\| root_strict_config/);
  assert.match(cli, /read_only_bundle: wi_fac_02_read_only/);
  assert.match(patches['codex-rs/app-server/src/read_only_bundle_config_tests.rs'], /loader_with_enterprise_requirement/);
});

test('native prefetch and offline build cannot invoke inherited credential helpers or see sign-in', { skip: process.platform === 'win32' }, (t) => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'codex-bundle-probe-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = path.join(dir, 'tools');
  const output = path.join(dir, 'bundle');
  const release = path.join(output, 'source/codex-rs/target/release');
  const inheritedHome = path.join(dir, 'existing-home');
  for (const sub of [bin, release, inheritedHome]) mkdirSync(sub, { recursive: true });
  writeFileSync(path.join(inheritedHome, 'auth.json'), 'private-fixture');
  writeFileSync(path.join(output, 'patch-manifest.json'), JSON.stringify({ version: upstream.version, files: {} }));
  writeFileSync(path.join(output, 'upstream.json'), JSON.stringify(upstream));
  for (const file of ['LICENSE', 'NOTICE']) writeFileSync(path.join(output, 'source', file), 'fixture');
  const trace = path.join(dir, 'trace');
  const marker = path.join(dir, 'credential-helper-invoked');
  const helper = path.join(dir, 'credential-helper');
  const globalConfig = path.join(inheritedHome, '.gitconfig');
  const quote = (value) => "'" + value.replaceAll("'", "'\\''") + "'";
  writeFileSync(helper, `#!/bin/sh\nprintf invoked > ${quote(marker)}\nprintf 'username=fixture\\npassword=fixture\\n'\n`, { mode: 0o700 });
  execFileSync('git', ['config', '--file', globalConfig, 'credential.helper', helper]);
  const check = `
case "$HOME" in ${quote(output + '/build-home-')}*) ;; *) exit 81 ;; esac
[ "$CODEX_HOME" = "$HOME/codex" ] || exit 81
[ ! -f "$CODEX_HOME/auth.json" ] || exit 82
[ "$GIT_CONFIG_COUNT" = 2 ] || exit 84
[ "$GIT_CONFIG_KEY_0" = credential.helper ] || exit 85
[ -z "$GIT_CONFIG_VALUE_0" ] || exit 86
[ -z "$GIT_CONFIG_PARAMETERS" ] || exit 87
[ "$GIT_CONFIG_GLOBAL" = /dev/null ] || exit 88
[ "$GIT_CONFIG_NOSYSTEM" = 1 ] || exit 88
[ "$GIT_CONFIG_KEY_1" = core.askPass ] || exit 88
[ -z "$GIT_CONFIG_VALUE_1" ] || exit 88
[ "$GIT_TERMINAL_PROMPT" = 0 ] || exit 88
[ "$CARGO_NET_GIT_FETCH_WITH_CLI" = true ] || exit 88
[ -z "\${GIT_ASKPASS+x}\${SSH_ASKPASS+x}\${CARGO_REGISTRY_TOKEN+x}" ] || exit 88
if printf 'protocol=https\\nhost=credential-fixture.invalid\\n\\n' | git credential fill >/dev/null 2>&1; then exit 89; fi
[ ! -f ${quote(marker)} ] || exit 90
printf '%s\\n' "$*" >> ${quote(trace)}
`;
  writeFileSync(path.join(bin, 'cargo'), '#!/bin/sh\n' + check + 'exit 0\n', { mode: 0o700 });
  writeFileSync(path.join(bin, 'rustc'), '#!/bin/sh\n' + check + 'echo "rustc 1.95.0 (fixture)"\n', { mode: 0o700 });
  writeFileSync(path.join(release, 'codex'), '#!/bin/sh\n' + check + `
case "$*" in
  --version) echo 'codex-cli ${upstream.version}' ;;
  'app-server --help') echo '--wi-fac-02-read-only' ;;
  *) exit 83 ;;
esac
`, { mode: 0o700 });
  const env = { PATH: bin + path.delimiter + process.env.PATH, HOME: inheritedHome, CODEX_HOME: inheritedHome, CARGO_HOME: path.join(dir, 'cargo-cache'), GIT_CONFIG_GLOBAL: globalConfig, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'credential.helper', GIT_CONFIG_VALUE_0: helper, GIT_CONFIG_PARAMETERS: 'must-not-use-inherited-parameters', GIT_ASKPASS: helper, SSH_ASKPASS: helper, GIT_TERMINAL_PROMPT: '0', CARGO_NET_GIT_FETCH_WITH_CLI: 'false', CARGO_REGISTRY_TOKEN: 'must-not-inherit' };
  execFileSync('git', ['credential', 'fill'], { cwd: dir, env: { ...process.env, ...env, GIT_CONFIG_COUNT: '0', GIT_CONFIG_PARAMETERS: '' }, input: 'protocol=https\nhost=credential-fixture.invalid\n\n', stdio: ['pipe', 'pipe', 'pipe'] });
  assert.equal(readFileSync(marker, 'utf8'), 'invoked');
  rmSync(marker);
  const saved = Object.fromEntries(Object.keys(env).map((key) => [key, process.env[key]]));
  t.after(() => { for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete process.env[key]; else process.env[key] = value; } });
  Object.assign(process.env, env);
  build(output);
  assert.equal(existsSync(marker), false);
  assert.equal(readFileSync(path.join(inheritedHome, 'auth.json'), 'utf8'), 'private-fixture');
  const calls = readFileSync(trace, 'utf8');
  assert.match(calls, /fetch --locked --config net.git-fetch-with-cli=true\nbuild --offline --locked --release[^\n]*\ntest --offline --locked --release/);
  assert.match(calls, /--version/);
  assert.match(calls, /app-server --help/);
  const receipt = JSON.parse(readFileSync(path.join(output, 'native-manifest.json')));
  assert.equal(receipt.gitCredentialHelpers, false);
  assert.equal(receipt.dependencyFetch.gitBackend, 'cli');
  assert.equal(receipt.dependencyFetch.anonymous, true);
  assert.deepEqual(receipt.build, { offline: true, locked: true });
  mkdirSync(env.CARGO_HOME);
  for (const file of ['config', 'config.toml', 'credentials', 'credentials.toml']) {
    writeFileSync(path.join(env.CARGO_HOME, file), 'must-not-load');
    assert.throws(() => build(output), /without Cargo configuration or credentials/);
    rmSync(path.join(env.CARGO_HOME, file));
  }
  assert.equal(readFileSync(trace, 'utf8'), calls);
});
