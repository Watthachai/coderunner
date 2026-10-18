import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { world, seed, insert, labelsFor, PID, DIGEST, REPO } from './helpers/gcp-world.mjs';

// deploy/gcp/uat-deploy.sh is CRN's deploy side: Cloud Build runs it for every
// image Artifact Registry announces and every five minutes for the sweep. It
// asks FITT Builder what the project wants, registers a cloud project on first
// use, deploys the exact digest behind a health check and reports back.
const SCRIPT = 'deploy/gcp/uat-deploy.sh';
const SHOP = 'crn-demo-shop-cc0b6195';
const SVC = 'uat-shop-cc0b6195';
const cloud = (extra = {}) => ({ [PID]: { project_id: PID, name: 'Shop', target: 'cloud', admin_email: 'admin@customer.test', ...extra } });
const registered = {
  sas: ['crn-uat-cc0b6195'], dbs: ['uat_shop_cc0b6195'], sqlusers: ['uat_shop_cc0b6195'], buckets: ['gs://fitt-proj-uat-cc0b6195-uploads'],
};
const registeredSecrets = () => Object.fromEntries([
  ['database-url', { closed: 'yes' }], ['auth-secret', {}], ['cron-secret', {}], ['bootstrap-admin-email', {}], ['bootstrap-admin-password', { delivered: 'yes' }],
].map(([n, labels]) => [`${SVC}-${n}`, { value: n === 'cron-secret' ? 'c'.repeat(64) : `value-of-${n}`, labels }]));
const withSecrets = extra => ({ ...seed().secrets, ...registeredSecrets(), ...extra });
const stages = evs => evs.map(e => e.stage);

test('a local project is left alone', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud({ target: 'local', admin_email: '' }) }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.equal(r.code, 0, r.out);
  assert.match(r.out, /local/);
  assert.ok(!(await w.gcloud()).some(c => /^(run deploy|iam service-accounts create|sql|storage)/.test(c)));
  assert.deepEqual(await w.events(), []);
});

