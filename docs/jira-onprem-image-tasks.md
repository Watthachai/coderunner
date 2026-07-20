# Jira Tasks (แยกทีละอัน) — On-prem Demo Image

> แต่ละหัวข้อ `##` = 1 issue ใน Jira. copy ตั้งแต่ Title ลงไปได้เลย
> Epic: **On-prem demo image delivery** (CRN build → app+migrate image + compose → ลูกค้ารันเครื่อง amd64 ตัวเอง)

---

## [EPIC] On-prem demo image delivery

**Type:** Epic · **Status:** In progress (core done)

**Description:**
CRN build 1 ครั้ง → ผลิต **artifact ที่ self-contained** เอาไปรันบนเครื่องลูกค้า (amd64) ได้เลย: app เป็น image opaque (ไม่มี source), DB ตั้ง schema+seed เอง, data อยู่ในเครื่องลูกค้า. ส่งมอบผ่าน GitLab registry (image) + docker-compose.customer.yml.

**Acceptance Criteria:**
- ลูกค้า `docker compose -f docker-compose.customer.yml up -d` บนเครื่อง amd64 → demo ทำงาน, schema+seed พร้อม, ไม่เห็น source code, data ใน volume ของลูกค้า
- ไม่มี manual step / ไม่ต้อง clone source

---

## [CRN] App image — opaque Next standalone

**Type:** Task · **Status:** Done (`bdbab56`)

**Description:**
Build demo (Next standalone) เป็น docker image opaque. runner copy แค่ `.next/standalone` + static + public → **ไม่มีไฟล์ `.tsx`/source ใน image**. Tag `crn-demo-<slug>-<id8>:v<n>` (id8 = project id กัน demo ชื่อซ้ำชนกัน).

**Acceptance Criteria:**
- เปิด `CRN_BUILD_IMAGE=true` → build → ได้ app image
- `docker run <image> --entrypoint sh` แล้ว **หา `.tsx` ไม่เจอ**
- 2 project ชื่อ demo ซ้ำ → tag ไม่ชนกัน (id8 ต่างกัน)

**Notes:** `buildstep/dockerbuild.go` (productionDockerfile, DemoImageTag)

---

## [CRN] Migrate image — migrate-on-start

**Type:** Task · **Status:** Done (`d05245a`)

**Description:**
image แยกที่มี **prisma CLI + schema + seed เท่านั้น (ไม่มี app source)**. รันบนเครื่องลูกค้า → `prisma db push` (sync schema) + `prisma db seed` ครั้งเดียวแล้ว exit. ใช้ `db push` (ไม่ใช่ `migrate deploy`) เพราะ harness regenerate schema ใหม่ทุก build ไม่มี migration history — `db push` diff live DB กับ schema จึงไม่มี checksum ให้ mismatch ตอนอัปเวอร์ชัน. idempotent + seed upsert → รันซ้ำปลอดภัย. Tag `…-migrate:v<n>`.

**Acceptance Criteria:**
- compose start `migrate` → schema+seed ถูกสร้างบน Postgres ลูกค้า แล้ว container exit 0
- migrate image **ไม่มี app source** (copy แค่ `prisma/` + package.json)
- รัน `up` ซ้ำ → ไม่ error / ไม่ duplicate data

**Notes:** `buildstep/dockerbuild.go` (migrateDockerfile, MigrateImageTag)

---

## [CRN] Build images เป็น amd64

**Type:** Task · **Status:** Done (`d05245a`)

**Description:**
CRN build บน Mac (arm64) แต่ลูกค้ารันเครื่อง x86_64 → image arm64 รันไม่ได้ (exec format error). pin `--platform linux/amd64` ใน `docker build` (buildx/QEMU emulate cross-build).

**Acceptance Criteria:**
- image ที่ push ขึ้น registry เป็น **amd64** (`docker manifest inspect` / `docker image inspect` → Architecture=amd64)
- pull ไปรันบนเครื่อง x86 Linux ลูกค้า → รันได้ ไม่ error format

