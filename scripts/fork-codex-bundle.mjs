import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const policyDir = fileURLToPath(new URL('./codex-bundle/', import.meta.url));
export const upstream = JSON.parse(readFileSync(path.join(policyDir, 'upstream.json')));
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const guard = () => readFileSync(path.join(policyDir, 'read_only_bundle.rs'), 'utf8');
export const disabledFeatures = () => [...guard().matchAll(/^    "([a-z_0-9]+)",$/gm)].map((m) => m[1]);
function replace(source, before, after, count = 1) {
  if (source.split(before).length - 1 !== count) throw new Error('Pinned native patch context mismatch');
  return source.replaceAll(before, after);
}

export function patchSources(files) {
  for (const [file, sha] of Object.entries(upstream.files)) {
    if (!files[file] || hash(files[file]) !== sha) throw new Error(`Pinned native source mismatch: ${file}`);
  }
  for (const [file, fixture] of [
    ['codex-rs/core-plugins/src/manager.rs', 'upstream-plugin-startup.rs'],
    ['codex-rs/core/src/config/managed_features.rs', 'upstream-managed-normalization.rs'],
  ]) {
    if (!files[file].toString().includes(readFileSync(path.join(policyDir, fixture), 'utf8'))) throw new Error('Pinned native fixture drift');
  }
  const result = {};
  const cli = 'codex-rs/cli/src/main.rs';
  let text = files[cli].toString();
  text = replace(text, '    author,\n    version,', `    author,\n    version = "${upstream.version}",`);
  text = replace(text, 'struct AppServerCommand {', 'struct AppServerCommand {\n    /// Refuse incompatible effective features before any plugin startup work.\n    #[arg(long)]\n    wi_fac_02_read_only: bool,');
  text = replace(text, '            let AppServerCommand {\n                subcommand,', '            let AppServerCommand {\n                wi_fac_02_read_only,\n                subcommand,');
  text = replace(text, '                    let runtime_options = codex_app_server::AppServerRuntimeOptions {', '                    let runtime_options = codex_app_server::AppServerRuntimeOptions {\n                        read_only_bundle: wi_fac_02_read_only,');
  text = replace(text, '            let strict_config = app_server_strict_config || root_strict_config;', '            let strict_config = wi_fac_02_read_only || app_server_strict_config || root_strict_config;');
  result[cli] = text;

  const lib = 'codex-rs/app-server/src/lib.rs';
  text = files[lib].toString();
  text = replace(text, 'pub struct AppServerRuntimeOptions {', 'pub struct AppServerRuntimeOptions {\n    pub read_only_bundle: bool,');
  text = replace(text, '            plugin_startup_tasks: PluginStartupTasks::Start,', '            read_only_bundle: false,\n            plugin_startup_tasks: PluginStartupTasks::Start,');
  text = replace(text, '        Arc::new(NoopThreadConfigLoader),\n    );', '        Arc::new(NoopThreadConfigLoader),\n    )\n    .with_read_only_bundle(runtime_options.read_only_bundle);');
  text = replace(text, '            warn!(error = %err, "Failed to preload config for cloud config bundle");', '            if runtime_options.read_only_bundle {\n                return Err(err);\n            }\n            warn!(error = %err, "Failed to preload config for cloud config bundle");');
  text = replace(text, '            if strict_config {\n                return Err(err);', '            if strict_config || runtime_options.read_only_bundle {\n                return Err(err);');
  result[lib] = text;

  const manager = 'codex-rs/app-server/src/config_manager.rs';
  text = files[manager].toString();
  text = '#[path = "read_only_bundle.rs"]\nmod read_only_bundle;\n' + text;
  text = replace(text, '    strict_config: bool,\n    cloud_config_bundle:', '    strict_config: bool,\n    read_only_bundle: bool,\n    cloud_config_bundle:');
  text = replace(text, '            strict_config,\n            cloud_config_bundle:', '            strict_config,\n            read_only_bundle: false,\n            cloud_config_bundle:');
  text = replace(text, '    pub(crate) fn codex_home(&self) -> &Path {', `    pub(crate) fn with_read_only_bundle(mut self, enabled: bool) -> Self {
        self.read_only_bundle = enabled;
        self
    }

    fn validate_read_only_bundle(&self, config: &Config) -> std::io::Result<()> {
        if self.read_only_bundle {
            read_only_bundle::validate(|key| {
                codex_features::feature_for_key(key).map(|feature| config.features.enabled(feature))
            })?;
            if !config.mcp_servers.get().is_empty() {
                return Err(std::io::Error::new(
                    std::io::ErrorKind::PermissionDenied,
                    "read-only bundle requires empty effective MCP configuration; managed settings will not be overridden",
                ));
            }
        }
        Ok(())
    }

    pub(crate) fn codex_home(&self) -> &Path {`);
  // Validate typed, normalized managed features on every native Config load,
  // including startup's cloud/MDM load and subsequent thread refreshes.
  text = replace(text, '        self.apply_arg0_paths(&mut config);\n        Ok(config)', '        self.apply_arg0_paths(&mut config);\n        self.validate_read_only_bundle(&config)?;\n        Ok(config)', 3);
  result[manager] = text + '\n#[cfg(test)]\n#[path = "read_only_bundle_config_tests.rs"]\nmod read_only_bundle_config_tests;\n';
  result['codex-rs/app-server/src/read_only_bundle_config_tests.rs'] = readFileSync(path.join(policyDir, 'read_only_bundle_config_tests.rs'), 'utf8');
  result['codex-rs/app-server/src/read_only_bundle.rs'] = guard();
  const test = 'codex-rs/app-server/tests/suite/v2/remote_control.rs';
  result[test] = replace(files[test].toString(), '            AppServerRuntimeOptions {\n                plugin_startup_tasks:', '            AppServerRuntimeOptions {\n                read_only_bundle: false,\n                plugin_startup_tasks:');
  // The release archive stamps workspace Cargo.toml but retains 0.0.0
  // for its 132 local packages in Cargo.lock. Normalize only those versions;
  // third-party versions, checksums and Git revisions remain byte-for-byte.
  const lock = 'codex-rs/Cargo.lock';
  let localPackages = 0;
  result[lock] = files[lock].toString().split('[[package]]').map((block) => {
    if (/^\nname = /m.test(block) && !/^source = /m.test(block) && block.includes('version = "0.0.0"')) {
      localPackages++;
      return replace(block, 'version = "0.0.0"', 'version = "0.144.6"');
    }
    return block;
  }).join('[[package]]');
  if (localPackages !== 132) throw new Error('Pinned local package count changed');
  return result;
}

