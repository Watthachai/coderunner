// The one helper every database path uses (runtime check, seed, pre-migrate gate).
// It validates the target and the CA before anything opens a connection, and its
// errors never carry the URL, credentials or query string.
import fs from 'node:fs';
import pg from 'pg';

const SCHEMA = /^[a-z_][a-z0-9_]{0,62}$/;
// FIXTURE_WEAK=1 is a deliberately broken helper: it lets a plaintext URL through, takes
// any CA file or none, and compares only the database of the two URLs, so the release
// checker's tests can show that it catches such an app.
const weak = process.env.FIXTURE_WEAK === '1';

export class TargetError extends Error {}

export function sanitize(message) {
  return String(message).replace(/postgres(?:ql)?:\/\/\S+/gi, '[url]').replace(/[A-Za-z0-9_.-]+:[^\s@/]+@/g, '[credentials]@');
}

export function parseAndValidateDatabaseTarget(raw, kind) {
  const name = kind === 'migration' ? 'MIGRATION_DATABASE_URL' : 'DATABASE_URL';
  if (!raw) throw new TargetError(`${name} is not set`);
  let u;
  try { u = new URL(raw); } catch { throw new TargetError(`${name} is not a valid URL`); }
  if (u.protocol !== 'postgresql:' && u.protocol !== 'postgres:') throw new TargetError(`${name} is not a postgresql URL`);
  const target = {
    host: u.hostname, port: Number(u.port || 5432),
    database: decodeURIComponent(u.pathname.slice(1)), user: decodeURIComponent(u.username),
    schema: u.searchParams.get('schema') ?? '',
  };
  if (!target.host || !target.database || !target.user || !Number.isInteger(target.port)) throw new TargetError(`${name} needs host, port, database and user`);
  if (!u.password) throw new TargetError(`${name} has no password`);
  if (!SCHEMA.test(target.schema) || target.schema === 'public') throw new TargetError(`${name} schema is missing, malformed or public`);
  const params = Object.fromEntries(u.searchParams);
  const want = kind === 'migration'
    ? { schema: target.schema, sslmode: 'require', sslaccept: 'strict', sslcert: process.env.DATABASE_CA_FILE ?? '' }
    : { schema: target.schema, sslmode: 'verify-full' };
  const same = JSON.stringify(Object.keys(params).sort()) === JSON.stringify(Object.keys(want).sort()) && Object.keys(want).every(k => params[k] === want[k]);
  if (!same && !(weak && kind === 'runtime')) throw new TargetError(`${name} TLS parameters are not the required form`);
  return target;
}

function readCA() {
  const file = process.env.DATABASE_CA_FILE;
  if (!file) throw new TargetError('DATABASE_CA_FILE is not set');
  if (weak) { try { return fs.readFileSync(file, 'utf8'); } catch { return undefined; } }
  let stat;
  try { stat = fs.lstatSync(file); } catch (e) { throw new TargetError(e.code === 'ENOENT' ? 'DATABASE_CA_FILE does not exist' : 'DATABASE_CA_FILE cannot be read'); }
  if (!stat.isFile()) throw new TargetError('DATABASE_CA_FILE is not a regular file');
  let ca;
  try { ca = fs.readFileSync(file, 'utf8'); } catch { throw new TargetError('DATABASE_CA_FILE cannot be read'); }
  if ((ca.match(/-----BEGIN CERTIFICATE-----/g) ?? []).length !== 1) throw new TargetError('DATABASE_CA_FILE must hold exactly one PEM certificate');
  return ca;
}

// pg.Pool config as separate fields, never connectionString, so nothing in the URL
// can override ssl. Trusts only the CA file and checks the server's IP/name.
export function runtimePool() {
  const raw = process.env.DATABASE_URL;
  const t = parseAndValidateDatabaseTarget(raw, 'runtime');
  const plaintext = weak && new URL(raw).searchParams.get('sslmode') !== 'verify-full';
  const ssl = plaintext ? false : { ca: readCA(), rejectUnauthorized: true };
  return new pg.Pool({
    host: t.host, port: t.port, database: t.database, user: t.user,
    password: decodeURIComponent(new URL(raw).password),
    ssl, options: `-c search_path=${t.schema}`,
    max: 8, connectionTimeoutMillis: 5000, idleTimeoutMillis: 10000,
  });
}

// Before migrate: both URLs valid and pointing at the same target, and a CA file the
// schema engine can rely on. It reads sslcert itself, so the file is checked here.
export function preMigrateCheck() {
  const runtime = parseAndValidateDatabaseTarget(process.env.DATABASE_URL, 'runtime');
  const migration = parseAndValidateDatabaseTarget(process.env.MIGRATION_DATABASE_URL, 'migration');
  const differ = (weak ? ['database'] : ['host', 'port', 'database', 'user', 'schema']).filter(k => runtime[k] !== migration[k]);
  if (differ.length) throw new TargetError(`DATABASE_URL and MIGRATION_DATABASE_URL differ in: ${differ.join(', ')}`);
  if (!weak) readCA();
}

export async function main(fn) {
  try { await fn(); } catch (e) { console.error(`error: ${sanitize(e.message)}`); process.exit(1); }
}
