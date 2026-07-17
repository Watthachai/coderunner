# Feedback Widget — Payload Spec (สำหรับต่อ API เพื่อน)

> ปุ่ม 💬 feedback ที่ CRN ฝังในทุก demo (`fitt-feedback.js`) ส่งอะไรบ้าง — เอาไปทำ endpoint รับได้เลย

## Request

```
POST <ingest-endpoint>
Content-Type: application/json
```
- **ไม่มี auth** (ตอนนี้ยิงตรงแบบ unauthenticated) — ถ้า API เพื่อนต้องการ auth ต้องบอกมาเพิ่ม
- endpoint ตั้งที่ CRN env **`CRN_FEEDBACK_INGEST_URL`** (baked เข้า widget ตอน build ผ่าน attribute `data-ingest`) → **ชี้มา API เพื่อนได้เลย**
- widget ถือว่าสำเร็จเมื่อได้ **HTTP 2xx**; non-2xx = โชว์ error ให้ user

## Body (payload เต็ม)

```jsonc
{
  "project_id": "<uuid>",        // demo ไหน (ค่าจาก data-project ตอน build)
  "category":   "bug" | "feature" | "style",   // default "feature"
  "priority":   "low" | "med" | "high",         // default "med"
  "note":       "<ข้อความที่ user พิมพ์>",       // required-ish (ต้องมี note หรือ pin อย่างน้อย 1)
  "page_url":   "https://demo-host/some/path",  // หน้าที่ user กดส่ง (location.href)
  "reporter":   "",              // ตอนนี้ว่างเสมอ (เผื่ออนาคต)
  "payload": {                   // object ซ้อน (nested) — เก็บ context การ pin
    "pins": [                    // จุดที่ user ปักบน UI (0..N รายการ)
      {
        "selector":    "#root > div.card:nth-child(2)",  // CSS path ของ element
        "label":       "Add to cart",     // ป้ายสั้นๆ ของ element (text/aria/tag)
        "note":        "",                // โน้ตต่อ pin (ตอนนี้ว่าง)
        "box":         { "x": 120, "y": 340, "w": 200, "h": 48 },  // ตำแหน่ง+ขนาด (page coords, px, int)
        "region_shot": "data:image/svg+xml;..." // ภาพ element นั้น (SVG data URI, self-contained)
      }
    ],
    "full_shot":  "",            // ตอนนี้ว่างเสมอ (เผื่อ full-page shot อนาคต)
    "viewport":   { "w": 1440, "h": 900 },     // ขนาดจอ user ตอนส่ง
    "user_agent": "Mozilla/5.0 ..."            // navigator.userAgent
  }
}
```

## หมายเหตุสำคัญ (สำหรับคนทำ API รับ)

1. **`payload.pins[].region_shot` ใหญ่ได้** — เป็น SVG data URI (base64) ของ element ที่ปัก อาจ **หลาย KB–MB ต่อ pin** → API ต้องรับ body ใหญ่ (อย่าจำกัด ~100KB). ถ้าไม่ต้องการภาพ บอกได้ จะตัดออกให้
2. **`note` / `page_url` / `category` / `priority` / `project_id`** = field หลักที่ต้องเก็บ; `payload` (nested) เก็บเป็น **JSON/jsonb** ทั้งก้อนได้
3. **enum**: category ∈ `bug|feature|style` · priority ∈ `low|med|high` (พิมพ์เล็ก)
4. **`reporter` + `full_shot` + `pins[].note`** = ว่างเสมอตอนนี้ (มี field ไว้ แต่ยังไม่ได้ใช้)
5. เงื่อนไขส่ง: ต้องมี **`note` หรือ `pins` อย่างน้อย 1** (widget เช็คก่อนยิง)

## ตัวอย่างจริง (มี 1 pin)

```json
{
  "project_id": "cc0b6195-46b4-4c29-a167-c0dd321d10d9",
  "category": "bug",
  "priority": "high",
  "note": "ปุ่มสั่งซื้อกดแล้วไม่มีอะไรเกิดขึ้น",
  "page_url": "http://192.168.1.50:3000/checkout",
  "reporter": "",
  "payload": {
    "pins": [
      {
        "selector": "#root > main > button.checkout-btn",
        "label": "ยืนยันคำสั่งซื้อ",
        "note": "",
        "box": { "x": 640, "y": 720, "w": 240, "h": 52 },
        "region_shot": "data:image/svg+xml;charset=utf-8,%3Csvg...%3C/svg%3E"
      }
    ],
    "full_shot": "",
    "viewport": { "w": 1512, "h": 982 },
    "user_agent": "Mozilla/5.0 (Macintosh; ...) Safari/605.1"
  }
}
```

## จะให้ widget ยิงมา API เพื่อน
ตั้ง CRN env แล้ว build demo ใหม่ (ค่าถูก bake ตอน build):
```bash
CRN_FEEDBACK_INGEST_URL=https://<friend-api>/feedback   # endpoint เพื่อน
```
> demo ที่ build ไปแล้วยังชี้ endpoint เก่า — ต้อง rebuild ถึงเปลี่ยน

## อ้างอิงโค้ด
- `internal/buildstep/fitt-feedback.js` — สร้าง body + `fetch(INGEST, {POST, JSON})` (~บรรทัด 491-508)
- ปัจจุบันปลายทาง = PostgREST ตาราง `feedback_requests` (คอลัมน์ตรงกับ field ข้างบน; `payload` เป็น jsonb)