export function prepare(archive, output) {
  if (hash(readFileSync(archive)) !== upstream.archiveSha256) throw new Error('Pinned Codex archive checksum mismatch');
  if (!path.isAbsolute(output)) throw new Error('Use a new absolute output directory');
  const root = path.resolve(policyDir, '../..');
  const parent = realpathSync(path.dirname(output));
  if (parent === root || parent.startsWith(root + path.sep)) throw new Error('Native output must stay outside the checkout');
  mkdirSync(output, { mode: 0o700 });
  output = realpathSync(output);
  const source = path.join(output, 'source');
  mkdirSync(source, { mode: 0o700 });
  execFileSync('tar', ['-xzf', archive, '-C', source, '--strip-components=1']);
  const files = Object.fromEntries(Object.keys(upstream.files).map((file) => [file, readFileSync(path.join(source, file))]));
  const patches = patchSources(files);
  for (const [file, bytes] of Object.entries(patches)) writeFileSync(path.join(source, file), bytes);
  copyFileSync(path.join(policyDir, 'upstream.json'), path.join(output, 'upstream.json'));
  writeFileSync(path.join(output, 'patch-manifest.json'), JSON.stringify({
    version: upstream.version, upstream, files: Object.fromEntries(Object.entries(patches).map(([file, bytes]) => [file, hash(bytes)])),
  }, null, 2) + '\n');
  return source;
}

