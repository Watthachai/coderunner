// End-to-end check of the release checker's external-tls-only mode against a real
// image, real PostgreSQL 17 stand-ins and real certificates. Opt-in: it needs Docker,
// OpenSSL 3.4+ and Node 22/24 first on PATH, and takes several minutes.
//   CRN_TLS_E2E=1 node --test tests/delivery-tls.e2e.test.mjs
// CRN_TLS_E2E_PLATFORM=linux/amd64 builds the fixture for that platform, as a delivered
// image is, so an arm64 machine runs the whole check under emulation.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { cpSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { verify, BAD_CA_FILES, TLS_PG_IMAGE, caShapeProblem } from '../internal/buildstep/verify-delivery.mjs';
import { forbiddenEntry } from '../internal/buildstep/accept-candidate.mjs';

const fixture = fileURLToPath(new URL('./fixtures/external-tls-app/', import.meta.url));
const skip = process.env.CRN_TLS_E2E === '1' ? false : 'set CRN_TLS_E2E=1 (needs Docker, OpenSSL 3.4+, Node 22/24)';

const WIDGET_JS = fileURLToPath(new URL('../internal/buildstep/fitt-feedback.js', import.meta.url));
const AMD64 = 'linux/amd64';
function buildImage({ weak = false, widget = '', platform = process.env.CRN_TLS_E2E_PLATFORM || 'native' } = {}) {
  const tag = `crn-tls-fixture:${weak ? 'weak' : 'ok'}-${widget || 'widget'}-${platform.replace('/', '-')}`;
  // The real widget, from a build context of its own: the repo keeps one copy of it.
  const context = mkdtempSync(path.join(os.tmpdir(), 'crn-tls-context-'));
  try {
    cpSync(fixture, context, { recursive: true });
    mkdirSync(path.join(context, 'public'));
    cpSync(WIDGET_JS, path.join(context, 'public/fitt-feedback.js'));
    const r = spawnSync('docker', ['build', '-q', ...(platform === 'native' ? [] : ['--platform', platform]), '-f', path.join(context, 'Dockerfile.fixture'), '--build-arg', `FIXTURE_WEAK=${weak ? 1 : 0}`, '--build-arg', `FIXTURE_WEAK_WIDGET=${widget}`, '-t', tag, context], { encoding: 'utf8' });
    assert.equal(r.status, 0, r.stderr);
  } finally { rmSync(context, { recursive: true, force: true }); }
  // The checker takes images by digest only.
  const id = spawnSync('docker', ['image', 'inspect', '--format', '{{.Id}}', tag], { encoding: 'utf8' }).stdout.trim();
  assert.match(id, /^sha256:[0-9a-f]{64}$/);
  return `crn-tls-fixture@${id}`;
}
async function copyFixture() {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-tls-e2e-'));
  await fs.cp(fixture, dir, { recursive: true });
  return dir;
}

test('external-tls-only: an app that follows the rules passes every check', { skip, timeout: 40 * 60_000 }, async () => {
  const dir = await copyFixture();
  try {
    const receipt = await verify(dir, undefined, { image: buildImage() });
    assert.equal(receipt.status, 'passed', receipt.error);
    const t = receipt.external_tls;
    assert.equal(receipt.database_mode, 'external-tls-only');
    assert.ok(t.stand_ins.every(s => s.server_version === '17.11' && s.image === TLS_PG_IMAGE), JSON.stringify(t.stand_ins));
    assert.deepEqual(t.runtime_identity, { current_user: 'crn_app', current_schema: 'crn_verify' });
    assert.equal(t.isolation.public_objects, 0);
    assert.equal(t.isolation.objects_not_owned_by_app, 0);
    assert.deepEqual(Object.keys(t.migrations.files), ['20261102000000_init']);
    assert.equal(t.must_fail.length, 53);
    assert.ok(t.must_fail.every(m => m.ok && m.result === 'failed'), JSON.stringify(t.must_fail.filter(m => !m.ok)));
    assert.equal(t.must_fail.filter(m => m.connections === null).length, 12, 'only the server-side cases run without the tripwire');
    assert.deepEqual(Object.keys(t.ca_shapes).sort(), Object.keys(BAD_CA_FILES).sort());
    for (const [name, shape] of Object.entries(BAD_CA_FILES)) assert.equal(caShapeProblem(shape, t.ca_shapes[name]), null, name);
    for (const m of t.must_fail.filter(m => m.connections !== null)) assert.equal(m.connections, 0, `${m.case} (${m.path})`);
    assert.equal(t.versions.pg, '8.23.1');
    assert.equal(t.versions.prisma, '7.10.1');
  } finally { await fs.rm(dir, { recursive: true, force: true }); }
});

