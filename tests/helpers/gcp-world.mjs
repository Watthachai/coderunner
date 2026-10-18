// A fake GCP + FITT Builder for deploy/gcp scripts. One temp dir holds a state
// file the test seeds; fake `gcloud` and `curl` (Node) read and change it and log
// every call, fake `psql` and `cloud-sql-proxy` record theirs. python3 and
// openssl are the real ones, as in Cloud Build.
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

export const PROJECT = 'fitt-proj';
export const REGION = 'asia-southeast1';
export const REPO = `${REGION}-docker.pkg.dev/${PROJECT}/crn`;
export const FB = 'https://builder.test';
export const FB_TOKEN = 'fbd_test-token';
export const DIGEST = 'sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682';
export const PID = 'cc0b6195-46b4-4c29-a167-c0dd321d10d9';

const FAKE = String.raw`#!/usr/bin/env node
const fs = require('fs'), path = require('path');
const dir = process.env.FAKE_DIR, stateFile = path.join(dir, 'state.json');
const st = JSON.parse(fs.readFileSync(stateFile, 'utf8'));
const save = () => fs.writeFileSync(stateFile, JSON.stringify(st));
const log = line => fs.appendFileSync(path.join(dir, 'calls.log'), line.replace(/\n/g, '\\n') + '\n');
const tool = path.basename(process.argv[1]);
const a = process.argv.slice(2);
const flag = name => { const f = a.find(x => x.startsWith('--' + name + '=')); if (f) return f.slice(name.length + 3); const i = a.indexOf('--' + name); return i >= 0 ? a[i + 1] : undefined; };
const stdin = () => { try { return fs.readFileSync(0, 'utf8'); } catch { return ''; } };
const out = s => process.stdout.write(s + '\n');
const has = (...p) => p.every((x, i) => a[i] === x);
if (tool === 'gcloud') {
  log('ARGS ' + a.join(' '));
  const fmt = flag('format') || '';
  if (has('auth', 'print-access-token')) return out('registry-token');
  if (has('secrets', 'versions', 'access')) { const s = st.secrets[flag('secret')]; if (!s) process.exit(1); return process.stdout.write(s.value); }
  if (has('secrets', 'describe')) { const s = st.secrets[a[2]]; if (!s) process.exit(1); const m = /^value\(labels\.(.+)\)$/.exec(fmt); return out(m ? (s.labels[m[1]] || '') : 'name: ' + a[2]); }
  if (has('secrets', 'create')) { if (st.secrets[a[2]]) process.exit(1); const v = stdin(); log('STDIN ' + v); st.secrets[a[2]] = { value: v, labels: {} }; return save(); }
  if (has('secrets', 'update')) { const l = flag('update-labels'); if (l) for (const kv of l.split(',')) { const [k, v] = kv.split('='); st.secrets[a[2]].labels[k] = v; } return save(); }
  if (has('iam', 'service-accounts', 'describe')) return st.sas.includes(a[3].split('@')[0]) ? out('ok') : process.exit(1);
  if (has('iam', 'service-accounts', 'create')) { if (st.sas.includes(a[3])) process.exit(1); st.sas.push(a[3]); return save(); }
  if (has('sql', 'instances', 'describe')) return st.instances.includes(a[3]) ? out('ok') : process.exit(1);
  if (has('sql', 'instances', 'create')) { st.instances.push(a[3]); return save(); }
  if (has('sql', 'databases', 'describe')) return st.dbs.includes(a[3]) ? out('ok') : process.exit(1);
  if (has('sql', 'databases', 'create')) { st.dbs.push(a[3]); return save(); }
  if (has('sql', 'users', 'list')) { const n = (flag('filter') || '').replace(/^name=/, ''); return st.sqlusers.includes(n) ? out(n) : undefined; }
  if (has('sql', 'users', 'create')) { if (st.sqlusers.includes(a[3])) process.exit(1); st.sqlusers.push(a[3]); return save(); }
  if (has('storage', 'buckets', 'describe')) return st.buckets.includes(a[3]) ? out('ok') : process.exit(1);
  if (has('storage', 'buckets', 'create')) { st.buckets.push(a[3]); return save(); }
  if (has('run', 'services', 'describe')) {
    const svc = st.services[a[3]]; if (!svc) process.exit(1);
    const url = 'https://' + a[3] + '-xyz.a.run.app', rev = a[3] + '-' + String(svc.rev).padStart(5, '0') + '-abc';
    if (fmt === 'json') return out(JSON.stringify({ status: { url, latestCreatedRevisionName: rev, traffic: [{ revisionName: svc.serving, percent: 100 }, { revisionName: rev, tag: 'candidate', url: 'https://candidate---' + a[3] + '-xyz.a.run.app' }] } }));
    if (fmt === 'value(status.url)') return out(url);
    if (fmt === 'value(status.latestCreatedRevisionName)') return out(rev);
    if (fmt === 'value(spec.template.spec.containers[0].image)') return out(svc.image);
    return out('ok');
  }
  if (has('run', 'deploy')) {
    if (st.deployFails) process.exit(1);
    const svc = st.services[a[2]] || (st.services[a[2]] = { rev: 0, serving: null });
    svc.rev += 1; svc.image = flag('image');
    const revName = a[2] + '-' + String(svc.rev).padStart(5, '0') + '-abc';
    (svc.images = svc.images || {})[revName] = svc.image;
    if (!a.includes('--no-traffic')) svc.serving = revName;
    return save();
  }
  if (has('run', 'services', 'list')) { const id8 = (/crn-project=([a-z0-9]+)/.exec(flag('filter') || '') || [])[1]; for (const n of Object.keys(st.services)) if (n.startsWith('uat-') && n.endsWith('-' + id8)) out(n); return; }
  if (has('run', 'revisions', 'describe')) { for (const svc of Object.values(st.services)) if (svc.images && svc.images[a[3]]) return out(svc.images[a[3]]); process.exit(1); }
  if (has('run', 'services', 'update-traffic')) { st.services[a[3]].serving = (flag('to-revisions') || '').split('=')[0]; return save(); }
  if (has('logging', 'read')) return out('app log line one\napp log line two');
  if (has('scheduler', 'jobs', 'describe')) return st.jobs.includes(a[3]) ? out('ok') : process.exit(1);
  if (has('scheduler', 'jobs', 'create') || has('scheduler', 'jobs', 'update')) { if (!st.jobs.includes(a[4])) st.jobs.push(a[4]); return save(); }
  if (has('scheduler', 'jobs', 'list')) { for (const j of st.jobs) out('projects/' + process.env.PROJECT_ID + '/locations/r/jobs/' + j); return; }
  if (has('scheduler', 'jobs', 'delete')) { st.jobs = st.jobs.filter(j => j !== a[3]); return save(); }
  if (has('artifacts', 'docker', 'images', 'list')) return out(JSON.stringify(st.artifacts));
  return;
}
// curl
let method = 'GET', url = '', outFile = '', fmtOut = '', fail = false, body = null; const headers = [];
for (let i = 0; i < a.length; i++) {
  const x = a[i];
  if (x === '-X') method = a[++i];
  else if (x === '-H') headers.push(a[++i]);
  else if (x === '-o') outFile = a[++i];
  else if (x === '-w') fmtOut = a[++i];
  else if (x === '--max-time') i++;
  else if (x === '--data-binary') { i++; body = stdin(); }
  else if (/^-[a-zA-Z]+$/.test(x)) { if (x.includes('f')) fail = true; }
  else url = x;
}
if (body !== null && method === 'GET') method = 'POST';
log('CURL ' + method + ' ' + url + ' ' + headers.filter(h => h.startsWith('Authorization')).join(' '));
let status = 404, res = '';
const reg = /\/v2\/(.+)\/(manifests|blobs)\/(sha256:[a-f0-9]+)$/.exec(url);
if (reg) {
  status = 200;
  if (reg[2] === 'manifests') res = JSON.stringify({ schemaVersion: 2, config: { digest: 'sha256:' + 'c'.repeat(64) } });
  else res = JSON.stringify({ config: { Labels: st.labels[reg[1].split('/').pop()] ?? null } });
} else if (url.startsWith(st.fb)) {
  const p = url.slice(st.fb.length);
  if (st.fbDown) status = 503;
  else if (!headers.includes('Authorization: Bearer ' + st.fbToken)) status = 401;
  else if (method === 'GET' && p === '/v1/integrations/deploy/pending') { status = 200; res = JSON.stringify({ projects: st.pending.map(project_id => ({ project_id })) }); }
  else if (method === 'GET' && /^\/v1\/integrations\/deploy\/projects\/[^/]+$/.test(p)) { const pr = st.projects[p.split('/').pop()]; if (pr) { status = 200; res = JSON.stringify(pr); } }
  else if (method === 'POST' && /\/events$/.test(p)) {
    const ev = JSON.parse(body);
    if ((st.refuseStages || []).includes(ev.stage)) status = 500;
    else { status = 202; res = '{}'; fs.appendFileSync(path.join(dir, 'events.jsonl'), JSON.stringify({ project: p.split('/')[5], ...ev }) + '\n'); }
  }
} else if (/\/api\/health$/.test(url)) { status = st.health; res = '{"ok":true}'; log('HEALTH ' + url); }
if (fmtOut === '%{http_code}') process.stdout.write(String(status));
if (fail && status >= 400) process.exit(22);
if (!outFile && res) process.stdout.write(res);
`;

