import test from 'node:test';
import assert from 'node:assert/strict';
import { validateContract, tlsUrls, tlsFailurePlan, leaks, sanitizedTail, TLS_SCRIPTS } from '../internal/buildstep/verify-delivery.mjs';

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

test('every TLS failure is tried through runtime, seed and migrate', () => {
  for (const name of ['wrong CA', 'IP not in SAN', 'expired certificate', 'server without TLS', 'no CA file']) {
    assert.deepEqual(plan.filter(c => c.name === name).map(c => c.path).sort(), ['migrate', 'runtime', 'seed'], name);
  }
  assert.ok(plan.every(c => c.script === TLS_SCRIPTS[c.path]));
  const host = c => new URL(c.env.DATABASE_URL).hostname;
  assert.ok(plan.filter(c => c.name === 'wrong CA').every(c => c.ca === 'wrong' && host(c) === ips.ok));
  assert.ok(plan.filter(c => c.name === 'IP not in SAN').every(c => host(c) === ips.otherName));
  assert.ok(plan.filter(c => c.name === 'expired certificate').every(c => host(c) === ips.expired));
  assert.ok(plan.filter(c => c.name === 'server without TLS').every(c => host(c) === ips.plain));
  // Without a CA file, runtime and seed must stop before any connection.
  assert.ok(plan.filter(c => c.name === 'no CA file' && c.path !== 'migrate').every(c => c.ca === null && c.noConnection && host(c) === ips.tripwire));
});

test('server-side TLS failures keep the contract URLs: no plaintext is ever offered', () => {
  for (const c of plan.filter(c => ['wrong CA', 'IP not in SAN', 'expired certificate', 'server without TLS', 'no CA file'].includes(c.name))) {
    assert.match(c.env.DATABASE_URL, /&sslmode=verify-full$/, `${c.name} ${c.path}`);
    assert.match(c.env.MIGRATION_DATABASE_URL, /&sslmode=require&sslaccept=strict&sslcert=\/run\/fitt-ca\/x\.pem$/, `${c.name} ${c.path}`);
  }
});

test('configuration errors must stop before any connection, at the tripwire', () => {
  const gates = plan.filter(c => !['wrong CA', 'IP not in SAN', 'expired certificate', 'server without TLS', 'no CA file'].includes(c.name));
  assert.ok(gates.length >= 20);
  assert.ok(gates.every(c => c.noConnection && new URL(c.env.DATABASE_URL).hostname === ips.tripwire), 'every gate case targets the tripwire');
  const names = new Set(gates.map(c => c.name));
  for (const n of ['MIGRATION_DATABASE_URL missing', 'DATABASE_URL sslmode missing', 'DATABASE_URL sslmode=prefer', 'DATABASE_URL sslmode=disable', 'DATABASE_URL sslmode=no-verify',
    'MIGRATION_DATABASE_URL sslaccept=accept_invalid_certs', 'MIGRATION_DATABASE_URL sslaccept missing', 'MIGRATION_DATABASE_URL sslcert missing',
    'schema "Film-Job"', 'schema "a;drop"', 'schema "public"', 'runtime and migration targets differ']) assert.ok(names.has(n), n);
  const missing = plan.find(c => c.name === 'MIGRATION_DATABASE_URL missing');
  assert.equal(missing.env.MIGRATION_DATABASE_URL, undefined);
  assert.doesNotMatch(plan.find(c => c.name === 'DATABASE_URL sslmode missing' && c.path === 'runtime').env.DATABASE_URL, /sslmode/);
  assert.match(plan.find(c => c.name === 'MIGRATION_DATABASE_URL sslaccept=accept_invalid_certs').env.MIGRATION_DATABASE_URL, /sslaccept=accept_invalid_certs/);
  assert.match(plan.find(c => c.name === 'schema "a;drop"' && c.path === 'seed').env.DATABASE_URL, /schema=a%3Bdrop&/);
  assert.match(plan.find(c => c.name === 'runtime and migration targets differ').env.MIGRATION_DATABASE_URL, /\/db_other\?/);
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
