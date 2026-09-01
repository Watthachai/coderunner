# Jira — ทุก Task (แยกทีละอัน, ทั้งระบบ)

> ทุกงานที่ทำ (CRN `fitt-coderunner` + FBD `fitt-builder-v2`) — แต่ละ `##` = 1 issue, copy ตั้งแต่ Title ลงไปเข้า Jira ได้เลย
> `Done` = commit+push แล้ว (`feat/feedback-panel` == `dev`) · repo ระบุใน Notes
> **ครอบคลุมถึง `8ab3c89` (2026-09-01)** · EPIC A–F = รอบแรก · EPIC G–N = รอบ on-prem/FITTCORE integration · **EPIC O = throughput**

---

# EPIC A — CRN build-pipeline reliability

## [EPIC] CRN build-pipeline reliability
**Type:** Epic · **Status:** Done
รวมงานทำให้ build lifecycle เสถียร: กัน build ค้าง/ผี, reset workspace, resume คิว, cancel จริง.

## [CRN] Reconcile ghost builds ตอน startup
**Type:** Task · **Status:** Done (`45356f3`)
**Desc:** build ที่ค้าง `building` ตอน server restart กลางคัน → ค้างตลอด. ตอน boot flip เป็น failed + แจ้ง subscriber.
**Subtasks:** store `FailOrphanedBuilds` · manager `ReconcileOrphans` · เรียกใน main ก่อน ListenAndServe · domain interface
**AC:** restart → job ค้าง building กลายเป็น failed อัตโนมัติ + ได้ terminal event
**Notes:** repo CRN · `jobs.go`, `store.go`, `main.go`

## [CRN] Atomic workspace reset (ENOTEMPTY)
**Type:** Task · **Status:** Done (`122e007`)
**Desc:** `os.RemoveAll(workDir)` race กับ Spotlight/watcher → `unlinkat: directory not empty` ตอน rebuild.
**Subtasks:** `resetWorkspace` rename-aside (`.stale-<jobid>`) + best-effort remove + sweep · test
**AC:** rebuild project เดิมไม่ ENOTEMPTY; workDir สะอาด
**Notes:** repo CRN · `jobs.go`

## [CRN] Resume stranded queued jobs ตอน startup
**Type:** Task · **Status:** Done (`cef7d09`)
**Desc:** job ที่ queued ก่อน restart ไม่มีใคร kick worker → ค้าง queued.
**Subtasks:** store `OrgsWithQueuedJobs` · manager `ResumeQueued` · เรียกใน main หลัง reconcile
**AC:** restart → job ค้างคิวเริ่ม build อัตโนมัติ
**Notes:** repo CRN · `jobs.go`, `store.go`, `main.go`

## [CRN] Cancel build (queued/in-flight)
**Type:** Task · **Status:** Done (`81f2f41`)
**Desc:** เดิม cancel แค่ flip DB, build วิ่งต่อ เผา token. ทำให้ตัดจริง (ฆ่า claude process group).
**Subtasks:** per-job cancellable ctx (`m.cancels`) · `Cancel` = interrupt/drop · `finishCancelled` · API `POST /internal/jobs/{id}/cancel` · dashboard ปุ่ม 2-click
**AC:** กด cancel → claude ถูกฆ่าจริง หยุดเผา token; status=cancelled
**Notes:** repo CRN · `jobs.go`, `api.go`, `NowBuilding.tsx`

## [CRN] First-class build_cancelled status
**Type:** Task · **Status:** Done (`3b33bbf`)
**Desc:** cancel เคยโชว์เป็น "failed" ใน activity feed. แยกเป็น status/event ของตัวเอง.
**Subtasks:** migration `0009` (คลาย CHECK) · domain `EventBuildCancelled` · emit `build_cancelled {reason}` · ActivityFeed สีเทา · contract doc
**AC:** cancel → activity โชว์ "cancelled" เทา ไม่ใช่ failed แดง
**Notes:** repo CRN · migration 0009, `jobs.go`, `ActivityFeed.tsx`

---

# EPIC B — Demo runnability

## [CRN] Per-project docker-compose port
**Type:** Task · **Status:** Done (`f9e0d05`)
**Desc:** demo default port 3000 ชนกับ studio/dashboard/demo อื่น. ให้ port ต่อ-project (4000–4999).
**Subtasks:** `ScaffoldPort` (hash project) · render `{{PORT}}` ใน compose/QUICKSTART · ready message โชว์ URL · test
**AC:** `docker compose up` demo ไม่ชน; port ต่อ demo; override ด้วย `APP_PORT` ได้
**Notes:** repo CRN · `scaffold.go`

---

# EPIC C — On-prem demo image delivery

> per-task blocks ละเอียดอยู่ที่ [jira-onprem-image-tasks.md](jira-onprem-image-tasks.md) — สรุปสั้นที่นี่

## [EPIC] On-prem demo image delivery
**Type:** Epic · **Status:** Core done (C1–C5, amd64) · air-gap/incremental-migration/E2E = Todo
**Desc:** CRN build → app+migrate image (opaque, amd64) + docker-compose.customer.yml → ลูกค้ารันเครื่องตัวเอง: schema+seed auto, ไม่มี source, data local.
**AC:** `docker compose -f docker-compose.customer.yml up -d` บนเครื่อง amd64 → demo ทำงาน, ไม่เห็น source

- **App image (opaque)** — Done `bdbab56`
- **Migrate image (migrate-on-start)** — Done `d05245a`
- **Build amd64** — Done `d05245a`
- **Customer compose + INSTALL** — Done `399957b`
- **Tarball air-gap** — Done · **Incremental migration** — additive done (db push) / destructive = Todo · **E2E amd64** — Todo

---

# EPIC D — Integration & contract

## [CRN] Feedback widget payload spec
**Type:** Task · **Status:** Done (`6a2841b`)
**Desc:** เอกสาร payload ที่ปุ่ม feedback ยิง (project_id/category/priority/note/page_url + nested pins/box/region_shot/viewport) ให้เพื่อนทำ API รับ.
**AC:** เพื่อนอ่านแล้วสร้าง endpoint รับได้; ชี้ widget มา API เพื่อนผ่าน `CRN_FEEDBACK_INGEST_URL`
**Notes:** repo CRN · `feedback-widget-payload.md`

## [CRN] FTC DV HTTP callback (§3)
**Type:** Task · **Status:** Done (session ก่อน)
**Desc:** `CRN_FTC_DV_CALLBACK_URL` → POST building/released/failed + git_remote/git_branch. best-effort.
**AC:** set URL → FITTCORE ได้ callback ต่อ lifecycle
**Notes:** repo CRN · contract §3

