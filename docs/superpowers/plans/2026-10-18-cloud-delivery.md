# Cloud Delivery (local or cloud from FITT Builder's export) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A project exported from FITT Builder with "cloud" reaches UAT on Cloud Run by itself — registration, deploy, link and one-time admin password back on the project — from the same image a "local" export delivers.

**Architecture:** CRN on the MacMini keeps building as today, stamps labels into every image and pushes every image to Artifact Registry. A deploy script of CRN's running in Cloud Build (triggered per image and by a 5-minute sweep) asks FITT Builder's new integration API what the project wants, registers it on first use, deploys by digest behind a health check and reports events back. FITT Builder stores the choice, the events and the sealed one-time password, and shows them in the export dialog.

**Tech Stack:** Go (CRN build pipeline), Node 22 ESM (skill assets, release checker, `node --test`), bash + gcloud (deploy side, tested with fake `gcloud`/`curl`/`psql`), Next.js 16 + TypeScript + Supabase + vitest (FITT Builder).

**Spec:** `docs/superpowers/specs/2026-10-18-cloud-delivery-design.md` (commit `f2ea9d6`, approved 2026-10-18)

**Rulings made while planning:**
- The spec's "check the real image before push" runs inside the release checker (`verify-delivery.mjs`), not as a new Go step: the checker already builds the customer image from the delivered Dockerfile and CRN runs it independently before any push, so making its runs read-only also covers the browser smoke tests, not only start-up. Cost if wrong: one Go step to add later.
- The app's scheduler and the checker each carry the schedule validation: `VerifyDelivery` ships the checker alone, so it cannot import the asset. One test runs both against the same cases.
- Upload buckets are named `<gcp project>-uat-<id8>-uploads`, not `crn-uat-<id8>-uploads`: bucket names are global across all of GCP.
- `new-uat.sh` goes away; registration happens in the deploy side.
- The Cloud box lives in the export dialog, where the choice is made.

**Executor note:** the author executes this plan inline in the same session, today. Task bodies give exact interfaces, file paths, test cases and the code that carries decisions; long glue (the deploy script body, React markup) is specified by behaviour + tests and written during execution. Deviations are ledgered as rulings.

## Global Constraints

- Region `asia-southeast1` everywhere a default region appears.
- MacMini holds only the `crn-pusher` credential; FITT Builder holds no GCP credential; registration powers live only in `crn-uat-deployer` inside Cloud Build.
- Secret values go to Secret Manager and HTTP bodies on stdin, never as command arguments and never echoed to logs (exception: the Cloud Scheduler header flag, which gcloud only takes as an argument).
- One image for local and cloud; the export choice never changes the build.
- User-visible strings in FITT Builder and in deploy events are Thai.
- CRN: never stage `frontend/package.json`, `frontend/package-lock.json`, root `package-lock.json` in the main checkout. CRN pipeline changes stay on `feature/gcp-mirror` and do not land on `dev` before the FTCDV 20/24 Oct deliveries unless the user says so.
- FITT Builder: work in a new worktree from `origin/main`; never touch `~/fitt-builder-v2`; migration number confirmed with the fitt-builder session before commit; never push `main` (it deploys production) without explicit user approval; bump `lib/changelog.ts` (feature → MINOR: `0.113.0`).
- No real GCP, no paid builds, no migrations applied to any database in this plan: those wait for the user.
- Commits end with the session's Co-Authored-By / Claude-Session trailers.

## Review Focus

1. A registration that died half way (secret created, DB user missing; DB closed but bucket missing) must converge on the next run, not fail on "already exists" — Task 5 test "registration resumes after a partial run".
2. Admin email, project name or log text containing quotes, `$`, backslashes or newlines must not break the bash script, the JSON bodies or the SQL — Task 5 test "hostile strings stay data"; Task 8 validates email shape.
3. Two concurrent "show password" clicks must reveal the password at most once — Task 9 test "a second reveal finds nothing" (conditional update on `ciphertext is not null`, checked by affected rows).
4. A candidate revision that fails `/api/health` must leave traffic where it was, and a failed `ready` POST must not mark the password delivered — Task 5 tests "unhealthy candidate keeps traffic" and "password stays undelivered when FITT Builder refuses".
5. FITT Builder unreachable or 5xx during a run must stop before any GCP mutation for that project and exit non-zero (Cloud Build shows it; the sweep retries) — Task 5 test "FITT Builder down: no mutation".

---

## Part A — CRN (`/Users/itswatthachai/fitt-coderunner/.claude/worktrees/gcp-mirror`, branch `feature/gcp-mirror`)

### Task 0: Rebase the branch on dev

- [ ] `git rebase dev` (branch is local-only, 4 commits on `9a1d597`; dev is `8ffdd98`). Expected: clean rebase.
- [ ] `go test ./... && node --test tests/*.test.mjs` → all pass (baseline).

### Task 1: Singapore by default

**Files:** Modify `deploy/gcp/setup.sh`, `deploy/gcp/new-uat.sh`, `deploy/gcp/README.md`, `tests/gcp-uat-deploy.test.mjs`

- [ ] **Test** (append to `tests/gcp-uat-deploy.test.mjs`):

```js
test('setup defaults to Singapore', () => {
  const res = spawnSync('bash', [path.resolve('deploy/gcp/setup.sh')], { env: { PATH: process.env.PATH, PROJECT_ID: 'fitt-proj', DRY_RUN: '1' }, encoding: 'utf8' });
  assert.equal(res.status, 0, res.stderr);
  assert.match(res.stdout, /--location=asia-southeast1 /);
  assert.match(res.stdout, /--region=asia-southeast1 /);
  assert.doesNotMatch(res.stdout, /asia-southeast3/);
});
```

- [ ] Run → FAIL (`asia-southeast3`).
- [ ] `REGION="${REGION:-asia-southeast1}"` in both scripts; README: region line → สิงคโปร์ `asia-southeast1`, drop the Bangkok load-balancer note, keep "ใช้ URL `*.run.app`"; test fixtures `asia-southeast3` → `asia-southeast1`.
- [ ] Run → PASS. Commit `fix(deploy): GCP defaults to Singapore`.

