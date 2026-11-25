# ผู้ช่วย AI ในหน้าต่าง 💬 (23 พ.ย. 2026)

ลูกค้ามักขอแก้เป็นประโยคคำถาม เช่น "ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม" แล้ว build ตอบว่า "ทำได้ครับ" โดยไม่ได้แก้ ในหน้าต่าง 💬 ลูกค้าจึงคุยกับผู้ช่วย AI ได้ ผู้ช่วยถามกลับเมื่อข้อมูลไม่พอ แล้วร่างคำขอแก้เป็นประโยคสั่ง เช่น "ย้ายปุ่ม "บันทึก" ไปไว้ใต้ตารางรายการ" ร่างนั้นจะไปเติมในฟอร์มเดิม ลูกค้าตรวจหรือแก้ได้ก่อนกดส่ง และส่งเป็น feedback ปกติ ส่วนที่ส่งออกไปไม่เปลี่ยน รูปที่ลูกค้าแนบในแชตส่งไปกับ feedback ด้วย เป็น pin รูปแบบเดิม (`selector: "region"`, `label: "รูปที่แนบ: <ชื่อไฟล์>"`, PNG ด้านยาวไม่เกิน 1600px) ส่วนเอกสาร (PDF, Excel, Word, ข้อความ) ให้ AI อ่านอย่างเดียว ไม่ได้ส่งไปกับ feedback AI จะใส่รายละเอียดที่ทีมต้องใช้ลงในคำขอแก้แทน

- **เปิดใช้:** ตั้ง `FITT_FEEDBACK_AI_URL` ใน env ของแอป ถ้าไม่ตั้ง หน้าต่างจะเป็นฟอร์มแบบเดิม
- **ใครเรียก AI:** server ของแอปเป็นคนเรียก ผ่าน route `/api/fitt-feedback?op=assist` เบราว์เซอร์ไม่เห็นทั้ง URL และ key
- **🎤:** ใช้ตัวแปลงเสียงเป็นข้อความของเบราว์เซอร์ ภาษาไทย (`th-TH`) ปุ่มขึ้นเฉพาะเบราว์เซอร์ที่รองรับ เช่น Chrome, Edge และ Safari ใน Chrome เสียงจะถูกส่งไปแปลงที่ Google
- **ถามให้ครบก่อนร่าง (25 พ.ย. 2026):** ลูกค้าเป็นเจ้าของธุรกิจ ไม่ใช่ dev และมักเขียนสั้น เช่น "เปลี่ยนเป็น กว้าง xยาว" (NTC build 9) ผู้ช่วยต้องรู้ 3 อย่างก่อนร่าง คือหน้าจอ, สิ่งที่จะแก้ด้วยคำบนหน้าจอ (ปุ่ม ช่อง เมนู ตาราง เอกสาร) และให้เป็นแบบไหน ห้ามเดาชื่อที่ลูกค้าไม่ได้บอก ของใหม่ (เอกสาร รายงาน เมนู) ต้องถามว่ามีอะไรบ้าง ร่างแต่ละบรรทัดขึ้นต้นด้วยหน้าจอ เช่น `หน้าจอรุ่นรถ: เปลี่ยนชื่อช่อง "สูง" เป็น "ยาว"` ส่วน `context.page` มีชื่อหน้าจอต่อท้าย และจุดที่ปักบอกด้วยคำบนหน้าจอ (ดู `docs/feedback-widget-payload.md`)

## ตัวเชื่อมของ CRN (Gemini, 24 พ.ย. 2026)

CRN มีตัวเชื่อมให้แล้วที่ `/feedback-ai` ของ server CRN (`internal/aiassist`) ถาม Gemini (`CRN_GEMINI_MODEL`, ค่าเริ่ม `gemini-3.1-flash-lite`) ผ่าน `generateContent` ตามสัญญาข้างล่าง

- แอปตั้ง `FITT_FEEDBACK_AI_URL=http://172.168.1.171:8080/feedback-ai` และ `FITT_FEEDBACK_AI_KEY=<key ของแอป>` แล้ว restart ไม่ต้อง build ใหม่ ถ้าแอปมี route ตั้งแต่ 462ae7d
- key อยู่ในไฟล์บนเครื่อง build อ่านใหม่ทุกคำขอ เปลี่ยนแล้วไม่ต้อง restart CRN:
  - `~/crn-secrets/gemini-key`: API key ของ Gemini
  - `~/crn-secrets/feedback-ai-key`: key ที่แอปส่งมาเป็น Bearer (ใช้ร่วมทุกแอป)
- ถ้ายังไม่มีไฟล์ใดไฟล์หนึ่ง ตอบ 503 และไม่ถาม Gemini ส่วน key ของแอปที่ผิดจะได้ 401
- log มีแค่สถานะ เวลา จำนวนข้อความและจำนวนไฟล์ ไม่มีสิ่งที่ลูกค้าเขียนหรือแนบ

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
  "files": [
    { "name": "เมนูใหม่.pdf", "type": "application/pdf", "data": "JVBERi0xLjQK…" },
    { "name": "จุดที่ 1.png", "type": "image/png", "data": "iVBORw0KGgo…" }
  ],
  "schema": { "type": "object", "properties": { "reply": {}, "draft": {} }, "required": ["reply"] }
}
```

- `files` คือรูปและเอกสารที่ลูกค้าแนบในแชต (📎 หรือวางรูปที่แคปไว้) และรูปของจุดที่ปักหรือครอปบนหน้า (ชื่อ "จุดที่ N.png") `data` เป็น base64 ส่งมาครบทุกรอบของการคุย เพราะตัวเชื่อมไม่ต้องจำอะไร
  - ชนิดที่ส่งได้: `image/png`, `image/jpeg`, `image/webp`, `application/pdf`, `text/plain`, `text/csv`, Excel (`.xlsx`, `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`) และ Word (`.docx`, `application/vnd.openxmlformats-officedocument.wordprocessingml.document`) ตั้งแต่ 24 พ.ย. 2026 ส่วน `.doc`, `.xls` และ PowerPoint หน้าต่างจะบอกให้ลูกค้าบันทึกเป็น `.docx`, `.xlsx` หรือ PDF ก่อน
  - Gemini อ่านไฟล์ Excel กับ Word ตรงๆ ไม่ได้ ตัวเชื่อมของ CRN จึงแปลงเป็นข้อความก่อน: Excel ทุก sheet เป็น CSV, Word เป็นย่อหน้า และแถวในตารางเป็น "ช่อง | ช่อง" ได้ไม่เกิน 60,000 ตัวอักษรต่อไฟล์ ตัวเชื่อมอื่นต้องแปลงเองแบบเดียวกัน
  - ไม่เกิน 10 ไฟล์ รวมกันไม่เกินประมาณ 9 MB ก่อนเข้ารหัส base64 ทั้งคำขอไม่เกิน 14 MB
  - สำหรับ Gemini ใส่แต่ละไฟล์เป็น `inlineData` (`mimeType` = `type`, `data` = `data`) ในข้อความของลูกค้าข้อความล่าสุด

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
