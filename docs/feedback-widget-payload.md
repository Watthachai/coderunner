# Feedback Widget — Payload Spec (สำหรับ receiver ฝั่ง FTC DV)

> ปุ่ม 💬 feedback ที่ CRN ฝังในทุก demo (`fitt-feedback.js`) ส่งอะไร ไปไหน — เอาไปทำ endpoint รับได้เลย
> ทุกค่ายืนยันจาก source จริง `internal/buildstep/fitt-feedback.js`

---

## 1. Endpoint

```
POST  {FITT_FEEDBACK_URL}
Content-Type: application/json
```

- **endpoint อ่านจาก RUNTIME env `FITT_FEEDBACK_URL`** — widget `data-ingest` ถูก server-render จาก `process.env.FITT_FEEDBACK_URL` ตอน request → operator ตั้งค่าเองต่อ deployment **ไม่ต้อง rebuild** (ตั้งใน Run demo Environment เหมือน `DATABASE_URL`)
  ```
  FITT_FEEDBACK_URL=http://172.168.1.247:3101/api/ingest/feedback
  ```
  ใส่ **full URL รวม path เอง** → ไม่ต้องมี alias route
- **ไม่มี auth header** (widget ยิงตรง unauthenticated). ถ้า receiver ต้องการ auth ต้องแจ้ง CRN เพิ่ม
- widget ถือว่าสำเร็จเมื่อได้ **HTTP 2xx** → โชว์ "ส่งแล้ว ขอบคุณ ✓". non-2xx → โชว์ error
- **build-time gate:** widget จะถูกฝังก็ต่อเมื่อ CRN build ด้วย `CRN_FEEDBACK_INGEST_URL` ตั้งค่า (ค่านั้นเป็น fallback ตอน `FITT_FEEDBACK_URL` ไม่ได้ตั้ง)

> ⚠️ **ต้อง rebuild demo** ด้วย CRN ที่มี fix นี้ (commit `609b8c9`+) — build เก่า `data-ingest` ยัง baked เป็น literal เปลี่ยนด้วย env ไม่ได้

---

## 2. Body (payload เต็ม)

```jsonc
{
  "project_id": "6cb637df-e8ed-4d78-aa4a-f4962a47b01c", // uuid (data-project) = demo ไหน
  "category":   "feature",           // enum: "bug" | "feature" | "style"  (default "feature")
  "priority":   "med",               // enum: "low" | "med" | "high"       (default "med")
  "note":       "ปุ่มบันทึกควรเป็นสีเขียว", // ข้อความหลักที่ user พิมพ์ (textarea)
  "page_url":   "http://localhost:4432/dashboard", // location.href หน้าที่กดส่ง
  "reporter":   "",                  // ตอนนี้ "" เสมอ (เผื่ออนาคต)
  "payload": {                       // object ซ้อน — context การ pin
    "pins": [                        // จุดที่ user ปักบน UI (0..N)
      {
        "selector":    "div.card > button.save",       // CSS path (หรือ "region" ถ้าปักพื้นที่)
        "label":       "Save button",                  // ป้ายสั้นของ element (หรือ "พื้นที่ 200×48")
        "note":        "เปลี่ยนเป็นสีเขียว",            // โน้ตต่อจุด (user พิมพ์แก้ได้)
        "box":         { "x": 120, "y": 340, "w": 200, "h": 48 }, // page coords (รวม scroll แล้ว) px
        "region_shot": "data:image/svg+xml;base64,PHN2Zy…"        // screenshot ของจุด — ⚠️ ใหญ่ได้มาก
      }
    ],
    "full_shot":  "",                // ตอนนี้ "" เสมอ
    "viewport":   { "w": 1280, "h": 800 },  // ขนาด viewport ตอนส่ง
    "user_agent": "Mozilla/5.0 …"           // navigator.userAgent
  }
}
```

### field reference