### Task 2: Every image says what it is

**Files:** Create `internal/buildstep/labels.go`, `internal/buildstep/labels_test.go`; Modify `internal/buildstep/dockerbuild.go` (`BuildImage`), `internal/jobs/jobs.go` (`buildAndPushImage` + call site), `internal/buildstep/mirror_test.go` (caller)

**Interfaces — Produces:**
```go
const RuntimeContract = "1"
type ImageLabels struct {
	ProjectID, JobID, Commit string
	BuildNo                  int
	Schedule                 string // compact JSON array from ReadSchedule
}
func (l ImageLabels) Map() map[string]string // keys: fitt.crn.project_id, fitt.crn.job_id, fitt.crn.build_no, fitt.crn.contract, fitt.crn.schedule, org.opencontainers.image.revision (omitted when Commit == "")
func ReadSchedule(workDir string) (string, error) // missing file → "[]"; not a JSON array → error naming crn.schedule.json
func BuildImage(ctx context.Context, dir, dockerfile, tag string, labels map[string]string, logger *slog.Logger) error // adds "--label k=v" per key, sorted
```

- [ ] **Tests** (`labels_test.go`):

```go
func TestImageLabelsNameTheProjectAndTheContract(t *testing.T) {
	m := ImageLabels{ProjectID: "p-1", JobID: "j-1", Commit: "abc123", BuildNo: 7, Schedule: "[]"}.Map()
	want := map[string]string{"fitt.crn.project_id": "p-1", "fitt.crn.job_id": "j-1", "fitt.crn.build_no": "7",
		"fitt.crn.contract": RuntimeContract, "fitt.crn.schedule": "[]", "org.opencontainers.image.revision": "abc123"}
	if !reflect.DeepEqual(m, want) { t.Fatalf("got %v", m) }
	if _, ok := (ImageLabels{ProjectID: "p"}).Map()["org.opencontainers.image.revision"]; ok { t.Error("empty commit must not label a revision") }
}

func TestReadScheduleIsAnArrayOrNothing(t *testing.T) {
	dir := t.TempDir()
	if got, err := ReadSchedule(dir); err != nil || got != "[]" { t.Fatalf("missing: %q %v", got, err) }
	os.WriteFile(filepath.Join(dir, "crn.schedule.json"), []byte("[\n {\"name\": \"a\", \"path\": \"/api/cron/a\", \"every_minutes\": 15}\n]"), 0o644)
	if got, _ := ReadSchedule(dir); got != `[{"name":"a","path":"/api/cron/a","every_minutes":15}]` { t.Fatalf("compact: %s", got) }
	for _, bad := range []string{`{}`, `not json`} {
		os.WriteFile(filepath.Join(dir, "crn.schedule.json"), []byte(bad), 0o644)
		if _, err := ReadSchedule(dir); err == nil || !strings.Contains(err.Error(), "crn.schedule.json") { t.Errorf("%s: %v", bad, err) }
	}
}

func TestBuildImagePassesLabelsSorted(t *testing.T) {
	args := stubDocker(t, `exit 0`)
	if err := BuildImage(context.Background(), t.TempDir(), "Dockerfile", "reg/app:v1", map[string]string{"b": "2", "a": "1"}, discardLogger()); err != nil { t.Fatal(err) }
	if ran := readFile(t, args); !strings.Contains(ran, "--label a=1 --label b=2") { t.Errorf("ran: %s", ran) }
}
```
(`stubDocker`, `discardLogger`, `readFile` exist in the package's tests on this branch.)

- [ ] Run `go test ./internal/buildstep/ -run 'Labels|Schedule|PassesLabels'` → FAIL (undefined).
- [ ] Implement `labels.go` (Map builds the map; `ReadSchedule` = `os.ReadFile`, `errors.Is(err, fs.ErrNotExist)` → `"[]"`, `json.Unmarshal` into `[]json.RawMessage`, `json.Compact`). `BuildImage`: build args, then for sorted keys append `"--label", k+"="+v` before `dir`.
- [ ] `buildAndPushImage(ctx, workDir, spec, job, commitSHA, log)`: `schedule, err := buildstep.ReadSchedule(workDir)`; error → return `fmt.Errorf("crn.schedule.json: %w", err)` (fails the build like a docker failure); labels from job (`job.ProjectID.String()`, `job.ID.String()`, `job.BuildNo`). Call site passes `commitSHA`.
- [ ] Update `mirror_test.go` caller (`nil` labels).
- [ ] Run `go test ./...` → PASS. Commit `feat(build): every image names its project, build and runtime contract`.

### Task 3: The image runs anywhere

**Files:** Modify `internal/buildstep/dockerbuild.go` (`productionDockerfile`, `customerCompose`, `installMD`, `DemoEnvExample`, `NewDemoEnvExample`), `cmd/server/skillassets/assets/Dockerfile` (byte-identical copy), `internal/buildstep/dockerbuild_test.go`

- [ ] **Test:**

```go
// One image for a customer's server and for Cloud Run: uploads live in one
// directory a volume can back, npm writes only under /tmp, the schedule ships.
func TestCustomerImageRunsAnywhere(t *testing.T) {
	builder := productionDockerfile[strings.Index(productionDockerfile, "AS builder"):strings.Index(productionDockerfile, "AS runner")]
	if !strings.Contains(builder, "test -f crn.schedule.json || echo '[]' > crn.schedule.json") { t.Error("builder must default the schedule") }
	runner := productionDockerfile[strings.Index(productionDockerfile, "AS runner"):]
	for _, want := range []string{"ENV UPLOAD_DIR=/data/uploads", "ENV CRN_SCHEDULER=internal", "ENV NPM_CONFIG_CACHE=/tmp/npm-cache",
		"mkdir -p /data/uploads && chown node:node /data/uploads", "/app/crn.schedule.json ./crn.schedule.json"} {
		if !strings.Contains(runner, want) { t.Errorf("runner missing %q", want) }
	}
	dir := t.TempDir()
	writePkg(t, dir, `{"dependencies":{"next":"16"}}`)
	if _, err := WriteImageBundle(dir, "reg/app:v1", "reg", 4123, nil); err != nil { t.Fatal(err) }
	compose := readFile(t, filepath.Join(dir, "docker-compose.customer.yml"))
	for _, want := range []string{`CRON_SECRET: "${CRON_SECRET:-}"`, "- uploads:/data/uploads", "volumes:\n  uploads: {}"} {
		if !strings.Contains(compose, want) { t.Errorf("compose missing %q", want) }
	}
	install := readFile(t, filepath.Join(dir, "INSTALL.md"))
	for _, want := range []string{"/data/uploads", "CRON_SECRET"} { if !strings.Contains(install, want) { t.Errorf("INSTALL missing %q", want) } }
	raw, _ := json.Marshal(NewDemoEnvExample(4123))
	if !strings.Contains(string(raw), `"CRON_SECRET":""`) { t.Errorf("env example: %s", raw) }
}
```

- [ ] Run → FAIL.
- [ ] Dockerfile: builder after `COPY . .` add `RUN test -f crn.schedule.json || echo '[]' > crn.schedule.json`; runner: the three `ENV` lines after `CHECKPOINT_DISABLE`, `RUN mkdir -p /data/uploads && chown node:node /data/uploads` before `USER node`, `COPY --from=builder --chown=node:node /app/crn.schedule.json ./crn.schedule.json` after the other COPYs. Copy the template verbatim to `cmd/server/skillassets/assets/Dockerfile`.
- [ ] Compose: `CRON_SECRET: "${CRON_SECRET:-}"` in environment; `volumes:\n      - uploads:/data/uploads` under `app`; top-level `volumes:\n  uploads: {}`. INSTALL: new section `## Files and scheduled work` — uploads persist in the `uploads` volume mounted at `/data/uploads` (back it up with the database); scheduled jobs run inside the app (`CRN_SCHEDULER=internal`); `CRON_SECRET` optional locally (the app generates one per start when empty). `DemoEnvExample.CronSecret string \`json:"CRON_SECRET"\`` left empty.
- [ ] Run `go test ./...` → PASS (includes the existing Dockerfile-copy equality test). Commit `feat(build): one image runs on a customer's server and on Cloud Run`.

### Task 4: Run-anywhere rules in the skill, the scheduler, and a read-only release check

**Files:** Create `cmd/server/skillassets/assets/scheduler.mjs`, `cmd/server/skillassets/references/runs-anywhere.md`, `tests/scheduler.test.mjs`; Modify `cmd/server/skillassets/SKILL.md`, `cmd/server/builtin_skill.go`, `cmd/server/skillassets/scripts/verify-delivery.mjs` and its copy `internal/buildstep/verify-delivery.mjs`, `tests/delivery.test.mjs`

**Interfaces — Produces:**
```js
// assets/scheduler.mjs (copied unchanged into apps as lib/scheduler.mjs)
export const EVERY = [5, 10, 15, 20, 30, 60];
export function validateSchedule(entries) // throws Error naming the bad entry; returns entries
export function nextDelayMs(entry, nowMs) // every_minutes: to the next multiple of N minutes past the hour (UTC clock); daily_at "HH:MM" Asia/Bangkok (UTC+7)
export function startScheduler({ env = process.env, entries, fetchImpl = fetch, setTimeoutImpl = setTimeout, now = Date.now }) // external → returns null; internal → sets env.CRON_SECRET if empty, schedules each entry, POSTs http://127.0.0.1:${env.PORT||3000}${path} with x-cron-secret; returns { stop() }
export function cronAuthorized(request, env = process.env) // timing-safe compare of x-cron-secret; false when CRON_SECRET empty
// verify-delivery.mjs
export function validateSchedule(entries) // same rules, duplicated: the checker ships alone (VerifyDelivery writes only the script + Dockerfile)
export const HARDENED_RUN // ['--read-only','--tmpfs','/tmp','--tmpfs','/data/uploads:mode=1777','--cap-drop','ALL','--security-opt','no-new-privileges']
```
Schedule entry: `{ "name": "^[a-z0-9][a-z0-9-]{0,39}$" (unique), "path": "/api/cron/<name>", exactly one of "every_minutes" ∈ EVERY | "daily_at": "HH:MM" }`.

- [ ] **Tests** `tests/scheduler.test.mjs`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { validateSchedule, nextDelayMs, startScheduler, cronAuthorized } from '../cmd/server/skillassets/assets/scheduler.mjs';
import { validateSchedule as checkerValidate } from '../internal/buildstep/verify-delivery.mjs';

const good = [{ name: 'reminders', path: '/api/cron/reminders', every_minutes: 15 }, { name: 'daily-report', path: '/api/cron/daily-report', daily_at: '08:00' }];
const bad = [
  [{ name: 'A', path: '/api/cron/A', every_minutes: 15 }],
  [{ name: 'a', path: '/api/other', every_minutes: 15 }],
  [{ name: 'a', path: '/api/cron/a', every_minutes: 7 }],
  [{ name: 'a', path: '/api/cron/a', daily_at: '24:00' }],
  [{ name: 'a', path: '/api/cron/a', every_minutes: 15, daily_at: '08:00' }],
  [{ name: 'a', path: '/api/cron/a' }],
  [{ name: 'a', path: '/api/cron/a', every_minutes: 5 }, { name: 'a', path: '/api/cron/a', every_minutes: 10 }],
  {},
];
test('the app and the checker accept and refuse the same schedules', () => {
  for (const v of [validateSchedule, checkerValidate]) {
    assert.deepEqual(v(good), good);
    assert.deepEqual(v([]), []);
    for (const b of bad) assert.throws(() => v(b), undefined, JSON.stringify(b));
  }
});
test('next run: every N minutes on the clock, daily in Bangkok time', () => {
  assert.equal(nextDelayMs({ every_minutes: 15 }, Date.parse('2026-10-18T10:07:00Z')), 8 * 60_000);
  assert.equal(nextDelayMs({ every_minutes: 60 }, Date.parse('2026-10-18T10:00:00Z')), 60 * 60_000);
  assert.equal(nextDelayMs({ daily_at: '08:00' }, Date.parse('2026-10-18T00:30:00Z')), 30 * 60_000);
  assert.equal(nextDelayMs({ daily_at: '08:00' }, Date.parse('2026-10-18T01:00:00Z')), 23 * 3600_000);
});
test('internal mode calls its own handler with a secret it made; external mode does nothing', async () => {
  const calls = [], timers = [];
  const env = { CRN_SCHEDULER: 'internal', PORT: '3000' };
  const s = startScheduler({ env, entries: good, now: () => Date.parse('2026-10-18T10:07:00Z'), setTimeoutImpl: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, fetchImpl: async (url, init) => { calls.push({ url, init }); return new Response(null, { status: 204 }); } });
  assert.ok(s && env.CRON_SECRET && env.CRON_SECRET.length >= 32);
  assert.deepEqual(timers.map(t => t.ms), [8 * 60_000, (21 * 60 + 53) * 60_000]);
  await timers[0].fn();
  assert.equal(calls[0].url, 'http://127.0.0.1:3000/api/cron/reminders');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.headers['x-cron-secret'], env.CRON_SECRET);
  assert.equal(timers.length, 3, 'reschedules after running');
  const ext = { CRN_SCHEDULER: 'external' };
  assert.equal(startScheduler({ env: ext, entries: good, setTimeoutImpl: () => assert.fail('no timers') }), null);
  assert.equal(ext.CRON_SECRET, undefined);
});
test('a cron handler accepts only the secret', () => {
  const req = s => new Request('http://x/api/cron/a', { method: 'POST', headers: s ? { 'x-cron-secret': s } : {} });
  assert.equal(cronAuthorized(req('k'.repeat(32)), { CRON_SECRET: 'k'.repeat(32) }), true);
  assert.equal(cronAuthorized(req('wrong'), { CRON_SECRET: 'k'.repeat(32) }), false);
  assert.equal(cronAuthorized(req(''), { CRON_SECRET: '' }), false);
  assert.equal(cronAuthorized(req(null), { CRON_SECRET: 'k'.repeat(32) }), false);
});
```
Append to `tests/delivery.test.mjs`:
```js
import { HARDENED_RUN } from '../internal/buildstep/verify-delivery.mjs';
test('the checker runs the customer image read-only with no capabilities', () => {
  assert.deepEqual(HARDENED_RUN, ['--read-only', '--tmpfs', '/tmp', '--tmpfs', '/data/uploads:mode=1777', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges']);
  const src = readFileSync(new URL('../internal/buildstep/verify-delivery.mjs', import.meta.url), 'utf8');
  const runs = src.split('\n').filter(l => /'run', '(-d|--rm)'/.test(l) && l.includes('imageName') && !l.includes('FAKE_STATUS_SERVER'));
  assert.ok(runs.length >= 3, 'migrations, app and switch runs');
  for (const l of runs) assert.ok(l.includes('...HARDENED_RUN'), l.trim());
});
```

- [ ] Run `node --test tests/scheduler.test.mjs tests/delivery.test.mjs` → FAIL (missing module/exports).
- [ ] Write `scheduler.mjs` (Node built-ins only: `node:crypto` `randomBytes`, `timingSafeEqual`). `startScheduler` reads `entries` (default: `JSON.parse(readFileSync(path.join(process.cwd(), 'crn.schedule.json')))` validated), logs one line per failed call, never throws out of a timer.
- [ ] Checker: add `validateSchedule` and `HARDENED_RUN`; in `verify()` after `validateContract`, `const schedule = await fs.readFile(path.join(cwd, 'crn.schedule.json'), 'utf8').catch(e => e.code === 'ENOENT' ? '[]' : Promise.reject(e)); validateSchedule(JSON.parse(schedule)); receipt.checks.push({ name: 'schedule', passed: true });`; add `...HARDENED_RUN` to `inImage`, `start customer image`, and the switch run. Copy the file to `internal/buildstep/verify-delivery.mjs` byte-identical.
- [ ] `references/runs-anywhere.md` (Required on every build and edit): the four rules from the spec (uploads only under `process.env.UPLOAD_DIR ?? '/data/uploads'` + `/tmp`; scheduled work only through `crn.schedule.json` + `app/api/cron/<name>/route.ts` guarded by `cronAuthorized`, scheduler started from `instrumentation.ts` `register()` when `process.env.NEXT_RUNTIME === 'nodejs'`, copy `assets/scheduler.mjs` unchanged to `lib/scheduler.mjs`; no state across requests in memory; no request over 5 minutes) + "the checker runs your image read-only; a write anywhere else fails the release". SKILL.md "Read what applies": `- \`references/runs-anywhere.md\`: one image for a customer's server and for Cloud Run — uploads, scheduled work, state, request length. Required on every build and edit.`
- [ ] `builtin_skill.go`: embed + map `references/runs-anywhere.md`, `assets/scheduler.mjs`.
- [ ] Run `go test ./... && node --test tests/*.test.mjs` → PASS. Commit `feat(skill): apps follow run-anywhere rules, and the checker runs the image read-only`.

### Task 5: The deploy side registers and deploys by itself

**Files:** Rewrite `deploy/gcp/uat-deploy.sh`; sync `deploy/gcp/cloudbuild-uat.yaml`; Modify `deploy/gcp/setup.sh`, `deploy/gcp/README.md`; Delete `deploy/gcp/new-uat.sh`; Rewrite `tests/gcp-uat-deploy.test.mjs`

**Behaviour (the script, functions in one file so the yaml can embed it):**
- Env: `_ACTION` (`INSERT` from topic `gcr`, `SWEEP` from topic `crn-sweep`), `_DIGEST`, `_TAG`, `_REGION`, `_SQL_INSTANCE`, `_STATUS_URL`, `_FB_URL`, `_REPO` (`<region>-docker.pkg.dev/<project>/crn`), `PROJECT_ID`. FITT Builder token = `gcloud secrets versions access latest --secret=crn-deploy-fb-token`. Sourcing with `CRN_DEPLOY_LIB=1` defines functions without running `main` (promote.sh reuses them).
- `names IMAGE` → `service` (`uat-<slug>-<id8>`, existing rules), `runtime` (`crn-uat-<id8>`), `id8`, `db` (service with `_`), `bucket` (`${PROJECT_ID}-uat-<id8>-uploads`, ≤63).
- `image_labels IMAGE DIGEST` → JSON labels via registry API with `curl -fsS -H "Authorization: Bearer $(gcloud auth print-access-token)"`: manifest (`Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json`) → `config.digest` → blob → `.config.Labels`; parsing with `python3 -c`.
- `fb GET|POST PATH [BODY on stdin]` → `curl -fsS --max-time 20 -H "Authorization: Bearer …" "${_FB_URL}/v1/integrations/deploy${PATH}"`; non-2xx → return 1.
- `event PID STAGE [MESSAGE]` → JSON built by `python3` from env vars (`EV_*`), `fb POST /projects/$PID/events`.
- `deploy_one IMAGE DIGEST`:
  1. labels; no `fitt.crn.project_id` → `skip: image has no project label` exit 0.
  2. `fb GET /projects/$pid` (failure → `return 1` before any mutation); `target != cloud` → `skip: project is local`.
  3. `fitt.crn.contract` < 1 → event `failed` "image รุ่นเก่า ต้อง build ใหม่หนึ่งครั้งก่อนขึ้น cloud"; return 0.
  4. `secrets describe ${service}-database-url` missing → event `registering`. `register` (each step create-if-missing): runtime SA; project binding `roles/cloudsql.client`; deployer `roles/iam.serviceAccountUser` on the runtime SA; `sql databases create` if `databases describe` fails; DB user + `database-url` secret together (user missing → create with new password; secret missing but user exists → `users set-password`); `auth-secret` (hex 32), `cron-secret` (hex 32), `bootstrap-admin-email` (from FB), `bootstrap-admin-password` (hex 12 = 24 chars) — all via `--data-file=-`, each bound to the runtime SA; `close_database` when `database-url` lacks label `closed=yes`: install `postgresql-client` if no `psql`, fetch pinned `cloud-sql-proxy` v2 if absent, run it in the background on 127.0.0.1:5432, `PGPASSWORD` from secret `crn-uat-postgres-password`, `psql -v ON_ERROR_STOP=1 -v db=… -f -` with `ALTER DATABASE :"db" OWNER TO :"db"; REVOKE CONNECT, TEMPORARY ON DATABASE :"db" FROM PUBLIC;`, then `secrets update --update-labels=closed=yes`; bucket create if `storage buckets describe` fails + `roles/storage.objectAdmin` for the runtime SA.
  5. event `deploying`. Existing service (`run services describe` succeeds) → `run deploy … --no-traffic --tag candidate`; health on the `candidate` URL; pass → `run services update-traffic … --to-revisions <latestCreatedRevisionName>=100`; fail → event `failed` "build ใหม่ขึ้นไม่สำเร็จ ยังใช้ build ก่อนหน้า: …" with the last 20 log lines from `gcloud logging read`, return 0. New service → `run deploy` with `--execution-environment gen2 --add-volume name=uploads,type=cloud-storage,bucket=… --add-volume-mount volume=uploads,mount-path=/data/uploads`; health on the service URL; fail → event `failed`.
     Common flags: `--image IMAGE@DIGEST --region --port 3000 --service-account runtime --add-cloudsql-instances --set-secrets DATABASE_URL,AUTH_SECRET,CRON_SECRET,BOOTSTRAP_ADMIN_EMAIL,BOOTSTRAP_ADMIN_PASSWORD (=<service>-<name>:latest) --set-env-vars FITT_STATUS_URL=…,CRN_SCHEDULER=external --allow-unauthenticated --min-instances 0 --max-instances 3 --cpu 1 --memory 1Gi --labels crn-project=<id8>,crn-stage=uat --quiet`.
     Health = `curl -s -o /dev/null -w '%{http_code}' --max-time 10 URL/api/health` every 5 s up to `HEALTH_TRIES` (default 48) until 200.
  6. `sync_schedule SERVICE URL SCHEDULE_JSON`: python prints `name<TAB>cron` (`*/N * * * *`, `0 * * * *`, `MM HH * * *`); job `<service>-<name>`; `scheduler jobs describe` → `update http … --update-headers` else `create http … --headers x-cron-secret=…`, both with `--location _REGION --schedule --time-zone Asia/Bangkok --uri URL/api/cron/<name> --http-method POST`; then `scheduler jobs list --filter` and delete `<service>-*` jobs not wanted.
  7. event `ready` with url, digest, build_no; if `bootstrap-admin-password` secret lacks label `delivered=yes`, include it; label it only after the POST succeeded.
- `sweep`: `fb GET /pending` → for each `project_id`: newest `v<n>` tag of a package ending `-<id8>` in `gcloud artifacts docker images list _REPO --include-tags --format=json`; none → event `waiting_image`; else `deploy_one` (a failure is logged and the loop continues; exit status 1 if any failed).
- `main`: `INSERT` with a valid `_DIGEST` → `deploy_one`; `SWEEP` → `sweep`; else skip. Any error inside `deploy_one` after step 2 → event `failed` "ขั้น <step> ล้ม: <first line>" (trap), exit 1.

**setup.sh additions:** substitutions `_FB_URL=${FB_URL:-https://fitt-builder.fittbsa.com}`, `_REPO`; topic `crn-sweep`; trigger `crn-uat-sweep` (same inline config, `_ACTION=SWEEP`); `gcloud scheduler jobs create pubsub crn-sweep --schedule='*/5 * * * *' --topic=crn-sweep --message-body='{}' --location=REGION`; secret `crn-uat-postgres-password` (generated, applied with `sql users set-password postgres`, value via stdin file); secret `crn-deploy-fb-token` (value read with `read -rs` from the terminal; empty → skipped with a reminder); deployer roles `roles/secretmanager.admin roles/cloudsql.admin roles/storage.admin roles/cloudscheduler.admin roles/iam.serviceAccountCreator roles/logging.viewer` + `roles/resourcemanager.projectIamAdmin` limited by condition `api.getAttribute('iam.googleapis.com/modifiedGrantsByRole', []).hasOnly(['roles/cloudsql.client'])` + `roles/iam.serviceAccountAdmin` limited by condition `resource.name.startsWith('projects/-/serviceAccounts/crn-uat-') || resource.name.startsWith('projects/-/serviceAccounts/crn-prod-')`; Artifact Registry cleanup policy keeping the 5 newest versions per package; services `cloudscheduler.googleapis.com storage.googleapis.com`.

- [ ] **Tests** (`tests/gcp-uat-deploy.test.mjs`, rewritten): a harness writes fake `gcloud`, `curl`, `psql`, `cloud-sql-proxy` into a temp dir. Fake `curl` serves: registry manifest/blob (labels from the test), `GET …/projects/<id>` (target/admin from the test, or HTTP 503 when `fbDown`), `GET …/pending`, `POST …/events` (appends the stdin body to `events.jsonl`; HTTP 500 when `refuseEvents`), `…/api/health` (`HEALTH` env: `200` or `503`). Fake `gcloud` logs argv, reads stdin for `--data-file=-` into `secrets/<name>`, answers `describe` from a state dir the test seeds (`existing: ['secret:uat-shop-cc0b6195-database-url', 'service:uat-shop-cc0b6195', ...]`), `run services describe --format=json` with a candidate traffic URL, `latestCreatedRevisionName` `uat-shop-cc0b6195-00002-abc`. Cases:
  1. `a local project is left alone` — no `run deploy`, no events, exit 0.
  2. `a cloud project is registered, deployed and reported on first image` — calls include `iam service-accounts create crn-uat-cc0b6195`, `sql databases create uat_shop_cc0b6195`, `sql users create`, secrets `database-url auth-secret cron-secret bootstrap-admin-email bootstrap-admin-password` created with `--data-file=-` (no secret value in any argv), psql stdin has `ALTER DATABASE` + `REVOKE CONNECT, TEMPORARY`, `storage buckets create gs://fitt-proj-uat-cc0b6195-uploads`; deploy has `--image …@DIGEST`, `--execution-environment gen2`, the volume flags, `CRN_SCHEDULER=external`, `CRON_SECRET=uat-shop-cc0b6195-cron-secret:latest`, no `--no-traffic`; events stages `registering, deploying, ready`, ready carries url/digest/build_no `3` and `initial_admin_password` equal to the stored secret; afterwards `secrets update uat-shop-cc0b6195-bootstrap-admin-password --update-labels=delivered=yes`.
  3. `registration resumes after a partial run` — seeded `secret:…-database-url` (labels empty) and SA exists: no `iam service-accounts create`, no `sql users create`, still closes the database and creates the bucket, no `registering` event.
  4. `a new image moves traffic only after the candidate is healthy` — seeded service: deploy has `--no-traffic --tag candidate`, health hit the candidate URL, `run services update-traffic uat-shop-cc0b6195 --to-revisions uat-shop-cc0b6195-00002-abc=100`.
  5. `an unhealthy candidate keeps traffic` — `HEALTH=503`, `HEALTH_TRIES=2`: no `update-traffic`, last event `failed` with "ยังใช้ build ก่อนหน้า".
  6. `an image without labels is skipped; an old contract is reported` — no project label → no FB call; label without contract on a cloud project → event `failed` containing "build ใหม่หนึ่งครั้ง", no deploy.
  7. `FITT Builder down: no mutation` — `fbDown`: exit ≠ 0, no `create`/`deploy`/`update` calls.
  8. `password stays undelivered when FITT Builder refuses` — `refuseEvents` on ready: no `--update-labels=delivered=yes`.
  9. `schedule becomes Cloud Scheduler jobs, and stale ones go` — labels schedule `[{"name":"reminders","path":"/api/cron/reminders","every_minutes":15},{"name":"report","path":"/api/cron/report","daily_at":"08:30"}]`, existing job `uat-shop-cc0b6195-old`: `scheduler jobs create http uat-shop-cc0b6195-reminders … --schedule */15 * * * * … --time-zone Asia/Bangkok`, `30 08 * * *` for report, `scheduler jobs delete uat-shop-cc0b6195-old`.
  10. `the sweep deploys the newest image of each pending project` — pending `[A, B]`, artifacts list has `crn-demo-shop-cc0b6195` tags `v2`, `v10` and nothing for B: deploy uses the `v10` digest; B gets event `waiting_image`.
  11. `hostile strings stay data` — admin email `o'neil+$x@example.com`, project name `a "b" \ c`: exit 0, the email secret file equals the email byte for byte, events parse as JSON.
  12. Keep: `a long image name still makes a valid Cloud Run service name`, `events that are not a newly tagged image are ignored`, `the Cloud Build config embeds the script verbatim`, `setup defaults to Singapore`; add `setup creates the sweep, its trigger and the limited deployer roles` (DRY_RUN output contains `pubsub topics create crn-sweep`, `crn-uat-sweep`, `scheduler jobs create pubsub crn-sweep`, `modifiedGrantsByRole`).
- [ ] Run → FAIL. Implement. Run `node --test tests/gcp-uat-deploy.test.mjs` → PASS; `bash -n deploy/gcp/*.sh`; `shellcheck` if installed.
- [ ] README: registration is automatic; the only manual steps are setup (with the FB token prompt) and the pusher key; remove the new-uat section and the SQL step; list what the deployer may do.
- [ ] Commit `feat(deploy): a cloud project registers and deploys itself, and reports to FITT Builder`.

### Task 6: Production by one command

**Files:** Create `deploy/gcp/promote.sh`, `tests/gcp-promote.test.mjs`

**Behaviour:** `PROJECT_ID=<gcp> ./deploy/gcp/promote.sh <fitt-project-id> [--always-on] [--tier <tier>]`, run by a person logged in with their own account. Sources `uat-deploy.sh` with `CRN_DEPLOY_LIB=1` and `STAGE=production`, which makes `names` produce `prod-<slug>-<id8>`, `crn-prod-<id8>`, bucket `${PROJECT_ID}-prod-<id8>-uploads`, SQL instance `crn-prod-<id8>` (created if missing with `--tier`, backups 7, its `postgres` password generated into secret `prod-<slug>-<id8>-postgres-password`), events `env: production`, labels `crn-stage=production`, min instances `1` with `--always-on` else `0`. The digest = the image the UAT service serves now (`run services describe uat-… --format='value(spec.template.spec.containers[0].image)'`); no UAT → exit 1 "ยังไม่มี UAT". Optional `FITT_LICENSE_KEY` typed at a hidden prompt → secret `…-license-key` wired as `FITT_LICENSE_KEY`.

- [ ] **Tests:** `promote copies the UAT digest to a new production service with its own database` (instance create `crn-prod-cc0b6195`, service `prod-shop-cc0b6195`, image = UAT image, `--min-instances 0`, event env `production` with a password); `--always-on keeps one instance`; `no UAT, no production` (exit 1, no create).
- [ ] Run → FAIL → implement → PASS. Commit `feat(deploy): one command takes a customer's UAT digest to production`.

---

## Part B — FITT Builder (new worktree `~/fitt-builder-cloud-delivery`, branch `feature/cloud-delivery` from `origin/main`)

Setup: `git -C ~/fitt-builder-billing-api worktree add ~/fitt-builder-cloud-delivery -b feature/cloud-delivery origin/main && cd ~/fitt-builder-cloud-delivery && npm ci`; baseline `npx vitest run lib/__tests__/billing-*.test.ts` → pass. Ask the fitt-builder session for the next migration number (main has 0058); written below as `00NN`.

### Task 7: Data, credential and sealing

**Files:** Create `supabase/migrations/00NN_code_runner_deploy.sql`, `lib/deploy/types.ts`, `lib/deploy/store.ts`, `lib/deploy/authenticate.ts`, `lib/deploy/secret-box.ts`, `lib/deploy/pending.ts`, `scripts/deploy-binding.mjs`, tests `lib/__tests__/deploy-migration.test.ts`, `deploy-secret-box.test.ts`, `deploy-pending.test.ts`, `deploy-authenticate.test.ts`

**Interfaces — Produces:**
```ts
export type DeliveryTarget = "local" | "cloud";
export type DeployEnv = "uat" | "production";
export type DeployStage = "waiting_image" | "registering" | "deploying" | "ready" | "failed";
export interface DeliveryRow { project_id: string; target: DeliveryTarget; admin_email: string; updated_at: string }
export interface DeployEventRow { project_id: string; env: DeployEnv; stage: DeployStage; build_no: number | null; digest: string | null; url: string | null; message: string; at: string }
export interface DeployBinding { id: string }
// store.ts (service role)
findDeployBinding(tokenHash): Promise<DeployBinding | null>; touchDeployBinding(id, now): Promise<void>
loadDeployProject(id): Promise<{ id: string; name: string; delivery: DeliveryRow | null } | null>
loadCloudDeliveries(): Promise<DeliveryRow[]>; loadUatEvents(projectIds: string[]): Promise<DeployEventRow[]>
insertDeployEvent(e: Omit<DeployEventRow, "at"> & { binding_id: string }): Promise<void>
storeInitialPassword(projectId, env, sealed): Promise<void> // upsert only while revealed_at is null
// secret-box.ts
seal(plain: string, keyB64 = process.env.FITT_DEPLOY_SECRET_KEY): string // "v1.<iv>.<tag>.<ct>" base64url, AES-256-GCM; throws when the key is not 32 bytes
open(sealed: string, keyB64 = …): string
// pending.ts
pendingProjects(deliveries: DeliveryRow[], uatEvents: DeployEventRow[]): string[] // cloud projects whose newest UAT event is absent or not ready, unless their newest 3 events are failed with the newest build_no
export const MAX_FAILURES = 3;
// authenticate.ts
authenticateDeploy(authorization: string | null, now?): Promise<{ ok: true; binding: DeployBinding } | { ok: false; code: "unauthorized" | "internal"; message: string }>
```

**Migration (essentials):** producer check rebuilt with the 0054 loop → `('FITT_VOICE', 'FITT_BILLING', 'CODE_RUNNER')`; scope check → `case when producer in ('FITT_BILLING', 'CODE_RUNNER') then org_id is null and user_id is null else org_id is not null end`; `fittbuilder_project_delivery` (target check, `admin_email` shape check `admin_email = '' or admin_email ~* '^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$'`, `check (target = 'local' or admin_email <> '')`, `updated_by uuid default auth.uid()`), RLS policies select `fittbuilder_can_read_project(project_id, auth.uid())`, insert/update `fittbuilder_can_edit_project(project_id, auth.uid())`; `fittbuilder_project_deploy_events` (env/stage checks, digest `~ '^sha256:[a-f0-9]{64}$'`, url `~ '^https://'`, message ≤ 2000, `binding_id` → bindings on delete set null, index `(project_id, env, at desc)`), RLS select by read access only; `fittbuilder_project_initial_passwords (project_id, env, ciphertext text, created_at, revealed_at, revealed_by uuid, primary key (project_id, env))`, RLS on, no policies; `notify pgrst, 'reload schema';`.

- [ ] **Tests:** migration text (producer list, scope expression exact, three tables with RLS enabled, no policy on initial_passwords, reload notify); secret-box (round trip, tamper → throw, wrong key → throw, short key → throw, two seals differ); pending (no events → pending; ready → not; failed ×2 same build → pending; failed ×3 same build → not; failed ×3 then a failed with a newer build_no → pending; local projects never); authenticate (Bearer missing → unauthorized; unknown → unauthorized; store throws → internal; ok touches).
- [ ] FAIL → implement → `npx vitest run lib/__tests__/deploy-*.test.ts` PASS. `scripts/deploy-binding.mjs`: copy of billing-binding with producer `CODE_RUNNER`, prefix `fbd_`, text naming what the credential can do (read every project's delivery choice and first-admin email, write deploy events). Commit `feat(deploy): FITT Builder keeps each project's delivery choice and deploy events`.

### Task 8: The API the deploy side calls

**Files:** Create `app/v1/integrations/deploy/projects/[id]/route.ts`, `app/v1/integrations/deploy/projects/[id]/events/route.ts`, `app/v1/integrations/deploy/pending/route.ts`, `lib/deploy/event-input.ts`, test `lib/__tests__/deploy-routes.test.ts`

**Interfaces:** `parseEventInput(body: unknown): { ok: true; value: { env; stage; build_no: number | null; digest: string | null; url: string | null; message: string; initial_admin_password: string | null } } | { ok: false; message: string }` — env/stage enums; `build_no` integer ≥ 0 or absent; digest regex; url `https://` ≤ 500; message ≤ 2000 (trimmed); password 16–200 chars and only with `stage: "ready"`. Errors use `billingError` (shared `fittbuilder.error.v1` envelope) with `requestId`; rate limit `deploy:<binding>` 120/min.

- [ ] **Tests** (mock `@/lib/deploy/authenticate`, `@/lib/deploy/store`, `@/lib/rate-limit` like billing-routes): GET project → 200 `{project_id, name, target: "cloud", admin_email}`; no delivery row → `target: "local", admin_email: ""`; unknown/invalid id → 404; unauthorized → 401 envelope; rate limited → 429 with retry-after; store throws → 500 retryable. GET pending → `{ projects: [{ project_id }] }` from `pendingProjects`. POST events → 202 and `insertDeployEvent` called without the password; with password → `storeInitialPassword(id, "uat", sealed)` and `open(sealed)` round-trips; invalid stage / password on `failed` / bad digest → 400 `invalid_request`; key missing → 500 and no event stored (so the deploy side retries and keeps it undelivered).
- [ ] FAIL → implement → PASS. Commit `feat(deploy): the Code Runner deploy side reads choices and reports events`.

### Task 9: Choice, status and the one-time password in the export dialog

**Files:** Create `app/api/projects/[id]/cloud/route.ts`, `app/api/projects/[id]/cloud/initial-password/route.ts`, `components/studio/CloudDelivery.tsx`, test `lib/__tests__/deploy-user-routes.test.ts`; Modify `components/studio/FittcoreExportModal.tsx`

**Behaviour:**
- `GET /api/projects/{id}/cloud` (session required → 401): reads with the user's client (RLS) `fittbuilder_projects` (none → 404), delivery, newest event per env; admin client only for `password_waiting: { uat, production }` booleans (rows with `ciphertext is not null`); `needs_team` = UAT not pending by failure count (`MAX_FAILURES` newest failed with the same build). Response `{ target, admin_email, events: { uat, production }, needs_team, password_waiting }`.
- `PUT /api/projects/{id}/cloud` `{ target, admin_email }`: validate (cloud needs an email of the DB's shape, ≤ 254); upsert with the user's client; RLS refusal (`42501` or zero rows) → 403 "ไม่มีสิทธิ์แก้โปรเจกต์นี้".
- `POST /api/projects/{id}/cloud/initial-password` `{ env }`: `rpc('fittbuilder_can_edit_project', { pid, uid })` with the user's client → false → 403. Admin client: select the row's `ciphertext`; null/none → 404 "รหัสผ่านถูกแสดงไปแล้ว หรือยังไม่มี". Then `update({ ciphertext: null, revealed_at, revealed_by }).eq('project_id').eq('env').eq('ciphertext', sealed).select('project_id')`; zero rows (someone else revealed it in between) → the same 404; else `{ password: open(sealed) }` with `cache-control: no-store`.
- `CloudDelivery` (in the dialog above the file list): radio "ส่งแบบ local — ได้ไฟล์และ image ไปติดตั้งที่เครื่องลูกค้า" / "ส่งแบบ cloud — ขึ้น UAT บน GCP ให้"; email field when cloud ("อีเมลผู้ดูแลคนแรก"); button "บันทึกโดยไม่ build ใหม่"; status line per env (รอ image / กำลังเตรียมฐานข้อมูล / กำลังขึ้น UAT / พร้อมใช้ + link + build #n + time / ล้มเหลว + message + "ระบบจะลองใหม่เองใน 5 นาที" or "ต้องให้ทีมดู" when `needs_team`); "แสดงรหัสผ่านครั้งแรก" when `password_waiting` → shows it once with a copy button and the note "แสดงครั้งเดียว ส่งต่อทางช่องทางลับ". Exposes `save(): Promise<boolean>` via a ref or callback so "ยืนยันส่ง" saves the choice first and stops the send when saving fails.
- [ ] **Tests** (mock `@/lib/supabase/server`, `@/lib/supabase/admin`): GET 401 without session, 404 when RLS hides the project, shape with events and booleans (no ciphertext in the body); PUT validation 400s, 403 on RLS refusal, 200 upsert payload; reveal 403 without edit, 200 once then 404 (second conditional update affects zero rows), body never contains ciphertext.
- [ ] FAIL → implement → PASS; `npx tsc --noEmit` clean for touched files; `npx eslint` on touched files. Commit `feat(studio): choose local or cloud when sending to Code Runner, and see the UAT link and first password`.

### Task 10: Changelog and docs

**Files:** Modify `lib/changelog.ts` (new top entry `0.113.0`, date `2026-10-18`, Thai title "ส่งไป Code Runner แบบ local หรือ cloud ได้ และเห็นลิงก์ UAT ในหน้าโปรเจกต์", items: feature ×2, improvement ×1); Create `docs/integrations/code-runner-deploy.md` (endpoints, auth, events, password handling, token script, `FITT_DEPLOY_SECRET_KEY` = 32 random bytes base64 in the server env).
- [ ] Changelog test if one exists (`npx vitest run lib/__tests__/changelog*`) → PASS. Full `npx vitest run` → record result. Commit `docs(deploy): how Code Runner's deploy side talks to FITT Builder (0.113.0)`.

---

## Final

- [ ] Whole-branch review of both branches (fresh reviewer, most capable model) against spec + Review Focus; one fix pass with RED→GREEN per finding.
- [ ] Report to the user: what landed where (CRN `feature/gcp-mirror`, FB `feature/cloud-delivery`, both unpushed), rulings, and what waits on them: GCP project + setup + pusher key + FB token + `FITT_DEPLOY_SECRET_KEY`, migration number/apply, FB push to main, CRN merge to dev after the FTCDV deliveries, the real Hello World run.
