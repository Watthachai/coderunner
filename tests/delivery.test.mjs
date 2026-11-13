import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { validateContract, assertTestResults, isolatedEnvironment, verify } from '../internal/buildstep/verify-delivery.mjs';
const docs = {'BUILD_NOTES.md':'Configured locally', 'TEST_CASES.md':'Verify persistence', 'PORT_CHECKLIST.md':'- [x] Required screen'};
const pkg = {scripts: Object.fromEntries(['typecheck','test:unit','test:smoke','build','start'].map(k => [k,k])), dependencies:{next:'16.2.9'}};
const lock = {lockfileVersion:3,packages:{'':{dependencies:{next:'16.2.9'}}}};
test('contract rejects missing DB operations, unresolved requirements, unpinned dependencies and stale lock',()=>{
  assert.doesNotThrow(()=>validateContract({schema_version:1,database:false},pkg,lock,docs));
  assert.throws(()=>validateContract({schema_version:1,database:true},pkg,lock,docs), /db:generate/);
  assert.throws(()=>validateContract({schema_version:1,database:false},pkg,lock,{...docs,'PORT_CHECKLIST.md':'- [ ] Login'}), /remain open/);
  assert.throws(()=>validateContract({schema_version:1,database:false},{...pkg,dependencies:{next:'^16.2.9'}},lock,docs), /exact/);
  assert.throws(()=>validateContract({schema_version:1,database:false},pkg,{...lock,packages:{'':{}}},docs), /Lockfile/);
});
test('zero tests, skipped tests and flaky retries cannot produce a release',()=>{
  const unit={success:true,numTotalTests:2,numPassedTests:2};
  assert.equal(assertTestResults('unit',unit),2);
  for(const patch of [{numTotalTests:0,numPassedTests:0},{numPendingTests:1},{numTodoTests:1},{success:false}]) assert.throws(()=>assertTestResults('unit',{...unit,...patch}));
  assert.equal(assertTestResults('browser',{stats:{expected:3}}),3);
  for(const stats of [{expected:0},{expected:3,skipped:1},{expected:3,flaky:1},{expected:3,unexpected:1}]) assert.throws(()=>assertTestResults('browser',{stats}));
});
test('host application secrets are excluded from child environment',()=>{
 assert.deepEqual(isolatedEnvironment({PATH:'/bin',HOME:'/test',DATABASE_URL:'customer',SMTP_PASSWORD:'mail',CUSTOMER_API_KEY:'key',AWS_ACCESS_KEY_ID:'id',NPM_TOKEN:'token'}), {PATH:'/bin',HOME:'/test'});
});
test('invalid input replaces a fabricated pass with an actual failed receipt',async()=>{
 const dir=await fs.mkdtemp(path.join(os.tmpdir(),'crn-negative-test-'));
 try {
  await fs.writeFile(path.join(dir,'CRN_VERIFICATION.json'),'{"status":"passed"}');
  const result=await verify(dir);
  assert.equal(result.status,'failed');
  assert.equal(JSON.parse(await fs.readFile(path.join(dir,'CRN_VERIFICATION.json'),'utf8')).status,'failed');
 } finally {await fs.rm(dir,{recursive:true,force:true});}
});

// The suspension switch check (FITT Billing): the customer image runs against a fake
// status server — itself, under node — and must obey it.
import { spawn as spawnChild } from 'node:child_process';
import { FAKE_STATUS_SERVER, SWITCH_TEST_KEY, SWITCH_TEST_MESSAGE, switchVerdict } from '../internal/buildstep/verify-delivery.mjs';
test('the fake status server answers like FITT Builder, and only to the test key', async () => {
  const port = String(20000 + Math.floor(Math.random() * 20000));
  // The customer image sets PORT=3000 for Next.js; the fake server must not take it.
  const child = spawnChild(process.execPath, ['-e', FAKE_STATUS_SERVER], { env: { PATH: process.env.PATH, PORT: '3000', CRN_FAKE_STATUS_PORT: port }, stdio: 'ignore' });
  try {
    const ask = async (p, key = SWITCH_TEST_KEY) => {
      for (let i = 0; i < 50; i++) {
        try { const r = await fetch(`http://127.0.0.1:${port}${p}`, { headers: { authorization: `Bearer ${key}` } }); return { status: r.status, body: await r.json() }; } catch { await new Promise(r => setTimeout(r, 100)); }
      }
      throw new Error('fake status server did not start');
    };
    const suspended = await ask('/suspended');
    assert.equal(suspended.status, 200);
    assert.equal(suspended.body.status, 'suspended');
    assert.equal(suspended.body.message, SWITCH_TEST_MESSAGE);
    assert.deepEqual((await ask('/active')).body.status, 'active');
    assert.equal((await ask('/suspended', 'fbl_wrong')).status, 401);
  } finally { child.kill('SIGKILL'); }
});
test('a release needs an app that obeys the switch', () => {
  const page = `<p>${SWITCH_TEST_MESSAGE}</p>`;
  assert.equal(switchVerdict('suspended', { homeStatus: 503, homeBody: page, healthStatus: 200 }), null);
  assert.equal(switchVerdict('suspended', { homeStatus: 503, homeBody: page, healthStatus: 404 }), null);
  assert.match(switchVerdict('suspended', { homeStatus: 200, homeBody: 'app', healthStatus: 200 }), /503/);
  assert.match(switchVerdict('suspended', { homeStatus: 503, homeBody: 'down', healthStatus: 200 }), /message/);
  assert.match(switchVerdict('suspended', { homeStatus: 503, homeBody: page, healthStatus: 503 }), /health/);
  assert.equal(switchVerdict('active', { homeStatus: 200, homeBody: 'app', healthStatus: 200 }), null);
  assert.equal(switchVerdict('active', { homeStatus: 307, homeBody: '', healthStatus: 200 }), null);
  assert.match(switchVerdict('active', { homeStatus: 503, homeBody: page, healthStatus: 200 }), /normally/);
});