| field | type | หมายเหตุ |
|---|---|---|
| `project_id` | uuid string | demo ไหน — map กับ CRN project |
| `category` | enum | `bug` \| `feature` \| `style` |
| `priority` | enum | `low` \| `med` \| `high` |
| `note` | string | คำอธิบายรวม |
| `page_url` | string (URL) | หน้าที่ user อยู่ตอนส่ง |
| `reporter` | string | "" เสมอ (ยังไม่เก็บ) |
| `payload.pins[]` | array | จุดปัก — ว่างได้ถ้ามีแค่ `note` |
| `payload.pins[].selector` | string | CSS path หรือ `"region"` |
| `payload.pins[].label` | string | ป้าย element / "พื้นที่ W×H" |
| `payload.pins[].note` | string | โน้ตต่อจุด |
| `payload.pins[].box` | `{x,y,w,h}` | **page** coords (รวม `scrollX/Y`) หน่วย px, int |
| `payload.pins[].region_shot` | string (data URL) | `data:image/svg+xml;base64,…` **หลาย MB ได้** |
| `payload.full_shot` | string | "" เสมอ |
| `payload.viewport` | `{w,h}` | ขนาด viewport |
| `payload.user_agent` | string | UA ของ browser |

---

## 3. ตัวอย่าง curl (ทดสอบ receiver)

```bash
curl -sS -X POST "http://172.168.1.247:3101/api/ingest/feedback" \
  -H "Content-Type: application/json" \
  -d '{
    "project_id": "6cb637df-e8ed-4d78-aa4a-f4962a47b01c",
    "category": "bug",
    "priority": "high",
    "note": "ปุ่มบันทึกกดแล้วไม่ทำงาน",
    "page_url": "http://localhost:4432/dashboard",
    "reporter": "",
    "payload": {
      "pins": [{
        "selector": "button.save",
        "label": "Save button",
        "note": "ไม่ตอบสนอง",
        "box": { "x": 120, "y": 340, "w": 200, "h": 48 },
        "region_shot": ""
      }],
      "full_shot": "",
      "viewport": { "w": 1280, "h": 800 },
      "user_agent": "curl-test"
    }
  }'
```
คาดหวัง: **HTTP 2xx** (widget อ่าน `res.ok`)

---

## 4. Receiver checklist (ฝั่ง FTC DV)

- [ ] **required:** `project_id` (uuid) + อย่างน้อย `note` **หรือ** 1 pin (widget บังคับก่อนส่ง)
- [ ] **body size limit สูง** — `region_shot` เป็น base64 SVG screenshot หลาย MB/pin (หลาย pin = ใหญ่มาก) → กัน 413. แนะนำ ≥ 25MB หรือ stream
- [ ] ตอบ **200/201** เมื่อรับสำเร็จ (ไม่งั้น widget โชว์ "ส่งไม่สำเร็จ")
- [ ] `note` อยู่ 2 ระดับ: top-level = คำอธิบายรวม · `payload.pins[].note` = ต่อจุด
- [ ] validate enum: `category` ∈ {bug,feature,style} · `priority` ∈ {low,med,high}
- [ ] **CORS:** widget POST จาก origin ของ demo (`http://<demo-host>:<port>`) → receiver ต้องอนุญาต origin นั้น (หรือ `*` สำหรับ ingest)
- [ ] เก็บ `payload` (nested) เป็น **jsonb** ทั้งก้อนได้; field บนเป็นคอลัมน์

---

## 5. หลังรับ feedback แล้ว — มี callback ไหม?

**ไม่มี callback แบบ build.** feedback เป็น POST sync → **response 2xx = ตัว ack** เอง (ไม่มีงาน async ให้รอ ต่างจาก build).

ตัวที่ callback แบบ build (`building`→`released`) คือตอนเอา feedback ไป **สั่ง edit build**:

```
widget → receiver (200 ack)  →  [FTC DV เลือก feedback ไปแก้]  →  ส่ง edit build เข้า CRN (mode:"edit")  →  build + callback ปกติ
```

feedback POST **ไม่ trigger edit build อัตโนมัติ** — FTC DV คุมเองว่า feedback ไหนเอาไปแก้ (ดู callback ของ edit build ที่ [crn-integration-contract.md](crn-integration-contract.md) §3)

---

## อ้างอิงโค้ด
- `internal/buildstep/fitt-feedback.js` — สร้าง body + `fetch(INGEST, {POST, JSON})` (~บรรทัด 491-508)
- `internal/buildstep/feedback.go` — inject `<script>` เข้า `app/layout.tsx`; `data-ingest={process.env.FITT_FEEDBACK_URL ?? "<fallback>"}` (runtime env)
- `internal/buildstep/dockerbuild.go` — `FITT_FEEDBACK_URL` อยู่ใน `DemoEnvExample` (env contract ที่ callback ส่งไป)