test('a cloud project is registered, deployed and reported on its first image', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud() }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.equal(r.code, 0, r.out);
  const g = await w.gcloud();
  const all = g.join('\n');
  for (const want of [
    /iam service-accounts create crn-uat-cc0b6195 /,
    /projects add-iam-policy-binding fitt-proj .*serviceAccount:crn-uat-cc0b6195@fitt-proj\.iam\.gserviceaccount\.com.*roles\/cloudsql\.client/,
    /iam service-accounts add-iam-policy-binding crn-uat-cc0b6195@fitt-proj\.iam\.gserviceaccount\.com .*serviceAccount:crn-uat-deployer@fitt-proj\.iam\.gserviceaccount\.com.*roles\/iam\.serviceAccountUser/,
    /storage buckets create gs:\/\/fitt-proj-uat-cc0b6195-uploads .*--location=asia-southeast1/,
    /storage buckets add-iam-policy-binding gs:\/\/fitt-proj-uat-cc0b6195-uploads .*roles\/storage\.objectAdmin/,
  ]) assert.match(all, want);
  const created = g.filter(c => c.startsWith('secrets create ')).map(c => c.split(' ')[2]);
  assert.deepEqual(created.sort(), ['database-url', 'auth-secret', 'cron-secret', 'bootstrap-admin-email', 'bootstrap-admin-password'].map(n => `${SVC}-${n}`).sort());
  assert.ok(g.filter(c => c.startsWith('secrets create ')).every(c => c.includes('--data-file=-')));
  const st = await w.state();
  assert.equal(st.secrets[`${SVC}-bootstrap-admin-email`].value, 'admin@customer.test');
  assert.match(st.secrets[`${SVC}-database-url`].value, /^postgresql:\/\/uat_shop_cc0b6195:[a-f0-9]{48}@localhost\/uat_shop_cc0b6195\?host=\/cloudsql\/fitt-proj:asia-southeast1:crn-uat$/);
  // Secret values travel on stdin or the environment, never a command line.
  const dbPassword = /:([a-f0-9]{48})@/.exec(st.secrets[`${SVC}-database-url`].value)[1];
  for (const n of ['auth-secret', 'cron-secret', 'bootstrap-admin-password']) assert.ok(!all.includes(st.secrets[`${SVC}-${n}`].value), `${n} on a command line`);
  assert.ok(!(await w.calls()).some(c => !c.startsWith('STDIN ') && c.includes(dbPassword)), 'database password on a command line');
  // No gcloud-made user or database. A gcloud user stays a member of
  // cloudsqlsuperuser, which would give every app that role's powers on the shared
  // instance; and Cloud SQL's postgres may not hand a database to another role. So
  // postgres makes the user in SQL (no CREATEDB: an app could squat another
  // project's database name) and owns the database, granting the app what it needs.
  assert.ok(!g.some(c => /^sql (users (create|set-password)|databases)/.test(c)), g.filter(c => c.startsWith('sql')).join('\n'));
  const psql = (await w.calls()).filter(c => c.startsWith('PSQL ') && c.includes('-f -'));
  assert.equal(psql.length, 1, psql.join('\n'));
  assert.ok(psql[0].includes('-U postgres') && psql[0].includes('-v db=uat_shop_cc0b6195'), psql[0]);
  const sql = await w.sql();
  assert.match(sql, /\\getenv pw NEWPW\nCREATE ROLE :"db" LOGIN PASSWORD :'pw';/);
  assert.doesNotMatch(sql, /CREATEDB|OWNER TO/);
  for (const want of [
    /\\if :new_db\nCREATE DATABASE :"db";\n\\endif/,
    /CASE WHEN pg_get_userbyid\(datdba\) = current_user THEN 'owner ok' ELSE \(datname \|\| ' is owned by another role, not by postgres'\)::int::text END/,
    /REVOKE CONNECT, TEMPORARY ON DATABASE :"db" FROM PUBLIC;\nGRANT CONNECT, TEMPORARY, CREATE ON DATABASE :"db" TO :"db";\n\\connect :"db"\nGRANT USAGE, CREATE ON SCHEMA public TO :"db";/,
  ]) assert.match(sql, want);
  assert.ok(sql.indexOf('CREATE ROLE') < sql.indexOf('CREATE DATABASE'), 'the user exists before it is granted the database');
  assert.equal(st.secrets[`${SVC}-database-url`].labels.closed, 'yes');
  const deploy = g.find(c => c.startsWith(`run deploy ${SVC} `));
  for (const want of [`--image ${REPO}/${SHOP}@${DIGEST}`, '--region asia-southeast1', '--port 3000', '--execution-environment gen2',
    '--add-volume name=uploads,type=cloud-storage,bucket=fitt-proj-uat-cc0b6195-uploads', '--add-volume-mount volume=uploads,mount-path=/data/uploads',
    'FITT_STATUS_URL=https://fitt-builder.fittbsa.com/v1/service-status,CRN_SCHEDULER=external', `CRON_SECRET=${SVC}-cron-secret:latest`,
    `BOOTSTRAP_ADMIN_PASSWORD=${SVC}-bootstrap-admin-password:latest`, '--service-account crn-uat-cc0b6195@fitt-proj.iam.gserviceaccount.com',
    '--min-instances 0', '--labels crn-project=cc0b6195,crn-stage=uat']) assert.ok(deploy.includes(want), `missing ${want} in ${deploy}`);
  assert.ok(!deploy.includes('--no-traffic'), 'nothing to protect on a new service');
  const evs = await w.events();
  assert.deepEqual(stages(evs), ['registering', 'deploying', 'ready']);
  const ready = evs.at(-1);
  assert.deepEqual({ env: ready.env, url: ready.url, digest: ready.digest, build_no: ready.build_no }, { env: 'uat', url: `https://${SVC}-xyz.a.run.app`, digest: DIGEST, build_no: 3 });
  assert.equal(ready.initial_admin_password, st.secrets[`${SVC}-bootstrap-admin-password`].value);
  assert.ok(ready.initial_admin_password.length >= 16);
  assert.equal(st.secrets[`${SVC}-bootstrap-admin-password`].labels.delivered, 'yes');
  assert.ok(!r.out.includes(ready.initial_admin_password), 'the password must not reach the build log');
});

