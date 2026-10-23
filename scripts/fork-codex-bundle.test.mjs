import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { disabledFeatures, upstream, patchSources } from './fork-codex-bundle.mjs';

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
