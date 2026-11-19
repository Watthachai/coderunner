# แนบรูปกับคำขอแก้ไข (edit attachments)

ตกลงกับ DV เมื่อ 19 พ.ย. 2026 (แบบข้อ 1) · โค้ด: `internal/attach`, `internal/api/api.go` (`handleEditProject`), `internal/jobs/attachments.go`

## Request

`POST /internal/projects/{project_id}/edit` ตัวเดิม (`Authorization: Bearer` เหมือนเดิม) เพิ่ม field `attachments` ซึ่งไม่บังคับ

```json
{
  "change": "FITTCORE-CHANGE-REQUEST v1 boundary=b1\n--b1 item=1 source=operator\n...\n--b1 item=2 source=operator\n...\n--b1 end=2\n",
  "feedback_widget": true,
  "attachments": [
    { "item": 2, "name": "pin-1.png", "mime": "image/png",
      "sha256": "9f2c…(64 hex)", "bytes": "iVBORw0KGgo…(base64)" },
    { "name": "deal-page.jpg", "mime": "image/jpeg",
      "sha256": "41ab…", "bytes": "/9j/4AAQ…" }
  ]
}
```

| field | บังคับ | ความหมาย |
|---|---|---|
| `item` | ไม่ | เลข `item=N` ของ FITTCORE-CHANGE-REQUEST ที่รูปนี้เป็นของ ถ้าไม่ใส่ = ใช้กับทั้งคำขอ |
| `name` | ใช่ | ชื่อรูปของผู้ส่ง ไม่เกิน 255 byte ไม่มีอักขระควบคุม CRN เก็บไว้และตอบกลับ แต่ไม่ใช้เป็นชื่อไฟล์ |
| `mime` | ใช่ | `image/png`, `image/jpeg` หรือ `image/webp` |
| `sha256` | ใช่ | sha256 ของตัวรูป (ก่อน base64) เป็น hex 64 ตัว |
| `bytes` | ใช่ | ตัวรูป base64 แบบมาตรฐาน (`A–Z a–z 0–9 + /` และมี `=` padding) |

**ข้อจำกัด**
- ไม่เกิน 10 รูปต่อคำขอ
- รูปละไม่เกิน 5 MiB (5,242,880 byte) และรวมกันไม่เกิน 20 MiB (20,971,520 byte) นับก่อน base64
- body ทั้งก้อนไม่เกิน 32 MiB
- ชนิดไฟล์ดูจากเนื้อไฟล์จริง และต้องตรงกับ `mime`
- รูปเดียวกันส่งซ้ำในคำขอเดียวกันไม่ได้
- fittcore ย่อรูปให้ด้านยาวไม่เกิน 1600 px ก่อนส่ง เพราะรูปที่ใหญ่กว่านี้จะถูกย่อก่อนให้ model ดูอยู่ดี

## Response `202`

```json
{
  "job_id": "…", "build_no": 12, "git_branch": "crn/…", "status": "queued", "rehearsal": false,
  "attachments_received": 2,
  "attachments": [
    { "item": 2, "name": "pin-1.png", "sha256": "9f2c…", "size": 48211 },
    { "name": "deal-page.jpg", "sha256": "41ab…", "size": 120533 }
  ]
}
```

คำขอที่ไม่มีรูปจะได้ `"attachments_received": 0, "attachments": []` ส่วนอื่นเหมือนเดิม

## Error `400`

ถ้ารูปใดไม่ผ่าน CRN จะปฏิเสธทั้งคำขอ ไม่สร้าง job และไม่เก็บรูปใดไว้ body เป็น `{"error": "<เหตุผล>"}` โดย `[i]` คือลำดับรูปใน `attachments` เริ่มจาก 0

