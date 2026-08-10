# TEST_CASES.md — the test script that ships with the demo

The people who receive this demo are not developers. They open the app, click
around, and need a sheet that tells them **what to try, what should happen, and
where to write down what actually happened**. That sheet is `TEST_CASES.md`,
written at the repo root next to `BUILD_NOTES.md`.

It is a *script to be run by a human*, not a report of tests you ran.

## Five rules

1. **Never fill in the result columns.** `ผลการทดสอบจริง` and `สถานะ` stay
   EMPTY. You did not run the app against a browser and a live database — the
   tester does. Pre-filling them is faking a test report.
2. **Every case must come from code you actually wrote.** Read your own ported
   screens before writing a case. A case about a field that does not exist, or a
   validation rule the app never implements, is noise that makes the whole
   document untrustworthy.
3. **Cover every screen in `PORT_CHECKLIST.md`.** One section per screen, in the
   order a user meets them (login → main list → detail → create/edit → …).
4. **Both directions.** For each form: the happy path (correct data → saved) AND
   the negative cases (bad data → rejected with a message). Negative cases are
   the point of the document — anyone can click "save" with good data.
5. **Write it in the language of the briefs** (IDEA/BRD/PRD — normally Thai),
   with the exact table columns below. The reader is the customer, not you.

## File shape

```markdown
# เอกสารทดสอบ — <ชื่อระบบ>

| | |
|---|---|
| **ระบบ** | <ชื่อ> |
| **เวอร์ชัน build** | <build ที่เทส> |
| **จำนวนเคส** | <n> |
| **ผู้ทดสอบ** | (กรอกเมื่อทดสอบ) |
| **วันที่ทดสอบ** | (กรอกเมื่อทดสอบ) |

## วิธีใช้เอกสารนี้
ทำตาม "ขั้นตอน" ทีละข้อ เทียบกับ "ผลลัพธ์ที่คาดหวัง" แล้วกรอก "ผลการทดสอบจริง"
กับ "สถานะ" (Pass / Fail) ถ้า Fail ให้แนบภาพหน้าจอไว้ท้ายเอกสาร

## 1. เข้าสู่ระบบ
<ตารางเคส>

## 2. <หน้าจอถัดไป>
<ตารางเคส>

## สรุปผล
| หัวข้อ | จำนวนเคส | Pass | Fail |
...

## สิ่งที่ demo นี้ยังไม่รองรับ (ทราบล่วงหน้า)
<ดูหัวข้อท้ายไฟล์นี้>
```

Case table — these six columns, always:

```markdown
| ID | ฟังก์ชันที่ทดสอบ | ขั้นตอน / สิ่งที่กรอก | ผลลัพธ์ที่คาดหวัง | ผลการทดสอบจริง | สถานะ |
|---|---|---|---|---|---|
| TC-01 | เข้าสู่ระบบ | กรอกอีเมล/รหัสผ่านที่ถูกต้อง แล้วกดเข้าสู่ระบบ | เข้าสู่หน้าหลักได้ | | |
| TC-02 | เข้าสู่ระบบ | กรอกอีเมลถูก แต่รหัสผ่านผิด | ไม่เข้าระบบ + ขึ้นข้อความแจ้งเตือน | | |
```

Number `TC-01`, `TC-02`, … continuously across the whole document (not per
section) so a tester can say "TC-14 fail" with no ambiguity.

## Negative-case catalogue — pick what applies, ignore the rest

This is a menu, not a checklist. Only write the rows whose field type actually
exists in the app you built.