## [CRN/Ops] Read-only DB role สำหรับ consumer (ftcdv)
**Type:** Task · **Status:** Done
**Desc:** role `ftcdv` (SELECT build_events + UPDATE notified_ftcdv). connection string ส่งเพื่อน. verify แล้ว (อ่าน build_events ได้, ตารางอื่น denied).
**AC:** เพื่อนต่อ DB อ่าน build_events แบบ read-only ได้
**Notes:** ops · `psql` role บน DB

## [CRN] build_events: image_ref + status ให้ consumer
**Type:** Task · **Status:** Done (`122e069`)
**Desc:** build_done payload เพิ่ม `image_ref` + git → consumer รู้ location ผ่าน DB. status = `event_type`.
**AC:** poll build_events → รู้สถานะ + `docker pull image_ref`
**Notes:** repo CRN · `jobs.go`, `fittcore-consumer-guide.md`

## [CRN] Example runtime env ใน build_done + callback
**Type:** Task · **Status:** Done (`b009b34`)
**Desc:** consumer ได้ `image_ref` แต่ไม่รู้ว่า image ต้องการ env อะไรถึงจะรัน → ต้องเดา `DATABASE_URL`/port เอง. ใส่ `env` (ตัวอย่าง runtime env contract) ทั้งใน payload `build_done` และ callback `released`.
**Subtasks:** `DemoEnvExample` struct + `NewDemoEnvExample(port)` · `ftcCallback` รับ env param (5 call sites) · `donePayload` เพิ่ม field · contract §2/§3 · test assert env
**AC:** FITTCORE อ่าน `env` จาก callback แล้ว inject ตอน `docker run` ได้เลย ไม่ต้องถาม
**Notes:** repo CRN · `dockerbuild.go`, `jobs.go` · เป็น **ตัวอย่าง** — ไม่มีค่าจริง bake ใน image

## [CRN] Deployment presets doc
**Type:** Task · **Status:** Done (`c286c9c`,`f53cf8d`)
**Desc:** env knobs + preset single-box/docker/DNS + no-hardcoded-IP.
**AC:** deploy เครื่องใหม่ = แก้ .env อย่างเดียว (มี preset ให้)
**Notes:** repo CRN · `deployment-config.md`

---

# EPIC E — FBD local dev

## [FBD] Direct-CRN mode
**Type:** Task · **Status:** Done (`c050b57`)
**Desc:** `/api/fittcore` ยิงตรง CRN local เมื่อ `FITTCORE_DIRECT_CRN_URL` set (ข้าม Gateway) — test flow FBD→CRN บนเครื่องเดียว. prod เว้นว่าง.
**AC:** ตั้ง env + build จาก studio → job เข้า CRN local; prod ไม่กระทบ
**Notes:** repo FBD · `app/api/fittcore/route.ts`

---

# EPIC F — Ops / environment

## [Ops] macOS Spotlight ENOTEMPTY mitigation
**Type:** Task · **Status:** Done
**Desc:** Spotlight index race `rm -rf .next`/workspaces → corrupt manifests. แก้ด้วย `.metadata_never_index` marker + `mv`-based clear.
**AC:** `rm -rf .next` ทำงานปกติ; dev server ไม่พังจาก manifest เสีย
**Notes:** ops · `.metadata_never_index` ที่ repo root

## [Ops] Turbopack workspace-root pin
**Type:** Task · **Status:** Done (session ก่อน)
**Desc:** stray `~/package-lock.json` → Next เลือก home เป็น root → dev server ปิดเอง. แก้ `turbopack.root` pin.
**AC:** dev server ไม่ปิดเอง
**Notes:** repo CRN · `frontend/next.config.ts`

---

# EPIC G — Image-only delivery & self-migrating image

## [EPIC] Image-only delivery + self-migrating image
**Type:** Epic · **Status:** Done
**Desc:** ส่งมอบเป็น **image อย่างเดียว** (เพื่อน: "อย่าเอา clone git เอาแค่ image") และ image ดูแล schema ตัวเอง โดย **DB อยู่นอก image** (เพื่อน provision ต่อ app) — ไม่มี migrate image แยก, ไม่มี postgres ใน image
**AC:** `docker pull` + inject `DATABASE_URL` → demo รันได้ schema ตรง ไม่ต้องแตะ source

## [CRN] Image-only release — build fail ถ้า push image ไม่ได้
**Type:** Task · **Status:** Done (`3e8beee`)
**Desc:** ตอน `CRN_BUILD_IMAGE=true` image คือ deliverable แต่ `buildAndPushImage` เป็น best-effort → docker fail แค่ log แล้ว job ยัง `done` ด้วย `imageRef="branch:main"` → consumer fallback ไป `git clone` → พังกับ private repo (prod เพื่อนเจอเคสนี้ตรง ๆ)
**Subtasks:** `buildAndPushImage` return error · fail ทั้ง build ผ่าน `finishFailed` เมื่อ build/push app image ไม่ผ่าน · air-gap tarball ยัง best-effort · contract §2/§3 · de-brand stream label
**AC:** image pipeline on → `image_ref` เป็น image ที่ pull ได้เสมอ; `branch:<name>` โผล่เฉพาะ legacy git-mode
**Notes:** repo CRN · `jobs.go`

## [CRN] ใช้ `prisma db push` แทน `migrate deploy`
**Type:** Task · **Status:** Done (`7c123d3`)
**Desc:** `migrate deploy` fail ด้วย checksum/ประวัติ migration ที่ demo ไม่มี → ใช้ `db push` ที่ sync schema ตรงจาก `schema.prisma`
**AC:** deploy ซ้ำ schema ตรงเสมอ ไม่ติด migration history
**Notes:** repo CRN · **ไม่ใส่ `--accept-data-loss`** (ผู้ใช้สั่ง: ข้อมูลต้องอยู่ครบ) → เพิ่ม column แบบมี default ผ่าน, drop ถูกบล็อก

## [CRN] Self-migrating app image + external DB (ตัด migrate image)
**Type:** Task · **Status:** Done (`d2be02a`)
**Desc:** สถาปัตยกรรมตามเพื่อน: DB อยู่นอก image, แอป migrate ตัวเองตอน start. entrypoint = `db push` → seed (ถ้าเปิด) → `node server.js`
**Subtasks:** `dbtools` stage bake prisma CLI + tsx · entrypoint script · ลบ `migrateDockerfile`/`MigrateImageTag` · `customerCompose` เหลือ app อย่างเดียว + `${DATABASE_URL:?}` · `WriteImageBundle` ตัด migrate param · แก้ test ที่ค้าง
**AC:** image เดียว + `DATABASE_URL` ภายนอก → start เอง schema ตรง ข้อมูลเดิมไม่หาย
**Notes:** repo CRN · `dockerbuild.go` · fallback ฝั่งเพื่อน = pg_dump snapshot ก่อน deploy

