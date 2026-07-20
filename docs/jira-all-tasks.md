# Jira — ทุก Task (แยกทีละอัน, ทั้งระบบ)

> ทุกงานที่ทำใน session นี้ (CRN `fitt-coderunner` + FBD `fitt-builder-v2`) — แต่ละ `##` = 1 issue, copy ตั้งแต่ Title ลงไปเข้า Jira ได้เลย
> `Done` = commit+push แล้ว (`feat/feedback-panel` == `dev`) · repo ระบุใน Notes

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