test('the checker runs the customer image read-only with no capabilities', async () => {
  const { HARDENED_RUN } = await import('../internal/buildstep/verify-delivery.mjs');
  assert.deepEqual(HARDENED_RUN, ['--read-only', '--tmpfs', '/tmp', '--tmpfs', '/data/uploads:mode=1777', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges']);
  const src = await fs.readFile(new URL('../internal/buildstep/verify-delivery.mjs', import.meta.url), 'utf8');
  const runs = src.split('\n').filter(l => /'run', '(-d|--rm)'/.test(l) && l.includes('imageName') && !l.includes('FAKE_STATUS_SERVER'));
  assert.ok(runs.length >= 3, 'migrations, app and switch runs');
  for (const l of runs) assert.ok(l.includes('...HARDENED_RUN'), l.trim());
});

// crn.runtime.json (agreed with Fittcore 3 Nov 2026): the installer fills in the
// env the app declares, so every variable the app reads must be declared.
import { validateRuntime, appEnvNames, PLATFORM_ENV } from '../internal/buildstep/verify-delivery.mjs';
const ntcEnv = [
  { name: 'DATABASE_URL', required: 'always', secret: true },
  { name: 'AUTH_SECRET', required: 'always', secret: true, min_length: 32 },
  { name: 'BOOTSTRAP_ADMIN_EMAIL', required: 'first_start' },
  { name: 'BOOTSTRAP_ADMIN_PASSWORD', required: 'first_start', secret: true, min_length: 16 },
  { name: 'ALLOW_INSECURE_COOKIES', required: 'no', note: 'true when served over http' },
];
const ntcReads = ['DATABASE_URL', 'AUTH_SECRET', 'BOOTSTRAP_ADMIN_EMAIL', 'BOOTSTRAP_ADMIN_PASSWORD', 'ALLOW_INSECURE_COOKIES', 'NODE_ENV', 'UPLOAD_DIR', 'PORT'];
test('runtime file declares every variable the app reads', () => {
  assert.doesNotThrow(() => validateRuntime({ env: ntcEnv }, ntcReads, '20261102000000_init'));
  assert.throws(() => validateRuntime({ env: ntcEnv.slice(1) }, ntcReads), /does not declare DATABASE_URL, which the app reads/);
  assert.throws(() => validateRuntime({ env: ntcEnv }, [...ntcReads, 'SMTP_URL', 'CRON_SECRET']), /CRON_SECRET, SMTP_URL/);
});
test('runtime file entries are well formed', () => {
  const bad = [
    [{ name: 'A', required: 'sometimes' }, /required/],
    [{ name: 'A', required: 'no', secret: 'yes' }, /secret/],
    [{ name: 'A', required: 'no', min_length: 0 }, /min_length/],
    [{ name: 'A', required: 'no', note: 3 }, /note/],
    [{ name: 'a_lower', required: 'no' }, /name/],
    [{ name: 'A', required: 'no', secrett: true }, /unknown key "secrett"/],
    [{ name: 'PORT', required: 'no' }, /set by the image/],
  ];
  for (const [entry, why] of bad) assert.throws(() => validateRuntime({ env: [entry] }, []), why, JSON.stringify(entry));
  assert.throws(() => validateRuntime({ env: [{ name: 'A', required: 'no' }, { name: 'A', required: 'always' }] }, []), /twice/);
  assert.throws(() => validateRuntime({ env: [], extra: 1 }, []), /unknown key "extra"/);
  assert.throws(() => validateRuntime({}, []), /"env" array/);
  assert.throws(() => validateRuntime([], []), /object/);
});
test('fields CRN writes must hold CRN values when present', () => {
  const full = { contract: 1, migrations_first: '20261102000000_init', port: 3000, health: '/api/health', volumes: ['/data/uploads'], writable: ['/tmp', '/data/uploads'], env: [] };
  assert.doesNotThrow(() => validateRuntime(full, [], '20261102000000_init'));
  assert.throws(() => validateRuntime({ ...full, port: 8080 }, [], '20261102000000_init'), /port must be 3000/);
  assert.throws(() => validateRuntime({ ...full, contract: 2 }, [], '20261102000000_init'), /contract/);
  assert.throws(() => validateRuntime(full, [], '20261201000000_other'), /migrations_first must be "20261201000000_other"/);
  assert.ok(PLATFORM_ENV.has('UPLOAD_DIR') && PLATFORM_ENV.has('NODE_ENV'));
});
test('the env scan reads app code, not tests, dependencies or build output', async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-env-scan-'));
  const put = async (file, body) => { await fs.mkdir(path.dirname(path.join(dir, file)), { recursive: true }); await fs.writeFile(path.join(dir, file), body); };
  try {
    await put('lib/session.ts', 'const s = process.env.AUTH_SECRET; const i = process.env["ALLOW_INSECURE_COOKIES"];');
    await put('prisma/bootstrap-once.mjs', 'export function b(p, x, env = process.env) { return env.BOOTSTRAP_ADMIN_EMAIL; }');
    await put('app/api/x/route.tsx', "const u = process.env['FITT_FEEDBACK_URL'];");
    await put('prisma.config.ts', 'url: process.env.DATABASE_URL');
    for (const f of ['tests/e2e/a.spec.ts', 'node_modules/x/index.js', '.next/server/a.js', 'lib/a.test.ts', 'playwright.config.ts', 'prisma/generated/client.ts', 'README.md'])
      await put(f, 'process.env.NOT_APP_CODE');
    assert.deepEqual(await appEnvNames(dir), ['ALLOW_INSECURE_COOKIES', 'AUTH_SECRET', 'BOOTSTRAP_ADMIN_EMAIL', 'DATABASE_URL', 'FITT_FEEDBACK_URL']);
  } finally { await fs.rm(dir, { recursive: true, force: true }); }
});
test('the release checker checks the runtime file before building anything', async () => {
  const src = await fs.readFile(new URL('../internal/buildstep/verify-delivery.mjs', import.meta.url), 'utf8');
  const body = src.slice(src.indexOf('export async function verify('));
  const check = body.indexOf('validateRuntime(');
  assert.ok(check > 0, 'verify() must call validateRuntime');
  assert.ok(body.slice(check, check + 200).includes('appEnvNames(cwd)'), 'with the names the app reads');
  assert.ok(check < body.indexOf("'locked install'"), 'before npm ci');
  assert.match(body, /name: 'runtime contract'/);
});

// OKR build 4 (13 Nov) failed "unit tests" with only "JSON report written to …/unit.json":
// the report, the one place that named the failing test, was deleted with the scratch dir.
import { unitFailures } from '../internal/buildstep/verify-delivery.mjs';
test('a unit test failure names the test and why it failed', () => {
  const report = { testResults: [
    { name: '/w/app/src/ok.test.ts', status: 'passed', assertionResults: [{ fullName: 'fine', status: 'passed', failureMessages: [] }] },
    { name: '/w/app/src/platform/architecture.test.ts', status: 'failed', assertionResults: [
      { fullName: 'environment variables use the MODULE_<name>_ prefix', status: 'failed',
        failureMessages: ['\u001b[31mAssertionError: expected [ Array(1) ] to deeply equal []\u001b[39m\n- Expected\n+ Received\n+   "src/app/api/fitt-feedback/route.ts: FITT_FEEDBACK_URL",\n    at /w/app/src/platform/architecture.test.ts:72:23\n    at file:///w/app/node_modules/@vitest/runner/dist/chunk-artifact.js:302:11\n    at new Promise (<anonymous>)'] },
    ] },
    { name: '/w/app/src/broken.test.ts', status: 'failed', message: 'Failed to load url ./missing', assertionResults: [] },
  ] };
  const text = unitFailures(report, '/w/app');
  assert.match(text, /src\/platform\/architecture\.test\.ts > environment variables use the MODULE_<name>_ prefix/);
  assert.match(text, /AssertionError: expected \[ Array\(1\) \] to deeply equal \[\]/);
  assert.match(text, /fitt-feedback\/route\.ts: FITT_FEEDBACK_URL/);
  assert.match(text, /src\/broken\.test\.ts: Failed to load url \.\/missing/);
  assert.match(text, /at src\/platform\/architecture\.test\.ts:72:23/);
  assert.doesNotMatch(text, /\u001b\[|fine|\/w\/app\/|node_modules|<anonymous>/);
});
