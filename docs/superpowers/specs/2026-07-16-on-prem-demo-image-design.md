# On-Prem Demo Image — Design

> สถานะ: **design เพื่อ review** (ยังไม่ลงมือ code) · 2026-07-16

## Goal

ให้ CRN **ผลิต demo เป็น Docker image แบบ opaque (ไม่มี source) + วิธีติดตั้ง** เพื่อส่งให้ลูกค้าเอาไปรัน**ในวงแลนตัวเอง** — data ลูกค้าไม่ออกจาก LAN, source เราไม่หลุด.

**Scope = ฝั่งเราเท่านั้น** (FBD + CRN อยู่ที่เรา; claude/gemini รันฝั่งเรา; FITTCORE เป็นของอีกทีม). เราแค่ **ผลิต artifact ส่งมอบ**. ไม่ต้อง: ship CRN เป็น appliance, Docker-in-Docker สำหรับ CRN, หรือ claude auth บนเครื่องลูกค้า.

## Current state (สำรวจจากโค้ด/DB จริง)

- **FBD export** = Vite + React SPA (frontend, ไม่มี DB).
- **harness skill `fitt-build`** สั่ง claude: แปลง Vite→**Next.js App Router + Prisma + Postgres**, ตั้ง `output:"standalone"`, และมี **`assets/Dockerfile` production พร้อม** (multi-stage: `deps`→`builder`→`runner`, node:20-alpine, non-root, copy แค่ `.next/standalone`+`.next/static`+`public` → **source ไม่ติดไปกับ image**), บังคับ `next build` ผ่านโดยไม่ต้องมี DB.
- **แต่ `buildstep.ScaffoldRun`** (jobs.go หลัง claude) **เขียนทับ** `docker-compose.yml` เป็น **dev compose** (node:20 + mount source + `npm run dev`) → artifact สุดท้ายเลยเป็น dev ที่เห็น source.
- **pipeline ไม่มี `docker build`** — `docker_tag` = `"branch:<name>"` (ชื่อ branch เฉยๆ), git push อย่างเดียว.

**สรุป: ชิ้นส่วน production มีอยู่แล้ว 80%** (Dockerfile + standalone จาก harness). เหลือ "หยุดทับ + build + package".

## Design — 3 การเปลี่ยน

### 1. Scaffold: อย่าทับ Dockerfile ของ harness; ออก **production compose สำหรับลูกค้า**
- **เก็บ** `Dockerfile` + `.dockerignore` ที่ harness สร้าง (ไม่ overwrite).
- เปลี่ยน `ScaffoldRun` ให้เขียน **`docker-compose.customer.yml`** ที่:
  - `image: crn-demo-<slug>:v<n>` (อ้าง image ที่ build แล้ว — **ไม่ใช่ `build:`** เพราะ `build:` ต้องมี source = ลูกค้าเห็น code)
  - service `postgres:16-alpine` + named volume (data ลูกค้าอยู่ local, persist)
  - one-shot `migrate` (รัน `prisma migrate deploy` + seed) ก่อน app start
  - เขียน `INSTALL.md` (ขั้นตอน `docker load` + `docker compose up`)
- ยังคง QUICKSTART เดิมสำหรับ dev ภายในได้ (แยกไฟล์)

### 2. Pipeline: เพิ่ม step `docker build` (หลัง claude, ก่อน/หลัง git push)
- ใน `runJob`: หลัง claude สำเร็จ + มี `Dockerfile` → `docker build -t crn-demo-<slug>:v<buildNo> <workDir>`
- `SetJobDockerTag` = **image tag จริง** (แทน `"branch:..."`)
- best-effort/มี flag เปิดปิด (`CRN_BUILD_IMAGE`) — build image แพงกว่า git push

### 3. Packaging + ส่งมอบ
- `docker save crn-demo-<slug>:v<n> | gzip > <slug>-v<n>.tar.gz`
- artifact ส่งมอบ = **tarball + `docker-compose.customer.yml` + `INSTALL.md`**
- เก็บไว้ที่ (เลือกใน impl): โฟลเดอร์ artifact บน CRN host / object storage / ลิงก์ให้ FITTCORE ดึง

**ลูกค้าทำ:** `docker load < demo.tar.gz` → `docker compose -f docker-compose.customer.yml up -d` → demo รันบน LAN (data ใน postgres volume ท้องถิ่น, ไม่มี source).

## Open details ต้องเคลียร์ตอน impl

1. **Migration ใน production image** — runner image (standalone) **ไม่มี `prisma` CLI + schema**. ต้องเลือก:
   - (ก) migrate init-container แยก (image ที่มี prisma+schema) รันก่อน app, หรือ
   - (ข) bake `prisma` + `prisma/` + entrypoint `migrate deploy` เข้า runner (image ใหญ่ขึ้นนิด)
   - → เอนเอียง (ก) สะอาดกว่า
2. **Static demo (ไม่มี Prisma)** — ไม่ต้อง postgres; compose มีแค่ app image. (harness ทำ DB-backed เป็นหลัก แต่รองรับ single-page client ได้)
3. **Docker access ของ CRN** — CRN host ต้องมี docker daemon. ถ้า CRN รันใน container ต้อง mount `/var/run/docker.sock` (DinD-lite) — แต่ตอนนี้ CRN รันบน host = ใช้ docker ได้เลย
4. **Image ชื่อ/tag/registry** — ตอนนี้ local + tarball (air-gap ลูกค้าชอบ). ถ้าจะ push registry ต่อ (option ภายหลัง)
5. **ขนาด + build time** — Next standalone ~150–250MB; `docker build` เพิ่มเวลา (npm ci + next build ใน image build ซ้ำกับที่ claude ทำ — อาจ reuse ได้)
6. **การส่งมอบจริง** — tarball ไปถึงลูกค้ายังไง (ผ่าน FITTCORE? download? USB?) — นอก scope โค้ด แต่ artifact พร้อม

## Implementation outline (ยังไม่ทำ — รอ review)

1. `buildstep/scaffold.go` — เลิก overwrite Dockerfile; เพิ่ม `WriteCustomerCompose(dir, image, port)` + `INSTALL.md`
2. `buildstep/dockerbuild.go` (ใหม่) — `BuildImage(ctx, workDir, tag) error` (exec `docker build`, stream log)
3. `buildstep/dockerbuild.go` — `SaveImage(ctx, tag, outPath) error` (`docker save | gzip`)
4. `jobs.go runJob` — หลัง claude สำเร็จ + Dockerfile มี → build + save + set docker_tag=image; gate ด้วย `CRN_BUILD_IMAGE`
5. `config.go` — `CRN_BUILD_IMAGE` (bool), `CRN_ARTIFACT_DIR`
6. migration init-container ใน customer compose (open detail #1)
7. tests: scaffold ออก compose ถูก, dockerbuild exec ถูก (mock/skip ถ้าไม่มี docker)
8. docs: อัปเดต contract/deployment doc

## ไม่ทำ (ตัดชัด)
- CRN appliance / DinD-for-CRN / claude บนเครื่องลูกค้า / registry (เฟสหน้า)
