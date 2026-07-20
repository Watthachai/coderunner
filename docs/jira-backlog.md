# Jira Backlog — FITT CRN / Builder

> Task/subtask พร้อม copy ใส่ Jira · อัปเดต 2026-07-17
> สถานะ: **Done** = commit + push แล้ว (`feat/feedback-panel` == `dev`) · **Todo** = ยังไม่ทำ
> ดอคเสริม: [design](superpowers/specs/2026-07-16-on-prem-demo-image-design.md) · [how-to](on-prem-demo-image-howto.md) · [deployment](deployment-config.md) · [contract](crn-integration-contract.md) · [feedback payload](feedback-widget-payload.md)

---

## EPIC A — CRN build-pipeline reliability  ✅ Done

### A1 · Reconcile ghost builds ตอน startup `Done` `45356f3`
Build ที่ค้าง `building` ตอน server restart กลางคัน → ค้างตลอด (dashboard โชว์ building ไม่จบ).
**Subtasks:** store `FailOrphanedBuilds` (UPDATE…RETURNING) · manager `ReconcileOrphans` (notify + FTC callback) · เรียกใน `main` ก่อน ListenAndServe · domain interface.
**AC:** restart แล้ว job ที่ค้าง building → flip เป็น failed อัตโนมัติ + subscriber ได้ terminal event.

### A2 · Atomic workspace reset (ENOTEMPTY) `Done` `122e007`
`os.RemoveAll(workDir)` race กับ Spotlight/watcher → `unlinkat: directory not empty` ตอน rebuild.
**Subtasks:** `resetWorkspace` rename-aside (`.stale-<jobid>`) + best-effort remove + sweep · test.
**AC:** rebuild project เดิมไม่ ENOTEMPTY; workDir ถูกเคลียร์สะอาด.

### A3 · Resume stranded queued jobs ตอน startup `Done` `cef7d09`
Job ที่ queued ก่อน restart ไม่มีใคร kick worker → ค้าง queued ตลอด.
**Subtasks:** store `OrgsWithQueuedJobs` · manager `ResumeQueued` (kick processOrg/org) · เรียกใน main หลัง reconcile.
**AC:** restart แล้ว job ที่ค้างคิว → เริ่ม build อัตโนมัติ.

### A4 · Cancel build (queued/in-flight) `Done` `81f2f41`
เดิม cancel แค่ flip DB, build วิ่งต่อ เผา token.
**Subtasks:** per-job cancellable ctx (`m.cancels`) · `Cancel` = interrupt running (SIGKILL process group) / drop queued · `finishCancelled` · API `POST /internal/jobs/{id}/cancel` · dashboard ปุ่ม 2-click.
**AC:** กด cancel → claude ถูกฆ่าจริง หยุดเผา token; status=cancelled.

### A5 · First-class `build_cancelled` status `Done` `3b33bbf`
Cancel เคยโชว์เป็น "failed" ใน activity feed.
**Subtasks:** migration `0009` (คลาย CHECK build_events) · domain `EventBuildCancelled` · emit `build_cancelled {reason}` · ActivityFeed สีเทา · contract doc.
**AC:** cancel → activity โชว์ "cancelled" (เทา) ไม่ใช่ failed แดง; FTC callback ยัง map เป็น failed.

---

## EPIC B — Demo runnability  ✅ Done

### B1 · Per-project docker-compose port `Done` `f9e0d05`
Demo default port 3000 ชนกับ studio/dashboard/demo อื่น.
**Subtasks:** `ScaffoldPort` (hash project → 4000–4999) · render `{{PORT}}` ใน compose/QUICKSTART · ready message โชว์ URL จริง · test.
**AC:** `docker compose up` ของ demo ไม่ชน; แต่ละ demo port ของตัวเอง; ยัง override ด้วย `APP_PORT`.

---

## EPIC C — On-prem demo image delivery  🟢 Core done (C1–C5) · C6–C9 optional

> ผลิต demo เป็น image opaque (ไม่มี source) → GitLab registry → ลูกค้ารันวงแลนตัวเอง. ราย task ละเอียด: [on-prem-demo-image-tasks.md](on-prem-demo-image-tasks.md)

### C1 · docker-image pipeline (CRN) `Done` `bdbab56`
**Subtasks:** config `CRN_BUILD_IMAGE`+`CRN_IMAGE_REGISTRY` · `buildstep/dockerbuild.go` (WriteDockerfile Next-standalone deterministic, BuildImage/PushImage, DemoImageTag/IsNextApp) · wire `buildAndPushImage` ใน runJob (set docker_tag=image) · tests · .env.example.
**AC:** เปิด env → build Next demo → ได้ image `crn-demo-<slug>:v<n>` (push ถ้ามี registry); source ไม่อยู่ในภาพ.

### C2 · เปิด GitLab Container Registry (INFRA-1) `Done`
`gitlab.rb` `registry_external_url` + TLS → reconfigure. ทำบน VM `172.168.1.234`.
**AC:** login registry + push image ตัวอย่างได้.

