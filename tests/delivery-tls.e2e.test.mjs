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
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { verify, BAD_CA_FILES, TLS_PG_IMAGE, caShapeProblem } from '../internal/buildstep/verify-delivery.mjs';

const fixture = fileURLToPath(new URL('./fixtures/external-tls-app/', import.meta.url));
const skip = process.env.CRN_TLS_E2E === '1' ? false : 'set CRN_TLS_E2E=1 (needs Docker, OpenSSL 3.4+, Node 22/24)';

function buildImage(weak) {
  const tag = `crn-tls-fixture:${weak ? 'weak' : 'ok'}`;
  const platform = process.env.CRN_TLS_E2E_PLATFORM ? ['--platform', process.env.CRN_TLS_E2E_PLATFORM] : [];
  const r = spawnSync('docker', ['build', '-q', ...platform, '-f', path.join(fixture, 'Dockerfile.fixture'), '--build-arg', `FIXTURE_WEAK=${weak ? 1 : 0}`, '-t', tag, fixture], { encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
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
    const receipt = await verify(dir, undefined, { image: buildImage(false) });
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
    const receipt = await verify(dir, undefined, { image: buildImage(true) });
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