## [CRN] Skill: Prisma guidance ตาม db-push
**Type:** Task · **Status:** Done (`5474d58`)
**Desc:** ปรับ `prisma-setup.md` ให้ตรงกับ pipeline จริง (db push / self-migrate / seed idempotent)
**AC:** demo ที่ generate ใหม่ deliver ผ่าน pipeline ได้โดยไม่ต้องแก้มือ
**Notes:** repo CRN · `skillassets/references/prisma-setup.md`

## [CRN] Skill: บังคับ `@default` ตามชนิดบนทุก required field
**Type:** Task · **Status:** Done (`66509b9`)
**Desc:** เพื่อนถาม — ถ้า version ใหม่เพิ่ม required column แล้ว DB มีข้อมูลอยู่ `db push` จะ backfill ไม่ได้ → demo crash ตอน start. แก้ที่ต้นทาง: บังคับ schema ต้องมี default
**Subtasks:** กฎตามชนิด (`String→""`, `Int→0`, `Boolean→false`, `DateTime→now()`, `Enum→`variant จริง, list→`[]`, FK→optional) · ห้าม blanket `@default("")` · §2 ใน prisma-setup.md
**AC:** เพิ่ม column ใน version ถัดไป → `db push` backfill แถวเก่าได้ ไม่ล้ม
**Notes:** repo CRN · `skillassets/references/prisma-setup.md`

---

# EPIC H — Build diagnosability & program version

## [CRN] Fix `-f Dockerfile` resolve ผิด working dir (root cause)
**Type:** Task · **Status:** Done (`4f1477e`)
**Desc:** build ล้มซ้ำ ๆ ด้วย `/go.sum: not found`, `FROM golang:1.23-alpine` — CRN ไป build **Dockerfile ของ Go server ตัวเอง** เพราะ `-f Dockerfile` resolve เทียบ **CWD ของ process** (repo root ของ CRN) ไม่ใช่ build context. build มือใน workspace เลยผ่าน (bug ถูกบัง)
**Subtasks:** `-f filepath.Join(dir, dockerfile)` (absolute path)
**AC:** build image ของ demo ใช้ Dockerfile ใน workspace เสมอ ไม่ว่ารันจากที่ไหน
**Notes:** repo CRN · `dockerbuild.go` · **detective**: เจอได้เพราะ `95282cf` เปิดให้เห็น output จริงก่อน

## [CRN] Surface docker output จริงใน error
**Type:** Task · **Status:** Done (`95282cf`)
**Desc:** error เดิมเหลือแค่ `exit status 1` ทำให้เดาสาเหตุไม่ได้ → wrap error พร้อม `tailLines(...)` 25 บรรทัดท้าย
**AC:** build fail → console เห็น docker output จริง
**Notes:** repo CRN · `dockerbuild.go` · commit นี้คือกุญแจที่ทำให้เจอ `4f1477e`

## [CRN] Stamp git revision (startup log + /healthz)
**Type:** Task · **Status:** Done (`b12949a`)
**Desc:** ไม่รู้ว่า CRN ที่รันอยู่เป็น commit ไหน → debug ข้ามเครื่องไม่ได้
**Subtasks:** `internal/buildinfo` อ่าน `debug.ReadBuildInfo()` (vcs.revision/time/modified) · log ตอน boot · `/healthz` คืน `{status, build}`
**AC:** `curl /healthz` บอก revision ที่รันอยู่
**Notes:** repo CRN · `buildinfo.go`, `main.go`, `api.go`

## [CRN] `make restart` ใช้ binary ที่ compile แล้ว
**Type:** Task · **Status:** Done (`46451f1`)
**Desc:** `revision=unknown` เพราะ `go run` ไม่ stamp VCS info
**Subtasks:** target `run-bin: build` · `restart` เรียก run-bin
**AC:** `go version -m bin/crn-server` เห็น vcs.revision; log boot ไม่ unknown
**Notes:** repo CRN · `Makefile`

---

# EPIC I — Build cost / efficiency

## [CRN] Source-hash dedup — reuse image เมื่อ prototype ไม่เปลี่ยน
**Type:** Task · **Status:** Done (`e608f36`)
**Desc:** retry/rebuild ที่ source เหมือนเดิมเผา Claude ใหม่ทุกครั้ง (~$5.60/รอบ)
**Subtasks:** `sourceHash(files)` sha256 (sort + length-prefixed) · migration `0010` (`projects.last_source_hash`, `last_image_ref`) · store `Get/SetProjectBuildCache` · `finishReused` re-emit image เดิม (ข้าม claude/git/docker) · เขียน cache ตอน done
**AC:** retry ที่ source เดิม → คืน image เดิมทันที ไม่เผา token; source เปลี่ยน → build ใหม่ปกติ
**Notes:** repo CRN · `jobs.go`, migration 0010 · edit mode ไม่ dedup

---

# EPIC J — Demo auth standardization

## [CRN] Seed dev login user จาก env
**Type:** Task · **Status:** Done (`e9b7d43`)
**Desc:** demo SISB login ไม่ได้ ("Access Denied") — prototype ใช้ mock `Math.random()`, Claude สร้าง login จริงจาก DB แต่ seed ไม่เคยรัน (`DEMO_SEED` ไม่ได้ตั้ง)
**Subtasks:** seed upsert Admin user จาก `DEV_EMAIL`/`DEV_PASSWORD` · ใส่ใน skill
**AC:** UAT login ด้วย credential จาก env ได้
**Notes:** repo CRN · skill assets

## [CRN] บังคับ login = env email+password ทุก demo + DEMO_SEED default ON
**Type:** Task · **Status:** Done (`1367970`)
**Desc:** prototype ทำ login คนละแบบ (SSO/allowlist/mock) → operator เข้าไม่ได้. **มาตรฐานเดียว**: ทุก demo login ด้วย `DEV_EMAIL`/`DEV_PASSWORD` เท่านั้น (เช็คกับ env ตรง ๆ ไม่ใช่ hashed password ใน DB)
**Subtasks:** SKILL.md §5a — login มาตรฐาน **override** ทั้ง auth ของ prototype และกฎ "faithful UI" (เฉพาะหน้า login) · seed upsert Admin · `DEMO_SEED` default ON (`${DEMO_SEED:-1} != "0"`)
**AC:** ทุก UAT เข้าได้ด้วย credential ชุดเดียวที่ operator รู้ ไม่ว่า prototype ทำ login แบบไหน
**Notes:** repo CRN · `SKILL.md`, `prisma-setup.md`, `dockerbuild.go`

