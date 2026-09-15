# CRN Integration Contract — สำหรับ FBD / Gateway / FTC DV

> อัปเดต **2026-09-15** · ตรวจทีละบรรทัดกับโค้ดจริง `fitt-coderunner` (`3cc732c`)
> เอกสารนี้บอก **สิ่งที่ CRN รับ/ส่งจริง ณ ตอนนี้** (ไม่ใช่ดีไซน์เป้าหมาย) เพื่อให้ฝั่ง FBD / Gateway / FTC DV ต่อกับ CRN ได้

---

## ✅ สถานะ (อ่านก่อน)

CRN **ต่อกับฝั่งดีไซน์ได้แล้ว** (เพิ่มแบบ *ไม่ทับ* ของเดิม):
- **ส่งงาน:** เลือกได้ทั้ง **`zip_base64`** (inline) หรือ **`zip_uri`** (ให้ CRN โหลด zip จาก URL / LAN IP เอง)
- **รับผล:** ได้ทั้ง **`build_events`** (เขียนลง DB กลางเสมอ) และ **HTTP callback ไป FTC DV** (เปิดด้วย env `CRN_FTC_DV_CALLBACK_URL`)

ทำตาม §1–§3 ได้เลย.

> ### ⚠️ เปลี่ยนจากฉบับ 2026-07-15 — consumer ต้องแก้ตาม
> **runtime env ของ image เปลี่ยน** — `DEV_EMAIL`/`DEV_PASSWORD` ถูกเลิกใช้แล้ว ตัวที่ต้องส่งจริงคือ
> `BOOTSTRAP_ADMIN_EMAIL`, `BOOTSTRAP_ADMIN_PASSWORD`, `AUTH_SECRET` (ดู §3 `env`).
> ถ้ายัง inject ตามฉบับเก่า **แอปลูกค้าจะ bootstrap ไม่ขึ้น** เพราะไม่มีบัญชีแรกและไม่มี session secret
>
> **image ไม่ `db push` แล้ว** — ใช้ `db:deploy` กับ migration ที่ commit ไว้ ทำให้ลูกค้าอัปเกรดได้โดยข้อมูลไม่หาย
>
> **เพิ่ม phase `verify`** คั่นระหว่าง agent กับ git push — เป็นขั้นที่นานที่สุดของ build (ดู §2)

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

> **ระหว่าง `build_started` กับ `build_done` มี phase อะไรบ้าง** — `build_events` มีแค่ 4 event ข้างบน แต่ live log WS (`/internal/projects/{id}/jobs/{build_no}/logs`) ส่ง phase marker ตามลำดับนี้ ใช้ทำ progress ได้:
>
> `repo` → `materialize` (edit build ใช้ `pull` แทน) → `claude` → **`verify`** → `git` → `push` → `docker`
>
> **`verify` เป็นขั้นที่นานที่สุด** — locked install → typecheck → unit tests → production build → build customer image จริง → รัน migration + seed ในคอนเทนเนอร์นั้น → ยิง browser test ใส่ image ที่รันอยู่ ถ้าขั้นนี้ไม่ผ่าน build จบเป็น `build_failed` **ก่อน** อะไรจะออกจากเครื่อง (ไม่มี git push ไม่มี image) · ถ้า UI ฝั่ง consumer แสดง progress ตาม phase อย่าลืมขั้นนี้ ไม่งั้นผู้ใช้จะเห็นค้างอยู่ที่ `claude` นานผิดปกติ

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
    "APP_PORT": "4123",       // host port แนะนำ → map ไป container 3000 (ต่อ-project)
    "AUTH_SECRET": "",                    // บังคับ — secret ของ session ต้องสุ่มต่อ deployment
    "BOOTSTRAP_ADMIN_EMAIL": "",          // บังคับตอนติดตั้งครั้งแรก (แอปที่มี login)
    "BOOTSTRAP_ADMIN_PASSWORD": "",       // บังคับตอนติดตั้งครั้งแรก — ≥16 ตัวอักษร ห้ามซ้ำข้ามลูกค้า
    "DEV_EMAIL": "",                 // deprecated — ส่งค่าว่างมาเพื่อ consumer รุ่นเก่า ไม่มีผลแล้ว
    "DEV_PASSWORD": "",              // deprecated — เหมือนกัน
    "FITT_FEEDBACK_URL": "http://FEEDBACK_HOST:PORT/api/ingest/feedback"  // ปลายทาง feedback widget (ถ้ามี)
  },
  "message": "…" }           // ใส่มาเฉพาะตอน failed
