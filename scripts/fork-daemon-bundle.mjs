import { createHash, randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmodSync, lstatSync, mkdirSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { imageInputs, renderRecipe } from './fork-images.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const json = (value) => `${JSON.stringify(value, null, 2)}\n`;
const run = (file, args, options = {}) => execFileSync(file, args, { encoding: 'utf8', cwd: root, ...options }).trim();
const write = (dir, file, bytes, mode = 0o600) => writeFileSync(path.join(dir, file), bytes, { flag: 'wx', mode });
const supported = new Set(['darwin/arm64', 'darwin/amd64', 'linux/arm64', 'linux/amd64', 'windows/amd64']);

export function targets(value) {
  const result = value.split(',');
  if (!result.length || new Set(result).size !== result.length || result.some((target) => !supported.has(target))) {
    throw new Error('Targets must be unique supported os/arch pairs');
  }
  return result;
}

export function profileSettings(source, identity = randomUUID()) {
  if (!/^[a-f0-9]{40}$/.test(source) || !/^[a-f0-9-]{36}$/.test(identity)) throw new Error('Invalid bundle identity');
  const profile = `wi-fac-02-${source.slice(0, 12)}-${identity}`;
  return {
    profile,
    preferredHealthPort: 19515 + ([...Buffer.from(profile)].reduce((total, b) => total + b, 0) % 1000),
    maxConcurrentTasks: 1,
    runtimePinned: true,
    disableAutoUpdate: true,
    disableAutoReload: true,
    gcEnabled: false,
    standalone: true,
  };
}

export function validateServerInputs(inputs, source, tree, recipe) {
  const expected = imageInputs(source);
  if (inputs.version !== 1 || inputs.source !== source || inputs.tree !== tree || inputs.tag !== expected.tag ||
      inputs.recipeSha256 !== hash(renderRecipe(recipe, expected))) throw new Error('Server recipe/source mismatch');
  if (!recipe.includes('FF_PUBLIC_DELEGATIONS_V1: ${FF_PUBLIC_DELEGATIONS_V1:-false}')) throw new Error('Source lacks default-off delegation forwarding');
}

export function build(serverInputs, output, selectedTargets) {
  targets(selectedTargets.join(','));
  const inputBytes = readFileSync(path.join(serverInputs, 'source-manifest.json'));
  const inputs = JSON.parse(inputBytes);
  const { source } = inputs;
  if (!/^[a-f0-9]{40}$/.test(source)) throw new Error('Use a full source SHA');
  const tree = run('git', ['rev-parse', `${source}^{tree}`]);
  const recipe = execFileSync('git', ['show', `${source}:docker-compose.selfhost.yml`], { cwd: root, encoding: 'utf8' });
  validateServerInputs(inputs, source, tree, recipe);
  if (hash(readFileSync(path.join(serverInputs, 'stack/docker-compose.selfhost.yml'))) !== inputs.recipeSha256) throw new Error('Prepared server recipe changed');
  const archive = execFileSync('git', ['archive', '--format=tar', source], { cwd: root, maxBuffer: 128 * 1024 * 1024 });
  if (hash(archive) !== inputs.archiveSha256) throw new Error('Server archive mismatch');
  if (!path.isAbsolute(output)) throw new Error('Output must be an absolute new directory outside the checkout');
  const parent = realpathSync(path.dirname(output));
  if (parent === root || parent.startsWith(`${root}${path.sep}`)) throw new Error('Output must stay outside the checkout');
  mkdirSync(output, { mode: 0o700 });
  output = realpathSync(output);
  const sourceDir = path.join(output, 'source');
  mkdirSync(sourceDir, { mode: 0o700 });
  execFileSync('tar', ['-xf', '-', '-C', sourceDir], { input: archive });
  const date = run('git', ['show', '-s', '--format=%cI', source]);
  const env = { ...process.env, CGO_ENABLED: '0', GOWORK: 'off', GOTOOLCHAIN: 'local', GOPROXY: 'off', GOSUMDB: 'off', GOENV: 'off', GOFLAGS: '', GOEXPERIMENT: '', GOCACHEPROG: '', GOAMD64: 'v1', GOARM64: 'v8.0' };
  const toolchain = run('go', ['env', 'GOVERSION'], { env });
  const ldflags = `-s -w -X main.version=fork-${source} -X main.commit=${source} -X main.buildCommit=${source} -X main.date=${date}`;
  const binaries = selectedTargets.map((target) => {
    const [GOOS, GOARCH] = target.split('/');
    const file = `multica-${GOOS}-${GOARCH}${GOOS === 'windows' ? '.exe' : ''}`;
    run('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags', ldflags, '-o', path.join(output, file), './cmd/multica'], {
      cwd: path.join(sourceDir, 'server'), env: { ...env, GOOS, GOARCH }, stdio: ['ignore', 'pipe', 'inherit'],
    });
    chmodSync(path.join(output, file), 0o700);
    const bytes = readFileSync(path.join(output, file));
    return { target, file, bytes: bytes.length, sha256: hash(bytes) };
  });
  write(output, 'server-source-manifest.json', inputBytes);
  write(output, 'install-and-start.md', readFileSync(path.join(sourceDir, 'docs/fork-daemon-bundle.md')));
  write(output, 'fork-server-images.md', readFileSync(path.join(sourceDir, 'docs/fork-server-images.md')));
  const manifest = {
    version: 1, source, tree, archiveSha256: hash(archive), serverSourceManifestSha256: hash(inputBytes),
    serverRecipeSha256: inputs.recipeSha256, toolchain, date,
    build: { cgo: false, trimpath: true, buildvcs: false, moduleMode: 'readonly', proxy: 'off', goenv: 'off', goflags: '', goexperiment: '', goamd64: 'v1', goarm64: 'v8.0', ldflags },
    modules: Object.fromEntries(['go.mod', 'go.sum'].map((file) => [file, hash(readFileSync(path.join(sourceDir, 'server', file)))])),
    profile: profileSettings(source), binaries,
    nativeRestrictions: {
      activation: 'held', verified: false,
      reason: 'Native permission auto-approval is present; profile and task-root isolation are not a filesystem/network sandbox. Prove effective native restrictions before activation.',
    },
    signing: 'unsigned', notarization: 'not performed',
  };
  write(output, 'bundle-manifest.json', json(manifest));
  const files = [...binaries.map(({ file }) => file), 'bundle-manifest.json', 'server-source-manifest.json', 'install-and-start.md', 'fork-server-images.md'];
  write(output, 'SHA256SUMS', files.map((file) => `${hash(readFileSync(path.join(output, file)))}  ${file}\n`).join(''));
  verify(output);
  return manifest;
}

