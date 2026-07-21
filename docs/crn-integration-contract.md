# CRN Integration Contract — สำหรับ FBD / Gateway / FTC DV

> อัปเดต **2026-07-15** · ตรวจทีละบรรทัดกับโค้ดจริง `fitt-coderunner`
> เอกสารนี้บอก **สิ่งที่ CRN รับ/ส่งจริง ณ ตอนนี้** (ไม่ใช่ดีไซน์เป้าหมาย) เพื่อให้ฝั่ง FBD / Gateway / FTC DV ต่อกับ CRN ได้

---

## ✅ สถานะ (อ่านก่อน)

CRN **ต่อกับฝั่งดีไซน์ได้แล้ว** (เพิ่มแบบ *ไม่ทับ* ของเดิม):
- **ส่งงาน:** เลือกได้ทั้ง **`zip_base64`** (inline) หรือ **`zip_uri`** (ให้ CRN โหลด zip จาก URL / LAN IP เอง)
- **รับผล:** ได้ทั้ง **`build_events`** (เขียนลง DB กลางเสมอ) และ **HTTP callback ไป FTC DV** (เปิดด้วย env `CRN_FTC_DV_CALLBACK_URL`)

ทำตาม §1–§3 ได้เลย.

---

## Ports / endpoints

| พอร์ต | บริการ |
|---|---|
| `:8080` | **CRN API** (`CRN_LISTEN_ADDR`) — ingest / trigger / log WS อยู่ที่นี่หมด |
| `/api/fittcore` | proxy ของ FBD (Next) — flow จริงยิงผ่านตรงนี้แล้ว forward server-side ไป CRN `:8080` |
| `:5433` (dev) | **Postgres กลาง** — อ่านผล build จากตาราง `build_events` ผ่าน `CRN_CENTRAL_DATABASE_URL` (→ container `5432`) |
| `:3010` / `:3000` | feedback PostgREST / dashboard — **คนละเรื่อง** ไม่เกี่ยวกับ contract นี้ |

---

## §1 · ส่งงานเข้าให้ CRN build

```
POST http://<crn-host>:8080/internal/projects
Content-Type: application/json
```
- **ไม่มี auth** (service-to-service ในเน็ตเวิร์กที่เชื่อถือกัน) — ไม่ต้องส่ง `Authorization` / `X-API-Key`
- **ไม่มี `Idempotency-Key`** — ยิงซ้ำ = build ใหม่ทุกครั้ง (`build_no` เพิ่มขึ้น), ส่ง `project_id` เดิมจะใช้ project row เดิมแต่ยัง build ซ้ำ
- decoder **เข้มงวด** (`DisallowUnknownFields`) → ฟิลด์ที่ **ไม่ได้ประกาศไว้** = **`400`** (`zip_uri` ประกาศแล้ว รับได้)

### Body
```jsonc
{
  "org_id": "<uuid>",         // ถ้าไม่ส่ง ใช้ defaultOrgID
  "org_name": "…",            // มีฟิลด์นี้ด้วย
  "project_id": "<uuid>",     // ใช้จับคู่ project (ส่งเดิม = ใช้ row เดิม)
  "name": "…", "tag": "…", "idea": "…",
  "brd": "<markdown>",        // ไม่มี limit บังคับฝั่ง CRN
  "prd": "<markdown>",
  "prompts": ["…"],           // ไม่มี limit บังคับ
  "zip_name": "prototype.zip",
  // ส่ง zip แบบใดแบบหนึ่ง (ถ้ามีทั้งคู่ CRN ใช้ base64 ก่อน):
  "zip_base64": "<base64 ของ zip>",               // (ก) แนบ inline
  "zip_uri": "http://172.168.1.167:8080/…zip",     // (ข) หรือให้ CRN โหลดเอง (LAN OK · ≤26MB · timeout 60s)
  "zip_bytes": 12345,         // metadata (CRN ยังไม่ verify)
  "file_count": 8
}
```
> **zip_uri (security):** โหลดเฉพาะ `http`/`https`, **ไม่ตาม redirect**, และ **บล็อก** loopback (`127.0.0.1`) + link-local (`169.254.*` เช่น cloud-metadata) — ส่วน LAN/private (`192.168.*`, `10.*`, `172.16–31.*`) และ public **อนุญาต**. เกิน 26MB = error (ไม่ตัดเงียบ).

