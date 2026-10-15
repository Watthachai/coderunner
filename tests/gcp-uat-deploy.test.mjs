import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

// deploy/gcp/uat-deploy.sh runs in Cloud Build when Artifact Registry announces a
// new image. It must deploy the exact digest, only for projects enrolled in UAT,
// and ignore every other event. A fake gcloud records what it was asked to do.
const script = path.resolve('deploy/gcp/uat-deploy.sh');
const DIGEST = 'sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682';
const REPO = 'asia-southeast3-docker.pkg.dev/fitt-proj/crn';

async function run(env, { secrets = [] } = {}) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-uat-'));
  const log = path.join(dir, 'gcloud.log');
  await fs.writeFile(path.join(dir, 'gcloud'), `#!/bin/sh
echo "$@" >> ${log}
case "$*" in
  "secrets describe "*) for s in ${secrets.join(' ')}; do [ "$3" = "$s" ] && exit 0; done; exit 1;;
  "run services describe "*) echo "https://uat-shop-cc0b6195-xyz.a.run.app";;
esac
exit 0
`, { mode: 0o755 });
  const res = spawnSync('bash', [script], { env: { PATH: `${dir}:${process.env.PATH}`, PROJECT_ID: 'fitt-proj', _REGION: 'asia-southeast3', _SQL_INSTANCE: 'fitt-proj:asia-southeast3:crn-uat', _STATUS_URL: 'https://fitt-builder.fittbsa.com/v1/service-status', ...env }, encoding: 'utf8' });
  const calls = (await fs.readFile(log, 'utf8').catch(() => '')).trim().split('\n').filter(Boolean);
  return { code: res.status, out: res.stdout + res.stderr, calls };
}
const event = name => ({ _ACTION: 'INSERT', _DIGEST: `${REPO}/${name}@${DIGEST}`, _TAG: `${REPO}/${name}:v3` });
const enrolled = ['uat-shop-cc0b6195-database-url', 'uat-shop-cc0b6195-auth-secret'];

test('an enrolled project is deployed to its UAT service at the exact digest', async () => {
  const r = await run(event('crn-demo-shop-cc0b6195'), { secrets: enrolled });
  assert.equal(r.code, 0, r.out);
  const deploy = r.calls.find(c => c.startsWith('run deploy '));
  assert.ok(deploy, r.calls.join('\n'));
  for (const want of [
    'run deploy uat-shop-cc0b6195',
    `--image ${REPO}/crn-demo-shop-cc0b6195@${DIGEST}`,
    '--region asia-southeast3',
    '--port 3000',
    '--add-cloudsql-instances fitt-proj:asia-southeast3:crn-uat',
    '--service-account crn-uat-runtime@fitt-proj.iam.gserviceaccount.com',
    'DATABASE_URL=uat-shop-cc0b6195-database-url:latest',
    'AUTH_SECRET=uat-shop-cc0b6195-auth-secret:latest',
    'FITT_STATUS_URL=https://fitt-builder.fittbsa.com/v1/service-status',
  ]) assert.ok(deploy.includes(want), `missing ${want} in: ${deploy}`);
  assert.match(r.out, /https:\/\/uat-shop-cc0b6195-xyz\.a\.run\.app/);
});

test('optional secrets are wired only when the project has them', async () => {
  const all = [...enrolled, 'uat-shop-cc0b6195-bootstrap-admin-email', 'uat-shop-cc0b6195-bootstrap-admin-password', 'uat-shop-cc0b6195-license-key'];
  const withAll = (await run(event('crn-demo-shop-cc0b6195'), { secrets: all })).calls.find(c => c.startsWith('run deploy '));
  for (const want of ['BOOTSTRAP_ADMIN_EMAIL=uat-shop-cc0b6195-bootstrap-admin-email:latest', 'BOOTSTRAP_ADMIN_PASSWORD=uat-shop-cc0b6195-bootstrap-admin-password:latest', 'FITT_LICENSE_KEY=uat-shop-cc0b6195-license-key:latest']) assert.ok(withAll.includes(want), want);
  const bare = (await run(event('crn-demo-shop-cc0b6195'), { secrets: enrolled })).calls.find(c => c.startsWith('run deploy '));
  assert.ok(!bare.includes('BOOTSTRAP_') && !bare.includes('FITT_LICENSE_KEY'), bare);
});

test('a project not enrolled in UAT is skipped, not failed', async () => {
  const r = await run(event('crn-demo-other-11111111'));
  assert.equal(r.code, 0, r.out);
  assert.ok(!r.calls.some(c => c.startsWith('run deploy')), r.calls.join('\n'));
  assert.match(r.out, /not enrolled/);
});

test('events that are not a newly tagged image are ignored', async () => {
  for (const env of [
    { ...event('crn-demo-shop-cc0b6195'), _ACTION: 'DELETE' },
    { ...event('crn-demo-shop-cc0b6195'), _TAG: '' },
    { _ACTION: 'INSERT', _DIGEST: `${REPO}/crn-demo-shop-cc0b6195@sha256:bad`, _TAG: `${REPO}/crn-demo-shop-cc0b6195:v3` },
  ]) {
    const r = await run(env, { secrets: enrolled });
    assert.equal(r.code, 0, r.out);
    assert.ok(!r.calls.some(c => c.startsWith('run deploy')), JSON.stringify(env));
  }
});

