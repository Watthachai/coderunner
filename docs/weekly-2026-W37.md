# สรุปงานสัปดาห์ที่ 37 — จ. 7 ถึง อา. 13 ก.ย. 2026

> แต่ละ `##` = 1 issue · copy ตั้งแต่ Title ลงไปวางใน tracker ได้เลย (รูปแบบเดียวกับ [jira-all-tasks.md](jira-all-tasks.md))
> repo: CRN `fitt-coderunner` · branch `dev` · ครอบคลุม `5769793` → `3cc732c`
> **ธีมของสัปดาห์:** เปลี่ยน CRN จาก "โรงงานปั้น demo" เป็น "โรงงานส่งมอบของจริงให้ลูกค้า"

> **ลงใน Fittcore แล้ว:** [DIG-2332 … DIG-2347](http://172.168.1.222:3100/DIG/projects/code-runner/issues) — 16 งาน, Done 15 + Todo 1 (`[SEC]` ที่ยังไม่ได้แก้)

## งานลงวันไหนบ้าง

| วัน | ทำอะไร | หลักฐาน |
|---|---|---|
| **จ. 7 ก.ย.** | เขียนประตูตรวจก่อนส่งมอบ + รัน validation | mtime ไฟล์ 16:08–16:37 · `docs/validation/customer-delivery.md` ระบุวันที่ 7 ก.ย. |
| **อ. 8 ก.ย.** | commit 9 ตัว (43 ไฟล์ +1825/−733) + ทำให้ระบบบอกสถานะตัวเองได้ | `5769793` → `6f8ae59` เวลา 16:28–16:46 |
| **พ. 9 ก.ย.** | — ไม่มี commit | — |
| **พฤ. 10 ก.ย.** | deploy ขึ้นเครื่องจริง + ไล่บั๊กที่เจอหน้างาน 3 ตัว | `eb34a19` → `3cc732c` + งาน ops (ไม่มี commit) |
| **ศ. 11 ก.ย.** | — ไม่มี commit | — |

> พุธกับศุกร์ไม่มีหลักฐานใน git ของ repo นี้ — ถ้ามีงานฝั่ง FBD หรือเครื่องอื่นต้องดูจากที่นั่น เอกสารนี้ครอบคลุมเฉพาะ CRN บนเครื่อง dev

---

# จันทร์ 7 ก.ย. — สร้างประตูตรวจก่อนส่งมอบ

## [CRN] ประตูตรวจก่อนส่งมอบ — ให้ CRN รัน release checker เอง  `DIG-2332`
**Type:** Story · **Status:** Done (`d1a7376`)

**ปัญหา:** เดิมเราสั่งให้ AI ตรวจงานตัวเองแล้วรายงานผลกลับมา ซึ่งแปลว่า **"รายงาน" กลายเป็นของส่งมอบ** แทนที่จะเป็น "แอปที่ใช้งานได้" — run ที่ข้าม test ไป กับ run ที่เขียนไฟล์ผลลัพธ์ขึ้นมาเองโดยไม่ได้รันอะไรเลย แยกจากกันไม่ออก

**ทำอะไรไป:** เพิ่ม `VerifyDelivery()` ที่ฝัง checker ของตัวเองไว้ใน binary แล้วรันเอง ไม่พึ่งไฟล์ในโปรเจกต์ที่ AI แก้ได้

**แก้ยังไง:** ลำดับที่ checker รันจริงทุกครั้ง —
`npm ci` (ล็อกเวอร์ชัน) → typecheck → unit tests → production build → **build customer image จริง** → รัน migration + seed ในคอนเทนเนอร์นั้น → **ยิง Playwright ใส่ image ที่กำลังรันอยู่**
ฐานข้อมูลเป็น PostgreSQL ชั่วคราวผูก loopback + Docker network แยก ไม่แตะฐานข้อมูลลูกค้า และลบใบเสร็จเก่าทิ้งก่อนรันทุกครั้ง เพื่อไม่ให้ใบเสร็จค้างกลายเป็นหลักฐานปลอม

**ผลดีต่อระบบ:** ของที่ตรวจไม่ผ่านจะออกไปหาลูกค้าไม่ได้เลย และ "ผ่าน" เปลี่ยนจากคำพูดของ AI เป็นคำสั่งที่รันจริงแล้วมีใบเสร็จ (`CRN_VERIFICATION.json`)

**Files:** `internal/buildstep/delivery.go`, `internal/buildstep/verify-delivery.mjs`, `cmd/server/skillassets/references/delivery-checks.md`

---

## [CRN] Migration ที่ส่งไปแล้ว ห้ามถูกแก้ย้อนหลัง  `DIG-2333`
**Type:** Task · **Status:** Done (`53166d1`)

**ปัญหา:** เวลาสั่งแก้แอปที่ส่งไปแล้ว (edit build) AI จะ resume session เดิมและมีสิทธิ์แก้ไฟล์ทุกไฟล์ รวมถึง `prisma/migrations/` ถ้ามันไปเปลี่ยนชื่อหรือแก้ migration ที่ฐานข้อมูลลูกค้า **apply ไปแล้ว** ทั้งสองฝั่งจะไม่ตรงกัน — `migrate deploy` จะไม่ยอมรัน หรือแย่กว่านั้นคือ replay ทับข้อมูลจริง

**ทำอะไรไป:** ถ่าย snapshot ของ migration ทั้งหมดก่อนเริ่มแก้ แล้วเทียบอีกครั้งหลัง AI ทำงานเสร็จ

**แก้ยังไง:** เพิ่ม `SnapshotMigrations()` + `CheckMigrationHistory()` — **เพิ่มไฟล์ใหม่ได้ แต่ลบหรือแก้ของเดิมไม่ได้** ถ้าตรวจเจอ build จะ fail พร้อมบอกชื่อไฟล์ที่ถูกแตะ

**ผลดีต่อระบบ:** ลูกค้าอัปเกรดแอปได้โดยข้อมูลไม่หาย ซึ่งเป็นเงื่อนไขพื้นฐานของการส่งมอบของจริง

**Files:** `internal/buildstep/migrations.go`

---

## [CRN] Bootstrap admin ครั้งเดียวต่อฐานข้อมูล — เลิกใช้รหัสร่วมกัน  `DIG-2334`
**Type:** Story · **Status:** Done (`021efa3`)

**ปัญหา:** ทุก build ที่ส่งออกไปใช้ **บัญชีเดียว รหัสเดียวกัน** จาก env `DEV_EMAIL`/`DEV_PASSWORD` ซึ่งติดไปกับไฟล์ compose ด้วย ใครอ่านไฟล์ได้ก็เข้าระบบลูกค้าได้ทุกราย

**ทำอะไรไป:** ลบ concept รหัสร่วมทิ้ง เปลี่ยนเป็นสร้าง admin คนแรก **ครั้งเดียวต่อฐานข้อมูล** จาก `BOOTSTRAP_ADMIN_EMAIL`/`BOOTSTRAP_ADMIN_PASSWORD` ที่ operator ใส่ตอน deploy

**แก้ยังไง:** helper `bootstrap-once.mjs` ใช้ PostgreSQL advisory lock + ตาราง `SystemBootstrap` เป็น marker ถาวร ทำให้ —
- รันพร้อมกัน 12 ตัว → ได้ admin คนเดียว
- ฐานข้อมูลที่มี user อยู่แล้ว → บันทึกว่าเสร็จ ไม่สร้างเพิ่ม
- ลบ user ทิ้ง → restart แล้วไม่ฟื้นคืนมา
- ไม่มีรหัส fallback ใดๆ ทั้งสิ้น

**ผลดีต่อระบบ:** ลูกค้าแต่ละรายมี credential ของตัวเอง และ restart กี่ครั้งก็ไม่ reset บัญชีที่ใช้งานอยู่

**Files:** `cmd/server/skillassets/assets/bootstrap-once.mjs`, `cmd/server/skillassets/references/auth-and-bootstrap.md`

---

## [CRN] รัน validation ทั้งชุด + เขียนเอกสารระบุขอบเขตที่ยังไม่ครอบคลุม  `DIG-2335`
**Type:** Task · **Status:** Done (`c6ee952`)

**ปัญหา:** งานชุดนี้แตะ runtime ของลูกค้าโดยตรง ถ้าไม่มีหลักฐานว่าทดสอบอะไรไปบ้าง คนที่รับช่วงต่อจะไม่รู้ว่าเชื่อได้แค่ไหน

**ทำอะไรไป:** รันทดสอบจริงแล้วบันทึกผล พร้อมระบุ **สิ่งที่ยังไม่ได้พิสูจน์** ไว้ในเอกสารเดียวกัน

**แก้ยังไง:** ผลที่บันทึก — go test/vet/build ผ่าน · release-contract 4 เคส (รวมเคสปฏิเสธใบเสร็จปลอม, test ที่ skip, lock ไม่ตรง) · PostgreSQL/Prisma integration 6 subtest · `TestDeliveryCustomerRuntime` ผ่านด้วย Docker + Chromium จริง
และเขียนตรงๆ ว่า **ยังไม่ได้ restart Runner จริง ยังไม่ได้อัป skill เข้า DB ยังไม่ได้รัน paid conversion** — เป็น source validation เท่านั้น

**ผลดีต่อระบบ:** ทีมรู้ว่าอะไรพิสูจน์แล้วอะไรยัง ไม่ต้องเดา และเอกสารนี้กลายเป็นตัวชี้เป้าให้งานวันพฤหัส (ข้อ Node 24 ก็มาจากเอกสารนี้)

**Files:** `docs/validation/customer-delivery.md`, `docs/validation/customer-runtime.json`, `tests/`

---

# อังคาร 8 ก.ย. — commit ของวันจันทร์ + ทำให้ระบบบอกสถานะตัวเองได้

## [CRN] Runtime ลูกค้า — เลิก `db push` เปลี่ยนเป็น migration ที่ commit ไว้  `DIG-2336`
**Type:** Story · **Status:** Done (`5769793`)

**ปัญหา:** image ที่ส่งลูกค้ารัน `prisma db push` ทุกครั้งที่ start ซึ่งเป็นท่าสำหรับ demo ไม่ใช่ของที่อัปเกรดได้ — การเปลี่ยน schema แบบเพิ่มฟิลด์จะ apply เงียบๆ ส่วนแบบที่ทำข้อมูลหายจะ fail ตอน boot เท่านั้น

**ทำอะไรไป:** เปลี่ยน entrypoint เป็น `npm run db:deploy` + `npm run db:seed` และยกเครื่อง Dockerfile

**แก้ยังไง:** `node:20-alpine` → `node:22-bookworm-slim` (ได้ openssl จาก distro ไม่ต้องแก้ปัญหา musl) · builder ตัด dev dependency ออกด้วย `npm prune` แทนการสร้าง stage `dbtools` แยก · compose ทั้งสองไฟล์ส่ง `AUTH_SECRET` + `BOOTSTRAP_ADMIN_*` แทน `DEV_EMAIL`/`DEV_PASSWORD`

**ผลดีต่อระบบ:** schema ของลูกค้าเดินผ่าน migration ที่ review แล้ว ไม่ใช่การเดาจาก schema ปัจจุบัน

**Files:** `internal/buildstep/dockerbuild.go`, `internal/buildstep/scaffold.go`, `cmd/server/skillassets/assets/Dockerfile`

---

## [CRN] เขียน fitt-build ใหม่ทั้งฉบับ — จาก demo เป็น customer deliverable  `DIG-2337`
**Type:** Story · **Status:** Done (`021efa3`)

**ปัญหา:** SKILL.md เดิมสั่งให้ AI "สร้าง demo ที่รันได้" โดยนิยามคำว่าเสร็จไว้แค่ "compile ผ่าน" ทุกอย่างที่มันผลิตออกมาจึงถูกต้องตามมาตรฐานของตัวเอง และใช้งานจริงไม่ได้

**ทำอะไรไป:** เขียน SKILL.md ใหม่ให้เป็นลูป **implement → verify → repair แบบมีขอบเขต** ที่จบด้วย release candidate และระบุตรงๆ ว่า compile ผ่านไม่ใช่หลักฐานว่า business logic, permission หรือการอัปเกรดทำงาน

**แก้ยังไง:** เพิ่ม reference 2 ไฟล์ที่ของเดิมขาด — `auth-and-bootstrap.md` (hashed password, session หมดอายุ, authorization ฝั่ง server, admin คนแรก) และ `delivery-checks.md` (สัญญาที่ต้องผ่าน + กติกาว่าเจอ test fail ให้ซ่อมได้ไม่เกิน 2 ครั้ง **ห้ามลบ test ทิ้ง**) พร้อมย้ายเนื้อหาที่ซ้ำออกจาก `prisma-setup.md` (−330 บรรทัด)

**ผลดีต่อระบบ:** AI ได้รับนิยามของคำว่า "เสร็จ" ที่ตรงกับสิ่งที่ลูกค้าต้องการ ไม่ใช่สิ่งที่ compiler ยอมรับ

**Files:** `cmd/server/skillassets/SKILL.md` + `references/`

---

## [CRN] ต่อประตูตรวจเข้า job lifecycle — ปล่อยของเมื่อตรวจผ่าน ไม่ใช่เมื่อ AI บอกว่าผ่าน  `DIG-2338`
**Type:** Story · **Status:** Done (`48bb615`)

**ปัญหา:** ของที่สร้างวันจันทร์ยังไม่ถูกเรียกใช้จริงใน pipeline

**ทำอะไรไป:** เพิ่ม phase `verify` คั่นระหว่าง AI กับ git push แล้วตามมาอีก 4 เรื่องที่เป็นผลพวง

**แก้ยังไง:**
1. phase `verify` ตรวจ migration history + รัน delivery checker — พังอย่างใดอย่างหนึ่ง = fail ก่อนของออกจากเครื่อง
2. **ปิด source-hash cache สำหรับ customer build** — reuse image เก่าแปลว่าส่งของที่ไม่เคยผ่าน gate ของ skill ชุดปัจจุบัน
3. **บังคับว่า `fitt-build` ต้อง enabled** ไม่งั้น job fail และบันทึก hash ของ skill ทุกตัวลง `CRN_SKILLS.json` เพื่อให้ย้อนได้ว่าของชิ้นนี้ผลิตด้วย harness ตัวไหน
4. **ปิด GitHub issue หลัง image ส่งถึงมือจริง** ไม่ใช่หลัง git push (เดิมปิดตั้งแต่ push ทั้งที่ image ยังอาจพัง)
5. ส่ง `runCtx` เข้า clone/push/verify/image — cancel แล้วขึ้นสถานะ cancelled ไม่ใช่ failed

**ผลดีต่อระบบ:** build ที่ปล่อยออกไปผ่านการตรวจจริงทุกครั้ง และ record บอกได้ว่าใช้ harness เวอร์ชันไหน

**Files:** `internal/jobs/jobs.go`

---

## [CRN] role-gated-ui — ตัด role switcher ฝั่ง client ออก ใช้ permission ฝั่ง server จริง  `DIG-2339`
**Type:** Task · **Status:** Done (`7245697`)

**ปัญหา:** skill นี้เขียนขึ้นตอนที่ระบบมี login เดียว จึงต้องสอนวิธี port ตัวสลับ role ฝั่ง client มาจาก prototype เพื่อให้ demo role ได้ พอเปลี่ยนเป็นบัญชีจริงที่มี role ใน database แล้ว **ตัวสลับนั้นกลายเป็นช่องข้ามสิทธิ์**

**ทำอะไรไป:** ตัดจาก 132 บรรทัดเหลือ 14 — เปลี่ยนจาก "วิธี demo role" เป็น "วิธีบังคับสิทธิ์จริง"

**แก้ยังไง:** เนื้อหาใหม่สั่งให้ authorize ทุก server action แยกกัน, เก็บ ownership/tenant scope ไว้ใน query, ทดสอบ role ด้วย fixture ชั่วคราวไม่ใช่บัญชี seed ของลูกค้า และอ้างไปที่ `auth-and-bootstrap.md` สำหรับการสร้างบัญชีแรก · พร้อมแก้ description ของ `charts` ที่มีวงเล็บมุมทำให้ frontmatter พัง

**ผลดีต่อระบบ:** ปิดช่องที่ทำให้ผู้ใช้ธรรมดากดทำสิ่งที่เป็นสิทธิ์ผู้จัดการได้

**Files:** `skills/role-gated-ui/SKILL.md`, `skills/charts/SKILL.md`

---

## [CRN] ตรวจจับ skill drift — เตือนเมื่อ skill ใน repo ไม่ตรงกับที่ CRN ใช้จริง  `DIG-2340`
**Type:** Story · **Status:** Done (`58599c2`)

**ปัญหา:** `fitt-build` seed ตัวเองจาก binary ทุกครั้งที่ restart แต่ **companion skill 9 ตัวใน `skills/` ต้องอัปมือ** — แก้ในrepo แล้ว `git pull` + restart ไม่มีผลอะไรเลย ฐานข้อมูลยังจ่ายเนื้อหาเก่าให้ทุก build อย่างเงียบๆ แปลว่า build หนึ่งผ่าน gate ครบทุกด่านได้ทั้งที่ใช้ harness ที่ไม่มีใคร review

**ทำอะไรไป:** ทำให้ `skills/` เป็น Go package ที่ embed `SKILL.md` + `references/` ของตัวเอง แล้วเทียบกับ row ใน database

**แก้ยังไง:** `GET /internal/skills` เพิ่ม key `drift` — `missing` (ไม่เคยอัปเลย) / `stale` (อัปแล้วแต่เนื้อหาต่าง พร้อมบอกว่าต่างไฟล์ไหน) · ตัวที่ตรงกันไม่รายงาน · row ที่เขียนใน dashboard เองโดยไม่มีต้นฉบับใน repo ก็ไม่นับเป็น drift
**เลือกที่จะรายงานอย่างเดียว ไม่เขียนทับอัตโนมัติ** เพราะทับงานที่ operator แก้ไว้คือความเสียหายที่หนักกว่า

**ผลดีต่อระบบ:** ตอนเปิดใช้จริงพบว่ามี **5 ตัวที่ drift อยู่** โดยไม่มีใครรู้มาก่อน (ดูงานวันพฤหัส)

**Files:** `skills/embed.go`, `internal/api/skilldrift.go` + test 5 เคส

---

## [CRN] Dashboard — บอกว่า daemon รัน commit ไหน และ skill ตัวไหนไม่ตรง  `DIG-2341`
**Type:** Task · **Status:** Done (`6f8ae59`)

**ปัญหา:** console ตอบคำถาม "เครื่องนี้รันอะไรอยู่" ไม่ได้ — repo ไม่มี release tag, version ใน `package.json` เป็นค่า default ของ npm ที่ไม่เคยขยับ, ส่วน VCS stamp ที่ Go ฝังให้มีอยู่ใน `/healthz` แต่ไม่มีใครแสดง **การยืนยันว่า deploy ลงแล้วจึงต้อง ssh เข้าไปดู**

**ทำอะไรไป:** เอา commit ขึ้นหน้าจอข้าง API base ทั้งสองหน้า + ทำ UI ของ drift report

**แก้ยังไง:** `BuildStamp` แสดง commit และเตือน 2 กรณีที่ binary ไม่ตรงกับ commit ไหนเลย — `dirty` (build จาก tree ที่มีของยังไม่ commit) และ `unstamped` (start ด้วย `go run` ซึ่งเป็นเรื่องปกติของ dev ไม่ใช่ความผิดพลาด) · หน้า skills ได้ banner สีเหลืองบอกรายการ drift + badge บนแถว — เหลืองเพราะ stale คือขั้นตอน deploy ที่ยังค้าง ไม่ใช่ daemon พัง ส่วน missing เป็นสีแดงเพราะไม่มี build ไหนเคยได้รับ skill นั้นเลย

**ผลดีต่อระบบ:** ยืนยัน deploy ได้จากหน้าจอใน 1 วินาที และสองฟีเจอร์นี้กลายเป็นเครื่องมือที่ใช้ยืนยัน deploy ของตัวเองในวันพฤหัส

**Files:** `frontend/app/components/BuildStamp.tsx`, `frontend/app/lib/useHealth.ts`, `frontend/app/skills/page.tsx`

---

# พฤหัส 10 ก.ย. — deploy ขึ้นเครื่องจริง แล้วเจอบั๊กหน้างาน 3 ตัว

## [OPS] Node 26 บน Runner ทำให้ทุก build ตกด่านตรวจ — ติดตั้ง Node 24 LTS  `DIG-2342`
**Type:** Bug · **Status:** Done (ไม่มี commit — แก้ที่ config ของเครื่อง)

**ปัญหา:** build #5 รัน AI จนจบ **30 นาที** แล้วไปตายที่ด่านตรวจด้วยข้อความ `Delivery verification requires Node 22 or 24 LTS` — เครื่อง Runner มี Node 26.5.0 ตัวเดียว ไม่มี version manager ให้สลับ

**ทำอะไรไป:** ยืนยันสภาพเครื่องจริงก่อน (Node อะไรบ้าง, มี nvm/fnm/asdf ไหม, สถาปัตยกรรม) แล้วค่อยตัดสินใจ

**แก้ยังไง:** `brew install node@24` (keg-only จึงไม่กระทบ Node 26 ที่ของอื่นใช้อยู่) แล้วตั้ง `CRN_VERIFY_NODE=/opt/homebrew/opt/node@24/bin/node`
**เลือก 24 ไม่ใช่ 22** เพราะ `docs/validation/customer-delivery.md` บันทึกไว้เองว่า fixture ผ่านด้วย 24.19.0 ส่วน Node 26 คือตัวที่ทำ Chromium ค้างตอนแตกไฟล์

**ผลดีต่อระบบ:** build #6 ผ่านด่านตรวจได้เป็นครั้งแรก — 24 unit tests + 9 browser tests รันจริงบน image จริง

**หมายเหตุ:** ระหว่างแก้พบว่า `CRN_DOCKER_USER` ที่ตั้งเป็นค่าตัวอย่าง `your-dockerhub-username` **ไม่ได้ถูกใช้งานที่ไหนเลย** — เป็น required config ที่ไม่มีโค้ดไหนอ่าน (ดู Backlog)

---

## [OPS] อัป companion skill 5 ตัวที่ค้างไม่เคยขึ้น CRN  `DIG-2343`
**Type:** Task · **Status:** Done

**ปัญหา:** พอเปิด drift detection ที่ทำวันอังคาร ระบบรายงานทันทีว่ามี 5 ตัวไม่ตรง — 4 ตัว (`barcode-scanning`, `calendar-scheduling`, `explainable-scoring`, `role-gated-ui`) ขึ้นสถานะ **`missing` แปลว่าไม่เคยถูกอัปเลยตั้งแต่ commit `fc8bbdb`** ทุก build ที่ผ่านมาไม่เคยได้รับ skill เหล่านี้ ส่วน `charts` เป็น `stale` เพราะ description ที่พังยังค้างอยู่ใน DB

**ทำอะไรไป:** ตรวจก่อนว่าไม่มี build กำลังรัน + จดค่า `enabled` ของ `charts` ไว้ (เพราะการอัปจะ force-enable เสมอ) แล้วอัปทั้ง 5 ตัว

**แก้ยังไง:** zip แต่ละโฟลเดอร์แล้ว `POST /internal/skills/upload` · ยืนยันผลด้วย `drift: []`

**ผลดีต่อระบบ:** CRN ได้รับ harness ชุดเดียวกับที่ review ไว้ใน repo **ครบทุกตัวเป็นครั้งแรก** และ `role-gated-ui` ฉบับที่ปิดช่องข้ามสิทธิ์เพิ่งมีผลจริงตอนนี้

---

## [CRN] `docker push` ล้มเพราะ OCI index แข่งกับ manifest ลูกตัวเอง  `DIG-2344`
**Type:** Bug · **Status:** Done (`eb34a19`, `b783347`)

**ปัญหา:** build #6 และ #7 **ผ่านทุกด่าน push git สำเร็จ แล้วมาตายที่ `docker push`** รวมเสียไป 2 รอบ ~30 นาที และ ~$20 ค่า agent ข้อความคือ `blob unknown to registry`

**ทำอะไรไป:** ไล่จาก log ของ GitLab registry เอง (ไม่เดาจากฝั่ง client)

**แก้ยังไง:** log ชี้ชัดว่า Docker 29 push **index กับ manifest ลูกพร้อมกัน** registry จึงตรวจ index ก่อนที่ลูกจะ commit เสร็จ —
```
05:53:08.947  PUT index 900a6f69
05:53:08.947  PUT image 76462b82
05:53:09.041  900a6f69 → 400 MANIFEST_BLOB_UNKNOWN detail=76462b82
05:53:09.053  76462b82 → 201 uploaded      ← ช้ากว่า 12 มิลลิวินาที
```
ตัดสาเหตุอื่นออกหมด: ไม่ใช่ auth (layer ขึ้นครบ) ไม่ใช่ดิสก์ (ใช้ 9%) ไม่ใช่ registry เก่า (v4.39.0 บน GitLab EE 18.11.2)
แก้ 2 ชั้น — `BuildImage` เพิ่ม `--provenance=false --sbom=false` เพื่อไม่ให้เกิด index ตั้งแต่แรก และ `PushImage` retry 3 ครั้งพร้อมยกเลิกทันทีเมื่อ job ถูก cancel

**พิสูจน์บนเครื่องจริง:** image content เดียวกันเป๊ะ — build แบบเก่าได้ `image.index.v1+json` push พังทุกครั้งแม้ retry / build ด้วย flag ใหม่ได้ `image.manifest.v1+json` **push ผ่านตั้งแต่ครั้งแรก**

**ผลดีต่อระบบ:** งาน 30 นาทีไม่ตายเพราะลำดับการ push ที่พลาดกันแค่ 12 มิลลิวินาทีอีกต่อไป

**หมายเหตุความถูกต้อง:** `eb34a19` เขียนคำอธิบายกลับด้าน (อ้างว่า retry คือตัวแก้) ผลทดสอบพิสูจน์ว่า **flag ต่างหากที่แก้ที่ราก retry เป็นแค่ตาข่าย** จึงตามแก้ comment ใน `b783347` — comment ที่ให้เครดิตกลไกผิดอันตรายกว่าไม่มี comment เพราะคนอ่านทีหลังจะไปเพิ่มจำนวน retry แทนที่จะดูที่ flag

**Files:** `internal/buildstep/dockerbuild.go`, `internal/buildstep/dockerpush_test.go` (4 เคส)

---

## [CRN] build ที่ล้มเหลวลืม commit ที่ push ขึ้นไปแล้ว  `DIG-2345`
**Type:** Bug · **Status:** Done (`3cc732c`)

**ปัญหา:** build #7 push commit `3f67520` ขึ้น repo ลูกค้าสำเร็จ แล้วตายที่ `docker push` — แต่ trace บันทึกว่า `commit=""`, `branch=""`, `remote=""` หน้าจอจึงแสดงว่า **"no commit"** คนอ่านสรุปว่าโค้ดไม่เคยขึ้น ทั้งที่มันอยู่บน GitHub แล้ว และเสียเวลาอีก 20 นาทีไปกับการพิสูจน์ว่ามันขึ้นไปแล้วจริง

**ทำอะไรไป:** ไล่หาสาเหตุที่แท้จริง — พบว่า `saveTrace()` **ถูกเรียกบน failure path อยู่แล้ว** แต่ถูกส่ง `traceMeta{cost, errMsg}` เปล่าๆ ของที่ build ทำสำเร็จไปแล้วอยู่ใน local variable ของ `runJob` และไม่เคยถูกส่งต่อ

**แก้ยังไง:** `runJob` ถือ `traceMeta` ตัวเดียวแล้วเติมข้อมูลเมื่อแต่ละอย่างเป็นจริง — `mode` หลัง parse payload · `remote`/`branch` ทันทีที่รู้ git model (ก่อนที่อะไรจะพังได้) · `cost` หลัง agent จบ · `commit` หลัง push สำเร็จ
เปลี่ยน signature ของ `finishFailed`/`finishCancelled` ให้รับ `traceMeta` แทน cost เปล่าๆ **เพื่อให้ compiler บังคับแก้ครบทั้ง 20 call site** ไม่มีที่ไหนหลุดไปส่ง 0 เงียบๆ
พบเพิ่มระหว่างแก้: `finishReused()` ไม่เคยบันทึก trace เลยสักครั้ง — เป็น terminal path เดียวที่ไม่ทิ้งอะไรไว้

**ผลดีต่อระบบ:** build ที่ตายตอนท้ายคือกรณีที่ต้องการ trace มากที่สุด แต่เดิมเป็นกรณีที่จำน้อยที่สุด — ตอนนี้กลับกัน

**Files:** `internal/jobs/jobs.go`, `internal/jobs/trace_test.go` (3 เคส)

---

## [OPS] กู้รหัส GitLab root + ตั้ง SSH เข้าถึงเครื่อง registry  `DIG-2346`
**Type:** Task · **Status:** Done

**ปัญหา:** ระหว่างไล่บั๊ก docker push ต้องอ่าน log ของ registry แต่เข้า GitLab ไม่ได้ (ลืมรหัส) และเครื่อง dev ไม่เคยมี SSH ไปที่กล่องนั้น

**ทำอะไรไป:** กู้การเข้าถึงผ่าน Proxmox console แล้วตั้ง SSH ให้ถูกหลัก

**แก้ยังไง:** `gitlab-rake "gitlab:password:reset[root]"` · สร้าง key เฉพาะกล่องนี้ (`id_dvgitlab`) ไม่ปนกับ key เดิม · **ยืนยัน host key ด้วยการเทียบ fingerprint อัตโนมัติ** ไม่ใช่ `accept-new` มั่ว · สร้าง user `crnops` + NOPASSWD sudo แทนการใช้ root ตรงๆ เพื่อให้มี audit trail และเพิกถอนได้ด้วยการลบ user ตัวเดียว

**ผลดีต่อระบบ:** ไล่ปัญหา registry ได้ด้วยตัวเองโดยไม่ต้องรอใครเปิดเครื่องให้ และการเข้าถึงถอนคืนได้ง่าย

---

## [SEC] พบ container registry เปิดออกอินเทอร์เน็ต — ยังไม่ได้ปิด  `DIG-2347`
**Type:** Bug · **Status:** **Open — ยังไม่แก้**

**ปัญหา:** ระหว่างอ่าน log ของ registry พบ request ที่ `host` เป็น **public IP `58.136.159.74:5050`** ไม่ใช่ LAN IP และมี bot สแกนหาช่องโหว่ยิงเข้ามา (`/rest/applinks/1.0/manifest`, `/administrator/manifests/files/joomla.xml`)

**ตัวเลขที่นับได้จาก log ปัจจุบัน:**

| ที่มา | ครั้ง |
|---|---|
| `172.168.1.171` (MacMini — CRN ตัวจริง) | 203 |
| `172.232.232.10` (Linode — scanner) | **126** |
| `139.162.119.146` (Linode — scanner) | **92** |
| `198.235.24.76` (ช่วงของ Censys) | 1 |
| IP นอกอื่นๆ อีก 4 ตัว | 4 |

**รวมทราฟฟิกจากนอก 223 ครั้ง มากกว่าการใช้งานจริงของ CRN (203)**

**ทำไมถึงน่ากังวล:** มี scanner จากคนละที่ 7 IP หามันเจอ**อิสระจากกัน** ซึ่งเกิดได้ทางเดียวคือถูก index ไว้ในฐานข้อมูลสาธารณะแล้ว และการเจอช่วงของ Censys ยืนยันเรื่องนี้ — ใครก็ค้นเจอได้โดยไม่ต้องสแกนเอง กล่องนี้ถือ image ของลูกค้าทุกราย

**สถานะปัจจุบัน:** request จากนอกได้ 401/404 ทั้งหมด **ยังไม่มีอะไรหลุด** และ **ยังไม่รู้ว่าเปิดออกทางไหน** (port forward ที่ router / nginx / Proxmox firewall ไม่ได้กั้น)

**ที่ต้องทำต่อ:** ยืนยันว่ามี request จากนอกที่ไม่ใช่ 401/404 หรือไม่ · เช็คว่า GitLab web (80/443) เปิดด้วยไหม · ปิดหรือจำกัดเป็น LAN/VPN

---

# Backlog — เจอระหว่างทาง ยังไม่ได้แก้

| งาน | รายละเอียด | ความสำคัญ |
|---|---|---|
| ปิด registry จากอินเทอร์เน็ต | ดูรายการ `[SEC]` ข้างบน | สูง |
| `CRN_DOCKER_USER` เป็น config ที่ไม่มีใครอ่าน | มีแค่ 3 ที่ใน `config.go` — ประกาศ, อ่าน env, บังคับว่าต้องไม่ว่าง ไม่เคยถูกส่งเข้า `jobs.NewManager` เลย ทำให้คนเข้าใจผิดว่าต้องแก้ | กลาง |
| phase track บน dashboard อาจเรนเดอร์ไม่ตรง trace | มีรายงานว่าเห็น git/push เป็นสีเทาทั้งที่ trace บันทึกว่าผ่านแล้ว — ยังไม่ยืนยัน | กลาง |
| `npm run lint` ของ frontend พังอยู่ก่อนแล้ว | script เป็น `next lint` ซึ่ง Next 16 ถอดออกแล้ว ต้องเรียก `eslint` ตรงๆ | ต่ำ |
| `schema_migrations` ของ dev DB ไม่ตรง | compose mount แค่ 0001–0008 เครื่องใหม่จะเจอปัญหาเดียวกัน ต้อง `make migrate-baseline` ก่อน | ต่ำ |
| `package-lock.json` stub ที่ root | ไฟล์ว่างเปล่าจากการรัน npm ผิดโฟลเดอร์ ยังไม่ commit | ต่ำ |
| ยังไม่เคยมี build วิ่งจบเขียวครบสาย | แต่ละชิ้นถูกซ่อมคนละเวลา ยังไม่มี build ไหนได้ใช้ของที่ซ่อมแล้วครบพร้อมกัน | — รอโปรเจกต์ถัดไป |

---

# สถานะระบบ ณ สิ้นสัปดาห์

| รายการ | สถานะ |
|---|---|
| daemon บน Runner | `3cc732c` · `modified: false` |
| skill drift | `[]` — ตรงกับ repo ครบ 10 ตัว |
| ด่านตรวจก่อนส่งมอบ | ผ่านจริง 2 รอบ (24 unit + 9 browser tests) |
| git push ของลูกค้า | `3194e78`, `3f67520` ขึ้น repo แล้ว |
| docker push | พิสูจน์ด้วยมือแล้ว — v7 ผ่านตั้งแต่ครั้งแรกหลังใส่ flag |
| go build / vet / test | เขียวทั้งหมด |
