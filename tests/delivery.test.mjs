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
  const child = spawnChild(process.execPath, ['-e', FAKE_STATUS_SERVER], { env: { PATH: process.env.PATH, PORT: port }, stdio: 'ignore' });
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