test('registration resumes after a partial run', async () => {
  const w = await world(seed({
    labels: { [SHOP]: labelsFor() }, projects: cloud(), sas: ['crn-uat-cc0b6195'], dbs: ['uat_shop_cc0b6195'], sqlusers: ['uat_shop_cc0b6195'],
    secrets: { ...seed().secrets, [`${SVC}-database-url`]: { value: 'postgresql://uat_shop_cc0b6195:seedpw@localhost/uat_shop_cc0b6195?host=/cloudsql/x', labels: {} }, [`${SVC}-auth-secret`]: { value: 'a', labels: {} } },
  }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.equal(r.code, 0, r.out);
  const g = await w.gcloud();
  assert.ok(!g.some(c => /^(iam service-accounts create|sql users|sql databases)/.test(c)), g.join('\n'));
  assert.ok(!g.some(c => c.startsWith(`secrets create ${SVC}-database-url `) || c.startsWith(`secrets create ${SVC}-auth-secret `)));
  assert.ok(g.some(c => c.startsWith(`secrets create ${SVC}-cron-secret `)));
  const sql = await w.sql();
  assert.match(sql, /REVOKE CONNECT/);
  assert.doesNotMatch(sql, /CREATE ROLE/, 'the user and its secret exist already');
  assert.ok(g.some(c => c.startsWith('storage buckets create gs://fitt-proj-uat-cc0b6195-uploads')));
  assert.deepEqual(stages(await w.events()), ['deploying', 'ready']);
});

test('a database user without its secret gets a new password, not a duplicate user', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), sqlusers: ['uat_shop_cc0b6195'] }));
  assert.equal(w.run(SCRIPT, insert(SHOP)).code, 0);
  const g = await w.gcloud();
  assert.ok(g.some(c => c.startsWith('sql users set-password uat_shop_cc0b6195 ')), 'Cloud SQL\'s admin API resets any user\'s password');
  assert.doesNotMatch(await w.sql(), /CREATE ROLE/);
});

test('a new image moves traffic only after the candidate is healthy', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), ...registered, secrets: withSecrets(),
    services: { [SVC]: { rev: 1, serving: `${SVC}-00001-abc`, image: 'old' } } }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.equal(r.code, 0, r.out);
  const g = await w.gcloud();
  const deploy = g.find(c => c.startsWith(`run deploy ${SVC} `));
  assert.ok(deploy.includes('--no-traffic') && deploy.includes('--tag candidate'), deploy);
  assert.ok(!deploy.includes('--add-volume'), 'an existing service keeps its volume');
  assert.ok((await w.calls()).some(c => c === `HEALTH https://candidate---${SVC}-xyz.a.run.app/api/health`));
  assert.ok(g.includes(`run services update-traffic ${SVC} --project fitt-proj --region asia-southeast1 --to-revisions ${SVC}-00002-abc=100 --quiet`), g.join('\n'));
  const evs = await w.events();
  assert.deepEqual(stages(evs), ['deploying', 'ready']);
  assert.equal(evs.at(-1).initial_admin_password, undefined, 'already delivered');
  assert.ok(!g.some(c => c.startsWith('secrets create')));
});

test('an unhealthy candidate keeps traffic where it was', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), ...registered, secrets: withSecrets(), health: 503,
    services: { [SVC]: { rev: 1, serving: `${SVC}-00001-abc`, image: 'old' } } }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.equal(r.code, 0, r.out);
  assert.ok(!(await w.gcloud()).some(c => c.startsWith('run services update-traffic')));
  const last = (await w.events()).at(-1);
  assert.equal(last.stage, 'failed');
  assert.match(last.message, /ยังใช้ build ก่อนหน้า/);
  assert.match(last.message, /app log line one/);
  assert.equal((await w.state()).services[SVC].serving, `${SVC}-00001-abc`);
});

test('a traffic move that fails is reported as failed, never ready', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), ...registered, secrets: withSecrets(), trafficFails: true,
    services: { [SVC]: { rev: 1, serving: `${SVC}-00001-abc`, image: 'old' } } }));
  assert.equal(w.run(SCRIPT, insert(SHOP)).code, 0);
  const evs = await w.events();
  assert.ok(!evs.some(e => e.stage === 'ready'), JSON.stringify(evs));
  assert.equal(evs.at(-1).stage, 'failed');
  assert.match(evs.at(-1).message, /ย้าย traffic/);
  assert.ok(!(await w.gcloud()).some(c => c.startsWith('scheduler jobs create')));
});

