# ผู้ช่วย AI ในหน้าต่าง 💬 (23 พ.ย. 2026)

ลูกค้ามักขอแก้เป็นประโยคคำถาม เช่น "ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม" แล้ว build ตอบว่า "ทำได้ครับ" โดยไม่ได้แก้ ในหน้าต่าง 💬 ลูกค้าจึงคุยกับผู้ช่วย AI ได้ ผู้ช่วยถามกลับเมื่อข้อมูลไม่พอ แล้วร่างคำขอแก้เป็นประโยคสั่ง เช่น "ย้ายปุ่ม "บันทึก" ไปไว้ใต้ตารางรายการ" ร่างนั้นจะไปเติมในฟอร์มเดิม ลูกค้าตรวจหรือแก้ได้ก่อนกดส่ง และส่งเป็น feedback ปกติ ส่วนที่ส่งออกไปไม่เปลี่ยน

- **เปิดใช้:** ตั้ง `FITT_FEEDBACK_AI_URL` ใน env ของแอป ถ้าไม่ตั้ง หน้าต่างจะเป็นฟอร์มแบบเดิม
- **ใครเรียก AI:** server ของแอปเป็นคนเรียก ผ่าน route `/api/fitt-feedback?op=assist` เบราว์เซอร์ไม่เห็นทั้ง URL และ key
- **🎤:** ใช้ตัวแปลงเสียงเป็นข้อความของเบราว์เซอร์ ภาษาไทย (`th-TH`) ปุ่มขึ้นเฉพาะเบราว์เซอร์ที่รองรับ เช่น Chrome, Edge และ Safari ใน Chrome เสียงจะถูกส่งไปแปลงที่ Google

## สิ่งที่ตัวเชื่อม AI ต้องรับ

แอปส่ง `POST` ไปที่ `FITT_FEEDBACK_AI_URL` เป็น JSON ถ้าตั้ง `FITT_FEEDBACK_AI_KEY` ไว้ จะใส่ `Authorization: Bearer <key>` ไปด้วย รอคำตอบนานสุด 30 วินาที และไม่ตาม redirect

```json
{
  "instructions": "You help a customer of this web app tell its team what they want changed. ...",
  "messages": [
    { "role": "user", "text": "ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม" },
    { "role": "assistant", "text": "อยากให้ไปอยู่ใต้ตารางรายการ หรือมุมขวาล่างของหน้าครับ" },
    { "role": "user", "text": "ใต้ตารางรายการ" }
  ],
  "context": { "page": "/deals", "pins": ["ปุ่มบันทึก — อันนี้"] },
  "schema": { "type": "object", "properties": { "reply": {}, "draft": {} }, "required": ["reply"] }
}
```

- `instructions` เป็นคำสั่งของ CRN ให้ส่งต่อให้โมเดลเป็น system instruction ตามที่ได้รับ ไม่ต้องแต่งเพิ่ม
- `schema` คือรูปแบบคำตอบ สำหรับ Gemini ใส่เป็น `responseSchema` พร้อม `responseMimeType: "application/json"`
- `messages` มีได้สูงสุด 20 ข้อความ ข้อความละไม่เกิน 2,000 ตัวอักษร `role` เป็น `user` (ลูกค้า) หรือ `assistant` (ผู้ช่วย) เท่านั้น สำหรับ Gemini ให้แปลง `assistant` เป็น `model`

## สิ่งที่ตัวเชื่อม AI ต้องตอบ

ตอบ `200` เป็น JSON:

```json
{ "reply": "สรุปคำขอให้แล้วครับ ตรวจแล้วกดส่งได้เลย",
  "draft": { "text": "ย้ายปุ่ม \"บันทึก\" ไปไว้ใต้ตารางรายการ", "category": "feature", "priority": "med" } }
```

- `reply` เป็นข้อความถึงลูกค้า ต้องมี และยาวไม่เกิน 4,000 ตัวอักษร
- `draft` เป็น `null` ถ้ายังไม่รู้ว่าจะแก้อะไร
  - `category` เป็น `bug`, `feature` หรือ `style`
  - `priority` เป็น `low`, `med` หรือ `high`
  - `text` ห้ามว่าง
- คำตอบที่ผิดรูปแบบ แอปจะไม่ส่งต่อให้ลูกค้า (ตอบ 502 `bad_answer`)
- ถ้าเรียกไม่ถึง ตอบไม่ใช่ 2xx หรือ redirect แอปจะตอบ 502 `ai_unreachable` หน้าต่างจะบอกลูกค้าให้ลองใหม่ หรือกด "เขียนคำขอเอง"

ลองตัวเชื่อมแบบเดียวกับที่แอปเรียก:

```bash
curl -s -X POST "$FITT_FEEDBACK_AI_URL" -H "content-type: application/json" -H "authorization: Bearer $FITT_FEEDBACK_AI_KEY" \
  -d '{"instructions":"ตอบเป็นภาษาไทย","messages":[{"role":"user","text":"ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม"}],"context":{"page":"/","pins":[]},"schema":{"type":"object","properties":{"reply":{"type":"string"}},"required":["reply"]}}'
```

## เมื่อแอปไหนจะได้

route และ widget เป็นของ CRN และ CRN ใส่ใหม่ให้ทุก build แอปจะมีผู้ช่วยตั้งแต่ build ถัดไป และต้องตั้ง `FITT_FEEDBACK_AI_URL` ใน env ของแอปนั้น (และ `FITT_FEEDBACK_AI_KEY` ถ้าตัวเชื่อมต้องใช้) CRN ใส่ทั้งสองตัวใน `crn.runtime.json` ให้เอง ทั้งคู่เป็น required: no ส่วน key เป็น secret