---

# EPIC K — Skill versioning & diff UI

## [CRN] Version built-in skill เมื่อเนื้อหาเปลี่ยน
**Type:** Task · **Status:** Done (`1c3306c`)
**Desc:** `EnsureBuiltinSkill` re-seed ทุก boot แต่ไม่เคยบันทึก version → แก้ SKILL.md แล้ว deploy ไม่เหลือร่องรอย
**Subtasks:** เพิ่ม `WHERE ... IS DISTINCT FROM` (re-seed ที่ไม่เปลี่ยน = no-op ไม่ปั่น version) · `RecordSkillVersion` snapshot body+files
**AC:** แก้ skill + restart → ได้ version ใหม่ พร้อม timestamp และ diff ได้
**Notes:** repo CRN · `store.go`

## [CRN] Diff เทียบ v_{n-1} → v_n
**Type:** Task · **Status:** Done (`40184ad`)
**Desc:** history เดิมเทียบ version กับ editor ปัจจุบัน ไม่ใช่กับ version ก่อนหน้า
**AC:** คลิก version → เห็นว่ารอบนั้นเปลี่ยนอะไรจากรอบก่อน
**Notes:** repo CRN · `frontend/app/skills/page.tsx`

## [CRN] Diff จริง (รวมไฟล์ reference ที่เนื้อหาเปลี่ยน) + กัน UI ล้นจอ
**Type:** Task · **Status:** Done (`46f915b`)
**Desc:** diff ยังโล่งเมื่อแก้แค่ content ของ reference file — `fileChanges` ดูแค่ path เพิ่ม/ลบ ไม่ดู content; body diff dump ทั้งไฟล์เป็น context; grid `1fr` มี `min-width:auto` → `<pre>` บรรทัดยาวดันหน้าเว็บกว้างเกินจอ
**Subtasks:** `fileChanges` คืน `modified` · `collapse()` แบบ git hunk (kind `gap` = "⋯ N unchanged") · `DiffPre` + per-file content diff · "ไม่มีการเปลี่ยนแปลง" เมื่อเหมือนกัน · `.skills-layout > * { min-width: 0 }`
**AC:** เห็นเขียว/แดงจริงทั้ง SKILL.md และไฟล์ reference; หน้าไม่ scroll แนวนอน
**Notes:** repo CRN · `linediff.ts`, `skills/page.tsx`, `globals.css`

---

# EPIC L — Feedback widget & edit request

## [CRN] Widget อ่าน ingest URL จาก runtime env
**Type:** Task · **Status:** Done (`609b8c9`)
**Desc:** `data-ingest` ถูก bake เป็น literal ตอน build → เปลี่ยนปลายทางต้อง rebuild. เปลี่ยนเป็น JSX expression `{process.env.FITT_FEEDBACK_URL ?? "<fallback>"}` (server-render ตอน request)
**AC:** operator ตั้ง `FITT_FEEDBACK_URL` ตอน run ได้เลย ไม่ต้อง rebuild
**Notes:** repo CRN · `feedback.go:146` · build เก่าต้อง rebuild 1 รอบ

## [CRN] Payload spec ฉบับ runtime-env
**Type:** Task · **Status:** Done (`4865e15`)
**Desc:** เขียน `feedback-widget-payload.md` ใหม่ให้ตรง — endpoint/body/field reference/curl/receiver checklist/ไม่มี callback
**AC:** ฝั่ง FTC DV ทำ receiver ได้จากเอกสารอย่างเดียว
**Notes:** repo CRN · `docs/feedback-widget-payload.md`

## [CRN] ปุ่ม 🐞 error — ตรวจจับ error อัตโนมัติ
**Type:** Task · **Status:** Done (`84830b9`)
**Desc:** เพิ่มปุ่มที่ 2 แยกจาก 💬 feedback — **โผล่เฉพาะตอนจับ error ได้** (มี badge นับ), ยืนยันก่อนส่งเหมือนกัน
**Subtasks:** จับ 4 ทาง (`window.onerror`, `unhandledrejection`, fetch ≥500, `window.__fittReportError` จาก React error boundary) · dedup by source+message+stack แรก (cap 50) · ส่ง `category:"error"` + `payload.error{message,stack,source,request,count}` · skill เพิ่ม `app/error.tsx` + `app/global-error.tsx` · doc §2a
**AC:** demo เกิด error → ปุ่ม 🐞 โผล่เอง กดส่งไปฝั่ง FTC DV ได้ พร้อม stack
**Notes:** repo CRN · `fitt-feedback.js`, `SKILL.md`

## [CRN] Edit-request contract §4
**Type:** Task · **Status:** Done (`9f15d2e`)
**Desc:** เพื่อนถามว่าเอา feedback ไปสั่งแก้ยังไง → `POST /internal/projects/{id}/edit` body `{change}` (ไม่มี auth, ไม่ต้องส่ง artifact, clone repo เดิม) → 202 → callback เหมือน build ปกติ
**AC:** FTC DV ส่ง edit request แล้วรับ callback `building`→`released` ได้
**Notes:** repo CRN · `docs/crn-integration-contract.md` §4

---

# EPIC M — Skill harness quality

## [CRN] Harness ตรงกับ image pipeline
**Type:** Task · **Status:** Done (`137adc7`)
**Desc:** image รัน `prisma db seed` ทุก deploy → seed **ต้อง idempotent** แต่ skill สอน `createMany/skipDuplicates` (idempotent เฉพาะเมื่อ field มี unique) และตัวอย่าง CRUD ใช้ `create`
**Subtasks:** seed ต้อง `upsert` keyed ด้วย id คงที่ · commit `package-lock.json` (image build ใช้ `npm ci`) · ระบุว่า CRN เขียน Dockerfile/compose เอง
**AC:** demo ที่ generate ใหม่ deploy ซ้ำได้โดยข้อมูลไม่ซ้ำ/ไม่ล้ม
**Notes:** repo CRN · `SKILL.md`