**Notes:** `buildstep/dockerbuild.go` (BuildImage `--platform`). ต้องมี buildx บน CRN host (Docker Desktop มีให้)

---

## [CRN] Customer compose + INSTALL.md

**Type:** Task · **Status:** Done (`399957b`)

**Description:**
CRN gen `docker-compose.customer.yml` (postgres + named volume + migrate one-shot + app) + `INSTALL.md` ใส่ใน build (commit ก่อน git push, ใช้ deterministic image tags). app รอ migrate เสร็จด้วย `service_completed_successfully`.

**Acceptance Criteria:**
- compose อ้าง `image:` (ไม่ใช่ `build:`) ของ app + migrate + postgres
- ลูกค้า `docker compose -f docker-compose.customer.yml up -d` → db → migrate → app ตามลำดับ
- data อยู่ volume `dbdata` (persist ข้าม restart)

**Notes:** `buildstep/dockerbuild.go` (customerCompose, installMD, WriteImageBundle); `jobs.go` (writeImageBundle)

---

## [CRN] build_events: image_ref + status ให้ consumer

**Type:** Task · **Status:** Done (`122e069`)

**Description:**
build_done payload เพิ่ม `image_ref` (docker image tag ที่ pull ได้) + git_remote/git_branch → FITTCORE รู้ location ผ่าน DB (ช่องเสถียร) ไม่ต้องพึ่ง HTTP callback. status = `event_type` (build_started/done/failed/cancelled).

**Acceptance Criteria:**
- consumer poll `build_events` → เห็น `event_type` = สถานะ build + `payload.image_ref` = image ที่ pull ได้
- ไม่ต้องรอ HTTP callback

**Notes:** `jobs.go` (donePayload); doc `fittcore-consumer-guide.md`

---

## [CRN] Incremental migration (demo versioned + data ค้าง)

**Type:** Task · **Status:** Additive done (db push) · destructive = Todo

**Description:**
~~เดิม~~ migrate image เปลี่ยนมาใช้ `prisma db push` (แทน `migrate deploy` + baked init migration) → ไม่มี migration checksum ให้ mismatch ตอนอัปเวอร์ชัน. v2 ที่ **เพิ่ม** table/column apply delta ให้เอง data เดิมอยู่ครบ = **แก้เคส additive แล้ว**. เหลือเคส **destructive** (ลบ/เปลี่ยน type column ที่มี data): `db push` ตั้งใจ **error หยุด** (ไม่ส่ง `--accept-data-loss`) กันข้อมูลหาย → ยังต้องมี delta tracking จริงถ้าอยาก auto-handle destructive โดยไม่ให้ operator แตะ DB.

**Acceptance Criteria:**
- ✅ deploy v2 (additive) ทับ v1 ที่มี data → schema อัปเดต data ไม่หาย ไม่ checksum-fail
- ☐ (future) deploy v2 (destructive) → migrate อัตโนมัติแบบ safe โดยไม่ต้อง operator แตะ DB มือ

---

## [CRN] Tarball fallback (air-gap)

**Type:** Task · **Status:** Done (`CRN_ARTIFACT_DIR`)

**Description:**
ลูกค้าที่ต่อ GitLab registry เราไม่ได้ (air-gap) → CRN `docker save <app> <migrate> | gzip` เป็น tarball + ส่งไฟล์. ลูกค้า `docker load` แล้ว `docker compose up`. env `CRN_ARTIFACT_DIR`.

**Acceptance Criteria:**
- ได้ `.tar.gz` โหลดขึ้นเครื่อง air-gap → `docker load` + run demo ได้ โดยไม่ต้องต่อ registry

---

## [QA] E2E บนเครื่องจริง (amd64)

**Type:** Task · **Status:** Todo

**Description:**
build demo จริง → push GitLab → **pull บนเครื่องที่ 2 (x86 Linux)** → `docker compose up` → เปิด demo ได้ end-to-end.

**Acceptance Criteria:**
- demo เปิดได้บนเครื่อง amd64 เครื่องอื่น, schema+seed พร้อม, data local, `docker exec` ไม่เห็น `.tsx`