test('a long image name still makes a valid Cloud Run service name', async () => {
  const name = 'crn-demo-' + 'a-very-long-customer-project-name-that-keeps-going' + '-cc0b6195';
  const r = await run(event(name));
  const svc = /checking (\S+)/.exec(r.out)?.[1] ?? '';
  assert.match(svc, /^uat-[a-z0-9-]+[a-z0-9]$/);
  assert.ok(svc.length <= 49, svc);
  assert.ok(svc.endsWith('cc0b6195'), 'the project id suffix keeps services apart');
});

test('the Cloud Build config runs this exact script', async () => {
  const yaml = await fs.readFile('deploy/gcp/cloudbuild-uat.yaml', 'utf8');
  const body = await fs.readFile(script, 'utf8');
  const block = yaml.split('\n').reduce((acc, line) => {
    if (acc.inside === undefined && /^\s+script: \|$/.test(line)) return { inside: true, lines: [] };
    if (acc.inside && (line.startsWith('      ') || line === '')) return { ...acc, lines: [...acc.lines, line.slice(6)] };
    if (acc.inside) return { ...acc, inside: false };
    return acc;
  }, {});
  assert.equal(block.lines.join('\n').trimEnd(), body.trimEnd());
  assert.match(yaml, /automapSubstitutions: true/);
});

async function fakeGcloud() {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-gcloud-'));
  const log = path.join(dir, 'gcloud.log');
  // Records each call; for "secrets create" also records what arrived on stdin.
  await fs.writeFile(path.join(dir, 'gcloud'), `#!/bin/sh
echo "ARGS $*" >> ${log}
case "$*" in *"--data-file=-"*) printf 'STDIN %s\\n' "$(cat)" >> ${log};; esac
exit 0
`, { mode: 0o755 });
  return { dir, read: async () => (await fs.readFile(log, 'utf8').catch(() => '')).trim().split('\n').filter(Boolean) };
}

test('setup in dry run prints every step, runs nothing, and never issues a key', async () => {
  const g = await fakeGcloud();
  const res = spawnSync('bash', ['deploy/gcp/setup.sh'], { env: { PATH: `${g.dir}:${process.env.PATH}`, PROJECT_ID: 'fitt-proj', DRY_RUN: '1' }, encoding: 'utf8' });
  assert.equal(res.status, 0, res.stderr);
  assert.deepEqual(await g.read(), []);
  const plan = res.stdout;
  assert.match(plan, /artifacts repositories create crn --repository-format=docker --location=asia-southeast3/);
  assert.match(plan, /pubsub topics create gcr/);
  assert.match(plan, /builds triggers create pubsub --name=crn-uat-deploy/);
  assert.match(plan, /--subscription-filter=_ACTION == "INSERT"/);
  // The pusher may write images to this one repository, nothing else.
  const pusher = plan.split('\n').filter(l => l.includes('crn-pusher@'));
  assert.ok(pusher.length >= 1 && pusher.every(l => l.includes('repositories add-iam-policy-binding') && l.includes('roles/artifactregistry.writer')), pusher.join('\n'));
  assert.doesNotMatch(plan, /keys create/);
  assert.doesNotMatch(plan, /roles\/(owner|editor)\b/);
});

test('enrolling a project keeps every secret value off the command line', async () => {
  const g = await fakeGcloud();
  const res = spawnSync('bash', ['deploy/gcp/new-uat.sh', 'crn-demo-shop-cc0b6195'], { env: { PATH: `${g.dir}:${process.env.PATH}`, PROJECT_ID: 'fitt-proj', BOOTSTRAP_ADMIN_EMAIL: 'admin@customer.test' }, input: '', encoding: 'utf8' });
  assert.equal(res.status, 0, res.stderr);
  const calls = await g.read();
  const args = calls.filter(l => l.startsWith('ARGS ')).join('\n');
  const created = calls.filter(l => /secrets create/.test(l)).map(l => /secrets create (\S+)/.exec(l)[1]);
  assert.deepEqual(created, ['uat-shop-cc0b6195-database-url', 'uat-shop-cc0b6195-auth-secret', 'uat-shop-cc0b6195-bootstrap-admin-email', 'uat-shop-cc0b6195-bootstrap-admin-password']);
  const accessors = calls.filter(l => /secrets add-iam-policy-binding/.test(l));
  assert.equal(accessors.length, created.length);
  assert.ok(accessors.every(l => l.includes('crn-uat-runtime@fitt-proj.iam.gserviceaccount.com') && l.includes('roles/secretmanager.secretAccessor')));
  // Values arrive on stdin only.
  const values = calls.filter(l => l.startsWith('STDIN ')).map(l => l.slice(6));
  const url = values.find(v => v.startsWith('postgresql://'));
  assert.match(url, /^postgresql:\/\/uat_shop_cc0b6195:[a-f0-9]{48}@localhost\/uat_shop_cc0b6195\?host=\/cloudsql\/fitt-proj:asia-southeast3:crn-uat$/);
  const [, dbPassword] = /:([a-f0-9]{48})@/.exec(url);
  const adminPassword = values[3];
  for (const secret of [values[1], adminPassword]) assert.ok(!args.includes(secret), 'a secret value appeared in gcloud arguments');
  // The one documented exception: gcloud takes a new SQL user's password only as a flag.
  assert.equal(args.split('\n').filter(l => l.includes(dbPassword)).length, 1);
  assert.match(args, /sql users create uat_shop_cc0b6195 .*--password=/);
  // The first administrator's password is shown once to the operator.
  assert.equal(res.stdout.split(adminPassword).length - 1, 1);
});
