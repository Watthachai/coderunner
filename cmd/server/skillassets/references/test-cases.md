# TEST_CASES.md — the test script that ships with the demo

The people who receive this demo are not developers. They open the app, click
around, and need a sheet that tells them **what to try, what should happen, and
where to write down what actually happened**. That sheet is `TEST_CASES.md`,
written at the repo root next to `BUILD_NOTES.md`.

It is a *script to be run by a human*, not a report of tests you ran.

## Where the cases come from

The requirements already exist — do not invent them from the screens. FITT
Builder interviews the customer and synthesizes the answers into the three docs
in the export's `docs/` folder. There is no separate Q&A file; **these ARE the
answers**, already structured.

**Find the sections by keyword, never by number.** These docs are generated from
a template: the heading *keywords* are stable, but section numbers and exact
wording drift between projects. Match on `เกณฑ์การยอมรับ` / `Acceptance
Criteria`, never on `## 8.`.

| ต้องการอะไร | หาที่ไหน (match ด้วย keyword) |
|---|---|
| เคส **positive** | BRD `เกณฑ์การยอมรับ` / `Acceptance Criteria` (`AC-x`) · BRD `ความต้องการเชิงฟังก์ชัน` / `Functional` (`F-xx`) · PRD `User Stories และเกณฑ์การยอมรับ` |
| เคส **negative** | **PRD `Validation` / `Edge Cases` / `การควบคุมความถูกต้อง`** — เขียนไว้ตรง ๆ แล้ว พร้อมข้อความ error จริง |
| ชนิด / หน่วย / ข้อจำกัดของฟิลด์ | PRD `Data Model` / `ข้อมูลและฟิลด์` · BRD `ข้อมูลหลักในระบบ` |
| เคส **สิทธิ์ / บทบาท** | PRD `Validation & Edge Cases` + คำว่า "เฉพาะบทบาท…" ที่กระจายอยู่ในหัวข้อรายหน้าจอ |
| สิ่งที่ **ห้าม** เขียนเป็นเคส | BRD `ขอบเขต` (ฝั่ง "ไม่ทำ") · PRD `ขอบเขตของ Demo (Out of Scope)` |
| ตรวจซ้ำระดับโค้ด | `src/types.ts` ของ prototype — optional / enum ที่เอกสารไม่ได้ระบุ |
| Success Criteria ของเดโม | `docs/IDEA.md` |

You read these in step 1 already. Read them again here, with a tester's eye.

**Positive cases are quoted, not invented.** An acceptance criterion is already
a test case in prose:

> AC-2: "บันทึกจ่ายออกม้วนผ้า (ขายยกม้วน) สถานะของม้วนต้องเปลี่ยนเป็น
> 'จ่ายออกแล้ว' และความยาวคงเหลือต้องกลายเป็น 0 ทันทีแบบ Real-time"

→ ขั้นตอน = บันทึกจ่ายออกม้วนผ้าแบบยกม้วน · ผลที่คาดหวัง = สถานะเป็น "จ่ายออกแล้ว"
และคงเหลือ = 0 ทันที. Put the id in the **ฟังก์ชันที่ทดสอบ** column so every case
traces back to something the customer actually agreed to — and **always prefix
it with the document it came from**: `BRD AC-2 · จ่ายออกม้วนผ้า`, `BRD F-03 · …`,
`PRD US-02 · …`. The two docs number independently (the PRD usually labels
`US-01…` and writes its acceptance criteria as Given/When/Then inside each user
story), so a bare `AC-2` turns ambiguous the moment a project numbers both.
Collect ids with `\b(AC|F|US)-\d+\b` — padding is inconsistent (`AC-1` vs
`US-01`).

**Negative cases are usually written down too — read them before inventing
one.** The PRD's Validation & Edge Cases section states them as condition →
result, with the real error text:

> 1. Duplicate Barcode: ถ้าสแกนบาร์โค้ดที่ยังสถานะ เต็มม้วน/เศษผ้า → บล็อก +
>    เตือนสีแดง "รหัสบาร์โค้ดนี้มีอยู่ในระบบแล้วและยังไม่ถูกจ่ายออก"