test('every run grants the app its secrets again, so a binding that failed once is repaired', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), ...registered, secrets: withSecrets(),
    services: { [SVC]: { rev: 1, serving: `${SVC}-00001-abc`, image: 'old' } } }));
  assert.equal(w.run(SCRIPT, insert(SHOP)).code, 0);
  const grants = (await w.gcloud()).filter(c => c.startsWith('secrets add-iam-policy-binding ')).map(c => c.split(' ')[2]);
  assert.deepEqual(grants.sort(), ['auth-secret', 'bootstrap-admin-email', 'bootstrap-admin-password', 'cron-secret', 'database-url'].map(n => `${SVC}-${n}`));
});

test('a revision that never starts is reported, and nothing else changes', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), ...registered, secrets: withSecrets(), deployFails: true,
    services: { [SVC]: { rev: 1, serving: `${SVC}-00001-abc`, image: 'old' } } }));
  assert.equal(w.run(SCRIPT, insert(SHOP)).code, 0);
  assert.equal((await w.events()).at(-1).stage, 'failed');
  assert.ok(!(await w.gcloud()).some(c => c.startsWith('run services update-traffic') || c.startsWith('scheduler jobs create')));
});

test('an image without labels is skipped; an image from before the contract is reported', async () => {
  const bare = await world(seed({ projects: cloud() }));
  const r = bare.run(SCRIPT, insert(SHOP));
  assert.equal(r.code, 0, r.out);
  assert.match(r.out, /no project label/);
  assert.ok(!(await bare.calls()).some(c => c.includes('builder.test')));
  const old = await world(seed({ labels: { [SHOP]: { 'fitt.crn.project_id': PID } }, projects: cloud() }));
  assert.equal(old.run(SCRIPT, insert(SHOP)).code, 0);
  const evs = await old.events();
  assert.deepEqual(stages(evs), ['failed']);
  assert.match(evs[0].message, /build ใหม่หนึ่งครั้ง/);
  assert.ok(!(await old.gcloud()).some(c => c.startsWith('run deploy')));
});

test('FITT Builder down: no change anywhere, and the run fails so the sweep retries', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), fbDown: true }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.notEqual(r.code, 0);
  assert.ok(!(await w.gcloud()).some(c => / (create|deploy|update|add-iam-policy-binding|set-password) /.test(` ${c} `)), (await w.gcloud()).join('\n'));
});

test('the password stays undelivered when FITT Builder refuses the ready event', async () => {
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud(), refuseStages: ['ready'] }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.notEqual(r.code, 0);
  const st = await w.state();
  assert.equal(st.secrets[`${SVC}-bootstrap-admin-password`].labels.delivered, undefined);
  assert.equal((await w.events()).at(-1).stage, 'failed');
});

test('the schedule becomes Cloud Scheduler jobs, and stale ones go', async () => {
  const schedule = JSON.stringify([{ name: 'reminders', path: '/api/cron/reminders', every_minutes: 15 }, { name: 'report', path: '/api/cron/report', daily_at: '08:30' }, { name: 'hourly', path: '/api/cron/hourly', every_minutes: 60 }]);
  const w = await world(seed({ labels: { [SHOP]: labelsFor(PID, { 'fitt.crn.schedule': schedule }) }, projects: cloud(), ...registered, secrets: withSecrets(),
    jobs: [`${SVC}-old`, `${SVC}-report`, 'uat-other-11111111-x'] }));
  assert.equal(w.run(SCRIPT, insert(SHOP)).code, 0);
  const g = await w.gcloud();
  const job = name => g.find(c => c.startsWith(`scheduler jobs create http ${SVC}-${name} `) || c.startsWith(`scheduler jobs update http ${SVC}-${name} `));
  assert.match(job('reminders'), /^scheduler jobs create http .* --schedule \*\/15 \* \* \* \* --time-zone Asia\/Bangkok --uri https:\/\/uat-shop-cc0b6195-xyz\.a\.run\.app\/api\/cron\/reminders --http-method POST --headers x-cron-secret=c{64}$/);
  assert.match(job('report'), /^scheduler jobs update http .* --schedule 30 8 \* \* \* .* --update-headers x-cron-secret=c{64}$/);
  assert.match(job('hourly'), / --schedule 0 \* \* \* \* /);
  assert.ok(g.some(c => c.startsWith(`scheduler jobs delete ${SVC}-old `)));
  assert.ok(!g.some(c => c.startsWith('scheduler jobs delete uat-other')), 'another project\'s job is not ours to delete');
});