```
- แมปสถานะ: `build_started → building`, `build_done → released`, `build_failed → failed`
- **`env` (เฉพาะ `released`)** = **example** runtime env ที่ image ต้องใช้ — operator inject ค่าจริงตอนรัน (ไม่มี bake ในภาพ). `DATABASE_URL` บังคับ — image **apply migration ที่ commit ไว้** (`db:deploy` แล้วตามด้วย `db:seed`) ยิงไป DB ภายนอกนี้ตอน start **และ fail ให้เห็นถ้า migration ไม่ผ่าน** (ไม่ start ต่อ); app listen container port `3000`, host เปิด `APP_PORT` (ต่อ-project). ตรงกับ `docker-compose.customer.yml` ที่ CRN เขียน
- **`AUTH_SECRET` + `BOOTSTRAP_ADMIN_EMAIL` + `BOOTSTRAP_ADMIN_PASSWORD`** = ตัวที่ต้องส่งจริง. บัญชีผู้ดูแลคนแรกถูกสร้าง **ครั้งเดียวต่อฐานข้อมูล** ผ่าน advisory lock + marker ถาวรในตาราง `SystemBootstrap` → รันพร้อมกันหลายตัวได้ admin คนเดียว · ฐานข้อมูลที่มี user อยู่แล้วจะไม่สร้างเพิ่ม · ลบ user ทิ้งแล้ว restart ไม่ฟื้นคืนมา · **ไม่มีรหัส fallback** ถ้าไม่ส่งมาก็ไม่มีใครเข้าระบบได้
  - รหัสต้อง **ไม่ซ้ำข้ามลูกค้า** — ต่างจากฉบับเก่าที่ทุก demo ใช้รหัสเดียวกัน
  - หลังติดตั้งเสร็จ ถอด `BOOTSTRAP_ADMIN_*` ออกจาก deployment ได้ ระบบไม่อ่านอีกเมื่อมี marker แล้ว
- **`DEV_EMAIL`/`DEV_PASSWORD`** = **เลิกใช้แล้ว** ยังส่งมาเป็นค่าว่างเพื่อไม่ให้ consumer รุ่นเก่าพัง แอปไม่อ่าน 2 ตัวนี้อีกต่อไป
- **`FITT_FEEDBACK_URL`** = ปลายทางที่ in-demo feedback widget POST ไป. widget อ่านจาก **runtime env** (`data-ingest` server-render จาก `process.env.FITT_FEEDBACK_URL`) → operator ชี้ receiver ของตัวเองได้ต่อ deployment ไม่ต้อง rebuild. ใส่ full URL รวม path เอง (เช่น `.../api/ingest/feedback`). เกี่ยวเฉพาะตอน widget ถูกฝัง (build ตั้ง `CRN_FEEDBACK_INGEST_URL`)
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

## §4 · สั่ง edit build (feedback → แก้ demo เดิม)

เมื่อ FTC DV ตัดสินใจเอา feedback ไปแก้ → **POST edit request มา CRN** → CRN รัน edit build (clone repo เดิม + ให้ Claude แก้ตาม `change` + push branch เดิม → build image ใหม่) → callback เหมือน build ปกติ

**Endpoint** (trusted-network `/internal/*` เหมือน ingest — ไม่มี auth):
```
POST /internal/projects/{project_id}/edit
Content-Type: application/json

{ "change": "<free-text: จะแก้อะไร>" }
```

- **`change` เป็น string เดียว** (free-text = คำสั่งแก้ที่ Claude ทำตาม) — required. compose จาก feedback เอง เช่น
  `"[bug/high] ปุ่มบันทึก (button.save) กดแล้วไม่ทำงาน — แก้ให้ทำงาน"`. หลาย feedback → รวมเป็น 1 `change` (edit build เดียว) หรือ POST หลายครั้ง
- **ไม่ต้องแนบ artifacts** (brd/prd/prompts/zip) — CRN clone repo ที่แปลงไว้แล้วของ project นั้น (ต้องเคย build สำเร็จมาก่อน) แล้ว resume Claude session เดิม apply แค่ `change`
- CRN สร้าง payload `{mode:"edit", change, name}` ให้เองภายใน — **FTC DV ไม่ต้องส่ง `mode`**

**Response `202`:**
```json
{ "job_id": "<uuid>", "build_no": 8, "git_branch": "crn/<slug>-<id8>", "status": "queued" }
```

**Callback — ✅ เหมือน build ปกติ:** edit build วิ่งผ่าน runJob เดียวกัน → ยิง `build_events` (`build_started`/`build_done`) + HTTP callback (§3) `building` → `released` พร้อม **`image_ref` ใหม่** (`:v<build_no>`) + `git_remote`/`git_branch`/`env`. poll ด้วย `job_id` ที่ได้จาก `202`

> **ทางเลือก (tenant API, API-key auth):** `POST /api/v1/projects/{id}/edit-request` body `{requester, diff_request, priority}` — `diff_request` เป็น raw JSON ที่ต้องมี `{"mode":"edit","change":"…"}` เอง. ใช้เมื่อผ่าน per-org API key แทน trusted network. response `202 {edit_request_id, job_id, build_no, status}`. **แนะนำใช้ `/internal/.../edit` ถ้าเพื่อนต่อผ่าน `INGEST_CRN_URL` อยู่แล้ว** (ง่ายกว่า, body แค่ `change`)

---

## อ้างอิงโค้ด
- `internal/api/api.go` — `handleIngest` (`POST /internal/projects`) · `handleEditProject` (`POST /internal/projects/{id}/edit`, §4) · `handleEditRequest` (`POST /api/v1/projects/{id}/edit-request`)
- `internal/store/store.go` — `Notify` → `INSERT build_events` + `pg_notify`
- `migrations/0001_init.sql` — ตาราง `build_events` + `CHECK (event_type IN (...))`
- `internal/config/config.go` — env ทั้งหมด (ทุกตัวขึ้นต้น `CRN_*`)