## [CRN] บังคับพอร์ตครบทุกไฟล์สำหรับ export ขนาดใหญ่
**Type:** Task · **Status:** Done (`42c50b9`)
**Desc:** export ~100 ไฟล์เสี่ยงหลุดหน้าจอเงียบ ๆ — เกณฑ์จบมีแค่ `next build` ผ่าน (ผ่านได้แม้พอร์ตครึ่งเดียว) และคู่มือ bias ว่า *"single-screen prototype (the common case)"* ทำให้ยุบรวม. (การส่งไฟล์ครบอยู่แล้ว — `WriteFiles` เขียนทุก entry, ไม่มี cap/timeout/max-turns)
**Subtasks:** step 3 ใหม่ = inventory เป็น `PORT_CHECKLIST.md` ก่อนแปลง · step 4 mirror tree 1:1 ห้ามยุบ/ย่อ/ข้าม · step 8 BUILD_NOTES เพิ่ม coverage line + "Not ported" · step 9 จบได้ต่อเมื่อ build เขียว **และ** checklist ติ๊กครบ · แก้คู่มือ: one page ≠ one screen (แอป 14 view ที่สลับด้วย state ก็ยังมี 14 view ให้พอร์ต)
**AC:** export ใหญ่ได้ demo ครบทุกหน้า; build เขียวบน checklist ไม่ครบ = FAILED
**Notes:** repo CRN · `SKILL.md`, `references/nextjs-conversion.md` · มีผลกับ build **รอบถัดไป** เท่านั้น

## [CRN] ทุก demo ต้องมี `TEST_CASES.md` ติดมาด้วย
**Type:** Task · **Status:** Done (`1834127`)
**Desc:** ลูกค้า/ทีมเทสได้แต่ตัวแอป ไม่มีเอกสารว่าต้องลองอะไรบ้าง โดยเฉพาะ **negative case** (กรอกผิดแล้วระบบต้องปฏิเสธ) — เพิ่มขั้นตอนในตัว harness ให้เขียนเอกสารเทสภาษาไทยติดไปกับ repo ทุก build
**Subtasks:** reference ใหม่ `references/test-cases.md` (ฟอร์แมต 6 คอลัมน์ · คลัง negative case แยกตามชนิดฟิลด์ · เคสประจำของ demo CRN · หัวข้อ "ยังไม่รองรับ") · SKILL.md step 9 ใหม่ + เกณฑ์จบข้อ (c) · ห้ามกรอกช่องผลจริง/สถานะ (ไม่ได้รันเอง = ห้ามเขียนผล) · ห้ามเพิ่มฟีเจอร์เพื่อให้เคสผ่าน ให้ประกาศเป็นข้อจำกัดแทน · QUICKSTART ชี้ไปที่ไฟล์
**AC:** demo ที่ generate ใหม่มี `TEST_CASES.md` ครบทุกหน้าจอตาม `PORT_CHECKLIST.md` มีทั้ง positive/negative และช่องผลว่างให้กรอก
**Notes:** repo CRN · `skillassets/SKILL.md`, `references/test-cases.md`, `scaffold.go` · มีผลกับ build **รอบถัดไป** · ต้อง `make restart` ให้ re-seed skill version ใหม่

## [CRN] ปักหมุด model ที่ build ใช้ = Opus ล่าสุด
**Type:** Task · **Status:** Done (`b313840`)
**Desc:** `CRN_CLAUDE_MODEL` ไม่เคยถูกตั้ง → ไม่ส่ง `--model` เลย แต่ละเครื่องเลย build ด้วย model ที่ account นั้น default อยู่ ซึ่งอาจไม่เหมือนกัน (export เดียวกันได้ demo คนละคุณภาพ และ log ไม่บอกว่าเพราะอะไร)
**Subtasks:** `defaultClaudeModel = "claude-opus-5"` (ใช้ id เต็ม ไม่ใช้ alias `opus` เพื่อให้ log/build trace ระบุ model ที่ผลิต demo ได้จริง) · `getEnv` แทน `os.Getenv` · main ตัดป้าย `(cli default)` ทิ้ง (ค่าว่างไม่มีอีกแล้ว) · `.env.example` + ตาราง env ใน deployment-config
**AC:** boot log `claude_model=claude-opus-5` · ตั้ง `CRN_CLAUDE_MODEL=claude-sonnet-5` แล้ว restart = เปลี่ยนได้ทันที ไม่ต้อง rebuild
**Notes:** repo CRN · `config.go`, `main.go` · **ค่าใช้จ่ายสูงขึ้น** — build เป็น agentic run ยาว ถ้าบิลพุ่งให้สลับเป็น sonnet

## [CRN] เคสเทสต้องมาจาก BRD/PRD ไม่ใช่จากหน้าจอ
**Type:** Task · **Status:** Done (`3efac43`)
**Desc:** กฎเดิมใน `references/test-cases.md` บอกว่า "ทุกเคสต้องมาจากโค้ดที่เขียนจริง" → เทสได้แค่สิ่งที่ทำไปแล้ว ถ้า port ข้าม AC ข้อไหนไป เอกสารเทสก็ข้ามตาม ลูกค้าไม่มีทางรู้ว่าขาด. ตรวจกับ FBD แล้ว (ยืนยันกับ session ที่ถือโปรเจกต์จริง): `docs/BRD.md` มีโครงตายตัว — Functional Requirements `F-01..`, Data Model (entity/PK/FK/หน่วย), **Acceptance Criteria `AC-1..`** และ `docs/PRD.md` มีรายหน้าจอ + กฎ role-gated · ไม่มีไฟล์ Q&A แยก คำตอบถูกสังเคราะห์ลง 3 เอกสารนี้แล้ว
**Subtasks:** positive case หยิบจาก AC/F พร้อมอ้าง id ในตาราง (`AC-2 · ...`) · negative case สังเคราะห์จาก Data Model (ชนิด/หน่วย/PK-FK/unique) + กฎ role-gated เพราะ BRD เขียนแต่ happy path · เพิ่ม **ตาราง coverage** ทุก AC + F ระดับ must ว่าเคสไหนคุม/ยังไม่รองรับ · หัวข้อ "ยังไม่รองรับ" ต้องอ้าง id ไม่ใช่หายเงียบ · SKILL step 1 อธิบายว่าเอกสารพวกนี้คือคำตอบลูกค้า ห้ามอ่านผ่าน
**AC:** เอกสารเทสตอบได้ว่า "เดโมทำได้ตามที่ตกลงไว้ไหม" ไม่ใช่แค่ "ที่ทำมาใช้ได้ไหม"
**Notes:** repo CRN · `SKILL.md`, `references/test-cases.md` · มีผลกับ build รอบถัดไป

