import fs from 'node:fs/promises';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';
import { randomBytes, createHash } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const DB_MODES = ['local', 'external-tls-only'];
export function validateContract(contract, pkg, lock, docs) {
  if (contract.schema_version !== 1 || typeof contract.database !== 'boolean') throw new Error('Invalid crn-delivery.json');
  if (contract.database_mode !== undefined && !DB_MODES.includes(contract.database_mode)) throw new Error(`crn-delivery.json database_mode must be one of ${DB_MODES.join(', ')}`);
  const tls = contract.database_mode === 'external-tls-only';
  if (tls && contract.database !== true) throw new Error('database_mode external-tls-only needs "database": true');
  if (tls && (typeof contract.database_ca_file !== 'string' || !/^\/run\/[A-Za-z0-9._/-]+\.pem$/.test(contract.database_ca_file) || contract.database_ca_file.includes('..'))) throw new Error('database_mode external-tls-only needs database_ca_file, an absolute .pem path under /run/');
  if (!tls && contract.database_ca_file !== undefined) throw new Error('database_ca_file is only for database_mode external-tls-only');
  for (const key of ['BUILD_NOTES.md', 'TEST_CASES.md', 'PORT_CHECKLIST.md']) {
    if (!docs[key]?.trim()) throw new Error(`Missing or empty ${key}`);
  }
  if (docs['PORT_CHECKLIST.md'].split('\n').some(s => /^\s*[-*]\s*\[ \]/.test(s) && !s.includes('[orphan]'))) throw new Error('Required port checklist items remain open');
  if (!Number.isInteger(lock.lockfileVersion) || lock.lockfileVersion < 2 || !lock.packages?.['']) throw new Error('An npm v2+ lockfile is required');
  const required = ['typecheck', 'test:unit', 'test:smoke', 'build', 'start', ...(contract.database ? ['db:generate', 'db:deploy', 'db:seed'] : []), ...(tls ? ['db:check'] : [])];
  for (const key of required) if (!pkg.scripts?.[key]) throw new Error(`Missing npm script ${key}`);
  for (const group of ['dependencies', 'devDependencies']) for (const [name, version] of Object.entries(pkg[group] ?? {})) {
    if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) throw new Error(`Pin an exact version for ${name}`);
    if (lock.packages[''][group]?.[name] !== version) throw new Error(`Lockfile disagrees for ${name}`);
  }
}
export function assertTestResults(kind, result) {
  if (kind === 'unit') {
    if (result.success !== true || !(result.numTotalTests > 0) || result.numPassedTests !== result.numTotalTests || result.numFailedTests || result.numPendingTests || result.numTodoTests) throw new Error('Unit tests must exist and all pass without skips');
    return result.numTotalTests;
  }
  const s = result.stats;
  if (!s || !(s.expected > 0) || s.unexpected || s.flaky || s.skipped || result.errors?.length) throw new Error('Browser tests must exist and all pass without skips or flaky retries');
  return s.expected;
}
// The suspension switch (FITT Billing, references/service-status.md): the customer image
// runs against this fake status server — started from the same image under node — and
// must answer 503 on / while suspended, keep /api/health reachable, and work normally
// while active. It listens on 8080 — not PORT, which the image sets to 3000 for Next.js.
export const SWITCH_TEST_KEY = 'fbl_crn-verify-switch';
export const SWITCH_TEST_MESSAGE = 'CRN verify: service suspended';
export const FAKE_STATUS_SERVER = `require('http').createServer((q,s)=>{const ok=q.headers.authorization==='Bearer ${SWITCH_TEST_KEY}';const st=q.url==='/suspended'?'suspended':'active';s.writeHead(ok?200:401,{'content-type':'application/json'});s.end(JSON.stringify(ok?{status:st,message:st==='suspended'?'${SWITCH_TEST_MESSAGE}':'',checkedAt:new Date().toISOString()}:{code:'unauthorized'}));}).listen(Number(process.env.CRN_FAKE_STATUS_PORT||8080));`;
export function switchVerdict(wanted, { homeStatus, homeBody, healthStatus }) {
  if (wanted === 'suspended') {
    if (homeStatus !== 503) return `A suspended app must answer 503 on / (got ${homeStatus}); wire assets/service-status.mjs into the request proxy`;
    if (!homeBody.includes(SWITCH_TEST_MESSAGE)) return 'The suspension page must show the message from the status server';
    if (healthStatus === 503) return '/api/health must stay reachable while the app is suspended';
    return null;
  }
  return homeStatus >= 500 ? `An active app must work normally (got ${homeStatus} on /)` : null;
}
// The run-anywhere rules (references/runs-anywhere.md): the customer image must work
// with a read-only root filesystem, writing only /tmp and the uploads directory, and
// without any Linux capability — on a customer's hardened server and on Cloud Run.
export const HARDENED_RUN = ['--read-only', '--tmpfs', '/tmp', '--tmpfs', '/data/uploads:mode=1777', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges'];
// Same rules as assets/scheduler.mjs validateSchedule. Kept here because CRN runs this
// checker on its own, without the skill's assets beside it.
const SCHEDULE_EVERY = [5, 10, 15, 20, 30, 60];
const SCHEDULE_KEYS = new Set(['name', 'path', 'every_minutes', 'daily_at']);
export function validateSchedule(entries) {
  if (!Array.isArray(entries)) throw new Error('crn.schedule.json must be a JSON array');
  const seen = new Set();
  for (const e of entries) {
    const label = JSON.stringify(e);
    if (!e || typeof e !== 'object' || Array.isArray(e)) throw new Error(`schedule entry is not an object: ${label}`);
    for (const k of Object.keys(e)) if (!SCHEDULE_KEYS.has(k)) throw new Error(`schedule entry has unknown key "${k}": ${label}`);
    if (typeof e.name !== 'string' || !/^[a-z0-9][a-z0-9-]{0,39}$/.test(e.name)) throw new Error(`schedule name must be lowercase letters, digits and dashes: ${label}`);
    if (seen.has(e.name)) throw new Error(`schedule name "${e.name}" is used twice`);
    seen.add(e.name);
    if (e.path !== `/api/cron/${e.name}`) throw new Error(`schedule path must be /api/cron/${e.name}: ${label}`);
    const every = e.every_minutes !== undefined, daily = e.daily_at !== undefined;
    if (every === daily) throw new Error(`schedule entry needs exactly one of every_minutes or daily_at: ${label}`);
    if (every && !SCHEDULE_EVERY.includes(e.every_minutes)) throw new Error(`every_minutes must be one of ${SCHEDULE_EVERY.join(', ')}: ${label}`);
    if (daily && (typeof e.daily_at !== 'string' || !/^([01]\d|2[0-3]):([0-5]\d)$/.test(e.daily_at))) throw new Error(`daily_at must be HH:MM (Bangkok time): ${label}`);
  }
  return entries;
}
// crn.runtime.json (references/runs-anywhere.md): the app declares every variable it
// reads at run time, so an installer can ask for them without reading the source.
// CRN writes contract, migrations_first, port, health, volumes and writable itself;
// when present they must hold its values. Shape agreed with Fittcore on 3 Nov 2026.
const RUNTIME_KEYS = new Set(['contract', 'migrations_first', 'port', 'health', 'volumes', 'writable', 'env']);
const RUNTIME_FIXED = { contract: 1, port: 3000, health: '/api/health', volumes: ['/data/uploads'], writable: ['/tmp', '/data/uploads'] };
const RUNTIME_ENV_KEYS = new Set(['name', 'required', 'secret', 'min_length', 'note']);
const RUNTIME_REQUIRED = ['always', 'first_start', 'no'];
// Set by the image, Node, Next.js or the platform; never by whoever installs the app.
export const PLATFORM_ENV = new Set(['NODE_ENV', 'NEXT_RUNTIME', 'NEXT_PHASE', 'NEXT_TELEMETRY_DISABLED', 'PORT', 'HOSTNAME', 'UPLOAD_DIR', 'CRN_SCHEDULER', 'CRN_FEEDBACK_INGEST_URL', 'CI', 'PATH', 'HOME']);
export function validateRuntime(runtime, used = [], firstMigration = '') {
  if (!runtime || typeof runtime !== 'object' || Array.isArray(runtime)) throw new Error('crn.runtime.json must be an object');
  for (const k of Object.keys(runtime)) if (!RUNTIME_KEYS.has(k)) throw new Error(`crn.runtime.json has unknown key "${k}"`);
  for (const [k, v] of Object.entries(RUNTIME_FIXED)) {
    if (runtime[k] !== undefined && JSON.stringify(runtime[k]) !== JSON.stringify(v)) throw new Error(`crn.runtime.json ${k} must be ${JSON.stringify(v)} (CRN writes it)`);
  }
  if (runtime.migrations_first !== undefined && runtime.migrations_first !== firstMigration) throw new Error(`crn.runtime.json migrations_first must be ${JSON.stringify(firstMigration)}, the first directory in prisma/migrations`);
  if (!Array.isArray(runtime.env)) throw new Error('crn.runtime.json needs an "env" array');
  const declared = new Set();
  for (const e of runtime.env) {
    const label = JSON.stringify(e);
    if (!e || typeof e !== 'object' || Array.isArray(e)) throw new Error(`env entry is not an object: ${label}`);
    for (const k of Object.keys(e)) if (!RUNTIME_ENV_KEYS.has(k)) throw new Error(`env entry has unknown key "${k}": ${label}`);
    if (typeof e.name !== 'string' || !/^[A-Z][A-Z0-9_]*$/.test(e.name)) throw new Error(`env name must be upper-case letters, digits and _: ${label}`);
    if (PLATFORM_ENV.has(e.name)) throw new Error(`${e.name} is set by the image or the platform; do not declare it`);
    if (declared.has(e.name)) throw new Error(`env ${e.name} is declared twice`);
    if (!RUNTIME_REQUIRED.includes(e.required)) throw new Error(`env required must be one of ${RUNTIME_REQUIRED.join(', ')}: ${label}`);
    if (e.secret !== undefined && typeof e.secret !== 'boolean') throw new Error(`env secret must be true or false: ${label}`);
    if (e.min_length !== undefined && !(Number.isInteger(e.min_length) && e.min_length > 0)) throw new Error(`env min_length must be a positive integer: ${label}`);
    if (e.note !== undefined && typeof e.note !== 'string') throw new Error(`env note must be text: ${label}`);
    declared.add(e.name);
  }
  const missing = [...new Set(used)].filter(n => !PLATFORM_ENV.has(n) && !declared.has(n)).sort();
  if (missing.length) throw new Error(`crn.runtime.json does not declare ${missing.join(', ')}, which the app reads`);
  return runtime;
}
// The variables app code reads: process.env.X, process.env['X'], and env.X where env
// defaults to process.env (assets/bootstrap-once.mjs, assets/scheduler.mjs). Tests,
// dependencies, build output and generated clients are not the app's runtime.
const SCAN_SKIP_DIRS = new Set(['node_modules', '.next', '.git', '.claude', 'tests', 'test', 'e2e', '__tests__', 'coverage', 'playwright-report', 'test-results', 'generated', 'public']);
const SCAN_SOURCE = /\.[cm]?[jt]sx?$/;
const SCAN_TEST_FILE = /\.(?:test|spec)\.[cm]?[jt]sx?$|^(?:playwright|vitest)\.config\./;
const ENV_READ = /\benv(?:\.([A-Z][A-Z0-9_]*)|\[\s*['"]([A-Z][A-Z0-9_]*)['"]\s*\])/g;
export async function appEnvNames(cwd) {
  const names = new Set();
  async function walk(dir) {
    for (const entry of await fs.readdir(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) { if (!SCAN_SKIP_DIRS.has(entry.name)) await walk(path.join(dir, entry.name)); continue; }
      if (!entry.isFile() || !SCAN_SOURCE.test(entry.name) || SCAN_TEST_FILE.test(entry.name)) continue;
      for (const m of (await fs.readFile(path.join(dir, entry.name), 'utf8')).matchAll(ENV_READ)) names.add(m[1] ?? m[2]);
    }
  }
  await walk(cwd);
  return [...names].sort();
}
async function firstMigration(cwd) {
  const entries = await fs.readdir(path.join(cwd, 'prisma', 'migrations'), { withFileTypes: true }).catch(e => { if (e.code === 'ENOENT') return []; throw e; });
  return entries.filter(e => e.isDirectory()).map(e => e.name).sort()[0] ?? '';
}
// External PostgreSQL over TLS only (crn-delivery.json "database_mode": "external-tls-only",
// references/delivery-checks.md). The app never talks to a database without TLS:
// node-postgres (runtime: db:check, seed: db:seed) verifies the server with the CA file in
// DATABASE_CA_FILE and its IP/name; Prisma's schema engine (migrate: db:deploy) uses
// sslaccept=strict&sslcert=<CA file>. The checker runs every step against PostgreSQL 17.11
// stand-ins it creates with a throwaway CA — never a plaintext database, never a real one.
export const TLS_SCHEMA = 'crn_verify';
export const TLS_ROLE = 'crn_app';
export const TLS_SCRIPTS = { migrate: 'db:deploy', seed: 'db:seed', runtime: 'db:check' };
// The stand-ins' image, pinned so every check runs on the same bytes; the version is checked again on each running stand-in.
export const TLS_PG_IMAGE = 'postgres:17.11-alpine@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b';
export const isWantedPostgres = version => /^17\.11(?:\s|$)/.test(version);
// The CA file must be a regular file, readable, holding exactly one PEM certificate. Each
// way it can be wrong, and which file the checker mounts for it (null: none).
export const BAD_CA_FILES = { 'no CA file': null, 'CA file is a symlink': 'symlink', 'CA file is empty': 'empty', 'CA file holds two certificates': 'two', 'CA file is unreadable': 'unreadable' };
// A file bind mount follows a symlink, and Docker cannot bind a file it may not read: those
// two reach the container inside a mounted directory that holds them under the CA file's name.
export function caFileMount(ca, files, target) {
  if (ca === null) return [];
  if (ca === 'symlink' || ca === 'unreadable') return ['-v', `${files[ca]}:${path.dirname(target)}:ro`];
  return ['-v', `${files[ca]}:${target}:ro`];
}
// Run inside the image, with the same mount a case gets: what the app will really find at the CA path.
export const CA_PROBE_JS = `const fs=require('fs');const f=process.env.CRN_CA_PROBE;const o={exists:false,symlink:false,readable:false,certs:0,size:0};try{o.symlink=fs.lstatSync(f).isSymbolicLink();o.exists=true}catch(e){o.exists=e.code!=='ENOENT'}if(o.exists){try{o.size=fs.statSync(f).size}catch{}try{const t=fs.readFileSync(f,'utf8');o.readable=true;o.certs=(t.match(/-----BEGIN CERTIFICATE-----/g)||[]).length}catch{}}console.log(JSON.stringify(o))`;
// A case proves nothing unless the file has its one defect: a lost symlink would pass as "no CA file".
export function caShapeProblem(shape, p) {
  const shapes = {
    symlink: p.exists && p.symlink && p.readable && p.certs === 1,
    empty: p.exists && !p.symlink && p.readable && p.size === 0,
    two: p.exists && !p.symlink && p.readable && p.certs === 2,
    unreadable: p.exists && !p.symlink && !p.readable,
  };
  return (shape === null ? !p.exists : shapes[shape]) ? null : `wanted ${shape ?? 'no file'}, found ${JSON.stringify(p)}`;
}
export function tlsUrls({ user, password, host, port = 5432, database, schema = TLS_SCHEMA, caFile }) {
  const base = `postgresql://${user}:${password}@${host}:${port}/${database}?schema=${schema}`;
  return { runtime: `${base}&sslmode=verify-full`, migration: `${base}&sslmode=require&sslaccept=strict&sslcert=${caFile}` };
}
// Every case that must FAIL. tls: the server side is wrong (bad CA, name, expiry, no TLS);
// gate: the configuration is wrong, so the app must stop before it opens any connection —
// those point at a tripwire that records every connection it receives.
export function tlsFailurePlan({ ips, user, password, database, caFile }) {
  const at = (host, extra = {}) => ({ ...tlsUrls({ user, password, host, database, caFile }), ...extra });
  const env = u => ({ DATABASE_URL: u.runtime, MIGRATION_DATABASE_URL: u.migration, DATABASE_CA_FILE: caFile });
  const plan = [];
  const each = (name, urls, ca, paths = Object.keys(TLS_SCRIPTS), noConnection = false, override = {}) => {
    for (const path of paths) plan.push({ name, path, script: TLS_SCRIPTS[path], env: { ...env(urls), ...override }, ca, noConnection });
  };
  each('wrong CA', at(ips.ok), 'wrong');
  // A wrong CA file stops every path before it opens a connection, migrate included.
  for (const [name, ca] of Object.entries(BAD_CA_FILES)) each(name, at(ips.tripwire), ca, Object.keys(TLS_SCRIPTS), true);
  each('IP not in SAN', at(ips.otherName), 'right');
  each('expired certificate', at(ips.expired), 'right');
  each('server without TLS', at(ips.plain), 'right');
  const t = at(ips.tripwire);
  const swap = (url, from, to) => to === '' ? url.replace(from, '') : url.replace(from, to);
  each('MIGRATION_DATABASE_URL missing', t, 'right', ['migrate'], true, { MIGRATION_DATABASE_URL: undefined });
  for (const [label, to] of [['sslmode missing', ''], ['sslmode=prefer', '&sslmode=prefer'], ['sslmode=disable', '&sslmode=disable'], ['sslmode=no-verify', '&sslmode=no-verify']]) {
    each(`DATABASE_URL ${label}`, t, 'right', ['runtime', 'seed'], true, { DATABASE_URL: swap(t.runtime, '&sslmode=verify-full', to) });
  }
  for (const [label, from, to] of [['sslaccept=accept_invalid_certs', 'sslaccept=strict', 'sslaccept=accept_invalid_certs'], ['sslaccept missing', '&sslaccept=strict', ''], ['sslcert missing', `&sslcert=${caFile}`, '']]) {
    each(`MIGRATION_DATABASE_URL ${label}`, t, 'right', ['migrate'], true, { MIGRATION_DATABASE_URL: swap(t.migration, from, to) });
  }
  for (const schema of ['Film-Job', 'a;drop', 'public']) {
    const bad = at(ips.tripwire);
    each(`schema ${JSON.stringify(schema)}`, { runtime: bad.runtime.replace(`schema=${TLS_SCHEMA}`, `schema=${encodeURIComponent(schema)}`), migration: bad.migration.replace(`schema=${TLS_SCHEMA}`, `schema=${encodeURIComponent(schema)}`) }, 'right', Object.keys(TLS_SCRIPTS), true);
  }
  // One field differs at a time. Only DATABASE_URL moves: the URL migrate would use stays
  // on the tripwire, so an app that fails to compare that field is seen connecting.
  const differs = { host: { host: ips.otherName }, port: { port: 5433 }, database: { database: `${database}_other` }, user: { user: `${user}_other` }, schema: { schema: `${TLS_SCHEMA}_other` } };
  for (const [field, change] of Object.entries(differs)) {
    each(`runtime and migration targets differ: ${field}`, t, 'right', ['migrate'], true, { DATABASE_URL: tlsUrls({ user, password, host: ips.tripwire, database, caFile, ...change }).runtime });
  }
  return plan;
}
// What a failure may print: never a credential, a connection URL or its query string.
export function leaks(output, secrets = []) {
  const found = [];
  for (const secret of secrets) if (secret && output.includes(secret)) found.push('a secret value');
  if (/postgres(?:ql)?:\/\//i.test(output)) found.push('a connection URL');
  if (/[?&](?:sslmode|sslcert|sslaccept|schema)=/.test(output)) found.push('a URL query string');
  if (/[A-Za-z0-9_.-]+:[^\s@/]+@[A-Za-z0-9_.-]+:\d+/.test(output)) found.push('user:password@host');
  return [...new Set(found)];
}
export function sanitizedTail(output, secrets = [], length = 300) {
  let text = output;
  for (const secret of secrets) if (secret) text = text.split(secret).join('[redacted]');
  return text.replace(/postgres(?:ql)?:\/\/\S+/gi, '[url]').replace(/\s+/g, ' ').trim().slice(-length);
}
// A listener on 5432 that counts the connections it receives (run under node in the image).
export const TRIPWIRE = `let n=0;require('net').createServer(s=>{n++;console.log('CONNECT '+n);s.destroy()}).listen(5432);`;
export function isolatedEnvironment(source) {
  // CLI/browser runtime settings only. Never inherit host application credentials.
  const allowed = ['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_CONFIG', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH', 'PLAYWRIGHT_BROWSERS_PATH'];
  return Object.fromEntries(allowed.filter(key => source[key] !== undefined).map(key => [key, source[key]]));
}
// Run inside the customer image (WORKDIR /app): the database packages it really ships,
// and the SHA-256 of every migration file it carries (Prisma's checksum).
const IMAGE_VERSIONS_JS = `const p=require('path');const v={node:process.version};for(const n of ['pg','pg-connection-string','@prisma/adapter-pg','@prisma/client','prisma','@prisma/engines']){try{v[n]=require(p.join(process.cwd(),'node_modules',n,'package.json')).version}catch{v[n]=null}}console.log(JSON.stringify(v))`;
const IMAGE_MIGRATIONS_JS = `const fs=require('fs'),c=require('crypto');const d='prisma/migrations';const o={};for(const n of fs.existsSync(d)?fs.readdirSync(d).sort():[]){const f=d+'/'+n+'/migration.sql';if(fs.existsSync(f))o[n]=c.createHash('sha256').update(fs.readFileSync(f)).digest('hex')}console.log(JSON.stringify(o))`;
// Every object the app owns lives in its schema, owned by its role; nothing in public.
const ISOLATION_SQL = `SELECT json_build_object(
 'public_objects', (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public')
   + (SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='public' AND t.typrelid=0 AND t.typelem=0)
   + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public'),
 'journal_in_schema', to_regclass('${TLS_SCHEMA}._prisma_migrations') IS NOT NULL,
 'schema_owner', (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='${TLS_SCHEMA}'),
 'objects_not_owned_by_app', (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='${TLS_SCHEMA}' AND pg_get_userbyid(c.relowner)<>'${TLS_ROLE}')
   + (SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname='${TLS_SCHEMA}' AND t.typrelid=0 AND t.typelem=0 AND pg_get_userbyid(t.typowner)<>'${TLS_ROLE}')
   + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='${TLS_SCHEMA}' AND pg_get_userbyid(p.proowner)<>'${TLS_ROLE}'),
 'objects_by_kind', (SELECT coalesce(json_object_agg(k, v), '{}'::json) FROM (SELECT c.relkind::text k, count(*) v FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='${TLS_SCHEMA}' GROUP BY 1) s),
 'journal', (SELECT json_agg(json_build_object('name', migration_name, 'checksum', checksum, 'finished', finished_at IS NOT NULL, 'rolled_back', rolled_back_at IS NOT NULL) ORDER BY migration_name) FROM ${TLS_SCHEMA}._prisma_migrations)
)`;
// An image given to --image is named by its registry digest, so the check runs on
// exactly the image that will be deployed; a tag can move.
export function requireDigestRef(ref) {
  if (typeof ref !== 'string' || !/^[a-z0-9][a-z0-9._:/-]*@sha256:[0-9a-f]{64}$/.test(ref) || /:[^/@]*@/.test(ref.slice(ref.lastIndexOf('/') + 1))) throw new Error('--image must name the image by digest: <repository>@sha256:<64 hex>, not a tag');
  return ref;
}
// options.image: run the whole check against an image that already exists (a candidate
// pushed by CRN) instead of building one, so acceptance runs on the real image.
export async function verify(cwd = process.cwd(), dockerfile = fileURLToPath(new URL('../assets/Dockerfile', import.meta.url)), options = {}) {
  const receipt = { schema_version: 1, status: 'failed', checks: [], started_at: new Date().toISOString() };
  if (options.image) receipt.image = options.image;
  const children = new Set();
  const containers = new Set();
  let stopped = false, networkName, imageName, scratch;
  const stop = () => { stopped = true; for (const child of children) { try { process.kill(-child.pid, 'SIGKILL'); } catch {} } };
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
  let timeout = setTimeout(stop, 15 * 60_000);
  // Do not forward runtime customer credentials to test setup. DB URL and bootstrap
  // values are always isolated below. Other integration tests must use mocks.
  const env = { ...isolatedEnvironment(process.env), CI: '1', NEXT_TELEMETRY_DISABLED: '1', DATABASE_URL: 'postgresql://build:build@127.0.0.1:1/build', AUTH_SECRET: randomBytes(32).toString('hex'), BOOTSTRAP_ADMIN_EMAIL: 'initial@example.test', BOOTSTRAP_ADMIN_PASSWORD: randomBytes(24).toString('hex'), FITT_FEEDBACK_URL: '' };
  env.CRN_TEST_ADMIN_EMAIL = env.BOOTSTRAP_ADMIN_EMAIL;
  env.CRN_TEST_ADMIN_PASSWORD = env.BOOTSTRAP_ADMIN_PASSWORD;
  const secrets = new Set([env.AUTH_SECRET, env.BOOTSTRAP_ADMIN_PASSWORD]);
  const redact = value => { for (const secret of secrets) value = value.split(secret).join('[redacted]'); return value.replace(/postgres(?:ql)?:\/\/\S+/gi, '[url]'); };
  const childEnv = extra => Object.fromEntries(Object.entries({ ...env, ...extra }).filter(([, v]) => v !== undefined));
  function start(command, args, extra = {}, input) {
    if (stopped) throw new Error('Verification cancelled or timed out');
    const child = spawn(command, args, { cwd, env: childEnv(extra), detached: true, stdio: [input === undefined ? 'ignore' : 'pipe', 'pipe', 'pipe'] });
    children.add(child); child.on('close', () => children.delete(child));
    if (input !== undefined) child.stdin.end(input);
    return child;
  }
  // attempt runs a command and reports how it ended; it never fails the check itself.
  async function attempt(command, args, extra = {}, { input, limitMs = 0, container } = {}) {
    const child = start(command, args, extra, input);
    let stdout = '', stderr = '';
    child.stdout.on('data', b => { stdout = (stdout + b).slice(-12_000_000); });
    child.stderr.on('data', b => { stderr = (stderr + b).slice(-12000); });
    let timer;
    if (limitMs) timer = setTimeout(() => { try { process.kill(-child.pid, 'SIGKILL'); } catch {} if (container) spawn('docker', ['rm', '-f', container], { env: isolatedEnvironment(process.env), stdio: 'ignore' }); }, limitMs);
    const code = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', resolve); });
    clearTimeout(timer);
    return { code, stdout, stderr, output: stderr + '\n' + stdout };
  }
  async function run(label, command, args, extra = {}, input) {
    console.error(`CRN verify: ${label}`);
    const { code, stdout, stderr } = await attempt(command, args, extra, { input });
    receipt.checks.push({ name: label, passed: code === 0 });
    if (code !== 0) {
      let detail = (stderr + '\n' + stdout).slice(-3500);
      for (const [key, value] of Object.entries(env)) if (/SECRET|TOKEN|PASSWORD|KEY|DATABASE_URL|DATABASE_CA_FILE/.test(key) && value) detail = detail.split(value).join('[redacted]');
      throw new Error(`${label} failed (exit ${code}): ${redact(detail)}`);
    }
    return stdout;
  }
  const check = (name, passed, problem) => { receipt.checks.push({ name, passed }); if (!passed) throw new Error(problem); };
  const npm = (label, script, args = []) => run(label, 'npm', ['--silent', 'run', script, '--', ...args]);
  const waitReady = async (label, container, user) => {
    for (let i = 0; i < 60 && !stopped; i++) {
      const r = await attempt('docker', ['exec', container, 'pg_isready', '-U', user]);
      if (r.code === 0) { receipt.checks.push({ name: label, passed: true }); return; }
      await new Promise(r => setTimeout(r, 500));
    }
    check(label, false, `${label}: the database did not start`);
  };
  try {
    await fs.rm(path.join(cwd, 'CRN_VERIFICATION.json'), { force: true });
    receipt.node_version = process.versions.node;
    if (![22, 24].includes(Number(process.versions.node.split('.')[0]))) throw new Error('Delivery verification requires Node 22 or 24 LTS; select it with CRN_VERIFY_NODE or PATH');
    const read = async f => JSON.parse(await fs.readFile(path.join(cwd, f), 'utf8'));
    const contract = await read('crn-delivery.json'), pkg = await read('package.json'), lock = await read('package-lock.json');
    const docs = {};
    for (const file of ['BUILD_NOTES.md', 'TEST_CASES.md', 'PORT_CHECKLIST.md']) docs[file] = await fs.readFile(path.join(cwd, file), 'utf8');
    validateContract(contract, pkg, lock, docs);
    receipt.checks.push({ name: 'delivery contract', passed: true });
    const tlsMode = contract.database_mode === 'external-tls-only';
    receipt.database_mode = contract.database ? (contract.database_mode ?? 'local') : 'none';
    if (tlsMode) {
      // Every step talks TLS, the build included: it gets targets of the right shape on a
      // port nothing listens on, never a plaintext URL.
      clearTimeout(timeout); timeout = setTimeout(stop, 30 * 60_000);
      Object.assign(env, { DATABASE_URL: `postgresql://build:build@127.0.0.1:1/build?schema=${TLS_SCHEMA}&sslmode=verify-full`, MIGRATION_DATABASE_URL: `postgresql://build:build@127.0.0.1:1/build?schema=${TLS_SCHEMA}&sslmode=require&sslaccept=strict&sslcert=${contract.database_ca_file}`, DATABASE_CA_FILE: contract.database_ca_file });
    }
    const schedule = await fs.readFile(path.join(cwd, 'crn.schedule.json'), 'utf8').catch(e => { if (e.code === 'ENOENT') return '[]'; throw e; });
    validateSchedule(JSON.parse(schedule));
    receipt.checks.push({ name: 'schedule', passed: true });
    const runtime = await read('crn.runtime.json').catch(e => { if (e.code === 'ENOENT') throw new Error('Missing crn.runtime.json: declare the environment the app reads (references/runs-anywhere.md)'); throw e; });
    validateRuntime(runtime, await appEnvNames(cwd), await firstMigration(cwd));
    receipt.checks.push({ name: 'runtime contract', passed: true });
    receipt.lockfile_sha256 = createHash('sha256').update(await fs.readFile(path.join(cwd, 'package-lock.json'))).digest('hex');
    scratch = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-verify-'));
    await run('locked install', 'npm', ['ci', '--no-audit', '--no-fund']);
    if (contract.database) await npm('generate client', 'db:generate');
    await npm('typecheck', 'typecheck');
    const unitFile = path.join(scratch, 'unit.json');
    await npm('unit tests', 'test:unit', ['--reporter=json', `--outputFile=${unitFile}`]);
    receipt.unit_tests = assertTestResults('unit', JSON.parse(await fs.readFile(unitFile, 'utf8')));
    const suffix = randomBytes(8).toString('hex');
    if (options.image) {
      imageName = requireDigestRef(options.image);
      await run('customer image present', 'docker', ['image', 'inspect', '--format', '{{.Id}}', imageName]);
    } else {
      await npm('production build without DB', 'build');
      await fs.access(path.join(cwd, '.next/standalone/server.js'));
      imageName = `crn-verify:${suffix}`;
      const runtimeDockerfile = path.join(scratch, 'Dockerfile');
      await fs.copyFile(dockerfile, runtimeDockerfile);
      await fs.writeFile(`${runtimeDockerfile}.dockerignore`, 'node_modules\n.next\n.git\n.env\n.env.*\n.claude\n');
      await run('build customer runtime image', 'docker', ['build', '--tag', imageName, '--file', runtimeDockerfile, cwd]);
    }
    networkName = `crn-verify-${suffix}`;
    const operationName = `crn-verify-task-${suffix}`;
    containers.add(operationName);
    // containerEnv: the values the docker CLI hands to each "-e NAME" in runtimeEnv.
    let containerEnv = { DATABASE_URL: '' };
    let runtimeEnv = ['-e', 'DATABASE_URL', '-e', 'AUTH_SECRET', '-e', 'BOOTSTRAP_ADMIN_EMAIL', '-e', 'BOOTSTRAP_ADMIN_PASSWORD'];
    const inImage = (label, script) => run(label, 'docker', ['run', '--rm', '--name', operationName, '--network', networkName, ...HARDENED_RUN, ...runtimeEnv, '--entrypoint', 'npm', imageName, '--silent', 'run', script], containerEnv);
    const bootstrapTwice = async () => {
      const outcomes = [await inImage('first-install bootstrap in customer image', 'db:seed')];
      env.BOOTSTRAP_ADMIN_EMAIL = 'changed@example.test'; env.BOOTSTRAP_ADMIN_PASSWORD = randomBytes(24).toString('hex');
      secrets.add(env.BOOTSTRAP_ADMIN_PASSWORD);
      outcomes.push(await inImage('repeat bootstrap with changed inputs', 'db:seed'));
      delete env.BOOTSTRAP_ADMIN_EMAIL; delete env.BOOTSTRAP_ADMIN_PASSWORD;
      outcomes.push(await inImage('restart without bootstrap inputs', 'db:seed'));
      return outcomes;
    };
    if (tlsMode) {
      // --- PostgreSQL 17 stand-ins on a network of their own, one throwaway CA ---
      const tlsDir = path.join(scratch, 'tls');
      const openssl = await attempt('openssl', ['version']);
      const [major, minor] = (openssl.stdout.match(/OpenSSL (\d+)\.(\d+)/) ?? []).slice(1).map(Number);
      check('OpenSSL 3.4+ for test certificates', openssl.code === 0 && (major > 3 || (major === 3 && minor >= 4)), 'TLS stand-ins need OpenSSL 3.4 or newer on the build machine');
      let prefix;
      for (let i = 0; i < 8 && !prefix; i++) {
        const candidate = `10.213.${randomBytes(1)[0]}`;
        if ((await attempt('docker', ['network', 'create', '--subnet', `${candidate}.0/24`, networkName])).code === 0) prefix = candidate;
      }
      check('isolated runtime network', !!prefix, 'Could not create a test network for the TLS stand-ins');
      const ips = { ok: `${prefix}.10`, otherName: `${prefix}.11`, expired: `${prefix}.12`, plain: `${prefix}.13`, tripwire: `${prefix}.14` };
      const cnf = path.join(tlsDir, 'min.cnf');
      await fs.mkdir(tlsDir, { recursive: true });
      await fs.writeFile(cnf, '[req]\ndistinguished_name=dn\n[dn]\n');
      const ossl = async args => { const r = await attempt('openssl', args, {}, { limitMs: 30_000 }); if (r.code !== 0) throw new Error(`test certificate failed: ${sanitizedTail(r.output, [...secrets])}`); };
      const key = ['-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes'];
      for (const d of ['ok', 'wrong', 'otherName', 'expired', 'ca', 'wrongca']) await fs.mkdir(path.join(tlsDir, d));
      // The production shape: one self-signed certificate is both the server's and the CA.
      await ossl(['req', '-x509', '-config', cnf, ...key, '-keyout', path.join(tlsDir, 'ok/server.key'), '-out', path.join(tlsDir, 'ok/server.crt'), '-subj', '/CN=crn-verify-db', '-days', '2', '-addext', `subjectAltName=IP:${ips.ok},IP:127.0.0.1`, '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,digitalSignature,keyCertSign']);
      await ossl(['req', '-x509', '-config', cnf, ...key, '-keyout', path.join(tlsDir, 'wrong/server.key'), '-out', path.join(tlsDir, 'wrong/server.crt'), '-subj', '/CN=crn-verify-db', '-days', '2', '-addext', `subjectAltName=IP:${ips.ok},IP:127.0.0.1`, '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,digitalSignature,keyCertSign']);
      const leaf = async (dir, san, validity) => {
        await ossl(['req', '-new', '-config', cnf, ...key, '-keyout', path.join(tlsDir, dir, 'server.key'), '-out', path.join(tlsDir, dir, 'server.csr'), '-subj', '/CN=crn-verify-db']);
        await fs.writeFile(path.join(tlsDir, dir, 'ext'), `subjectAltName=${san}\nbasicConstraints=CA:FALSE\nkeyUsage=digitalSignature\nextendedKeyUsage=serverAuth\n`);
        await ossl(['x509', '-req', '-in', path.join(tlsDir, dir, 'server.csr'), '-CA', path.join(tlsDir, 'ok/server.crt'), '-CAkey', path.join(tlsDir, 'ok/server.key'), '-set_serial', `0x${randomBytes(8).toString('hex')}`, ...validity, '-extfile', path.join(tlsDir, dir, 'ext'), '-out', path.join(tlsDir, dir, 'server.crt')]);
      };
      await leaf('otherName', 'IP:192.0.2.1', ['-days', '2']);
      await leaf('expired', `IP:${ips.expired}`, ['-not_before', '20250101000000Z', '-not_after', '20250201000000Z']);
      const caFile = path.join(tlsDir, 'ca/ca.pem'), wrongCaFile = path.join(tlsDir, 'wrongca/ca.pem');
      await fs.copyFile(path.join(tlsDir, 'ok/server.crt'), caFile);
      await fs.copyFile(path.join(tlsDir, 'wrong/server.crt'), wrongCaFile);
      const caPem = await fs.readFile(caFile, 'utf8');
      const caName = path.basename(contract.database_ca_file);
      const caFiles = { right: caFile, wrong: wrongCaFile, empty: path.join(tlsDir, 'ca/empty.pem'), two: path.join(tlsDir, 'ca/two.pem'), symlink: path.join(tlsDir, 'calink'), unreadable: path.join(tlsDir, 'canoread') };
      await fs.writeFile(caFiles.empty, '');
      await fs.writeFile(caFiles.two, caPem + await fs.readFile(wrongCaFile, 'utf8'));
      await fs.mkdir(caFiles.symlink);
      await fs.writeFile(path.join(caFiles.symlink, 'real.pem'), caPem);
      await fs.symlink('real.pem', path.join(caFiles.symlink, caName));
      await fs.mkdir(caFiles.unreadable);
      await fs.writeFile(path.join(caFiles.unreadable, caName), caPem, { mode: 0o000 });
      receipt.checks.push({ name: 'TLS test certificates', passed: true });
      const adminPw = randomBytes(24).toString('hex'), appPw = randomBytes(24).toString('hex'), matrixPw = randomBytes(24).toString('hex');
      for (const s of [adminPw, appPw, matrixPw]) secrets.add(s);
      const standIns = [
        { role: 'TLS server', ip: ips.ok, certs: 'ok', publish: true, san: [ips.ok, '127.0.0.1'] },
        { role: 'certificate without this IP in its SAN', ip: ips.otherName, certs: 'otherName', san: ['192.0.2.1'] },
        { role: 'expired certificate', ip: ips.expired, certs: 'expired', san: [ips.expired] },
        { role: 'no TLS', ip: ips.plain, certs: null, san: [] },
      ];
      for (const s of standIns) {
        s.name = `crn-verify-pg-${s.certs ?? 'plain'}-${suffix}`;
        containers.add(s.name);
        const args = ['run', '-d', '--name', s.name, '--network', networkName, '--ip', s.ip, '-e', 'POSTGRES_USER=crn_admin', '-e', 'POSTGRES_PASSWORD', '-e', 'POSTGRES_DB=postgres', ...(s.publish ? ['-p', '127.0.0.1::5432'] : [])];
        if (s.certs) args.push('-v', `${path.join(tlsDir, s.certs)}:/certs-ro:ro`, '--entrypoint', 'sh', TLS_PG_IMAGE, '-c', 'mkdir -p /tmp/certs && cp /certs-ro/server.crt /certs-ro/server.key /tmp/certs/ && chown postgres:postgres /tmp/certs/server.crt /tmp/certs/server.key && chmod 600 /tmp/certs/server.key && exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/certs/server.crt -c ssl_key_file=/tmp/certs/server.key');
        else args.push(TLS_PG_IMAGE);
        await run(`start PostgreSQL 17.11 stand-in (${s.role})`, 'docker', args, { POSTGRES_PASSWORD: adminPw });
      }
      for (const s of standIns) await waitReady(`stand-in ready (${s.role})`, s.name, 'crn_admin');
      const main = standIns[0].name;
      const tripwireName = `crn-verify-tripwire-${suffix}`;
      containers.add(tripwireName);
      await run('start connection tripwire', 'docker', ['run', '-d', '--name', tripwireName, '--network', networkName, '--ip', ips.tripwire, ...HARDENED_RUN, '--entrypoint', 'node', imageName, '-e', TRIPWIRE]);
      const tripwireCount = async () => ((await attempt('docker', ['logs', tripwireName])).stdout.match(/^CONNECT /gm) ?? []).length;
      // The app's own role: not a superuser, a connection limit like the customer's.
      await run('create application role and database on the stand-in', 'docker', ['exec', '-i', main, 'psql', '-U', 'crn_admin', '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-q'], {},
        `CREATE ROLE ${TLS_ROLE} LOGIN PASSWORD '${appPw}' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT 20;\nCREATE DATABASE ${TLS_SCHEMA} OWNER ${TLS_ROLE};\n`);
      for (const s of standIns) {
        s.version = (await run(`stand-in server version (${s.role})`, 'docker', ['exec', s.name, 'psql', '-U', 'crn_admin', '-d', 'postgres', '-Atc', 'show server_version'])).trim();
        check(`stand-in is PostgreSQL 17.11 (${s.role})`, isWantedPostgres(s.version), `The stand-in (${s.role}) runs PostgreSQL ${s.version}, not 17.11`);
      }
      receipt.external_tls = {
        attestation: 'Every database this check used is a container it created on its own network, with a CA it generated; no other host, credential or CA was given to the app.',
        stand_ins: standIns.map(s => ({ role: s.role, ip: s.ip, tls: !!s.certs, san: s.san, image: TLS_PG_IMAGE, server_version: s.version })),
        tripwire: { ip: ips.tripwire, purpose: 'counts connections; configuration errors must leave it at zero' },
        schema: TLS_SCHEMA, app_role: TLS_ROLE, ca_mount: contract.database_ca_file,
      };
      const port = (await run('database port', 'docker', ['port', main, '5432/tcp'])).trim().split(':').pop();
      const hostUrls = tlsUrls({ user: TLS_ROLE, password: appPw, host: '127.0.0.1', port, database: TLS_SCHEMA, caFile });
      Object.assign(env, { DATABASE_URL: hostUrls.runtime, MIGRATION_DATABASE_URL: hostUrls.migration, DATABASE_CA_FILE: caFile });
      const urls = tlsUrls({ user: TLS_ROLE, password: appPw, host: ips.ok, database: TLS_SCHEMA, caFile: contract.database_ca_file });
      containerEnv = { DATABASE_URL: urls.runtime, MIGRATION_DATABASE_URL: urls.migration, DATABASE_CA_FILE: contract.database_ca_file };
      runtimeEnv = ['-e', 'DATABASE_URL', '-e', 'MIGRATION_DATABASE_URL', '-e', 'DATABASE_CA_FILE', '-v', `${caFile}:${contract.database_ca_file}:ro`, '-e', 'AUTH_SECRET', '-e', 'BOOTSTRAP_ADMIN_EMAIL', '-e', 'BOOTSTRAP_ADMIN_PASSWORD'];
      receipt.external_tls.versions = JSON.parse((await run('database packages in the image', 'docker', ['run', '--rm', ...HARDENED_RUN, '--entrypoint', 'node', imageName, '-e', IMAGE_VERSIONS_JS])).trim().split('\n').pop());
      // --- the paths that must work: migrate, seed, runtime ---
      await inImage('apply migrations in customer image (TLS)', 'db:deploy');
      const outcomes = await bootstrapTwice();
      check('seed reports already-complete once bootstrapped', outcomes.slice(1).every(o => o.includes('already-complete')), 'A repeated seed must print already-complete (contract §5)');
      const who = JSON.parse((await inImage('runtime connection check (db:check, TLS)', 'db:check')).trim().split('\n').pop());
      receipt.external_tls.runtime_identity = who;
      check('runtime connects as the app role in its schema', who.current_user === TLS_ROLE && who.current_schema === TLS_SCHEMA, `db:check reported user ${who.current_user} and schema ${who.current_schema}, want ${TLS_ROLE} and ${TLS_SCHEMA}`);
      const isolation = JSON.parse((await run('schema isolation query', 'docker', ['exec', main, 'psql', '-U', 'crn_admin', '-d', TLS_SCHEMA, '-Atc', ISOLATION_SQL])).trim());
      receipt.external_tls.isolation = isolation;
      check('schema isolation', isolation.public_objects === 0 && isolation.journal_in_schema && isolation.schema_owner === TLS_ROLE && isolation.objects_not_owned_by_app === 0, `Schema isolation: ${JSON.stringify({ public_objects: isolation.public_objects, journal_in_schema: isolation.journal_in_schema, schema_owner: isolation.schema_owner, objects_not_owned_by_app: isolation.objects_not_owned_by_app })}`);
      const files = JSON.parse((await run('migration files in the image', 'docker', ['run', '--rm', ...HARDENED_RUN, '--entrypoint', 'node', imageName, '-e', IMAGE_MIGRATIONS_JS])).trim().split('\n').pop());
      const journal = isolation.journal ?? [];
      receipt.external_tls.migrations = { files, journal };
      check('migration journal matches the image', JSON.stringify(journal.map(j => j.name)) === JSON.stringify(Object.keys(files)) && journal.every(j => j.finished && !j.rolled_back && j.checksum === files[j.name]), 'The migration journal must hold exactly the image\'s migrations, finished, with their file checksums');
      // --- each bad CA file really has its one defect, as the image sees it ---
      receipt.external_tls.ca_shapes = {};
      for (const [name, shape] of Object.entries(BAD_CA_FILES)) {
        const found = JSON.parse((await run(`look at the CA file (${name})`, 'docker', ['run', '--rm', '--network', 'none', ...HARDENED_RUN, '-e', 'CRN_CA_PROBE', ...caFileMount(shape, caFiles, contract.database_ca_file), '--entrypoint', 'node', imageName, '-e', CA_PROBE_JS], { CRN_CA_PROBE: contract.database_ca_file })).trim().split('\n').pop());
        receipt.external_tls.ca_shapes[name] = found;
        const problem = caShapeProblem(shape, found);
        check(`CA file prepared as intended (${name})`, !problem, `The checker could not prepare "${name}": ${problem}`);
      }
      // --- the cases that must fail, each through migrate, seed and runtime ---
      const matrix = [];
      const plan = tlsFailurePlan({ ips, user: TLS_ROLE, password: appPw, database: TLS_SCHEMA, caFile: contract.database_ca_file });
      for (const [i, c] of plan.entries()) {
        if (stopped) throw new Error('Verification cancelled or timed out');
        const name = `${operationName}-m${i}`;
        containers.add(name);
        const before = c.noConnection ? await tripwireCount() : 0;
        const mount = caFileMount(c.ca, caFiles, contract.database_ca_file);
        console.error(`CRN verify: must fail — ${c.name} (${c.path})`);
        const r = await attempt('docker', ['run', '--rm', '--name', name, '--network', networkName, ...HARDENED_RUN, '-e', 'DATABASE_URL', '-e', 'MIGRATION_DATABASE_URL', '-e', 'DATABASE_CA_FILE', '-e', 'AUTH_SECRET', '-e', 'BOOTSTRAP_ADMIN_EMAIL', '-e', 'BOOTSTRAP_ADMIN_PASSWORD', ...mount, '--entrypoint', 'npm', imageName, '--silent', 'run', c.script],
          { ...c.env, BOOTSTRAP_ADMIN_EMAIL: 'matrix@example.test', BOOTSTRAP_ADMIN_PASSWORD: matrixPw }, { limitMs: 120_000, container: name });
        const connections = c.noConnection ? (await tripwireCount()) - before : null;
        const leaked = leaks(r.output, [...secrets]);
        const ok = r.code !== 0 && (!c.noConnection || connections === 0) && leaked.length === 0;
        matrix.push({ case: c.name, path: c.path, expected: 'fail', result: r.code === 0 ? 'passed' : 'failed', exit: r.code, connections, leaks: leaked, ok, error: sanitizedTail(r.output, [...secrets]) });
      }
      receipt.external_tls.must_fail = matrix;
      const wrong = matrix.filter(m => !m.ok);
      check('TLS and configuration failure matrix (runtime, seed, migrate)', wrong.length === 0, `Cases that did not fail as required: ${wrong.map(m => `${m.case} (${m.path}): ${m.result === 'passed' ? 'succeeded' : m.leaks.length ? `printed ${m.leaks.join(', ')}` : `opened ${m.connections} connection(s)`}`).join('; ')}`);
    } else {
      await run('isolated runtime network', 'docker', ['network', 'create', networkName]);
      if (contract.database) {
        const dbName = `crn-verify-db-${suffix}`;
        containers.add(dbName);
        const pw = randomBytes(24).toString('hex');
        secrets.add(pw);
        await run('start isolated database', 'docker', ['run', '-d', '--name', dbName, '--network', networkName, '--network-alias', 'database', '-e', 'POSTGRES_USER=crn_test', '-e', 'POSTGRES_PASSWORD', '-e', 'POSTGRES_DB=crn_test', '-p', '127.0.0.1::5432', 'postgres:16-alpine'], { POSTGRES_PASSWORD: pw });
        const binding = (await run('database port', 'docker', ['port', dbName, '5432/tcp'])).trim().split(':').pop();
        env.DATABASE_URL = `postgresql://crn_test:${pw}@127.0.0.1:${binding}/crn_test?schema=public`;
        containerEnv = { DATABASE_URL: `postgresql://crn_test:${pw}@database:5432/crn_test?schema=public` };
        await waitReady('database readiness', dbName, 'crn_test');
        await inImage('apply migrations in customer image', 'db:deploy');
        await bootstrapTwice();
      }
    }
    await run('browser installation', 'node', ['node_modules/@playwright/test/cli.js', 'install', 'chromium']);
    const appName = `crn-verify-app-${suffix}`;
    containers.add(appName);
    await run('start customer image', 'docker', ['run', '-d', '--name', appName, '--network', networkName, '-p', '127.0.0.1::3000', ...HARDENED_RUN, ...runtimeEnv, imageName], containerEnv);
    const port = (await run('application port', 'docker', ['port', appName, '3000/tcp'])).trim().split(':').pop();
    env.CRN_BASE_URL = `http://127.0.0.1:${port}`;
    let ready = false;
    for (let i = 0; i < 60 && !stopped; i++) {
      try { const response = await fetch(env.CRN_BASE_URL, { redirect: 'manual', signal: AbortSignal.timeout(1000) }); if (response.status < 500) { ready = true; break; } } catch {}
      await new Promise(r => setTimeout(r, 500));
    }
    if (!ready) throw new Error(`Customer image did not become ready: ${redact((await run('startup logs', 'docker', ['logs', '--tail', '30', appName])).slice(-3500))}`);
    const browser = await npm('browser smoke tests', 'test:smoke', ['--reporter=json', '--workers=1', '--retries=0']);
    receipt.browser_tests = assertTestResults('browser', JSON.parse(browser));
    const statusName = `crn-verify-status-${suffix}`;
    containers.add(statusName);
    await run('start fake status server', 'docker', ['run', '-d', '--name', statusName, '--network', networkName, '--network-alias', 'fitt-status', '--entrypoint', 'node', imageName, '-e', FAKE_STATUS_SERVER]);
    for (const wanted of ['suspended', 'active']) {
      const switchName = `crn-verify-switch-${suffix}`;
      containers.add(switchName);
      await run(`start customer image while ${wanted}`, 'docker', ['run', '-d', '--name', switchName, '--network', networkName, '-p', '127.0.0.1::3000', ...HARDENED_RUN, ...runtimeEnv, '-e', `FITT_STATUS_URL=http://fitt-status:8080/${wanted}`, '-e', `FITT_LICENSE_KEY=${SWITCH_TEST_KEY}`, imageName], containerEnv);
      const switchBase = `http://127.0.0.1:${(await run('switch application port', 'docker', ['port', switchName, '3000/tcp'])).trim().split(':').pop()}`;
      let verdict = `The customer image did not answer while ${wanted}`;
      for (let i = 0; i < 60 && !stopped; i++) {
        try {
          const home = await fetch(switchBase, { redirect: 'manual', signal: AbortSignal.timeout(5000) });
          const homeBody = await home.text();
          const health = await fetch(`${switchBase}/api/health`, { redirect: 'manual', signal: AbortSignal.timeout(5000) });
          verdict = switchVerdict(wanted, { homeStatus: home.status, homeBody, healthStatus: health.status });
          if (!verdict) break;
        } catch {}
        await new Promise(r => setTimeout(r, 500));
      }
      receipt.checks.push({ name: `suspension switch while ${wanted}`, passed: !verdict });
      if (verdict) throw new Error(verdict);
      await run(`stop customer image while ${wanted}`, 'docker', ['rm', '-f', switchName]);
      containers.delete(switchName);
    }
    if (stopped) throw new Error('Verification cancelled or timed out');
    receipt.status = 'passed';
  } catch (error) { receipt.error = redact(error.message); }
  finally {
    stop(); clearTimeout(timeout); process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop);
    const cleanup = args => new Promise(resolve => { const c = spawn('docker', args, { env: isolatedEnvironment(process.env), stdio: 'ignore', timeout: 8000, killSignal: 'SIGKILL' }); c.on('error', resolve); c.on('close', resolve); });
    // -v: the PostgreSQL containers' anonymous data volumes go with them.
    if (containers.size) await cleanup(['rm', '-f', '-v', ...containers]);
    await Promise.all([networkName && cleanup(['network', 'rm', networkName]), imageName && !options.image && cleanup(['image', 'rm', '-f', imageName])]);
    if (scratch) await fs.rm(scratch, { recursive: true, force: true });
    receipt.finished_at = new Date().toISOString();
    await fs.writeFile(path.join(cwd, 'CRN_VERIFICATION.json'), JSON.stringify(receipt, null, 2) + '\n');
  }
  return receipt;
}
// node verify-delivery.mjs <Dockerfile> [--image <ref>]
if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  const args = process.argv.slice(2);
  const at = args.indexOf('--image');
  const image = at >= 0 ? args[at + 1] : undefined;
  if (at >= 0 && !image) throw new Error('--image needs an image reference');
  const dockerfile = (at >= 0 ? args.filter((_, i) => i !== at && i !== at + 1) : args)[0];
  const result = await verify(process.cwd(), dockerfile, { image });
  console.log(JSON.stringify(result));
  process.exitCode = result.status === 'passed' ? 0 : 1;
}