test('external-tls-only: an app that lets a plaintext URL through is refused', { skip, timeout: 40 * 60_000 }, async () => {
  const dir = await copyFixture();
  try {
    const receipt = await verify(dir, undefined, { image: buildImage({ weak: true }) });
    assert.equal(receipt.status, 'failed');
    const bad = receipt.external_tls.must_fail.filter(m => !m.ok).map(m => `${m.case} (${m.path})`);
    const caught = [
      'DATABASE_URL sslmode=disable (runtime)', 'DATABASE_URL sslmode missing (seed)',
      'no CA file (runtime)', 'no CA file (seed)',
      'CA file is a symlink (runtime)', 'CA file is a symlink (seed)',
      'CA file is empty (runtime)', 'CA file is empty (seed)',
      'CA file holds two certificates (runtime)', 'CA file holds two certificates (seed)',
      'CA file is unreadable (runtime)', 'CA file is unreadable (seed)',
      'CA file is a symlink (migrate)', 'CA file holds two certificates (migrate)',
      'runtime and migration targets differ: host (migrate)', 'runtime and migration targets differ: port (migrate)',
      'runtime and migration targets differ: user (migrate)', 'runtime and migration targets differ: schema (migrate)',
    ];
    for (const c of caught) assert.ok(bad.includes(c), `${c} not caught: ${bad.join('; ')}`);
    assert.ok(!bad.includes('runtime and migration targets differ: database (migrate)'), 'the weak helper still compares the database');
    assert.match(receipt.error, /Cases that did not fail as required/);
  } finally { await fs.rm(dir, { recursive: true, force: true }); }
});

// --- the gate before "released", always on a linux/amd64 image -----------------------------
const PROJECT = '11111111-1111-4111-8111-111111111111';
const ACCEPT_SCRIPT = fileURLToPath(new URL('../internal/buildstep/accept-candidate.mjs', import.meta.url));
// As CRN runs it: one process per candidate, in a clean tree, through the command line.
async function accept(name, imageOptions, { widget = true, forbidden = forbiddenEntry('203.0.113.250') } = {}) {
  const dir = await copyFixture();
  const work = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-accept-e2e-'));
  try {
    const list = path.join(work, 'forbidden'), receiptFile = path.join(work, 'receipt.json');
    await fs.writeFile(list, `${forbidden}\n`);
    const image = buildImage({ platform: AMD64, ...imageOptions });
    const r = spawnSync(process.execPath, [ACCEPT_SCRIPT, '--image', image, '--commit', 'c0ffee', '--project', PROJECT, '--widget', widget ? 'on' : 'off', '--forbidden', list, '--receipt', receiptFile], { cwd: dir, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 });
    const raw = await fs.readFile(receiptFile, 'utf8');
    const receipt = JSON.parse(raw);
    assert.equal(r.status, receipt.status === 'passed' ? 0 : 1, 'the exit code follows the receipt');
    if (process.env.CRN_TLS_E2E_RECEIPTS) await fs.writeFile(path.join(process.env.CRN_TLS_E2E_RECEIPTS, `${name}.json`), raw);
    return receipt;
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
    await fs.rm(work, { recursive: true, force: true });
  }
}