## [CRN] แก้แหล่งเคส negative — PRD เขียนไว้ตรง ๆ อยู่แล้ว
**Type:** Task · **Status:** Done (`82328a4`)
**Desc:** รอบก่อน (`3efac43`) สั่งให้ agent สังเคราะห์เคส negative เองจาก Data Model เพราะเข้าใจว่าเอกสารเขียนแต่ happy path — จริงเฉพาะ BRD. **PRD มี `§ การควบคุมความถูกต้องของข้อมูล (Validation & Edge Cases)`** เขียน negative ไว้ตรง ๆ เป็น เงื่อนไข→ผลลัพธ์ พร้อม**ข้อความ error จริง** และ Data Model ที่ละเอียดอยู่ใน **PRD** ไม่ใช่ BRD
**Subtasks:** negative หยิบจาก PRD Validation ก่อน + copy ข้อความ error **verbatim** (คนเทสเทียบคำต่อคำ) · derive จาก Data Model เป็นส่วนขยาย ไม่ใช่แหล่งหลัก (`> 0`, ทศนิยม 2, บังคับกรอก, ห้ามซ้ำ, FK = 4-5 เคสในบรรทัดเดียว) · **match หัวข้อด้วย keyword ห้ามใช้เลข §** (เอกสาร generate จาก template เลขดริฟต์ได้ หาไม่เจอ = ถอยไปเดาจากหน้าจอ ซึ่งคือปัญหาที่จะแก้พอดี) · `ขอบเขต (ไม่ทำ)` / `Out of Scope` ห้ามเขียนเป็นเคส
**AC:** เคส negative ในเอกสารตรงกับที่ PRD ระบุ รวมถึงข้อความเตือนคำต่อคำ
**Notes:** repo CRN · ยืนยันโครงเอกสารกับ session ที่ถือโปรเจกต์จริง (Chokpranee Fabric Stock)

## [CRN] อ้าง requirement id ต้องบอกด้วยว่ามาจากเอกสารไหน
**Type:** Task · **Status:** Done (`7c1246a`)
**Desc:** `AC-2` เฉย ๆ กำกวม — BRD ใช้ `AC-1..`/`F-01..` ส่วน PRD ใช้ `US-01..` แล้วเขียน AC เป็น Given/When/Then แทรกในแถว US (โปรเจกต์นี้ไม่ชนกัน แต่เอกสาร LLM generate → โปรเจกต์อื่น PRD อาจ label เป็น `AC-x` จริงก็ได้) · อีกจุด: **negative case ใน PRD Validation & Edge Cases ไม่มี id เลย** เป็น list เปล่า `1. / 2. / 3.` ถ้าอ้างด้วยเลขลำดับจะเพี้ยนทันทีที่ regenerate เอกสาร
**Subtasks:** บังคับ prefix ชื่อเอกสารทุกครั้ง (`BRD AC-2`, `BRD F-03`, `PRD US-02`) · edge case อ้างด้วย**ชื่อ** mint เป็น `EC-DuplicateBarcode` ห้ามใช้ตำแหน่งในลิสต์ · เก็บ id ด้วย `\b(AC|F|US)-\d+\b` เพราะ padding ไม่ตรงกัน (`AC-1` vs `US-01`) · ตาราง coverage ต้องครอบทั้ง BRD AC + F ระดับ must + PRD US + edge case ที่มีชื่อ
**AC:** ตาราง coverage อ่านแล้วรู้ทันทีว่าข้อกำหนดมาจากเอกสารไหน ไม่ต้องไล่เช็ค scheme ของแต่ละโปรเจกต์
**Notes:** repo CRN · `SKILL.md`, `references/test-cases.md`

## [CRN] หน้าจอนับจาก wiring ไม่ใช่จาก file tree (ไฟล์ผีใน export)
**Type:** Task · **Status:** Done (`32d73e3`)
**Desc:** export จริงมีไฟล์ orphan — `src/pages/` 9 ไฟล์ แต่ `App.tsx` import แค่ 2 ที่เหลือ render จาก `src/components/` แทน (เศษจากตอน codegen iterate) · step 3 เดินจาก file tree เลยนับไฟล์ตายเป็นงาน แล้วกฎ "port ทุกไฟล์ห้ามข้าม" ก็บังคับให้พอร์ตของที่ไม่มีใครเรียก · step 9 ก็จะเขียนเคสเทสให้หน้าที่กดเข้าไม่ถึง = coverage เขียวแต่ไม่ได้พิสูจน์อะไร
**Subtasks:** รายชื่อหน้าจอมาจาก **wiring** (route table หรือ `activeMenu`/`activeView` switch) cross-check กับ PRD `หน้าจอทั้งหมด` · orphan **ไม่ข้ามเงียบ** — ต้องมีบรรทัดใน PORT_CHECKLIST ติดป้าย `[orphan]` + หลักฐานว่าไม่มีใคร import + ซ้ำใน BUILD_NOTES "Not ported" (กันไม่ให้ "มันเป็น orphan" กลายเป็นข้ออ้างข้ามงาน ซึ่งจะเปิดรูที่ `42c50b9` เพิ่งปิด) · เกณฑ์ตัดสินเป็นกลไก: มีใคร import ไหม / PRD ระบุเป็นหน้าจอไหม · schema ดึงจาก **PRD Data Model** เป็นหลัก (มี type/หน่วย/required/unique/FK ครบ) แล้ว cross-check `src/types.ts`
**AC:** demo ไม่มีหน้าที่กดเข้าไม่ถึง · เอกสารเทสไม่มีเคสของหน้าผี · orphan ทุกไฟล์ถูกประกาศพร้อมเหตุผล
**Notes:** repo CRN · `SKILL.md`, `references/nextjs-conversion.md`, `references/prisma-setup.md`, `references/test-cases.md` · ฝั่ง FBD ยืนยันว่า `docs/` มีแค่ 3 ไฟล์ (IDEA/BRD/PRD) และ prototype เป็น in-memory ล้วน ไม่มี persistence

## [CRN] แก้กฎ "ทุก entity ต้องมีฟอร์มสร้าง" — มันฟ้องผิด
**Type:** Task · **Status:** Done (`4c8ee7b`)
**Desc:** ตอนตัด mock data ออก (`1afb2d3`) ผมพ่วงกฎว่า "ทุก entity ต้องมี create form ใน UI" ไม่งั้นแอปจะเป็นซากเปล่า — แต่กฎนี้**ฟ้องโค้ดที่ถูกต้อง**: entity ประเภท event/log ถูกสร้างเป็น side-effect เช่นรายการเคลื่อนไหวสต็อกเกิดตอน check-in/check-out (Chokpranee ไม่มี CreateTransaction form เลย และนั่นถูกแล้ว) การมีฟอร์มสร้างตรง ๆ ต่างหากที่ผิด
**Subtasks:** เกณฑ์ใหม่ = **populatable จากในแอปทางใดทางหนึ่ง** (ฟอร์มของตัวเอง **หรือ** insert ที่เป็นผลพลอยได้ของ flow อื่น) · เคสที่ต้อง flag จริงแคบกว่ามาก = entity ที่โผล่แต่ใน `INITIAL_<X>` โดยไม่มี write path ที่ไหนเลย (report-only / derived / lookup มักเป็นตัวนี้) · เฉพาะกรณีนั้นให้เพิ่มฟอร์มขั้นต่ำ + ประกาศใน BUILD_NOTES หัวข้อ "Added to keep the app usable" เป็นข้อยกเว้นที่ประกาศไว้ ทรงเดียวกับ login
**AC:** ไม่ฟ้อง entity ที่ถูกสร้างแบบ side-effect · แต่จับ entity ที่ตัด mock แล้วกรอกไม่ได้จริง
**Notes:** repo CRN · ฝั่ง FBD ไล่ write path ครบทั้ง 6 entity ของ export จริงมายืนยัน · prompt ของ codegen เป็น skill-based (erp/booking/crm/…) **ไม่มีกฎกลางว่าทุก entity ต้องมีฟอร์ม** → harness ต้อง detect ไม่ใช่ assume