### C3 · CRN box login + push (INFRA-2) `Done (validated บน .168 · .171 prod pending)`
deploy token (`write_registry`) · `docker login` · ตั้ง `CRN_IMAGE_REGISTRY`.
**AC:** build → image โผล่ใน GitLab registry.

### C4 · Customer compose + INSTALL.md (CRN-6) `Done`
Scaffold ออก `docker-compose.customer.yml` (`image:` + postgres + volume) + INSTALL.md.
**AC:** ลูกค้า `docker compose up` → demo รัน, data local, ไม่มี source.

### C5 · Migrate image — migrate-on-start (CRN-7) `Done`
runner image ไม่มี prisma CLI → init-container รัน `migrate deploy` + seed ก่อน app.
**AC:** compose up ครั้งแรก schema+seed สร้าง.

### C6 · Tarball fallback air-gap (CRN-8) `Todo`
`docker save | gzip` + `CRN_ARTIFACT_DIR`.
**AC:** โหลด tarball เครื่องอื่น `docker load` + run ได้.

### C7 · ย้าย git remote → GitLab (CRN-9) `Todo`
`CRN_GIT_REMOTE` → GitLab private + git credential.
**AC:** build push source ขึ้น GitLab, ไม่ไป GitHub.

### C8 · GitLab → FITTCORE delivery `Todo` `Dev+เพื่อน`
CI/webhook/pull ให้ FITTCORE รับ image ใหม่.

### C9 · E2E จริง (QA) `Todo`
build → image → push → pull เครื่อง 2 → run.

### C10 · Build images เป็น amd64 `Done`
CRN build บน Mac arm64 แต่ลูกค้ารัน x86_64 → image arm64 รันไม่ได้. pin `--platform linux/amd64` ใน BuildImage (buildx/QEMU emulate).
**AC:** image ที่ push เป็น amd64 → รันบนเครื่อง x86 ลูกค้าได้.

---

## EPIC D — Integration & contract  ✅ Done

### D1 · Feedback widget payload spec `Done` `6a2841b`
เอกสาร payload ที่ปุ่ม feedback ยิง (project_id/category/priority/note/page_url + nested pins/box/region_shot/viewport) ให้เพื่อนทำ API รับ. → [feedback-widget-payload.md](feedback-widget-payload.md)
**AC:** เพื่อนอ่านแล้วสร้าง endpoint รับได้; ชี้ widget มา API เพื่อนผ่าน `CRN_FEEDBACK_INGEST_URL`.

### D2 · FTC DV HTTP callback (§3) `Done` (session ก่อน)
`CRN_FTC_DV_CALLBACK_URL` → POST building/released/failed + git_remote/git_branch. best-effort. → contract §3.

### D3 · Read-only DB role สำหรับ consumer `Done` (ops)
`ftcdv` (SELECT build_events + UPDATE notified_ftcdv). connection string ส่งเพื่อน. verify แล้ว (rows ok, project_jobs denied).
**AC:** เพื่อนต่อ DB อ่าน build_events ได้แบบ read-only.

### D4 · Deployment presets doc `Done` `c286c9c` `f53cf8d`
env knobs + single-box/docker/DNS presets + no-hardcoded-IP. → [deployment-config.md](deployment-config.md)

### D5 · image_ref + status ใน build_events (consumer) `Done` `122e069`
build_done payload เพิ่ม `image_ref` (tag GitLab ที่ pull ได้) + git_remote/git_branch → consumer (FITTCORE) รู้ location ผ่าน DB ช่องเสถียร ไม่ต้องพึ่ง HTTP callback. status = `event_type` (started/done/failed/cancelled). + doc [fittcore-consumer-guide.md](fittcore-consumer-guide.md).
**AC:** เพื่อน poll build_events → รู้ build เสร็จ/กำลังทำ + `docker pull image_ref` ได้.

---

## EPIC E — FBD local dev  ✅ Done

### E1 · Direct-CRN mode `Done` `c050b57` (repo FBD)
`/api/fittcore` ยิงตรง CRN local เมื่อ `FITTCORE_DIRECT_CRN_URL` set (ข้าม Gateway) — test flow FBD→CRN บนเครื่องเดียว. prod เว้นว่าง.
**AC:** ตั้ง env + build จาก studio → job เข้า CRN local; prod ไม่กระทบ (ผ่าน Gateway).

---

## EPIC F — Ops / environment  ✅ Done

### F1 · macOS Spotlight ENOTEMPTY mitigation `Done` (ops)
Spotlight index race `rm -rf .next`/workspaces → corrupt. แก้: `.metadata_never_index` marker + `mv`-based clear.
**AC:** `rm -rf .next` ทำงานปกติ; dev server ไม่พังจาก manifest เสีย.

### F2 · Turbopack workspace-root pin `Done` (session ก่อน)
stray `~/package-lock.json` → Next เลือก home เป็น root → dev server ปิดเอง. แก้: `turbopack.root` pin.
