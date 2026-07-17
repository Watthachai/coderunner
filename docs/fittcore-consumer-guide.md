# FITTCORE Consumer Guide — รับ build status + docker image จาก CRN

> สำหรับทีม FITTCORE: วิธีรู้ว่า build **กำลังทำ / เสร็จ / fail** และดึง **docker image** ของ demo ไปส่งลูกค้า
> อ้างอิง: [crn-integration-contract.md](crn-integration-contract.md) §2

---

## ภาพรวม

```
FBD → FITTCORE → CRN build → docker push → GitLab registry (172.168.1.234:5050)
                    └→ เขียน build_events ลง DB กลาง (status + image_ref)
FITTCORE  ── อ่าน build_events (DB) ──→ รู้ status + image_ref
          ── docker pull <image_ref> จาก GitLab ──→ ส่งลูกค้า
```

**image อยู่บน GitLab (ที่เดียว)** — CRN ไม่ได้ส่งไฟล์ image มา FITTCORE, ส่งแค่ **"ที่อยู่" (`image_ref`)** ผ่าน `build_events`. FITTCORE `docker pull` เอง

**2 ช่องรับผล (ใช้ช่องไหนก็ได้ — แนะนำ DB):**
| ช่อง | เสถียร | มี image_ref |
|---|---|---|
| **`build_events` (DB)** ⭐ | สูง (poll/LISTEN ได้เอง) | ✅ (ใน `build_done`) |
| HTTP callback (`CRN_FTC_DV_CALLBACK_URL`) | best-effort (ต่อไม่ติด = หาย) | ✅ |

---

## 1. ต่อ DB (build_events)

Connection string (read-only role):
```
postgres://ftcdv:<token>@172.168.1.234... หรือ <crn-host>:5433/crn?sslmode=disable
```
> ใช้ role `ftcdv` (SELECT `build_events` + UPDATE `notified_ftcdv`). ขอ connection string จริงจากทีม CRN

## 2. STATUS = คอลัมน์ `event_type`

| `event_type` | แปลว่า |
|---|---|
| `build_started` | **กำลัง build** |
| `build_done` | **เสร็จ (สำเร็จ)** — มี `image_ref` ให้ pull |
| `build_failed` | build ล้ม (payload `{error}`) |
| `build_cancelled` | ถูกยกเลิก (payload `{reason}`) |

รูปแบบแถว:
```
id · job_id · event_type · payload(jsonb) · created_at · notified_ftcdv
```
`payload` ของ **`build_done`**:
```json
{
  "cost_usd": 0.42,
  "session_id": "…",
  "image_ref": "172.168.1.234:5050/fitt/demos/crn-demo-cafe-pre-order-cc0b6195:v3",
  "git_remote": "…",
  "git_branch": "…"
}
```

## 3. วิธี consume (at-least-once)

```sql
-- real-time
LISTEN build_events;                 -- payload = id ของแถวใหม่

-- หรือ poll แถวที่ยังไม่ได้รับ
SELECT id, job_id, event_type, payload, created_at
FROM   build_events
WHERE  notified_ftcdv = false
ORDER  BY created_at;

-- process เสร็จ mark ของตัวเอง
UPDATE build_events SET notified_ftcdv = true WHERE id = $1;
```

**Logic ฝั่ง FITTCORE (pseudo):**
```
for row in new build_events:
    match row.event_type:
      "build_started"  -> ส่ง status "building" ให้ผู้ใช้
      "build_done"     -> image = row.payload.image_ref
                          docker pull image   # จาก GitLab
                          ส่งลูกค้า / mark "ready"
      "build_failed"   -> ส่ง status "failed" (row.payload.error)
      "build_cancelled"-> ส่ง status "cancelled" (row.payload.reason)
    mark notified_ftcdv = true
```

## 4. ดึง image จาก GitLab (build_done)

```bash
# login ครั้งเดียว (deploy token scope read_registry)
docker login 172.168.1.234:5050 -u <deploy-token-user> -p <token>

# pull ด้วย image_ref จาก payload
docker pull 172.168.1.234:5050/fitt/demos/crn-demo-<slug>-<id8>:v<n>
```
> GitLab registry เป็น **HTTP** → docker client ต้องตั้ง `"insecure-registries": ["172.168.1.234:5050"]` (daemon.json / Docker Desktop)

## 5. mapping status → ผู้ใช้

| build_events | status ที่ส่งต่อ |
|---|---|
| build_started | 🔨 กำลัง build |
| build_done | ✅ เสร็จ (+ image พร้อม pull) |
| build_failed | ❌ ล้มเหลว |
| build_cancelled | ⏹ ยกเลิก |

---

## หมายเหตุ
- `image_ref` เป็น `branch:<name>` (ไม่ใช่ image tag) ถ้า CRN ยังไม่เปิด `CRN_BUILD_IMAGE` — เช็ค prefix `172.168...` เพื่อรู้ว่าเป็น image จริง
- ต่อ build เดียวจะมี ≥2 events: `build_started` แล้วตามด้วย `build_done`/`build_failed`/`build_cancelled` (จับคู่ด้วย `job_id`)
- อยากได้ HTTP callback แบบ real-time push แทน poll → ดู contract §3 (`CRN_FTC_DV_CALLBACK_URL`) — payload คล้ายกัน มี `image_ref`
