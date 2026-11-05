import test from 'node:test';
import assert from 'node:assert/strict';
import { validateContract, tlsUrls, tlsFailurePlan, leaks, sanitizedTail, TLS_SCRIPTS, BAD_CA_FILES, TLS_PG_IMAGE, LOCAL_PG_IMAGE, isWantedPostgres, caShapeProblem, caFileMount, CA_PROBE_JS } from '../internal/buildstep/verify-delivery.mjs';

const docs = { 'BUILD_NOTES.md': 'notes', 'TEST_CASES.md': 'cases', 'PORT_CHECKLIST.md': '- [x] done' };
const scripts = ['typecheck', 'test:unit', 'test:smoke', 'build', 'start', 'db:generate', 'db:deploy', 'db:seed'];
const pkg = extra => ({ scripts: Object.fromEntries([...scripts, ...extra].map(k => [k, k])), dependencies: { next: '16.2.9' } });
const lock = { lockfileVersion: 3, packages: { '': { dependencies: { next: '16.2.9' } } } };
const tls = { schema_version: 1, database: true, database_mode: 'external-tls-only', database_ca_file: '/run/fitt-ca/ntc-db-ca.pem' };

test('external-tls-only needs a database, a CA path under /run and a db:check script', () => {
  assert.doesNotThrow(() => validateContract(tls, pkg(['db:check']), lock, docs));
  assert.throws(() => validateContract(tls, pkg([]), lock, docs), /db:check/);
  assert.throws(() => validateContract({ ...tls, database: false }, pkg(['db:check']), lock, docs), /"database": true/);
  for (const bad of [undefined, 'ca.pem', '/etc/ca.pem', '/run/fitt-ca/../x.pem', '/run/ca.crt']) {
    assert.throws(() => validateContract({ ...tls, database_ca_file: bad }, pkg(['db:check']), lock, docs), /database_ca_file/, String(bad));
  }
  assert.throws(() => validateContract({ ...tls, database_mode: 'tls' }, pkg(['db:check']), lock, docs), /database_mode/);
  assert.throws(() => validateContract({ schema_version: 1, database: true, database_ca_file: '/run/x.pem' }, pkg([]), lock, docs), /only for database_mode/);
  assert.doesNotThrow(() => validateContract({ schema_version: 1, database: true }, pkg([]), lock, docs), 'a local-database app is unchanged');
});

test('the URLs the checker hands the app are TLS-only, in the contract shape', () => {
  const u = tlsUrls({ user: 'app', password: 'pw', host: '10.0.0.10', database: 'db', caFile: '/run/fitt-ca/x.pem' });
  assert.equal(u.runtime, 'postgresql://app:pw@10.0.0.10:5432/db?schema=crn_verify&sslmode=verify-full');
  assert.equal(u.migration, 'postgresql://app:pw@10.0.0.10:5432/db?schema=crn_verify&sslmode=require&sslaccept=strict&sslcert=/run/fitt-ca/x.pem');
});

const ips = { ok: '10.0.0.10', otherName: '10.0.0.11', expired: '10.0.0.12', plain: '10.0.0.13', tripwire: '10.0.0.14' };
const plan = tlsFailurePlan({ ips, user: 'app', password: 'pw', database: 'db', caFile: '/run/fitt-ca/x.pem' });

test('53 cases must fail, 12 of them on the server side and 41 stopped before any connection', () => {
  assert.equal(plan.length, 53);
  assert.equal(plan.filter(c => c.noConnection).length, 41);
});

test('every TLS failure is tried through runtime, seed and migrate', () => {
  for (const name of ['wrong CA', 'IP not in SAN', 'expired certificate', 'server without TLS']) {
    assert.deepEqual(plan.filter(c => c.name === name).map(c => c.path).sort(), ['migrate', 'runtime', 'seed'], name);
  }
  assert.ok(plan.every(c => c.script === TLS_SCRIPTS[c.path]));
  const host = c => new URL(c.env.DATABASE_URL).hostname;
  assert.ok(plan.filter(c => c.name === 'wrong CA').every(c => c.ca === 'wrong' && host(c) === ips.ok));
  assert.ok(plan.filter(c => c.name === 'IP not in SAN').every(c => host(c) === ips.otherName));
  assert.ok(plan.filter(c => c.name === 'expired certificate').every(c => host(c) === ips.expired));
  assert.ok(plan.filter(c => c.name === 'server without TLS').every(c => host(c) === ips.plain));
});