| error | เมื่อ |
|---|---|
| `attachments: at most 10 pictures per request, got 11` | เกิน 10 รูป |
| `attachments[i]: name is required` · `name is longer than 255 bytes` · `name is not UTF-8` · `name has a control character` | `name` ไม่ถูก |
| `attachments[i]: item must be 1 or more` | `item` น้อยกว่า 1 |
| `attachments[i]: item is given but the change has no item=N blocks` | ใส่ `item` แต่ `change` ไม่ใช่ FITTCORE-CHANGE-REQUEST |
| `attachments[i]: item 4 is not an item of the change (it has items 1, 2, 3)` | ไม่มี `item=4` ใน `change` |
| `attachments[i]: mime must be image/png, image/jpeg or image/webp, not "image/gif"` | `mime` อื่น |
| `attachments[i]: bytes is empty` · `bytes is not base64 (standard alphabet, padded)` | `bytes` ไม่ถูก |
| `attachments[i]: the picture is 6000000 bytes; at most 5242880 (5 MiB)` | รูปใหญ่เกิน |
| `attachments[i]: the bytes are not a PNG, JPEG or WebP picture` | เนื้อไฟล์ไม่ใช่รูปทั้ง 3 ชนิด |
| `attachments[i]: the bytes are image/jpeg, not image/png` | เนื้อไฟล์ไม่ตรงกับ `mime` |
| `attachments[i]: sha256 must be 64 hex characters` · `sha256 does not match the bytes` | `sha256` ไม่ถูก หรือไม่ตรงกับรูป |
| `attachments[i]: the same picture as attachments[j]` | ส่งรูปเดียวกันซ้ำ |
| `attachments: the pictures add up to 26214400 bytes; at most 20971520 (20 MiB)` | รวมเกิน 20 MiB |
| `request body is larger than 32 MiB` | body ใหญ่เกิน |
| `invalid request body: json: unknown field "…"` | มี field ที่ไม่อยู่ในสัญญานี้ |

## รูปไปไหน

- CRN เก็บรูปเป็นไฟล์ในโฟลเดอร์ของ job นั้น บนเครื่อง build (`CRN_ATTACHMENTS_DIR` ค่าเริ่มต้นคือ `.crn-attachments` ข้าง `CRN_PROJECTS_DIR`) นอก repo ทุกตัว โฟลเดอร์และไฟล์อ่านได้เฉพาะบัญชีที่รัน CRN (0700/0600)
- CRN เขียนรูปให้เสร็จก่อนสร้าง job เพราะฉะนั้นจะไม่มี job ไหนเริ่ม build โดยไม่มีรูป
- payload ของ job ในฐานข้อมูลเก็บแค่ `item`, `name`, `mime`, `sha256`, `size` และชื่อไฟล์ที่ CRN ตั้ง ไม่เก็บตัวรูป
- ก่อน agent เริ่ม CRN ตรวจว่ารูปยังอยู่และ sha256 ยังตรง ถ้าไม่ตรง build ไม่ผ่านโดยไม่รัน agent
- prompt บอก agent ว่ารูปไหนเป็นของข้อไหนและอยู่ที่ไหน agent เปิดด้วย Read ซึ่งให้ model เห็นภาพจริง ส่วน agent ที่ตรวจซ้ำ (ตั้งแต่ 10 ข้อ) ก็เห็นรูปชุดเดียวกัน
- หลัง agent ทำงานเสร็จ ถ้าในโปรเจกต์มีไฟล์ไหนเหมือนรูปที่แนบมาทุก byte ไม่ว่าจะตั้งชื่ออะไร build ไม่ผ่าน และไม่ commit อะไรเลย
- CRN ลบโฟลเดอร์รูปเมื่อ build จบ ไม่ว่าจะผ่าน ไม่ผ่าน หรือถูกยกเลิก รวมถึง job ที่ถูกยกเลิกตั้งแต่ยังรอคิว ทุกครั้งที่ CRN เริ่มทำงานใหม่ จะลบโฟลเดอร์ของ job ที่ไม่ได้รอคิวอยู่ทิ้งทั้งหมด
- รูปไม่อยู่ใน git, image หรือ callback และไม่มี endpoint ให้ดึงรูปกลับ
- ระหว่าง build รูปถูกส่งให้ Claude (Anthropic) ประมวลผล และอยู่ในประวัติ session ของ agent บนเครื่อง build ซึ่ง build ถัดไปของโปรเจกต์เดียวกันใช้ต่อ ประวัตินี้ถูกลบอัตโนมัติเมื่อไม่ได้ใช้ครบ 30 วัน