export function seed(overrides = {}) {
  return {
    fb: FB, fbToken: FB_TOKEN, fbDown: false, refuseStages: [], health: 200, deployFails: false,
    secrets: { 'crn-deploy-fb-token': { value: FB_TOKEN, labels: {} }, 'crn-uat-postgres-password': { value: 'pg-admin-pw', labels: {} } },
    sas: [], dbs: [], sqlusers: [], buckets: [], instances: ['crn-uat'], services: {}, jobs: [],
    labels: {}, projects: {}, pending: [], artifacts: [],
    ...overrides,
  };
}

export async function world(state) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-gcp-'));
  for (const tool of ['gcloud', 'curl']) await fs.writeFile(path.join(dir, tool), FAKE, { mode: 0o755 });
  await fs.writeFile(path.join(dir, 'psql'), `#!/bin/sh\necho "PSQL $*" >> "${dir}/calls.log"\ncase "$*" in *"-f -"*) cat >> "${dir}/psql.sql";; esac\n`, { mode: 0o755 });
  await fs.writeFile(path.join(dir, 'cloud-sql-proxy'), `#!/bin/sh\necho "PROXY $*" >> "${dir}/calls.log"\nexec sleep 3\n`, { mode: 0o755 });
  await fs.writeFile(path.join(dir, 'state.json'), JSON.stringify(state));
  const read = async f => fs.readFile(path.join(dir, f), 'utf8').catch(() => '');
  return {
    dir,
    run(script, env = {}, args = [], input = '') {
      const res = spawnSync('bash', [path.resolve(script), ...args], {
        env: { PATH: `${dir}:${process.env.PATH}`, FAKE_DIR: dir, PROJECT_ID: PROJECT, _REGION: REGION, _REPO: REPO, _FB_URL: FB,
          _SQL_INSTANCE: `${PROJECT}:${REGION}:crn-uat`, _STATUS_URL: 'https://fitt-builder.fittbsa.com/v1/service-status', HEALTH_TRIES: '3', HEALTH_INTERVAL: '0', ...env },
        input, encoding: 'utf8', timeout: 60_000,
      });
      return { code: res.status, out: (res.stdout || '') + (res.stderr || '') };
    },
    async calls() { return (await read('calls.log')).split('\n').filter(Boolean); },
    async gcloud() { return (await this.calls()).filter(l => l.startsWith('ARGS ')).map(l => l.slice(5)); },
    async events() { return (await read('events.jsonl')).split('\n').filter(Boolean).map(l => JSON.parse(l)); },
    async sql() { return read('psql.sql'); },
    async state() { return JSON.parse(await read('state.json')); },
  };
}

export const insert = (name, digest = DIGEST) => ({ _ACTION: 'INSERT', _DIGEST: `${REPO}/${name}@${digest}`, _TAG: `${REPO}/${name}:v3` });
export const labelsFor = (pid = PID, extra = {}) => ({ 'fitt.crn.project_id': pid, 'fitt.crn.job_id': 'job-1', 'fitt.crn.build_no': '3', 'fitt.crn.contract': '1', 'fitt.crn.schedule': '[]', ...extra });
