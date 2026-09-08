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