### Response — สำเร็จ `202 Accepted`
```json
{
  "project_id": "<uuid>",
  "job_id": "<uuid>",
  "build_no": 7,
  "org_id": "<uuid>",
  "git_remote": "<CRN_GIT_REMOTE>",
  "git_branch": "crn/<slug>-<id8>",
  "status": "queued"
}
```
- `job_id` (ไม่ใช่ `jobId`), `status:"queued"` พิมพ์เล็ก (ไม่ใช่ `state:"QUEUED"`), **ไม่มี** `duplicate`
- **repo/branch ยังไม่ถูกสร้างตอนนี้** — request เขียนแค่ DB rows, git จริงสร้างตอน build async. `git_branch` เป็นแค่ string ที่ประกอบไว้
- `git_remote`/`git_branch` ถูกเฉพาะโหมด **shared-remote**; โหมด **owner** จะ push `main` เข้า repo ต่อ project (ค่าพวกนี้จะไม่ตรงของจริง)

### Error codes (ที่มีจริง)
| โค้ด | เมื่อ |
|---|---|
| `400` | JSON พัง / UUID ผิด / **มีฟิลด์ที่ไม่รู้จัก** |

> **ยังไม่มี:** `403 org_mismatch` (ไม่มี auth ให้เทียบ), `413 zip_too_large` (ไม่จำกัดขนาด), `422 invalid_payload` (ใช้ `400` แทน), และ `200 duplicate` (ไม่มี dedupe)

---

## §2 · รับผล build

**CRN ไม่ยิง HTTP callback.** เมื่อ build เปลี่ยนสถานะ CRN จะ `INSERT` แถวลงตาราง `build_events` ใน **DB กลาง** แล้ว `pg_notify` → subscriber (FBD / FTC DV) มาอ่านเอง.

### วิธี consume
```sql
-- ตื่นแบบ real-time…
LISTEN build_events;              -- payload = id ของแถวใหม่

-- …หรือ poll แถวที่ consumer เรายังไม่ได้รับ
SELECT id, job_id, event_type, payload, created_at
FROM   build_events
WHERE  notified_ftcdv = false     -- ใช้ flag ของ consumer คุณ (notified_fbd สำหรับ FBD)
ORDER  BY created_at;

-- process เสร็จแล้วมาร์คของตัวเอง (at-least-once):
UPDATE build_events SET notified_ftcdv = true WHERE id = $1;
```

### รูปแบบแถว `build_events`
| คอลัมน์ | ค่า |
|---|---|
| `job_id` | uuid ของงาน (ตรงกับ `job_id` ใน response §1) |
| `event_type` | **`build_started` \| `build_done` \| `build_failed` \| `build_cancelled`** (บังคับด้วย DB CHECK) |
| `payload` (jsonb) | `build_done` → `{cost_usd, session_id, image_ref, git_remote, git_branch, env}` · `build_failed` → `{error}` · `build_cancelled` → `{reason}` |

> **`image_ref` (ใน `build_done`)** = docker image tag ที่ pull ได้ เมื่อเปิด image pipeline (`CRN_BUILD_IMAGE`) เช่น `172.168.1.234:5050/fitt/demos/crn-demo-<slug>-<id8>:v<n>` — consumer `docker pull` จากตรงนี้ได้เลย. **status = `event_type`** (`build_started`=กำลัง build · `build_done`=เสร็จ · `build_failed` · `build_cancelled`) — ไม่ต้องรอ HTTP callback
>
> **การันตี image-only (เปิด `CRN_BUILD_IMAGE`)** — เมื่อเปิด image pipeline, `build_done` จะมี `image_ref` เป็น **image จริงที่ pull ได้เสมอ**. ถ้า build/push image ไม่สำเร็จ CRN จะ **fail build** (ยิง `build_failed`) ไม่ปล่อย `build_done` ที่ image ใช้ไม่ได้ → **consumer `docker pull image_ref` ได้เลย ไม่ต้อง clone git** (image ทึบ ไม่มี source). ค่า `branch:<name>` โผล่เฉพาะตอน **ปิด** image pipeline (git-mode legacy) เท่านั้น