test('every bad CA file stops runtime, seed and migrate before any connection', () => {
  assert.deepEqual(BAD_CA_FILES, { 'no CA file': null, 'CA file is a symlink': 'symlink', 'CA file is empty': 'empty', 'CA file holds two certificates': 'two', 'CA file is unreadable': 'unreadable' });
  for (const [name, ca] of Object.entries(BAD_CA_FILES)) {
    const cases = plan.filter(c => c.name === name);
    assert.deepEqual(cases.map(c => c.path).sort(), ['migrate', 'runtime', 'seed'], name);
    for (const c of cases) {
      assert.equal(c.ca, ca, name);
      assert.ok(c.noConnection, `${name} (${c.path}) must count connections`);
      assert.equal(new URL(c.env.DATABASE_URL).hostname, ips.tripwire, name);
      assert.equal(new URL(c.env.MIGRATION_DATABASE_URL).hostname, ips.tripwire, name);
    }
  }
});

test('a bad CA file counts only when it really has the intended shape', () => {
  const good = { exists: true, symlink: false, readable: true, certs: 1, size: 800 };
  const cases = [
    ['symlink', { ...good, symlink: true }],
    ['empty', { ...good, certs: 0, size: 0 }],
    ['two', { ...good, certs: 2, size: 1600 }],
    ['unreadable', { ...good, readable: false, certs: 0 }],
    [null, { exists: false, symlink: false, readable: false, certs: 0, size: 0 }],
  ];
  for (const [shape, probe] of cases) {
    assert.equal(caShapeProblem(shape, probe), null, String(shape));
    assert.ok(caShapeProblem(shape, good), `a good file must not pass as ${shape}`);
  }
  assert.ok(caShapeProblem('symlink', { ...good, symlink: true, readable: false }), 'the link must lead to a good CA, or a bad target would hide the symlink case');
  assert.ok(caShapeProblem('symlink', { exists: false, symlink: false, readable: false, certs: 0, size: 0 }), 'a mount that lost the link is not the symlink case');
});

test('the probe tells a missing file from one it may not look at', async t => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-ca-probe-'));
  t.after(async () => { await fs.chmod(path.join(dir, 'locked'), 0o755).catch(() => {}); await fs.rm(dir, { recursive: true, force: true }); });
  const pem = '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n';
  const probe = file => JSON.parse(spawnSync(process.execPath, ['-e', CA_PROBE_JS], { env: { CRN_CA_PROBE: file }, encoding: 'utf8' }).stdout);
  await fs.writeFile(path.join(dir, 'empty.pem'), '');
  await fs.writeFile(path.join(dir, 'two.pem'), pem + pem);
  await fs.writeFile(path.join(dir, 'real.pem'), pem);
  await fs.symlink('real.pem', path.join(dir, 'link.pem'));
  await fs.writeFile(path.join(dir, 'noread.pem'), pem, { mode: 0o000 });
  await fs.mkdir(path.join(dir, 'locked'));
  await fs.writeFile(path.join(dir, 'locked', 'ca.pem'), pem);
  await fs.chmod(path.join(dir, 'locked'), 0o000);
  assert.equal(caShapeProblem(null, probe(path.join(dir, 'missing.pem'))), null);
  assert.equal(caShapeProblem('empty', probe(path.join(dir, 'empty.pem'))), null);
  assert.equal(caShapeProblem('two', probe(path.join(dir, 'two.pem'))), null);
  assert.equal(caShapeProblem('symlink', probe(path.join(dir, 'link.pem'))), null);
  if (process.getuid?.() === 0) return; // root reads everything
  assert.equal(caShapeProblem('unreadable', probe(path.join(dir, 'noread.pem'))), null, 'readable to lstat, not to read');
  // Docker Desktop refuses lstat too: EACCES is not a missing file.
  assert.equal(caShapeProblem('unreadable', probe(path.join(dir, 'locked', 'ca.pem'))), null, 'not even lstat');
  assert.ok(caShapeProblem(null, probe(path.join(dir, 'locked', 'ca.pem'))), 'must not pass as a missing file');
});

