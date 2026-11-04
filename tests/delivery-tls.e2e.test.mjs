// End-to-end check of the release checker's external-tls-only mode against a real
// image, real PostgreSQL 17 stand-ins and real certificates. Opt-in: it needs Docker,
// OpenSSL 3.4+ and Node 22/24 first on PATH, and takes several minutes.
//   CRN_TLS_E2E=1 node --test tests/delivery-tls.e2e.test.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { verify } from '../internal/buildstep/verify-delivery.mjs';

const fixture = fileURLToPath(new URL('./fixtures/external-tls-app/', import.meta.url));
const skip = process.env.CRN_TLS_E2E === '1' ? false : 'set CRN_TLS_E2E=1 (needs Docker, OpenSSL 3.4+, Node 22/24)';

function buildImage(weak) {
  const tag = `crn-tls-fixture:${weak ? 'weak' : 'ok'}`;
  const r = spawnSync('docker', ['build', '-q', '-f', path.join(fixture, 'Dockerfile.fixture'), '--build-arg', `FIXTURE_WEAK=${weak ? 1 : 0}`, '-t', tag, fixture], { encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  return tag;
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
    assert.ok(t.stand_ins.every(s => s.server_version.startsWith('17.')));
    assert.deepEqual(t.runtime_identity, { current_user: 'crn_app', current_schema: 'crn_verify' });
    assert.equal(t.isolation.public_objects, 0);
    assert.equal(t.isolation.objects_not_owned_by_app, 0);
    assert.deepEqual(Object.keys(t.migrations.files), ['20261102000000_init']);
    assert.ok(t.must_fail.length >= 37 && t.must_fail.every(m => m.ok && m.result === 'failed'), JSON.stringify(t.must_fail.filter(m => !m.ok)));
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
    for (const c of ['DATABASE_URL sslmode=disable (runtime)', 'DATABASE_URL sslmode missing (seed)']) assert.ok(bad.includes(c), `${c} not caught: ${bad.join('; ')}`);
    assert.match(receipt.error, /Cases that did not fail as required/);
  } finally { await fs.rm(dir, { recursive: true, force: true }); }
});
