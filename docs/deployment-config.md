# CRN Deployment & Config — env ทั้งหมด + deploy เครื่องลูกค้า

> **ทุก host/URL เป็น env ล้วน — ไม่มี IP hardcode ในโค้ด.** Deploy เครื่องใหม่ = แก้ `.env` อย่างเดียว ไม่ต้องแตะโค้ด/build ใหม่.

---

## Flow — ใครยิงหาใคร (+ env knob)

```
[FBD studio :3000]
  │ POST /api/fittcore  (proxy ฝั่ง server, กัน URL/key หลุด client)
  │   ├─ prod:  FITTCORE_GATEWAY_URL /v1/ingest        (+ FITTCORE_GATEWAY_API_KEY)
  │   └─ local: FITTCORE_DIRECT_CRN_URL /internal/projects   (ข้าม gateway; dev เท่านั้น)
  ▼
[CRN backend :8080]  materialize → claude → git push (CRN_GIT_REMOTE)
  │
  ├─(A) เขียนผลลง build_events (DB กลาง)      ◄── FITTCORE/consumer อ่านที่นี่
  │        env: CRN_CENTRAL_DATABASE_URL (ว่าง = ใช้ CRN_DATABASE_URL)
  │        port: 5433 (host) → 5432 (container)
  │
  └─(B) ยิง HTTP callback → CRN_FTC_DV_CALLBACK_URL   ◄── "ยิงกลับ FITTCORE"
           (+ CRN_FTC_DV_CALLBACK_TOKEN)  best-effort: ต่อไม่ติด = log แล้วปล่อย

[Dashboard :3001] → derive backend = <browser-host>:8080 เอง (ไม่ต้อง config)
```

**รับผล build มี 2 ทาง (ใช้คู่กันได้):** (A) อ่าน `build_events` จาก DB กลาง = แหล่งความจริงเสมอ · (B) HTTP callback = ตัวเสริม เปิดเมื่อ set `CRN_FTC_DV_CALLBACK_URL`

---

## จุด config ทั้งหมด (env)

| box | env | หน้าที่ |
|---|---|---|
| **FBD** | `FITTCORE_GATEWAY_URL` / `FITTCORE_GATEWAY_API_KEY` | ยิง build ผ่าน gateway (prod) |
| | `FITTCORE_DIRECT_CRN_URL` | ยิงตรง CRN (dev/local) — **prod ลบทิ้ง** |
| **CRN** | `CRN_LISTEN_ADDR` | backend listen (`:8080`) |
| | `CRN_DATABASE_URL` | store DB |
| | `CRN_CENTRAL_DATABASE_URL` | DB กลาง (build_events); ว่าง = ใช้ store DB |
| | `CRN_FTC_DV_CALLBACK_URL` / `_TOKEN` | HTTP callback ไป FITTCORE |
| | `CRN_FEEDBACK_INGEST_URL` | PostgREST feedback ingest (`:3010`) |
| | `CRN_GIT_REMOTE` | git push target (ว่าง = skip push) |
| | `CRN_MONGO_URL` | Mongo |
| **Dashboard** | `NEXT_PUBLIC_CRN_API` | **ปล่อยว่าง** = derive จาก browser host เอง |

---

## Preset A — ลูกค้า 1 เครื่อง (แนะนำ · ปกติสุด)

ทุก service รันบนเครื่องเดียวกัน → **`localhost` ล้วน → ไม่มี LAN IP** → ทุกเครื่องลูกค้าใช้ `.env` ก้อนเดียวกันเป๊ะ ไม่ต้องแก้ต่อเครื่อง

```bash
# FBD .env
FITTCORE_DIRECT_CRN_URL=http://localhost:8080     # ยิงตรง CRN บนเครื่องเดียวกัน
# (ไม่ต้องมี GATEWAY_URL ถ้าไม่ผ่าน gateway)

# CRN .env
CRN_LISTEN_ADDR=:8080
CRN_DATABASE_URL=postgres://crn:<pw>@localhost:5433/crn?sslmode=disable
CRN_FTC_DV_CALLBACK_URL=http://localhost:3101/api/ingest/crn/callback
CRN_FEEDBACK_INGEST_URL=http://localhost:3010/feedback_requests
```

## Preset B — docker-compose เดียว (แยก container)

ยิงกันด้วย **service name** (docker resolve เอง) แทน IP/localhost:

```bash
# CRN .env (ใน compose)
CRN_DATABASE_URL=postgres://crn:<pw>@postgres:5432/crn?sslmode=disable
CRN_FTC_DV_CALLBACK_URL=http://fittcore:3101/api/ingest/crn/callback
# FBD
FITTCORE_DIRECT_CRN_URL=http://crn:8080
```

## Preset C — แยกหลายเครื่องจริง

ใช้ **hostname/DNS** แทน IP → ย้ายเครื่อง/เปลี่ยน IP แก้ที่ DNS ที่เดียว ไม่ต้องไล่แก้ env:

```bash
CRN_FTC_DV_CALLBACK_URL=http://fittcore.company.local:3101/api/ingest/crn/callback
FITTCORE_GATEWAY_URL=http://gateway.company.local:8080
```

> **หลักการ:** อย่า hardcode IP ใน `.env` ถ้าเลี่ยงได้ — เรียงลำดับความทน: `localhost` (เครื่องเดียว) > docker service name > DNS hostname > IP (ท้ายสุด). ที่ตอนนี้เต็มไปด้วย `172.168.1.x` เพราะ **dev กระจาย 3 ส่วน 3 เครื่อง** — deploy จริงมักยุบเหลือ Preset A.

---

## เช็คว่า deploy ลงจริง (build version)

CRN ฝัง git revision + build time ไว้ใน binary (จาก `go build` อัตโนมัติ) — ใช้ยืนยันว่าเครื่องนั้นรัน commit ไหน หลัง `git pull` + restart:

- **ตอน boot** log บรรทัด `starting CRN` มี field `revision` / `built` / `modified`
- **จากเครื่องอื่น** `curl http://<crn-host>:8080/healthz` →
  ```json
  { "status": "ok", "build": { "revision": "4f1477e", "time": "2026-07-21T…Z", "modified": false } }
  ```
  เทียบ `revision` กับ `git rev-parse --short=7 HEAD` (หรือ commit ที่ตั้งใจ deploy). `modified: true` = build จาก working tree ที่ยังไม่ commit

---

## Read-only DB user สำหรับ consumer (FITTCORE / ทีมอื่น)

อย่าส่ง superuser `crn` ให้ consumer. สร้าง role อ่านอย่างเดียว (mark ได้เฉพาะ `notified_ftcdv`):

```sql
CREATE ROLE ftcdv LOGIN PASSWORD '<ตั้งรหัส>';
GRANT CONNECT ON DATABASE crn TO ftcdv;
GRANT USAGE  ON SCHEMA public TO ftcdv;
GRANT SELECT ON build_events TO ftcdv;
GRANT UPDATE (notified_ftcdv) ON build_events TO ftcdv;
```

Consumer ต่อด้วย: `postgres://ftcdv:<รหัส>@<crn-host>:5433/crn?sslmode=disable`
แล้ว consume ตาม [crn-integration-contract.md](./crn-integration-contract.md) §2 (`LISTEN build_events` หรือ poll `WHERE notified_ftcdv=false`).

> **security:** `?sslmode=disable` = trust network ปลายทาง — ใช้เฉพาะ LAN ที่เชื่อถือได้ / อย่า expose `:5433` ออก public. Production จริงควรเปิด TLS + จำกัด `pg_hba.conf`/firewall ตาม IP consumer.