export function build(output) {
  const source = path.join(output, 'source', 'codex-rs');
  const manifest = JSON.parse(readFileSync(path.join(output, 'patch-manifest.json')));
  if (manifest.version !== upstream.version) throw new Error('Unexpected native patch version');
  for (const [file, sha] of Object.entries(manifest.files)) {
    if (hash(readFileSync(path.join(output, 'source', file))) !== sha) throw new Error('Native patched source changed');
  }
  if (!process.env.CARGO_HOME || !path.isAbsolute(process.env.CARGO_HOME)) throw new Error('Set CARGO_HOME to a separate absolute build cache');
  if (['config', 'config.toml', 'credentials', 'credentials.toml'].some((file) => existsSync(path.join(process.env.CARGO_HOME, file)))) {
    throw new Error('Use a separate cache without Cargo configuration or credentials');
  }
  const buildHome = mkdtempSync(path.join(output, 'build-home-'));
  const probeHome = path.join(buildHome, 'codex');
  mkdirSync(probeHome, { mode: 0o700 });
  const env = Object.fromEntries(['PATH', 'TMPDIR', 'TMP', 'TEMP', 'SYSTEMROOT', 'SDKROOT', 'DEVELOPER_DIR', 'MACOSX_DEPLOYMENT_TARGET', 'RUSTUP_HOME']
    .filter((key) => process.env[key] !== undefined).map((key) => [key, process.env[key]]));
  Object.assign(env, {
    HOME: buildHome, XDG_CONFIG_HOME: path.join(buildHome, 'config'), CODEX_HOME: probeHome,
    CARGO_HOME: process.env.CARGO_HOME, CARGO_TARGET_DIR: path.join(source, 'target'), CARGO_INCREMENTAL: '0',
    CARGO_NET_GIT_FETCH_WITH_CLI: 'true', GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1',
    GIT_TERMINAL_PROMPT: '0', GIT_CONFIG_PARAMETERS: '', GIT_CONFIG_COUNT: '2',
    GIT_CONFIG_KEY_0: 'credential.helper', GIT_CONFIG_VALUE_0: '', GIT_CONFIG_KEY_1: 'core.askPass', GIT_CONFIG_VALUE_1: '',
  });
  const options = { cwd: source, stdio: 'inherit', env };
  const rustc = execFileSync('rustc', ['--version'], { cwd: source, env, encoding: 'utf8' }).trim();
  if (!rustc.startsWith('rustc 1.95.0 ')) throw new Error('Use the pinned Rust 1.95.0 toolchain');
  if (!['darwin', 'linux'].includes(process.platform)) throw new Error('Unsupported native Codex platform');
  const fetch = ['fetch', '--locked', '--config', 'net.git-fetch-with-cli=true'];
  execFileSync('cargo', fetch, options);
  execFileSync('cargo', ['build', '--offline', '--locked', '--release', '-p', 'codex-cli', '--bin', 'codex', '-j', '2'], options);
  const verification = ['test', '--offline', '--locked', '--release', '-p', 'codex-app-server', '--lib', 'read_only_bundle', '-j', '2', '--', '--nocapture'];
  execFileSync('cargo', verification, options);
  const binary = path.join(output, 'codex');
  copyFileSync(path.join(source, 'target', 'release', path.basename(binary)), binary);
  const reported = execFileSync(binary, ['--version'], { cwd: source, env: options.env, encoding: 'utf8' }).trim();
  if (reported !== `codex-cli ${upstream.version}`) throw new Error('Native version mismatch');
  const help = execFileSync(binary, ['app-server', '--help'], { cwd: source, env: options.env, encoding: 'utf8' });
  if (!help.includes('--wi-fac-02-read-only')) throw new Error('Native startup guard switch missing');
  writeFileSync(path.join(output, 'native-manifest.json'), JSON.stringify({
    ...manifest, rustc, gitCredentialHelpers: false, verification: { result: 'passed', command: ['cargo', ...verification] }, platform: process.platform, arch: process.arch, binary: path.basename(binary), sha256: hash(readFileSync(binary)),
    dependencyFetch: { command: ['cargo', ...fetch], anonymous: true, gitBackend: 'cli', isolatedHome: true, globalGitConfig: false, systemGitConfig: false, askpass: false },
    build: { offline: true, locked: true },
    patchManifestSha256: hash(readFileSync(path.join(output, 'patch-manifest.json'))),
  }, null, 2) + '\n');
  for (const file of ['LICENSE', 'NOTICE']) copyFileSync(path.join(output, 'source', file), path.join(output, file));
  writeFileSync(path.join(output, 'SHA256SUMS'), ['codex', 'native-manifest.json', 'patch-manifest.json', 'upstream.json', 'LICENSE', 'NOTICE'].map((file) => `${hash(readFileSync(path.join(output, file)))}  ${file}\n`).join(''));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [command, ...args] = process.argv.slice(2);
  if (command === 'prepare' && args.length === 2) console.log(prepare(...args));
  else if (command === 'build' && args.length === 1) build(args[0]);
  else throw new Error('Usage: fork-codex-bundle.mjs prepare PINNED_ARCHIVE NEW_OUTPUT | build OUTPUT');
}
