# CRN Integration Contract — สำหรับ FBD / Gateway / FTC DV

> อัปเดต **2026-07-15** · ตรวจทีละบรรทัดกับโค้ดจริง `fitt-coderunner`
> เอกสารนี้บอก **สิ่งที่ CRN รับ/ส่งจริง ณ ตอนนี้** (ไม่ใช่ดีไซน์เป้าหมาย) เพื่อให้ฝั่ง FBD / Gateway / FTC DV ต่อกับ CRN ได้

---

## ⚠️ สถานะ (อ่านก่อน)

ตอนนี้ CRN ยังเป็น **MVP**. ฝั่งปลายทางที่ build ตามดีไซน์ (`zip_uri` + FTC DV HTTP callback) **ยังต่อกับ CRN ไม่ได้ทันที** — ต้องเติม 2 จุดฝั่ง CRN ก่อน (ดู [§3](#3--ช่องที่ต้องเติมฝั่ง-crn-ตกลงจะทำ)).

ระหว่างนี้ ถ้าจะทดสอบให้ทะลุได้เลย ต่อแบบ MVP ตาม §1–§2: **ส่ง `zip_base64` + อ่านผลจาก `build_events`**.

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
- decoder **เข้มงวด** (`DisallowUnknownFields`) → มีฟิลด์แปลกปลอม (เช่น `zip_uri`) = **`400`**

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
  "zip_base64": "<base64 ของ zip ทั้งก้อน>",   // ⚠️ แนบมาในตัว ไม่ใช่ URL
  "zip_bytes": 12345,         // metadata (CRN ยังไม่ verify)
  "file_count": 8
}
```

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
| `event_type` | **`build_started` \| `build_done` \| `build_failed`** (บังคับด้วย DB CHECK) |
| `payload` (jsonb) | `build_done` → `{cost_usd, session_id}` · `build_failed` → `{error}` |
| `created_at`, `notified_fbd`, `notified_ftcdv` | เวลา + flag การส่งต่อ consumer |

> **ไม่มี** สถานะ `released` (สำเร็จ = `build_done`), **ไม่มี** `build_no`/`image_ref` ใน event (docker image push ยัง TODO), **ไม่มี** `409` (ไม่ใช่ HTTP)

---

## §3 · ช่องที่ต้องเติมฝั่ง CRN (ตกลงจะทำ)

เพื่อให้ตรงกับฝั่งที่ทีม build ตามดีไซน์ไว้แล้ว (Gateway `172.168.1.167:8080` ใช้ `zip_uri`, FTC DV `172.168.1.167:3101` มี callback + token) CRN ต้องเพิ่ม (แบบ **เพิ่ม ไม่ทับของเดิม**):

1. **รับ `zip_uri`** ใน body ของ `/internal/projects` แล้ว **`HTTP GET` โหลด zip** จาก LAN IP (ทำงานคู่กับ `zip_base64` เดิม)
2. **ยิง HTTP callback ไป FTC DV** หลัง build — `POST {FTC_DV_URL}/api/ingest/crn/callback` + `Authorization: Bearer <token>`, แมปสถานะ `build_started→building`, `build_done→released`, `build_failed→failed` (ทำเพิ่มจากการเขียน `build_events`)
3. **config ใหม่**: `CRN_FTC_DV_CALLBACK_URL`, `CRN_FTC_DV_CALLBACK_TOKEN`

> จนกว่า 2 ข้อแรกจะเสร็จ ให้ฝั่งทีมใช้ path MVP: **ส่ง `zip_base64`** และ **อ่าน `build_events`** ไปก่อน

---

## อ้างอิงโค้ด
- `internal/api/api.go` — `handleIngest`, route `POST /internal/projects`, decoder `DisallowUnknownFields`
- `internal/store/store.go` — `Notify` → `INSERT build_events` + `pg_notify`
- `migrations/0001_init.sql` — ตาราง `build_events` + `CHECK (event_type IN (...))`
- `internal/config/config.go` — env ทั้งหมด (ทุกตัวขึ้นต้น `CRN_*`)
