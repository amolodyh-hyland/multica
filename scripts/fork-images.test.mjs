import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { imageInputs, imageManifest, renderRecipe } from './fork-images.mjs';

const source = '24da23749ef7369dcbafa175d57c3154b5bba323';
const recipe = readFileSync(new URL('../docker-compose.selfhost.yml', import.meta.url), 'utf8');

test('source pin requires a full SHA; image-only recipe forwards delegation off by default', () => {
  assert.throws(() => imageInputs('main'));
  assert.throws(() => imageInputs('24da23749'));
  const rendered = renderRecipe(recipe, imageInputs(source));
  assert.match(rendered, /name: multica/);
  assert.match(rendered, /\$\{MULTICA_BACKEND_IMAGE:-local\/multica-backend\}:\$\{MULTICA_IMAGE_TAG:-fork-24da23749ef7369dcbafa175d57c3154b5bba323\}/);
  assert.match(rendered, /\$\{MULTICA_WEB_IMAGE:-local\/multica-web\}:\$\{MULTICA_IMAGE_TAG:-fork-24da23749ef7369dcbafa175d57c3154b5bba323\}/);
  assert.match(rendered, /FF_PUBLIC_DELEGATIONS_V1: \$\{FF_PUBLIC_DELEGATIONS_V1:-false\}/);
  assert.doesNotMatch(rendered, /latest|^\s+build:/m);
  assert.deepEqual([...rendered.matchAll(/^\s+- "([^\n]+)"/gm)].map((match) => match[1]).filter((v) => v.includes(':8080') || v.includes(':3000')).every((v) => v.startsWith('127.0.0.1:')), true);
  assert.throws(() => renderRecipe('services: {}', imageInputs(source)));
});

test('manifest inspects only the selected local engine and rejects wrong image source', () => {
  const inputs = imageInputs(source);
  const calls = [];
  let revision = source;
  const run = (file, args) => {
    calls.push([file, ...args]);
    if (args.includes('info')) return 'engine-local-1234';
    return JSON.stringify([{ Id: `sha256:${'a'.repeat(64)}`, RepoTags: [args.at(-1)], Config: { Labels: { 'org.opencontainers.image.revision': revision } }, Os: 'linux', Architecture: 'arm64' }]);
  };
  const manifest = imageManifest(inputs, 'unix:///test/docker.sock', run);
  assert.equal(manifest.source, source);
  assert.equal(manifest.images.length, 2);
  assert.ok(calls.every((c) => c[0] === 'docker' && c[1] === '--host' && c[2] === 'unix:///test/docker.sock'));
  assert.ok(calls.every((c) => !c.some((arg) => ['build', 'pull', 'compose', 'up', 'down'].includes(arg))));
  revision = 'wrong';
  assert.throws(() => imageManifest(inputs, 'unix:///test/docker.sock', run), /source mismatch/);
  assert.throws(() => imageManifest(inputs, 'tcp://remote', run), /local engine/);
  assert.throws(() => imageManifest({ ...inputs, tag: 'latest' }, 'unix:///test/docker.sock', run), /inputs changed/);
});