## [CRN] เลิกใส่ mock data — เหลือแค่บัญชี login
**Type:** Task · **Status:** Done (`1afb2d3`)
**Desc:** เกือบทุกหน้าขึ้นข้อมูลปลอม และ Prisma ก็มี mock data ด้วย เพราะ **skill สั่งเองว่า** "port the prototype's mock array into a seed so the app has data" + image รัน `prisma db seed` **ทุกครั้งที่ start** → ลบข้อมูลตัวอย่างทิ้งแล้วรีสตาร์ทมันกลับมา, แก้แถวที่ seed ไว้แล้วโดน upsert ทับ = ลูกค้าเอาไปใช้งานจริงไม่ได้
**Subtasks:** prisma-setup §5 เขียนใหม่ = seed **บัญชี Admin อย่างเดียว** (`upsert` by email + `update: {}`) · mock array ถูกลบทิ้ง ไม่ย้ายไป seed · ยกเว้นเฉพาะ reference/lookup ที่ไม่มีแล้ว UI วาดไม่ได้ (ต้องประกาศใน BUILD_NOTES) · บังคับว่าทุก entity ต้องมีทางสร้างข้อมูลจาก UI + empty state ไม่ใช่หน้าขาว · เกณฑ์จบเพิ่มข้อ (c) รันบน DB ว่างได้ · rename `DEMO_SEED` → `SEED_LOGIN` (ชื่อเดิมโกหก และไม่เคยอยู่ใน env contract) · แก้ INSTALL/QUICKSTART/on-prem guide · TEST_CASES เพิ่มเคส "เปิดครั้งแรก DB ว่าง" + "รีสตาร์ทแล้วของที่ลบต้องไม่กลับมา"
**AC:** demo ที่ build ใหม่เปิดมาไม่มีข้อมูลปลอมเลย · login ด้วย `DEV_EMAIL`/`DEV_PASSWORD` ได้ · กรอกข้อมูลเอง → รีสตาร์ท/deploy ใหม่ ข้อมูลอยู่ครบ ของที่ลบไม่กลับมา
**Notes:** repo CRN · `SKILL.md`, `references/prisma-setup.md`, `dockerbuild.go`, `scaffold.go` · **มีผลกับ build รอบถัดไปเท่านั้น — demo เก่าต้อง rebuild**

## [CRN] ตัวตรวจว่า build ทำตามกฎ harness ไหม (seed / TEST_CASES / checklist)
**Type:** Task · **Status:** Done (`eb22875`)
**Desc:** กฎทั้งหมดที่แก้กันวันนี้ (seed แค่บัญชี login, ต้องมี `TEST_CASES.md`, `PORT_CHECKLIST` ต้องติ๊กครบ) **เป็น prompt ล้วน ๆ** — โมเดลไม่ทำตามเมื่อไหร่ build ก็ยังขึ้นว่าสำเร็จ ไม่มีใครรู้จนลูกค้าเปิดเจอข้อมูลปลอม กฎเลยไม่มีฟัน
**Subtasks:** `buildstep/compliance.go` + test 12 เคส · เสียบใน `runJob` หลัง scaffold ก่อน git push · เตือน 3 อย่าง: seed เขียนเกิน 1 model / ใช้ `createMany` / เขียนเกิน 3 ครั้ง · ไม่มี `TEST_CASES.md` · `PORT_CHECKLIST` เหลือข้อไม่ติ๊ก (ข้าม `[orphan]` เพราะเป็นการประกาศ ไม่ใช่งานค้าง) · publish เป็น `WSAssistantText` → เห็นทั้ง live stream และ trace ที่เก็บไว้ + log `harness rule violated` ให้ grep ได้
**AC:** build ที่ไม่ทำตามกฎ มีบรรทัด ⚠ โผล่ข้าง ๆ "✓ pushed" · build ที่ถูกต้องเงียบสนิท
**Notes:** repo CRN · **เตือนอย่างเดียว ไม่ fail build** — เป็น heuristic และ reference data ที่ถูกกฎแยกจาก mock row ด้วยการนับไม่ได้ ต้องให้คนตัดสิน · client ใน seed ไม่ได้ชื่อ `prisma` เสมอ (บาง build ใช้ `db`) จึงอ่าน model จาก segment ที่ 2

## [CRN] Skill library ชุดแรก 4 ตัว (thai-formatting / data-tables / charts / printable-documents)
**Type:** Task · **Status:** Done (`8ab3c89`) · รอ upload หลัง deploy
**Desc:** skill store มีแค่ `fitt-build` + `mail-service` → ทุก build ต้องคิดใหม่เองหมดว่าจัดรูปแบบเงินไทยยังไง ตารางว่างแสดงอะไร กราฟไม่มีข้อมูลทำไง ใบเสนอราคาพิมพ์ยังไง ได้ผลไม่เหมือนกันทุกครั้ง · เลือก 4 ตัวจาก**หลักฐาน** (feature ที่ FBD สั่ง generate จริงข้าม erp/crm/dashboard/booking/ecommerce/landing) ไม่ใช่เดา · 2 ใน 4 ปิดรูที่เพิ่งเปิดเช้านี้ — พอตัด mock data ทุกกราฟ/ตารางเจอ 0 แถวเป็นครั้งแรก
**Subtasks:** เขียนขนานด้วย 4 agent · `skills/README.md` วางสัญญา (description คือทุกอย่างเพราะ progressive disclosure · บอก HOW ห้ามสั่ง WHAT · สมมติ DB ว่าง · ห้ามถาม · ห้ามข้อมูลปลอม · แบ่งความเป็นเจ้าของกันชนกัน) · agent ตรวจสอบข้อเท็จจริงด้วยการรันจริง เจอ: `th-TH` เป็น พ.ศ. อยู่แล้ว (`+543` ได้ 3112) · `Decimal` ใน `reduce(...,0)` ต่อ string เป็น `฿NaN` · VAT ปัดต่อบรรทัด vs ต่อใบ ต่างกัน 1 สตางค์ · headless browser รันใน `.next/standalone` ไม่ได้ และ PDF writer แบบ JS ล้วนทำสระ/วรรณยุกต์ไทยเพี้ยน
**AC:** demo ที่ build ใหม่จัดรูปแบบเงิน/วันที่/VAT ตรงกันทุกหน้า · ตารางและกราฟที่ไม่มีข้อมูลแสดง empty state ภาษาไทย ไม่ใช่หน้าขาว/crash
**Notes:** repo CRN · `skills/` (10 ไฟล์ 1,785 บรรทัด) · รีวิวแก้ 2 จุด: ข้ออ้างเรื่อง collation ที่ยังไม่ได้พิสูจน์ และเลขรันเอกสารที่ 2 skill สั่งคนละแบบ (`count()+1` ชนกันตอนกดพร้อมกัน → ใช้ counter row ใน `$transaction`) · **ยังไม่ upload** ต้องรอ backend บน .171

