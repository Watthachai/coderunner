import fs from 'node:fs/promises';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn } from 'node:child_process';
import { randomBytes, createHash } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';

export function validateContract(contract, pkg, lock, docs) {
  if (contract.schema_version !== 1 || typeof contract.database !== 'boolean') throw new Error('Invalid crn-delivery.json');
  for (const key of ['BUILD_NOTES.md', 'TEST_CASES.md', 'PORT_CHECKLIST.md']) {
    if (!docs[key]?.trim()) throw new Error(`Missing or empty ${key}`);
  }
  if (docs['PORT_CHECKLIST.md'].split('\n').some(s => /^\s*[-*]\s*\[ \]/.test(s) && !s.includes('[orphan]'))) throw new Error('Required port checklist items remain open');
  if (!Number.isInteger(lock.lockfileVersion) || lock.lockfileVersion < 2 || !lock.packages?.['']) throw new Error('An npm v2+ lockfile is required');
  const required = ['typecheck', 'test:unit', 'test:smoke', 'build', 'start', ...(contract.database ? ['db:generate', 'db:deploy', 'db:seed'] : [])];
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
export function isolatedEnvironment(source) {
  // CLI/browser runtime settings only. Never inherit host application credentials.
  const allowed = ['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'LANG', 'LC_ALL', 'DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_CONFIG', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH', 'PLAYWRIGHT_BROWSERS_PATH'];
  return Object.fromEntries(allowed.filter(key => source[key] !== undefined).map(key => [key, source[key]]));
}
export async function verify(cwd = process.cwd(), dockerfile = fileURLToPath(new URL('../assets/Dockerfile', import.meta.url))) {
  const receipt = { schema_version: 1, status: 'failed', checks: [], started_at: new Date().toISOString() };
  const children = new Set();
  let stopped = false, dbName, appName, operationName, networkName, imageName, scratch;
  const stop = () => { stopped = true; for (const child of children) { try { process.kill(-child.pid, 'SIGKILL'); } catch {} } };
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
  const timeout = setTimeout(stop, 15 * 60_000);
  // Do not forward runtime customer credentials to test setup. DB URL and bootstrap
  // values are always isolated below. Other integration tests must use mocks.
  const env = { ...isolatedEnvironment(process.env), CI: '1', NEXT_TELEMETRY_DISABLED: '1', DATABASE_URL: 'postgresql://build:build@127.0.0.1:1/build', AUTH_SECRET: randomBytes(32).toString('hex'), BOOTSTRAP_ADMIN_EMAIL: 'initial@example.test', BOOTSTRAP_ADMIN_PASSWORD: randomBytes(24).toString('hex'), FITT_FEEDBACK_URL: '' };
  env.CRN_TEST_ADMIN_EMAIL = env.BOOTSTRAP_ADMIN_EMAIL;
  env.CRN_TEST_ADMIN_PASSWORD = env.BOOTSTRAP_ADMIN_PASSWORD;
  const secrets = new Set([env.AUTH_SECRET, env.BOOTSTRAP_ADMIN_PASSWORD]);
  const redact = value => { for (const secret of secrets) value = value.split(secret).join('[redacted]'); return value; };
  function start(command, args, extra = {}) {
    if (stopped) throw new Error('Verification cancelled or timed out');
    const child = spawn(command, args, { cwd, env: { ...env, ...extra }, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
    children.add(child); child.on('close', () => children.delete(child));
    return child;
  }
  async function run(label, command, args, extra = {}) {
    console.error(`CRN verify: ${label}`);
    const child = start(command, args, extra);
    let stdout = '', stderr = '';
    child.stdout.on('data', b => { stdout = (stdout + b).slice(-12_000_000); });
    child.stderr.on('data', b => { stderr = (stderr + b).slice(-12000); });
    const code = await new Promise((resolve, reject) => { child.once('error', reject); child.once('close', resolve); });
    receipt.checks.push({ name: label, passed: code === 0 });
    if (code !== 0) {
      let detail = (stderr + '\n' + stdout).slice(-3500);
      for (const [key, value] of Object.entries(env)) if (/SECRET|TOKEN|PASSWORD|KEY|DATABASE_URL/.test(key) && value) detail = detail.split(value).join('[redacted]');
      throw new Error(`${label} failed (exit ${code}): ${redact(detail)}`);
    }
    return stdout;
  }
  const npm = (label, script, args = []) => run(label, 'npm', ['--silent', 'run', script, '--', ...args]);
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
    receipt.lockfile_sha256 = createHash('sha256').update(await fs.readFile(path.join(cwd, 'package-lock.json'))).digest('hex');
    scratch = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-verify-'));
    await run('locked install', 'npm', ['ci', '--no-audit', '--no-fund']);
    if (contract.database) await npm('generate client', 'db:generate');
    await npm('typecheck', 'typecheck');
    const unitFile = path.join(scratch, 'unit.json');
    await npm('unit tests', 'test:unit', ['--reporter=json', `--outputFile=${unitFile}`]);
    receipt.unit_tests = assertTestResults('unit', JSON.parse(await fs.readFile(unitFile, 'utf8')));
    await npm('production build without DB', 'build');
    await fs.access(path.join(cwd, '.next/standalone/server.js'));
    const suffix = randomBytes(8).toString('hex');
    imageName = `crn-verify:${suffix}`;
    networkName = `crn-verify-${suffix}`;
    const runtimeDockerfile = path.join(scratch, 'Dockerfile');
    await fs.copyFile(dockerfile, runtimeDockerfile);
    await fs.writeFile(`${runtimeDockerfile}.dockerignore`, 'node_modules\n.next\n.git\n.env\n.env.*\n.claude\n');
    await run('build customer runtime image', 'docker', ['build', '--tag', imageName, '--file', runtimeDockerfile, cwd]);
    await run('isolated runtime network', 'docker', ['network', 'create', networkName]);
    let runtimeDatabaseURL = '';
    const runtimeEnv = ['-e', 'DATABASE_URL', '-e', 'AUTH_SECRET', '-e', 'BOOTSTRAP_ADMIN_EMAIL', '-e', 'BOOTSTRAP_ADMIN_PASSWORD'];
    if (contract.database) {
      dbName = `crn-verify-db-${suffix}`;
      const pw = randomBytes(24).toString('hex');
      secrets.add(pw);
      await run('start isolated database', 'docker', ['run', '-d', '--name', dbName, '--network', networkName, '--network-alias', 'database', '-e', 'POSTGRES_USER=crn_test', '-e', 'POSTGRES_PASSWORD', '-e', 'POSTGRES_DB=crn_test', '-p', '127.0.0.1::5432', 'postgres:16-alpine'], { POSTGRES_PASSWORD: pw });
      const binding = (await run('database port', 'docker', ['port', dbName, '5432/tcp'])).trim().split(':').pop();
      env.DATABASE_URL = `postgresql://crn_test:${pw}@127.0.0.1:${binding}/crn_test?schema=public`;
      runtimeDatabaseURL = `postgresql://crn_test:${pw}@database:5432/crn_test?schema=public`;
      let ready = false;
      for (let i = 0; i < 40 && !stopped; i++) {
        try { await run('database readiness', 'docker', ['exec', dbName, 'pg_isready', '-U', 'crn_test']); ready = true; break; } catch { await new Promise(r => setTimeout(r, 500)); }
      }
      // Failed readiness probes are retries, not failed release checks.
      receipt.checks = receipt.checks.filter(c => c.name !== 'database readiness' || c.passed);
      if (!ready) throw new Error('Isolated test database did not start');
      operationName = `crn-verify-task-${suffix}`;
      const inImage = (label, script) => run(label, 'docker', ['run', '--rm', '--name', operationName, '--network', networkName, ...runtimeEnv, '--entrypoint', 'npm', imageName, '--silent', 'run', script], { DATABASE_URL: runtimeDatabaseURL });
      await inImage('apply migrations in customer image', 'db:deploy');
      await inImage('first-install bootstrap in customer image', 'db:seed');
      env.BOOTSTRAP_ADMIN_EMAIL = 'changed@example.test'; env.BOOTSTRAP_ADMIN_PASSWORD = randomBytes(24).toString('hex');
      secrets.add(env.BOOTSTRAP_ADMIN_PASSWORD);
      await inImage('repeat bootstrap with changed inputs', 'db:seed');
      delete env.BOOTSTRAP_ADMIN_EMAIL; delete env.BOOTSTRAP_ADMIN_PASSWORD;
      await inImage('restart without bootstrap inputs', 'db:seed');
    }
    await run('browser installation', 'node', ['node_modules/@playwright/test/cli.js', 'install', 'chromium']);
    appName = `crn-verify-app-${suffix}`;
    await run('start customer image', 'docker', ['run', '-d', '--name', appName, '--network', networkName, '-p', '127.0.0.1::3000', ...runtimeEnv, imageName], { DATABASE_URL: runtimeDatabaseURL });
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
    if (stopped) throw new Error('Verification cancelled or timed out');
    receipt.status = 'passed';
  } catch (error) { receipt.error = redact(error.message); }
  finally {
    stop(); clearTimeout(timeout); process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop);
    const cleanup = args => new Promise(resolve => { const c = spawn('docker', args, { env: isolatedEnvironment(process.env), stdio: 'ignore', timeout: 8000, killSignal: 'SIGKILL' }); c.on('error', resolve); c.on('close', resolve); });
    const containers = [appName, operationName, dbName].filter(Boolean);
    if (containers.length) await cleanup(['rm', '-f', ...containers]);
    await Promise.all([networkName && cleanup(['network', 'rm', networkName]), imageName && cleanup(['image', 'rm', '-f', imageName])]);
    if (scratch) await fs.rm(scratch, { recursive: true, force: true });
    receipt.finished_at = new Date().toISOString();
    await fs.writeFile(path.join(cwd, 'CRN_VERIFICATION.json'), JSON.stringify(receipt, null, 2) + '\n');
  }
  return receipt;
}
if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  const result = await verify(process.cwd(), process.argv[2]);
  console.log(JSON.stringify(result));
  process.exitCode = result.status === 'passed' ? 0 : 1;
}
