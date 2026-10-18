import test from 'node:test';
import assert from 'node:assert/strict';
import { world, seed, labelsFor, PID, DIGEST, REPO } from './helpers/gcp-world.mjs';

// deploy/gcp/promote.sh: after the customer accepts UAT, a person in the team takes
// the digest UAT serves to the customer's own production — its own Cloud SQL,
// account, secrets and bucket — and FITT Builder shows the production link and a
// new first-administrator password once.
const SCRIPT = 'deploy/gcp/promote.sh';
const SHOP = 'crn-demo-shop-cc0b6195';
const UAT = 'uat-shop-cc0b6195';
const PROD = 'prod-shop-cc0b6195';
const good = `${REPO}/${SHOP}@${DIGEST}`;
const project = (target = 'cloud') => ({ [PID]: { project_id: PID, name: 'Shop', target, admin_email: 'admin@customer.test' } });
const uat = { [UAT]: { rev: 2, serving: `${UAT}-00001-abc`, image: `${REPO}/${SHOP}@sha256:${'e'.repeat(64)}`,
  images: { [`${UAT}-00001-abc`]: good, [`${UAT}-00002-abc`]: `${REPO}/${SHOP}@sha256:${'e'.repeat(64)}` } } };

test('promote takes the digest UAT serves to a new production with its own database', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: project(), services: uat }));
  const r = w.run(SCRIPT, {}, [PID]);
  assert.equal(r.code, 0, r.out);
  const g = await w.gcloud();
  const all = g.join('\n');
  assert.match(all, /sql instances create crn-prod-cc0b6195 .*--tier=db-custom-1-3840 .*--region=asia-southeast1/);
  assert.match(all, /sql users set-password postgres --instance=crn-prod-cc0b6195 /);
  assert.ok(!g.some(c => c.startsWith('sql databases') || c.startsWith('sql users create')), 'the app makes its own database');
  assert.match(all, /iam service-accounts create crn-prod-cc0b6195 /);
  const st = await w.state();
  assert.ok(st.secrets[`${PROD}-postgres-password`], 'the instance\'s postgres password is kept');
  assert.ok(!g.some(c => c.startsWith(`secrets add-iam-policy-binding ${PROD}-postgres-password`)), 'the app never reads its instance\'s admin password');
  assert.ok((await w.calls()).some(c => c.startsWith('PROXY ') && c.includes('fitt-proj:asia-southeast1:crn-prod-cc0b6195')));
  // Production moves only on a person's command: the automatic deployer may not act as its account.
  assert.ok(!g.some(c => c.startsWith('iam service-accounts add-iam-policy-binding crn-prod-') && c.includes('crn-uat-deployer')), 'deployer must not act as production');
  const deploy = g.find(c => c.startsWith(`run deploy ${PROD} `));
  // The revision serving UAT, not the newest one (it may be a candidate that failed).
  assert.ok(deploy.includes(`--image ${good} `), deploy);
  for (const want of ['--min-instances 0', '--labels crn-project=cc0b6195,crn-stage=production', '--add-cloudsql-instances fitt-proj:asia-southeast1:crn-prod-cc0b6195',
    'bucket=fitt-proj-prod-cc0b6195-uploads', 'CRN_SCHEDULER=external']) assert.ok(deploy.includes(want), `missing ${want}`);
  const evs = await w.events();
  assert.ok(evs.every(e => e.env === 'production'));
  assert.deepEqual(evs.map(e => e.stage), ['registering', 'deploying', 'ready']);
  assert.ok(evs.at(-1).initial_admin_password.length >= 16);
  assert.notEqual(evs.at(-1).initial_admin_password, st.secrets[`${UAT}-bootstrap-admin-password`]?.value);
  assert.ok(!r.out.includes(evs.at(-1).initial_admin_password));
});

test('--always-on keeps one instance running', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: project(), services: uat }));
  assert.equal(w.run(SCRIPT, {}, [PID, '--always-on']).code, 0);
  assert.ok((await w.gcloud()).find(c => c.startsWith(`run deploy ${PROD} `)).includes('--min-instances 1'));
});

test('no UAT, no production', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: project() }));
  const r = w.run(SCRIPT, {}, [PID]);
  assert.equal(r.code, 1);
  assert.match(r.out, /ยังไม่มี UAT/);
  assert.ok(!(await w.gcloud()).some(c => / create /.test(` ${c} `)));
});

test('a local project is refused before anything is created', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: project('local'), services: uat }));
  const r = w.run(SCRIPT, {}, [PID]);
  assert.equal(r.code, 1);
  assert.match(r.out, /local/);
  assert.ok(!(await w.gcloud()).some(c => / create /.test(` ${c} `)));
});

test('a project id that is not a UUID is refused', async () => {
  const w = await world(seed());
  const r = w.run(SCRIPT, {}, ['../../etc']);
  assert.equal(r.code, 1);
  assert.deepEqual(await w.gcloud(), []);
});
