import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync, readdirSync, lstatSync, readlinkSync, writeFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const sha256 = (value) => createHash('sha256').update(value).digest('hex');
const command = (file, args, options = {}) => execFileSync(file, args, { cwd: root, encoding: 'utf8', ...options }).trim();
const jsonFile = (file, value) => writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { flag: 'wx', mode: 0o600 });

export function imageInputs(source) {
  if (!/^[a-f0-9]{40}$/.test(source)) throw new Error('Use a full source commit SHA');
  return {
    source,
    tag: `fork-${source}`,
    backend: 'local/multica-backend',
    web: 'local/multica-web',
    backendArgs: { VERSION: `fork-${source}`, COMMIT: source, DATE: '1970-01-01T00:00:00Z' },
    webArgs: { NEXT_PUBLIC_APP_VERSION: `fork-${source}` },
  };
}

export function renderRecipe(recipe, inputs) {
  if (!recipe.includes('${MULTICA_BACKEND_IMAGE:-ghcr.io/multica-ai/multica-backend}:${MULTICA_IMAGE_TAG:-latest}') ||
      !recipe.includes('${MULTICA_WEB_IMAGE:-ghcr.io/multica-ai/multica-web}:${MULTICA_IMAGE_TAG:-latest}')) throw new Error('Unexpected selfhost recipe');
  return recipe.replace('${MULTICA_BACKEND_IMAGE:-ghcr.io/multica-ai/multica-backend}', `\${MULTICA_BACKEND_IMAGE:-${inputs.backend}}`)
    .replace('${MULTICA_WEB_IMAGE:-ghcr.io/multica-ai/multica-web}', `\${MULTICA_WEB_IMAGE:-${inputs.web}}`)
    .replaceAll('${MULTICA_IMAGE_TAG:-latest}', `\${MULTICA_IMAGE_TAG:-${inputs.tag}}`);
}

function inventory(dir, prefix = '') {
  return readdirSync(dir).sort().flatMap((name) => {
    const relative = path.posix.join(prefix, name);
    const file = path.join(dir, name);
    const stat = lstatSync(file);
    if (stat.isDirectory()) return inventory(file, relative);
    if (stat.isSymbolicLink()) return [{ file: relative, link: readlinkSync(file) }];
    return [{ file: relative, mode: stat.mode & 0o777, sha256: sha256(readFileSync(file)) }];
  });
}

export function prepare(source, output, run = command) {
  const inputs = imageInputs(source);
  if (!path.isAbsolute(output)) throw new Error('Output must be an absolute new directory outside the checkout');
  const parent = realpathSync(path.dirname(output));
  if (parent === root || parent.startsWith(`${root}${path.sep}`)) throw new Error('Output must stay outside the checkout');
  if (run('git', ['rev-parse', `${source}^{commit}`]) !== source) throw new Error('Unknown source commit');
  mkdirSync(output, { mode: 0o700 });
  const context = path.join(output, 'source');
  mkdirSync(context, { mode: 0o700 });
  const archive = execFileSync('git', ['archive', '--format=tar', source], { cwd: root, maxBuffer: 128 * 1024 * 1024 });
  execFileSync('tar', ['-xf', '-', '-C', context], { input: archive });
  const recipe = renderRecipe(readFileSync(path.join(context, 'docker-compose.selfhost.yml'), 'utf8'), inputs);
  const stack = path.join(output, 'stack');
  mkdirSync(stack, { mode: 0o700 });
  writeFileSync(path.join(stack, 'docker-compose.selfhost.yml'), recipe, { mode: 0o600, flag: 'wx' });
  writeFileSync(path.join(output, 'build.env'), `MULTICA_SOURCE=${source}\nMULTICA_IMAGE_TAG=${inputs.tag}\nMULTICA_BACKEND_IMAGE=${inputs.backend}\nMULTICA_WEB_IMAGE=${inputs.web}\n`, { mode: 0o600, flag: 'wx' });
  const manifest = { version: 1, ...inputs, tree: run('git', ['rev-parse', `${source}^{tree}`]), archiveSha256: sha256(archive), recipeSha256: sha256(recipe), files: inventory(context) };
  jsonFile(path.join(output, 'source-manifest.json'), manifest);
  return manifest;
}

export function imageManifest(inputs, engine, run = command) {
  if (!/^unix:\/\/\/[^\n\r\0]+$/.test(engine) || engine.includes('..')) throw new Error('Select the local engine socket used by the server controls');
  const expected = imageInputs(inputs.source);
  if (inputs.tag !== expected.tag || inputs.backend !== expected.backend || inputs.web !== expected.web) throw new Error('Build inputs changed');
  const docker = (args) => run('docker', ['--host', engine, ...args]);
  const engineId = docker(['info', '--format', '{{.ID}}']);
  if (!engineId) throw new Error('Missing engine identity');
  const images = ['backend', 'web'].map((key) => {
    const reference = `${inputs[key]}:${inputs.tag}`;
    const result = JSON.parse(docker(['image', 'inspect', reference]));
    if (!Array.isArray(result) || result.length !== 1) throw new Error('Ambiguous image');
    const image = result[0];
    if (!/^sha256:[a-f0-9]{64}$/.test(image.Id) || image.Config?.Labels?.['org.opencontainers.image.revision'] !== inputs.source || !image.RepoTags?.includes(reference)) throw new Error(`Image source mismatch: ${reference}`);
    return { service: key === 'web' ? 'frontend' : key, reference, id: image.Id, repoDigests: image.RepoDigests ?? [], os: image.Os, architecture: image.Architecture };
  });
  if (docker(['info', '--format', '{{.ID}}']) !== engineId) throw new Error('Engine changed');
  return { version: 1, source: inputs.source, sourceManifestSha256: sha256(`${JSON.stringify(inputs, null, 2)}\n`), recipeSha256: inputs.recipeSha256, engine, engineId, images };
}

function main(args) {
  const [action, ...rest] = args;
  const flag = (name) => {
    const index = rest.indexOf(name);
    if (index < 0 || !rest[index + 1]) throw new Error(`Missing ${name}`);
    return rest[index + 1];
  };
  if (action === 'prepare') prepare(flag('--source'), flag('--output'));
  else if (action === 'manifest') {
    const dir = flag('--inputs');
    const source = JSON.parse(readFileSync(path.join(dir, 'source-manifest.json'), 'utf8'));
    if (sha256(readFileSync(path.join(dir, 'stack/docker-compose.selfhost.yml'))) !== source.recipeSha256 || JSON.stringify(inventory(path.join(dir, 'source'))) !== JSON.stringify(source.files)) throw new Error('Prepared source or recipe changed');
    jsonFile(path.join(dir, 'image-manifest.json'), imageManifest(source, flag('--engine')));
  } else throw new Error('Usage: fork-images.mjs prepare --source FULL_SHA --output NEW_ABSOLUTE_DIR | manifest --inputs DIR --engine unix:///socket');
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main(process.argv.slice(2)); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