Copy the expected message **verbatim** into ผลลัพธ์ที่คาดหวัง — a tester
comparing wording needs the exact string, not your paraphrase.

These entries carry **no id** — they are a bare numbered list — but each has a
name. Cite the name, never the position: `EC-DuplicateBarcode`, not
`PRD §6.1 #2`. Regenerating the docs reorders the list; the name survives.

**Only then extend by derivation**, field by field from the Data Model, which
carries type, unit and constraint:

> - ความยาวเริ่มต้น (Initial Length): Decimal (ทศนิยม 2 ตำแหน่ง, บังคับกรอก, > 0, หน่วย: หลา)

`> 0` → ติดลบ / ศูนย์ · `ทศนิยม 2 ตำแหน่ง` → ใส่ 3 ตำแหน่ง · `บังคับกรอก` →
ปล่อยว่าง · `ห้ามซ้ำ` → ใส่ค่าซ้ำ · `ต้องมีอยู่จริง` (FK) → อ้างถึงรายการที่ถูกลบ.
The catalogue further down is the backstop for fields the docs left
unconstrained — never the starting point.

**Then check every case against the app you really built.** A case whose feature
does not exist is NOT deleted — it moves to "ยังไม่รองรับ" carrying its `AC`/`F`
id. That is the whole point: the document then answers "does this demo match
what was agreed?", not merely "does what was built work?".

## Five rules

1. **Never fill in the result columns.** `ผลการทดสอบจริง` and `สถานะ` stay
   EMPTY. You did not run the app against a browser and a live database — the
   tester does. Pre-filling them is faking a test report.
2. **Docs first, then code.** Positive cases are lifted from BRD `AC-x`/`F-xx`,
   negative cases from the PRD's Validation & Edge Cases (then extended from the
   Data Model). Only after that do you read your own ported screens and
   reconcile: a case the app cannot pass moves to "ยังไม่รองรับ" with its
   requirement id, always source-prefixed (`BRD AC-2`, `PRD US-01`,
   `EC-<ชื่อเคส>`) — never invent a rule the docs never asked for, and never
   quietly drop one they did.
3. **Cover every REACHABLE screen in `PORT_CHECKLIST.md`** — the ones wired into the router or the `activeMenu` switch, not every file in `src/pages/`; exports carry orphan components nobody can navigate to, and a case for a screen the user cannot open is a false green. One section per screen, in the
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

## ความครอบคลุมข้อกำหนด (BRD)
| ข้อกำหนด | ใจความ | เคสที่คุม | สถานะ |
|---|---|---|---|
| `BRD AC-1` | <ย่อ 1 บรรทัด> | TC-04, TC-05 | มีเคส |
| `BRD F-03` | <ย่อ 1 บรรทัด> | TC-07 | มีเคส |
| `PRD US-02` | <ย่อ 1 บรรทัด> | TC-11 | มีเคส |
| `EC-DuplicateBarcode` | <ย่อ 1 บรรทัด> | TC-12 | มีเคส |
| `BRD AC-4` | <ย่อ 1 บรรทัด> | — | ยังไม่รองรับ (ดูท้ายเอกสาร) |