> **`build_cancelled`** (เพิ่ม migration 0009) = operator กด cancel — build ถูกฆ่าจริง (SIGKILL). แยกจาก `build_failed` เพื่อให้ dashboard โชว์ "cancelled" ไม่ใช่ error. **consumer ควร map เป็น "ไม่สำเร็จ/หยุดแล้ว"** (ไม่ใช่ error ต้อง retry). ฝั่ง HTTP callback (§3) ยัง map เป็น `failed` เพราะ vocab มีแค่ building/released/failed.
| `created_at`, `notified_fbd`, `notified_ftcdv` | เวลา + flag การส่งต่อ consumer |

> ใน `build_events`: **ไม่มี** สถานะ `released` (สำเร็จ = `build_done`), **ไม่มี** `409`. ถ้าอยากได้ HTTP callback แบบ `building`/`released`/`failed` + token → ดู §3

---

## §3 · HTTP callback ไป FTC DV — ✅ ทำแล้ว

เมื่อ set env `CRN_FTC_DV_CALLBACK_URL` CRN จะยิง callback หลัง build **เพิ่มจาก** `build_events` (ไม่ใช่แทน):

```
POST {CRN_FTC_DV_CALLBACK_URL}
Authorization: Bearer {CRN_FTC_DV_CALLBACK_TOKEN}
Content-Type: application/json

{ "status": "building" | "released" | "failed",
  "job_id": "<uuid ตรงกับ response §1>",
  "build_no": 7,
  "git_remote": "https://github.com/owner/repo.git",  // เฉพาะ released
  "git_branch": "main",                                 // เฉพาะ released
  "image_ref": "…:v4",       // ใส่มาถ้ามี docker_tag
  "env": {                    // เฉพาะ released — example runtime env ของ image
    "DATABASE_URL": "postgresql://USER:PASSWORD@HOST:5432/DB?schema=public",
    "PORT": "3000",           // port ที่ app listen ใน container (fixed)
    "APP_PORT": "4123"        // host port แนะนำ → map ไป container 3000 (ต่อ-project)
  },
  "message": "…" }           // ใส่มาเฉพาะตอน failed
```
- แมปสถานะ: `build_started → building`, `build_done → released`, `build_failed → failed`
- **`env` (เฉพาะ `released`)** = **example** runtime env ที่ image ต้องใช้ — operator inject ค่าจริงตอนรัน (ไม่มี bake ในภาพ). `DATABASE_URL` บังคับ (app + migrate image อ่าน); app listen container port `3000`, host เปิด `APP_PORT` (ต่อ-project). ตรงกับ `docker-compose.customer.yml` ที่ CRN เขียน
- **โหมด image (`CRN_BUILD_IMAGE` เปิด — แนะนำ):** ใช้ `image_ref` → `docker pull` แล้วรันได้เลย **ไม่ต้อง clone git**. `image_ref` บน `released` เป็น image จริงที่ pull ได้เสมอ (build ล้มถ้า image สร้าง/push ไม่ได้)
- **`git_remote`/`git_branch` ใส่มาเฉพาะตอน `released`** (โหมด git legacy — เมื่อ **ปิด** image pipeline) = repo/branch จริงที่ build push ไป (ถูกทั้งโหมด **shared** และ **owner**) → FTC DV clone จากค่านี้ ไม่ใช่ค่าใน `202` (โหมด owner ค่าใน 202 เป็น shared remote ซึ่งไม่ตรง). **เมื่อเปิด image pipeline ไม่ต้องใช้เส้นนี้**
- **best-effort**: ยิงไม่ผ่าน (หรือได้ non-2xx) = log แล้วปล่อย ไม่ล้ม build — `build_events` ยังเป็นแหล่งความจริงเสมอ
- ไม่ set `CRN_FTC_DV_CALLBACK_URL` = ปิด callback (ใช้ `build_events` อย่างเดียว)

**config**
```
CRN_FTC_DV_CALLBACK_URL=http://172.168.1.167:3101/api/ingest/crn/callback
CRN_FTC_DV_CALLBACK_TOKEN=<token>
```

---

## อ้างอิงโค้ด
- `internal/api/api.go` — `handleIngest`, route `POST /internal/projects`, decoder `DisallowUnknownFields`
- `internal/store/store.go` — `Notify` → `INSERT build_events` + `pg_notify`
- `migrations/0001_init.sql` — ตาราง `build_events` + `CHECK (event_type IN (...))`
- `internal/config/config.go` — env ทั้งหมด (ทุกตัวขึ้นต้น `CRN_*`)