test('the gate passes a good amd64 candidate after every check', { skip, timeout: 40 * 60_000 }, async () => {
  const r = await accept('passed', {});
  assert.equal(r.status, 'passed', r.error);
  assert.equal(r.platform, AMD64);
  assert.deepEqual(r.artifact_scan.hits, []);
  assert.ok(r.artifact_scan.source.files > 5);
  assert.ok(r.artifact_scan.image.layers > 3 && r.artifact_scan.image.blobs.some(b => b.encoding === 'gzip'), JSON.stringify(r.artifact_scan.image));
  assert.ok(r.artifact_scan.image.config.env > 3 && r.artifact_scan.image.config.history > 3, JSON.stringify(r.artifact_scan.image.config));
  assert.equal(r.widget.expected, true);
  assert.deepEqual(r.widget.outside_requests, []);
  assert.equal(r.checker.status, 'passed');
  assert.equal(r.checker.external_tls.must_fail.length, 53);
  assert.ok(r.checker.checks.some(c => c.name === 'widget opens' && c.passed));
});

test('the gate finds a forbidden value in the image config, the history, a layer and a deleted file', { skip, timeout: 20 * 60_000 }, async () => {
  const surfaces = {
    'NPM_CONFIG_UPDATE_NOTIFIER=false': 'config: Env',
    'apt-get install -y --no-install-recommends': 'config: history',
    'fixture login</p>': 'a layer: server.mjs',
    'TLS-only database access': 'a lower layer: TEST_CASES.md, which a later layer deleted',
  };
  const values = Object.keys(surfaces);
  const r = await accept('failed-forbidden', {}, { forbidden: values.map(forbiddenEntry).join('\n') });
  assert.equal(r.status, 'failed');
  assert.equal(r.checker, undefined, 'stops before the long checks');
  values.forEach((v, i) => assert.ok(r.artifact_scan.hits.some(h => h.label === `real value #${i + 1}` && h.where.startsWith('image ')), `${surfaces[v]} not found in the image`));
  for (const v of values) assert.ok(!JSON.stringify(r).includes(v), `the receipt holds ${surfaces[v]}`);
});

test('the gate refuses a placeholder left in the image', { skip, timeout: 20 * 60_000 }, async () => {
  const r = await accept('failed-placeholder', { widget: 'placeholder' });
  assert.equal(r.status, 'failed');
  assert.ok(r.artifact_scan.hits.some(h => h.label === 'placeholder WITHHELD_DB_HOST' && h.where.startsWith('image ')), JSON.stringify(r.artifact_scan.hits));
  assert.ok(r.artifact_scan.hits.some(h => h.label === 'placeholder WITHHELD_DB_HOST' && h.where === 'source server.mjs') === false, 'the commit itself is clean here');
});

test('the gate refuses a widget loaded twice', { skip, timeout: 40 * 60_000 }, async () => {
  const r = await accept('failed-widget-twice', { widget: 'twice' });
  assert.equal(r.status, 'failed');
  assert.match(r.error, /2 widget tags/);
});

test('the gate refuses a page that asks another origin', { skip, timeout: 40 * 60_000 }, async () => {
  const r = await accept('failed-widget-outside', { widget: 'outside' });
  assert.equal(r.status, 'failed');
  assert.match(r.error, /192\.0\.2\.1/);
});

test('the gate refuses a widget on a build that must not carry one', { skip, timeout: 40 * 60_000 }, async () => {
  const r = await accept('failed-widget-unwanted', {}, { widget: false });
  assert.equal(r.status, 'failed');
  assert.match(r.error, /must not carry the widget/);
});

test('the gate refuses an image for this machine when that is not amd64', { skip: skip || (process.arch === 'x64' ? 'this machine is amd64' : false), timeout: 10 * 60_000 }, async () => {
  const r = await accept('failed-platform', { platform: 'native' });
  assert.equal(r.status, 'failed');
  assert.match(r.error, /want linux\/amd64/);
});