## สิ่งที่ demo นี้ยังไม่รองรับ (ทราบล่วงหน้า)
<ดูหัวข้อท้ายไฟล์นี้>
```

The coverage table must list **every** BRD `AC`, every `must`-level BRD `F`,
every PRD `US`, and every named entry in the PRD's Validation & Edge Cases —
one row each, no omissions. It is what turns the file from a click-list
into an answer to "ตกลงเดโมนี้ทำได้ตามที่คุยไว้ไหม".

Case table — these six columns, always:

```markdown
| ID | ฟังก์ชันที่ทดสอบ | ขั้นตอน / สิ่งที่กรอก | ผลลัพธ์ที่คาดหวัง | ผลการทดสอบจริง | สถานะ |
|---|---|---|---|---|---|
| TC-01 | เข้าสู่ระบบ | กรอกอีเมล/รหัสผ่านที่ถูกต้อง แล้วกดเข้าสู่ระบบ | เข้าสู่หน้าหลักได้ | | |
| TC-02 | เข้าสู่ระบบ | กรอกอีเมลถูก แต่รหัสผ่านผิด | ไม่เข้าระบบ + ขึ้นข้อความแจ้งเตือน | | |
| TC-03 | `BRD AC-2` · จ่ายออกม้วนผ้า | เลือกม้วน → บันทึกจ่ายออกแบบยกม้วน | สถานะเป็น "จ่ายออกแล้ว" และคงเหลือ = 0 ทันที | | |
```

Number `TC-01`, `TC-02`, … continuously across the whole document (not per
section) so a tester can say "TC-14 fail" with no ambiguity.

## Negative-case catalogue — pick what applies, ignore the rest

Use this only for fields the PRD's Validation & Edge Cases and Data Model left
unconstrained. It is a menu, not a checklist: write a row only where that field
type actually exists in the app you built.

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
| สิทธิ์เข้าถึง | เปิด URL ของหน้าภายในทั้งที่ยังไม่ล็อกอิน → ต้องเด้งไปหน้าเข้าสู่ระบบ · **ทุกกฎ role-gated ใน PRD** ("ปุ่มนี้ใช้ได้เฉพาะ Owner") → เข้าด้วยบทบาทอื่นแล้วต้องไม่เห็น/กดไม่ได้ |
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
| เปิดใช้ครั้งแรก (DB ว่าง) | ทุกหน้าเปิดได้ ขึ้น "ยังไม่มีข้อมูล" ไม่ใช่หน้าขาว/ไม่ error · ไม่มีข้อมูลตัวอย่างโผล่มาเอง · ข้อมูลทุกชนิดสร้างได้จากในแอป (ฟอร์มของตัวเอง หรือเกิดจาก flow อื่น เช่นรายการเคลื่อนไหวที่เกิดตอนรับ/จ่ายของ) |
| รีสตาร์ทระบบ / deploy ซ้ำ | ข้อมูลที่กรอกไว้อยู่ครบ · รายการที่ลบไปแล้ว **ไม่กลับมา** · ไม่มีข้อมูลตัวอย่างงอกใหม่ · ล็อกอินเดิมยังใช้ได้ |
| เปิดหน้าจอทุกหน้าตาม `PORT_CHECKLIST.md` | ทุกหน้าเปิดได้ ไม่มีหน้าขาว/ไม่มี error |

## The honest section at the end

A ported prototype is not a hardened product. Where the app genuinely has no
validation for a case in the catalogue, do NOT invent a rule and do NOT quietly
drop the case — list it under **"สิ่งที่ demo นี้ยังไม่รองรับ (ทราบล่วงหน้า)"**:

```markdown
| อ้างอิง | สิ่งที่ยังไม่รองรับ | ผลที่จะเกิดถ้าลอง | ถ้าต้องการให้รองรับ |
|---|---|---|---|
| `BRD AC-4` | แจ้งเตือนเมื่อสต็อกต่ำกว่า Reorder Point | ไม่มีแถบเตือน ต้องดูตัวเลขเอง | คำนวณผลรวมต่อรหัสแล้วเทียบ Reorder Point ตอน render |
| (derive) | ช่องราคายังไม่กันค่าติดลบ | บันทึกได้ ยอดรวมติดลบ | เพิ่มการตรวจค่า > 0 ทั้งหน้าจอและ server action |
| (derive) | ยังไม่ล็อกบัญชีเมื่อกรอกรหัสผิดหลายครั้ง | ลองรหัสผ่านซ้ำได้ไม่จำกัด | นับครั้งที่ผิดต่อบัญชี |
```

Cite the source-prefixed id when the gap is something the docs asked for
(`BRD AC-4`, `PRD US-02`, `EC-DuplicateBarcode`), and `(derive)` when it is a
validation rule nobody wrote down. This section is what makes the
document useful: the customer learns the demo's real boundaries instead of
discovering them as "bugs", and sees exactly which agreed requirement is still
open. Writing it does NOT mean
implementing the missing rules — the port stays faithful (see the hard rules in
SKILL.md); it means being straight about what the demo does today.