| ชนิดของช่อง | เคสที่ต้องมี |
|---|---|
| ช่องบังคับกรอก (required) | ปล่อยว่างแล้วกดบันทึก · กด spacebar รัว ๆ แล้วบันทึก (ช่องว่างล้วนต้องถือว่าไม่กรอก) |
| ตัวเลข / ราคา / จำนวน | กรอกตัวอักษรแทนตัวเลข ("หนึ่งร้อย") · ค่าติดลบ · `0` (ถ้าเป็นราคา/จำนวนที่ต้องมากกว่าศูนย์) · ใส่สัญลักษณ์ปน (`100฿`, `1,000`) · ตัวเลขยาวเกินขอบเขต `Int` (เช่น 9 ยี่สิบตัว) · ทศนิยมเกินที่กำหนด |
| ข้อความ | ยาวเกินขีดจำกัดของฟิลด์ · อักขระพิเศษ/อีโมจิ/ภาษาไทย · เว้นวรรคหน้า-หลัง |
| ความปลอดภัยของช่องข้อความ | ใส่ `<script>alert('x')</script>` → ต้องแสดงเป็นข้อความธรรมดา **ห้ามรันโค้ด** · ใส่ `' OR 1=1 --` → ต้องถูกเก็บเป็นข้อความ ไม่กระทบการค้นหา (Prisma ส่งค่าเป็นพารามิเตอร์อยู่แล้ว — เคสนี้คือการยืนยัน ไม่ใช่การเดา) |
| อีเมล | รูปแบบผิด (`abc`, `a@`, `a@b`) · อีเมลซ้ำ (เฉพาะเมื่อมีหน้าสมัคร/สร้างผู้ใช้จริง) |
| วันที่ | วันสิ้นสุดก่อนวันเริ่ม · วันที่ในอดีต (ถ้าธุรกิจห้าม) · รูปแบบวันที่ผิด |
| ตัวเลือก / ความสัมพันธ์ | เลือกรายการที่ถูกลบไปแล้ว · ไม่เลือกอะไรเลยแล้วบันทึก |
| รายการ / ตาราง | ไม่มีข้อมูลเลย (empty state ต้องไม่ใช่หน้าขาว) · ค้นหาคำที่ไม่มี · หน้าสุดท้ายของ pagination |
| ลบ / ยกเลิก | กดลบแล้วยกเลิก → ข้อมูลต้องยังอยู่ · ลบรายการที่มีของอื่นอ้างถึง |
| สิทธิ์เข้าถึง | เปิด URL ของหน้าภายในทั้งที่ยังไม่ล็อกอิน → ต้องเด้งไปหน้าเข้าสู่ระบบ |
| ตะกร้า / ชำระเงิน *(เฉพาะแอปที่มีจริง)* | กดชำระเงินโดยตะกร้าว่าง · สั่งเกินสต็อก · คูปองหมดอายุ / ใช้ซ้ำ |
| อัปโหลดไฟล์ *(เฉพาะแอปที่มีจริง)* | ไฟล์ผิดชนิด · ไฟล์ใหญ่เกินกำหนด |

## Cases every CRN demo should have

These come from what CRN itself puts in the image, so they apply to every build:

| ต้องเทส | ผลลัพธ์ที่คาดหวัง |
|---|---|
| เข้าสู่ระบบด้วย `DEV_EMAIL` / `DEV_PASSWORD` | เข้าได้ (ถ้าแอปมีหน้าล็อกอิน) |
| เข้าสู่ระบบด้วยรหัสผ่านผิด / อีเมลผิด / เว้นว่าง | ไม่เข้า + มีข้อความแจ้ง |
| ปุ่ม 💬 feedback มุมจอ | กดแล้วส่งข้อความได้ มีหน้ายืนยันก่อนส่ง |
| ปุ่ม 🐞 error | โผล่เฉพาะตอนเกิด error จริงเท่านั้น |
| รีสตาร์ทระบบ / deploy ซ้ำ | ข้อมูลตัวอย่างไม่ซ้ำซ้อน (seed เป็น upsert) และข้อมูลที่ผู้ใช้กรอกไว้ยังอยู่ |
| เปิดหน้าจอทุกหน้าตาม `PORT_CHECKLIST.md` | ทุกหน้าเปิดได้ ไม่มีหน้าขาว/ไม่มี error |

## The honest section at the end

A ported prototype is not a hardened product. Where the app genuinely has no
validation for a case in the catalogue, do NOT invent a rule and do NOT quietly
drop the case — list it under **"สิ่งที่ demo นี้ยังไม่รองรับ (ทราบล่วงหน้า)"**:

```markdown
| สิ่งที่ยังไม่รองรับ | ผลที่จะเกิดถ้าลอง | ถ้าต้องการให้รองรับ |
|---|---|---|
| ช่องราคายังไม่กันค่าติดลบ | บันทึกได้ ยอดรวมติดลบ | เพิ่มการตรวจค่า > 0 ทั้งฝั่งหน้าจอและ server action |
| ยังไม่มีการล็อกบัญชีเมื่อกรอกรหัสผิดหลายครั้ง | ลองรหัสผ่านซ้ำได้ไม่จำกัด | เพิ่มการนับครั้งที่ผิดต่อบัญชี |
```

This section is what makes the document useful: the customer learns the demo's
real boundaries instead of discovering them as "bugs". Writing it does NOT mean
implementing the missing rules — the port stays faithful (see the hard rules in
SKILL.md); it means being straight about what the demo does today.
