# Jira Tasks — On-prem Demo Image (CRN → GitLab → ลูกค้า)

> **Epic:** ผลิต demo เป็น Docker image opaque (ไม่มี source) → เก็บบน GitLab registry (self-host) → ส่งให้ลูกค้ารันในวงแลนตัวเอง (data ไม่หลุด)
> Design: `docs/superpowers/specs/2026-07-16-on-prem-demo-image-design.md`
> สถานะ: **CRN-1..CRN-5 = DONE (โค้ด commit แล้ว)** · ที่เหลือ = TODO

---

## ✅ DONE (โค้ดฝั่ง CRN — commit แล้ว)

### CRN-1 · Config: เปิด/ปิด docker-image pipeline
**Type:** Task · **Status:** Done
เพิ่ม env `CRN_BUILD_IMAGE` (bool, default false) + `CRN_IMAGE_REGISTRY` (prefix เช่น `registry.gitlab.local/fitt`).
**AC:** ตั้ง env แล้ว config โหลดถูก; ไม่ตั้ง = พฤติกรรมเดิม (ไม่ build image).

### CRN-2 · Production Dockerfile writer (deterministic)
**Type:** Task · **Status:** Done
`buildstep.WriteDockerfile` เขียน multi-stage Dockerfile (Next standalone) + `.dockerignore` แบบ CRN คุมเอง (ทับของ model). runner copy แค่ `.next/standalone`+static+public → **ไม่มี `.tsx` ในภาพ**. ข้าม (return false) ถ้าไม่ใช่ Next app.
**AC:** Next app → เขียน Dockerfile ที่ copy standalone; non-Next → ไม่เขียนอะไร. (มี unit test)

### CRN-3 · docker build + push functions
**Type:** Task · **Status:** Done
`buildstep.BuildImage` (`docker build -t`), `buildstep.PushImage` (`docker push`), `DemoImageTag` (`<reg>/crn-demo-<slug>:v<n>`). ใช้ ambient docker credential (host `docker login` ไว้).
**AC:** exec docker ถูก args; fail แล้ว surface output จริง. (มี unit test tag/slug)

### CRN-4 · Wire เข้า build pipeline
**Type:** Task · **Status:** Done
หลัง build สำเร็จ (gated `CRN_BUILD_IMAGE`) → write Dockerfile → build → push (ถ้ามี registry) → set `docker_tag` = image tag จริง → โชว์ใน live stream. Best-effort: docker fail ไม่ล้ม build.
**AC:** เปิด env + build demo Next → ได้ image tag ใน dashboard; release callback พก image_ref.

### CRN-5 · Unit tests
**Type:** Task · **Status:** Done
tag/slug sanitization, IsNextApp, WriteDockerfile. `go test` เขียว.

---

## 🔲 TODO

### INFRA-1 · เปิด GitLab Container Registry
**Type:** Task · **Team:** Infra
`gitlab.rb`: ตั้ง `registry_external_url` + TLS cert → `gitlab-ctl reconfigure`. สร้าง group/project สำหรับ image.
**AC:** `docker login registry.<gitlab>` + push image ตัวอย่างขึ้นได้.

### INFRA-2 · CRN box login registry
**Type:** Task · **Team:** Infra
สร้าง **deploy token / PAT** (scope `write_registry`) → `docker login` บนเครื่อง CRN → เก็บ credential ให้ persist (daemon).
**AC:** CRN `docker push` ขึ้น GitLab ได้โดยไม่ถาม auth.

### CRN-6 · Customer compose + INSTALL.md
**Type:** Story · **Team:** Dev
Scaffold ออก `docker-compose.customer.yml` (`image:` ที่ push แล้ว + postgres + named volume) + `INSTALL.md` (`docker load`/`pull` → `compose up`). **ไม่มี `build:`** (ไม่ให้ source หลุด).
**AC:** ลูกค้า `docker compose up` แล้ว demo รัน, data อยู่ volume local, ไม่มีไฟล์ source.

### CRN-7 · Migration ใน production image
**Type:** Story · **Team:** Dev
runner image ไม่มี prisma CLI+schema → เพิ่ม **migrate init-container** (image ที่มี prisma+schema) รัน `prisma db push` + seed ก่อน app start ใน customer compose.
**AC:** compose up ครั้งแรก schema+seed ถูกสร้าง, app ต่อ DB ได้.

### CRN-8 · Tarball fallback (air-gap)
**Type:** Task · **Team:** Dev
`docker save <tag> | gzip` → artifact + วิธี `docker load` สำหรับลูกค้าที่ต่อ registry เราไม่ได้. env `CRN_ARTIFACT_DIR`.
**AC:** ได้ `.tar.gz` โหลดขึ้นเครื่องอื่นแล้ว `docker load` + run ได้.

### CRN-9 · ย้าย git remote → GitLab (self-host)
**Type:** Task · **Team:** Dev/Infra
เปลี่ยน `CRN_GIT_REMOTE` จาก GitHub → GitLab private repo (source อยู่ในบ้านเรา). ตั้ง git credential ให้ CRN.
**AC:** build push source ขึ้น GitLab ได้; ไม่มีอะไรไป GitHub.

### FITTCORE-1 · GitLab → FITTCORE delivery
**Type:** Story · **Team:** Dev + เพื่อน
เลือกกลไก: (ก) GitLab CI pipeline แจ้ง/ส่ง FITTCORE เมื่อมี image ใหม่ · (ข) webhook → FITTCORE · (ค) FITTCORE pull จาก registry. ประสานสัญญากับทีม FITTCORE.
**AC:** image ใหม่ → FITTCORE รับรู้ + ดึงไปส่งลูกค้าได้.

### QA-1 · E2E จริง
**Type:** Task · **Team:** Dev
build demo จริง → image → push GitLab → pull เครื่อง 2 → `compose up` → เปิด demo ได้ (data local, ไม่เห็น source).
**AC:** demo รันบนเครื่องที่ 2 สำเร็จ end-to-end.

### DOC-1 · อัปเดตเอกสาร
**Type:** Task · **Team:** Dev
เพิ่ม image flow ลง `crn-integration-contract.md` + `deployment-config.md` (image_ref ใน callback/build_events, registry).