test('a symlink or an unreadable file reaches the container inside a mounted directory; other shapes mount the file', () => {
  const files = { right: '/s/ca.pem', wrong: '/s/wrong.pem', empty: '/s/empty.pem', two: '/s/two.pem', symlink: '/s/link', unreadable: '/s/noread' };
  const target = '/run/fitt-ca/ntc-db-ca.pem';
  assert.deepEqual(caFileMount(null, files, target), []);
  assert.deepEqual(caFileMount('symlink', files, target), ['-v', '/s/link:/run/fitt-ca:ro']);
  assert.deepEqual(caFileMount('unreadable', files, target), ['-v', '/s/noread:/run/fitt-ca:ro']);
  for (const shape of ['right', 'wrong', 'empty', 'two']) assert.deepEqual(caFileMount(shape, files, target), ['-v', `/s/${shape === 'right' ? 'ca' : shape}.pem:${target}:ro`]);
});

test('the stand-ins run one PostgreSQL image pinned by digest, version 17.11 exactly', async () => {
  assert.match(TLS_PG_IMAGE, /^postgres:17\.11-alpine@sha256:[0-9a-f]{64}$/);
  const src = await fs.readFile(new URL('../internal/buildstep/verify-delivery.mjs', import.meta.url), 'utf8');
  assert.doesNotMatch(src, /postgres:17-alpine/);
  // The database of the other path is pinned the same way: a moving tag changes what the checker proves.
  assert.match(LOCAL_PG_IMAGE, /^postgres:16\.14-alpine@sha256:[0-9a-f]{64}$/);
  assert.doesNotMatch(src, /postgres:16-alpine/);
  assert.match(src, /'-p', '127\.0\.0\.1::5432', LOCAL_PG_IMAGE\]/);
  for (const ok of ['17.11', '17.11 (Debian 17.11-1)']) assert.ok(isWantedPostgres(ok), ok);
  for (const bad of ['17.1', '17.10', '17.12', '17.110', '16.11', '7.11', '']) assert.ok(!isWantedPostgres(bad), bad);
});

test('server-side TLS failures keep the contract URLs: no plaintext is ever offered', () => {
  for (const c of plan.filter(c => ['wrong CA', 'IP not in SAN', 'expired certificate', 'server without TLS', ...Object.keys(BAD_CA_FILES)].includes(c.name))) {
    assert.match(c.env.DATABASE_URL, /&sslmode=verify-full$/, `${c.name} ${c.path}`);
    assert.match(c.env.MIGRATION_DATABASE_URL, /&sslmode=require&sslaccept=strict&sslcert=\/run\/fitt-ca\/x\.pem$/, `${c.name} ${c.path}`);
  }
});