---

# EPIC N — Dashboard UX

## [CRN] Build phases เพิ่ม repo+docker; console ไม่เด้งขึ้นบน
**Type:** Task · **Status:** Done (`2317d70`)
**Desc:** (1) pipeline มีแค่ materialize→claude→git→push แต่ runner ยัง emit `repo` (owner mode) และ `docker` (phase สุดท้าย) (2) console กระพริบเด้งบนทุก poll เพราะ reset check ใช้ `events[0]._id` — หัวของ array ที่โตขึ้นเรื่อย ๆ จึงจริงตลอด → `term.clear()` + เขียนใหม่ทั้งหมดทุกรอบ
**Subtasks:** PHASES = repo→materialize→claude→git→push→docker (map `pull` ของ edit mode ลงช่อง materialize) · ตรวจ rewind จาก **id สูงสุด** ที่ตกต่ำกว่าที่เขียนล่าสุดแทน
**AC:** เห็น progress ครบทุก phase; ในหนึ่ง build console ต่อท้ายอย่างเดียว ไม่ rewrite
**Notes:** repo CRN · `PhaseTrack.tsx`, `BuildConsole.tsx`

---

# EPIC O — Build throughput (รันหลายโปรเจคพร้อมกัน)

## [EPIC] Build throughput
**Type:** Epic · **Status:** Done
คิว build เดิมเป็นทีละตัวต่อ org → ลูกค้าหลายเจ้ารอต่อคิวกันยาว. ย้ายกฎการ serialize ลงมาที่ระดับโปรเจค + ใส่เพดานทรัพยากร.

## [CRN] รัน build คนละโปรเจคพร้อมกันได้
**Type:** Task · **Status:** Done (`91de383`)
**Desc:** ล็อกเดิมเป็น `pg_try_advisory_lock(hashOrg(orgID))` + unique index `uq_jobs_one_building_per_org` = 1 build ต่อ 1 org ทั้งก้อน ทำให้โปรเจคที่ไม่เกี่ยวกันต้องรอกัน. จริง ๆ build แชร์กันแค่ระดับ **โปรเจค** (workDir / repo / image tag) — ตรวจแล้วว่า build path ไม่มี global state (ไม่มี `os.Chdir`/`os.Setenv`/git global config, ทุก exec set `cmd.Dir`)
**Subtasks:** migration `0011` drop index ราย org → สร้าง `uq_jobs_one_building_per_project` · `AcquireOrgLock`→`AcquireProjectLock` (`ErrProjectLocked`, `hashUUID`) · `NextQueuedJob(orgID)`+`OrgsWithQueuedJobs` → `NextRunnableJob()` FIFO ทั้งระบบ (`NOT EXISTS` ข้ามโปรเจคที่ build อยู่) + `NextQueuedJobForProject` สำหรับหน้า status · `processOrg`→`dispatch()` + slot semaphore + `lockMissLimit=3` กัน race อ่าน-แล้ว-ล็อก · env `CRN_MAX_CONCURRENT_BUILDS` (default 5, ต่ำสุด 1) · API ตอบ 202 `project busy, queued`
**AC:** ยิง 3 โปรเจคพร้อมกัน → build พร้อมกันทั้ง 3 · ยิงโปรเจคเดิมซ้ำ → เรียงทีละตัว ไม่ทับ · slot เต็ม → job ยัง queued แล้วถูกหยิบต่อเอง
**Notes:** repo CRN · `migrations/0011_concurrent_builds.sql`, `jobs.go`, `store.go`, `ports.go`, `config.go`, `main.go` · **ต้อง `make migrate` ก่อน `make restart`**

## [CRN] Test script — build พร้อมกัน
**Type:** Task · **Status:** Done (`docs/test-scripts/concurrent-builds.md`)
**Desc:** เอกสารเทสให้ QA รันเองได้ แบ่ง 2 รอบ: รอบ A เทส dispatcher ด้วย `CRN_RUN_CLAUDE=false` (เร็ว ไม่เผา token) 7 เคส · รอบ B ของจริง Claude+docker 4 เคส
**Subtasks:** SQL พิสูจน์ overlap ข้ามโปรเจค (>0) และห้าม overlap ในโปรเจคเดียวกัน (=0) · SQL หา peak concurrency เทียบเพดาน slot · ตาราง sign-off · ตาราง troubleshooting
**AC:** คนอื่นหยิบเอกสารไปรันเทสเองได้โดยไม่ต้องถาม
**Notes:** repo CRN · `docs/test-scripts/concurrent-builds.md`

---

# Todo / รอฝั่งอื่น

| งาน | เจ้าของ | สถานะ |
|---|---|---|
| Deploy `42c50b9` + `91de383` (**migrate ก่อน restart**) บน .171 | เรา | รอทำ |
| รัน test script `docs/test-scripts/concurrent-builds.md` บน .171 | เรา/QA | รอทำ |
| Rebuild dashboard ให้ diff UI (`46f915b`) มีผล | เรา | รอทำ |
| E2E amd64 บนเครื่องลูกค้า | เรา/เพื่อน | Todo |
| Destructive migration (drop/rename column) | — | Todo (ตอนนี้ `db push` บล็อก) |
| Receiver `category:"error"` + review UI | เพื่อน (FTC DV) | กำลังทำ |
| ยิง edit-request `POST /internal/projects/{id}/edit` | เพื่อน (FTC DV) | กำลังทำ |
| pg_dump snapshot ก่อน deploy | เพื่อน (FTC DV) | กำลังทำ |