export function verify(dir) {
  const manifest = JSON.parse(readFileSync(path.join(dir, 'bundle-manifest.json')));
  if (manifest.version !== 1 || !/^[a-f0-9]{40}$/.test(manifest.source)) throw new Error('Invalid bundle manifest');
  const list = readFileSync(path.join(dir, 'SHA256SUMS'), 'utf8').trim().split('\n');
  const checksums = new Map();
  for (const line of list) {
    const match = /^([a-f0-9]{64})  ([a-zA-Z0-9.-]+)$/.exec(line);
    if (!match || checksums.has(match[2])) throw new Error('Invalid checksum entry');
    const [, checksum, file] = match;
    if (!lstatSync(path.join(dir, file)).isFile() || hash(readFileSync(path.join(dir, file))) !== checksum) throw new Error(`Checksum mismatch: ${file}`);
    checksums.set(file, checksum);
  }
  targets(manifest.binaries.map(({ target }) => target).join(','));
  const expectedFiles = ['bundle-manifest.json', 'server-source-manifest.json', 'install-and-start.md', 'fork-server-images.md'];
  for (const binary of manifest.binaries) {
    const [goos, arch] = binary.target.split('/');
    const file = `multica-${goos}-${arch}${goos === 'windows' ? '.exe' : ''}`;
    if (binary.file !== file || checksums.get(file) !== binary.sha256 || readFileSync(path.join(dir, file)).length !== binary.bytes) throw new Error('Binary manifest mismatch');
    expectedFiles.push(file);
  }
  if (expectedFiles.length !== checksums.size || expectedFiles.some((file) => !checksums.has(file))) throw new Error('Incomplete checksums');
  const server = JSON.parse(readFileSync(path.join(dir, 'server-source-manifest.json')));
  if (server.source !== manifest.source || server.tree !== manifest.tree || server.archiveSha256 !== manifest.archiveSha256 ||
      server.recipeSha256 !== manifest.serverRecipeSha256 || checksums.get('server-source-manifest.json') !== manifest.serverSourceManifestSha256) throw new Error('Server manifest mismatch');
  const profile = manifest.profile;
  const identity = profile.profile.slice(`wi-fac-02-${manifest.source.slice(0, 12)}-`.length);
  if (JSON.stringify(profile) !== JSON.stringify(profileSettings(manifest.source, identity)) || manifest.nativeRestrictions.activation !== 'held' || manifest.nativeRestrictions.verified !== false) throw new Error('Invalid activation/profile contract');
  return manifest;
}

function main(args) {
  const [action, ...rest] = args;
  if (action === 'verify' && rest.length === 1) {
    const manifest = verify(rest[0]);
    console.log(`Verified ${manifest.source}; native activation ${manifest.nativeRestrictions.activation}`);
  } else if (action === 'build' && rest.length >= 2 && rest.length <= 3) {
    const target = `${os.platform()}/${os.arch() === 'x64' ? 'amd64' : os.arch()}`;
    const manifest = build(rest[0], rest[1], targets(rest[2] ?? target));
    console.log(`Built ${manifest.binaries.length} targets from ${manifest.source}; native activation held`);
  } else throw new Error('Usage: fork-daemon-bundle.mjs build SERVER_INPUTS NEW_ABSOLUTE_OUTPUT [os/arch,...] | verify BUNDLE');
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main(process.argv.slice(2)); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