test('configuration errors must stop before any connection, at the tripwire', () => {
  const gates = plan.filter(c => !['wrong CA', 'IP not in SAN', 'expired certificate', 'server without TLS'].includes(c.name));
  assert.ok(gates.length >= 20);
  const usedBy = c => new URL(c.path === 'migrate' ? c.env.MIGRATION_DATABASE_URL ?? c.env.DATABASE_URL : c.env.DATABASE_URL).hostname;
  assert.ok(gates.every(c => c.noConnection && usedBy(c) === ips.tripwire), 'every gate case targets the tripwire with the URL its path uses');
  const names = new Set(gates.map(c => c.name));
  for (const n of ['MIGRATION_DATABASE_URL missing', 'DATABASE_URL sslmode missing', 'DATABASE_URL sslmode=prefer', 'DATABASE_URL sslmode=disable', 'DATABASE_URL sslmode=no-verify',
    'MIGRATION_DATABASE_URL sslaccept=accept_invalid_certs', 'MIGRATION_DATABASE_URL sslaccept missing', 'MIGRATION_DATABASE_URL sslcert missing',
    'schema "Film-Job"', 'schema "a;drop"', 'schema "public"']) assert.ok(names.has(n), n);
  const missing = plan.find(c => c.name === 'MIGRATION_DATABASE_URL missing');
  assert.equal(missing.env.MIGRATION_DATABASE_URL, undefined);
  assert.doesNotMatch(plan.find(c => c.name === 'DATABASE_URL sslmode missing' && c.path === 'runtime').env.DATABASE_URL, /sslmode/);
  assert.match(plan.find(c => c.name === 'MIGRATION_DATABASE_URL sslaccept=accept_invalid_certs').env.MIGRATION_DATABASE_URL, /sslaccept=accept_invalid_certs/);
  assert.match(plan.find(c => c.name === 'schema "a;drop"' && c.path === 'seed').env.DATABASE_URL, /schema=a%3Bdrop&/);
});

test('two URLs that differ in any one target field stop before migrate, with nothing connected', () => {
  const fields = { host: u => u.hostname, port: u => u.port, database: u => u.pathname, user: u => u.username, schema: u => u.searchParams.get('schema') };
  for (const [field, read] of Object.entries(fields)) {
    const cases = plan.filter(c => c.name === `runtime and migration targets differ: ${field}`);
    assert.deepEqual(cases.map(c => c.path), ['migrate'], field);
    const [c] = cases;
    assert.ok(c.noConnection, field);
    assert.equal(c.ca, 'right', field);
    const runtime = new URL(c.env.DATABASE_URL), migration = new URL(c.env.MIGRATION_DATABASE_URL);
    assert.notEqual(read(runtime), read(migration), field);
    for (const [other, r] of Object.entries(fields)) if (other !== field) assert.equal(r(runtime), r(migration), `only ${field} may differ, not ${other}`);
    // What migrate would connect to is the tripwire, on the port it listens on.
    assert.equal(migration.hostname, ips.tripwire, field);
    assert.equal(migration.port, '5432', field);
  }
  assert.equal(plan.filter(c => c.name === 'runtime and migration targets differ').length, 0);
});

test('a failure may not print a secret, a connection URL, its query string or user:password@host', () => {
  assert.deepEqual(leaks('Error: certificate verify failed (IP address mismatch)', ['s3cret']), []);
  assert.deepEqual(leaks('connect failed with s3cret', ['s3cret']), ['a secret value']);
  assert.ok(leaks('bad url postgresql://app:pw@10.0.0.1:5432/db').includes('a connection URL'));
  assert.ok(leaks('invalid ?sslmode=disable').includes('a URL query string'));
  assert.ok(leaks('auth app:hunter2@db.local:5432 refused').includes('user:password@host'));
  const tail = sanitizedTail('first line\nfailed: postgresql://app:pw@h:1/db?x=1 with s3cret', ['s3cret']);
  assert.equal(tail, 'first line failed: [url] with [redacted]');
});

import { requireDigestRef } from '../internal/buildstep/verify-delivery.mjs';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

test('--image takes an image by digest only, never a tag', () => {
  const d = 'sha256:' + 'a'.repeat(64);
  assert.equal(requireDigestRef(`172.168.1.234:5050/fitt/app@${d}`), `172.168.1.234:5050/fitt/app@${d}`);
  for (const bad of ['172.168.1.234:5050/fitt/app:v3', 'app', `app@sha256:${'a'.repeat(63)}`, `app:v3@sha256:${'A'.repeat(64)}`, '']) {
    assert.throws(() => requireDigestRef(bad), /digest/, bad);
  }
});

test('cleanup removes the stand-ins\' anonymous volumes with their containers', async () => {
  const src = await fs.readFile(new URL('../internal/buildstep/verify-delivery.mjs', import.meta.url), 'utf8');
  assert.match(src, /cleanup\(\['rm', '-f', '-v', \.\.\.containers\]\)/);
});
