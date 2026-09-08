import test from 'node:test';
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
import { bootstrapOnce } from '../cmd/server/skillassets/assets/bootstrap-once.mjs';
const url = process.env.CRN_BOOTSTRAP_TEST_DATABASE_URL;
const modulePath = process.env.CRN_TEST_PRISMA_MODULE;
const enabled = Boolean(url && modulePath);
// This integration suite deliberately mutates only a disposable loopback test DB.
if (enabled) {
 const parsed = new URL(url);
 if (!['127.0.0.1','localhost'].includes(parsed.hostname) || parsed.pathname !== '/crn_test') throw new Error('A disposable loopback crn_test database is required');
}
const env = {BOOTSTRAP_ADMIN_EMAIL:' FIRST@example.test ',BOOTSTRAP_ADMIN_PASSWORD:'unique-test-password-123'};
const callbacks = {
 hasUsers:async tx=>(await tx.user.count())>0,
 createAdmin:async(tx,{email,password})=>tx.user.create({data:{email,passwordHash:`test-hash:${password}`,mustChangePassword:true}}),
};
test('once-only bootstrap against actual PostgreSQL and Prisma', {skip:!enabled}, async t=>{
 const {PrismaClient}=await import(pathToFileURL(modulePath).href);
 const db=new PrismaClient({datasourceUrl:url});
 const reset=async()=>{await db.user.deleteMany();await db.systemBootstrap.deleteMany();await db.item.deleteMany();};
 try {
  await t.test('concurrent installers create one admin and one durable marker',async()=>{
   await reset();
   const results=await Promise.all(Array.from({length:12},()=>bootstrapOnce(db,callbacks,env)));
   assert.equal(results.filter(x=>x==='created').length,1);
   assert.equal(results.filter(x=>x==='already-complete').length,11);
   assert.equal(await db.user.count(),1);assert.equal(await db.systemBootstrap.count(),1);
   assert.equal((await db.user.findFirst()).email,'first@example.test');
  });
  await t.test('restart and upgrade inputs preserve password, account and business data',async()=>{
   const before=await db.user.findMany();const item=await db.item.create({data:{name:'Customer sentinel'}});
   assert.equal(await bootstrapOnce(db,callbacks,{BOOTSTRAP_ADMIN_EMAIL:'new@example.test',BOOTSTRAP_ADMIN_PASSWORD:'changed-test-password'}),'already-complete');
   assert.equal(await bootstrapOnce(db,callbacks,{}),'already-complete');
   assert.deepEqual(await db.user.findMany(),before);
   assert.deepEqual(await db.item.findUnique({where:{id:item.id}}),item);
  });
  await t.test('deleting users cannot resurrect the initial admin',async()=>{
   await db.user.deleteMany();assert.equal(await bootstrapOnce(db,callbacks,env),'already-complete');assert.equal(await db.user.count(),0);
  });
  await t.test('existing customer accounts are adopted without changing their hash',async()=>{
   await reset();const user=await db.user.create({data:{email:'existing@example.test',passwordHash:'existing-hash',mustChangePassword:false}});
   assert.equal(await bootstrapOnce(db,callbacks,{}),'adopted-existing-users');
   assert.deepEqual(await db.user.findMany(),[user]);
  });
  await t.test('failed creation rolls back both account and marker, then permits retry',async()=>{
   await reset();
   await assert.rejects(bootstrapOnce(db,{...callbacks,createAdmin:async(tx,data)=>{await callbacks.createAdmin(tx,data);throw Error('simulated failure');}},env),/simulated failure/);
   assert.equal(await db.user.count(),0);assert.equal(await db.systemBootstrap.count(),0);
   assert.equal(await bootstrapOnce(db,callbacks,env),'created');
  });
  await t.test('missing or weak first-install credentials fail without partial state',async()=>{
   await reset();
   for(const vars of [{},{BOOTSTRAP_ADMIN_EMAIL:'first@example.test',BOOTSTRAP_ADMIN_PASSWORD:'short'}])await assert.rejects(bootstrapOnce(db,callbacks,vars),/First installation requires/);
   assert.equal(await db.user.count(),0);assert.equal(await db.systemBootstrap.count(),0);
  });
 } finally {await db.$disconnect();}
});