test('the sweep deploys the newest image of each pending project and reports the ones without one', async () => {
  const other = '11111111-2222-4333-8444-555555555555';
  const newer = 'sha256:' + 'b'.repeat(64);
  const w = await world(seed({
    labels: { [SHOP]: labelsFor() }, projects: { ...cloud(), [other]: { project_id: other, name: 'Other', target: 'cloud', admin_email: 'o@x.test' } },
    pending: [PID, other], ...registered, secrets: withSecrets(),
    artifacts: [
      { package: `${REPO}/${SHOP}`, version: DIGEST, tags: ['v2'] },
      { package: `${REPO}/${SHOP}`, version: newer, tags: ['v10'] },
      { package: `${REPO}/crn-demo-shop-cc0b6195-old`, version: 'sha256:' + 'd'.repeat(64), tags: ['v99'] },
    ],
  }));
  const r = w.run(SCRIPT, { _ACTION: 'SWEEP' });
  assert.equal(r.code, 0, r.out);
  const deploy = (await w.gcloud()).find(c => c.startsWith(`run deploy ${SVC} `));
  assert.ok(deploy.includes(`--image ${REPO}/${SHOP}@${newer}`), deploy);
  const evs = await w.events();
  assert.deepEqual(evs.filter(e => e.project === other).map(e => e.stage), ['waiting_image']);
  assert.equal(evs.filter(e => e.project === PID).at(-1).stage, 'ready');
});

test('hostile strings stay data', async () => {
  const email = `o'neil+$x\`id\`@example.com`;
  const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud({ name: 'a "b" \\ c $(id)', admin_email: email }) }));
  const r = w.run(SCRIPT, insert(SHOP));
  assert.equal(r.code, 0, r.out);
  assert.equal((await w.state()).secrets[`${SVC}-bootstrap-admin-email`].value, email);
  assert.equal((await w.events()).at(-1).stage, 'ready');
});

test('events that are not a newly tagged image are ignored', async () => {
  for (const env of [{ ...insert(SHOP), _ACTION: 'DELETE' }, { ...insert(SHOP), _TAG: '' }, { _ACTION: 'INSERT', _DIGEST: `${REPO}/${SHOP}@sha256:bad`, _TAG: `${REPO}/${SHOP}:v3` }]) {
    const w = await world(seed({ labels: { [SHOP]: labelsFor() }, projects: cloud() }));
    const r = w.run(SCRIPT, env);
    assert.equal(r.code, 0, r.out);
    assert.deepEqual(await w.gcloud(), [], JSON.stringify(env));
  }
});

test('a long image name still makes a valid Cloud Run service name', async () => {
  const name = 'crn-demo-' + 'a-very-long-customer-project-name-that-keeps-going' + '-cc0b6195';
  const w = await world(seed());
  const r = w.run(SCRIPT, { ...insert(name), PRINT_SERVICE: '1' });
  const [svc, runtime] = r.out.trim().split(' ');
  assert.match(svc, /^uat-[a-z0-9-]+[a-z0-9]$/);
  assert.ok(svc.length <= 49, svc);
  assert.ok(svc.endsWith('cc0b6195'), 'the project id suffix keeps services apart');
  assert.equal(runtime, 'crn-uat-cc0b6195');
});

test('the Cloud Build config runs this exact script', async () => {
  const yaml = await fs.readFile('deploy/gcp/cloudbuild-uat.yaml', 'utf8');
  const body = await fs.readFile(SCRIPT, 'utf8');
  const block = yaml.split('\n').reduce((acc, line) => {
    if (acc.inside === undefined && /^\s+script: \|$/.test(line)) return { inside: true, lines: [] };
    if (acc.inside && (line.startsWith('      ') || line === '')) return { ...acc, lines: [...acc.lines, line.slice(6)] };
    if (acc.inside) return { ...acc, inside: false };
    return acc;
  }, {});
  assert.equal(block.lines.join('\n').trimEnd(), body.trimEnd());
  assert.match(yaml, /automapSubstitutions: true/);
});

const setupPlan = () => {
  const res = spawnSync('bash', [path.resolve('deploy/gcp/setup.sh')], { env: { PATH: process.env.PATH, PROJECT_ID: 'fitt-proj', DRY_RUN: '1' }, input: '', encoding: 'utf8' });
  assert.equal(res.status, 0, res.stderr);
  return res.stdout;
};

test('setup in dry run prints every step and never issues a key', () => {
  const plan = setupPlan();
  assert.match(plan, /artifacts repositories create crn --repository-format=docker --location=asia-southeast1/);
  assert.match(plan, /pubsub topics create gcr/);
  assert.match(plan, /builds triggers create pubsub --name=crn-uat-deploy/);
  assert.match(plan, /--subscription-filter=_ACTION == "INSERT"/);
  const pusher = plan.split('\n').filter(l => l.includes('crn-pusher@'));
  assert.ok(pusher.length >= 1 && pusher.every(l => l.includes('repositories add-iam-policy-binding') && l.includes('roles/artifactregistry.writer')), pusher.join('\n'));
  assert.doesNotMatch(plan, /keys create/);
  assert.doesNotMatch(plan, /roles\/(owner|editor)\b/);
});

test('setup creates the sweep, its trigger, the secrets and the limited deployer roles', () => {
  const plan = setupPlan();
  for (const want of [
    /pubsub topics create crn-sweep/,
    /builds triggers create pubsub --name=crn-uat-sweep .*--topic=projects\/fitt-proj\/topics\/crn-sweep/,
    /scheduler jobs create pubsub crn-sweep --location=asia-southeast1 --schedule=\*\/5 \* \* \* \* --topic=crn-sweep/,
    /_FB_URL=https:\/\/fitt-builder\.fittbsa\.com/,
    /_REPO=asia-southeast1-docker\.pkg\.dev\/fitt-proj\/crn/,
    /secrets create crn-uat-postgres-password/,
    /sql users set-password postgres --instance=crn-uat/,
    /roles\/resourcemanager\.projectIamAdmin --condition=.*modifiedGrantsByRole.*roles\/cloudsql\.client/,
    // A condition on the account's name never matches: IAM checks service accounts by numeric id (first real run).
    /--condition=None --member=serviceAccount:crn-uat-deployer@fitt-proj\.iam\.gserviceaccount\.com --role=roles\/iam\.serviceAccountAdmin/,
    /artifacts repositories set-cleanup-policies crn/,
  ]) assert.match(plan, want);
  // An empty substitution value may be refused, and keep() would hide it as "already exists".
  assert.doesNotMatch(plan, /_[A-Z]+=,/);
  assert.doesNotMatch(plan, /resource\.name\.extract/);
  // The deployer's gcloud projects add-iam-policy-binding needs Cloud Resource Manager (found on the first real run).
  assert.match(plan.split('\n').find(l => l.includes('services enable')), /cloudresourcemanager\.googleapis\.com/);
});

test('setup defaults to Singapore', () => {
  const plan = setupPlan();
  assert.match(plan, /--location=asia-southeast1 /);
  assert.match(plan, /--region=asia-southeast1 /);
  assert.doesNotMatch(plan, /asia-southeast3/);
});

test('setup replaces existing triggers, so a new deploy script reaches Cloud Build', async () => {
  const w = await world(seed());
  const r = w.run('deploy/gcp/setup.sh', {}, [], '');
  assert.equal(r.code, 0, r.out);
  const g = await w.gcloud();
  for (const name of ['crn-uat-deploy', 'crn-uat-sweep']) {
    const del = g.findIndex(c => c.startsWith(`builds triggers delete ${name} `));
    const create = g.findIndex(c => c.startsWith(`builds triggers create pubsub --name=${name} `));
    assert.ok(del >= 0 && create > del, `${name}: delete then create\n${g.filter(c => c.includes('triggers')).join('\n')}`);
  }
});
