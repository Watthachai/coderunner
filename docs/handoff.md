# ใบส่งงาน — CRN (อัปเดต 2026-09-01)

> สำหรับ session/เครื่องถัดไปที่มารับงานต่อ · อ่านไฟล์นี้ก่อน แล้วค่อยไป [jira-all-tasks.md](./jira-all-tasks.md) ถ้าอยากได้รายละเอียดรายงาน

---

## เครื่องที่รันอยู่จริง

| | |
|---|---|
| CRN backend | `172.168.1.171:8080` (Mac mini, user `macagents`, arm64) |
| dashboard | `:3001` · Postgres `:5433` · PostgREST `:3010` · Mongo `:27017` |
| registry | `172.168.1.234:5050/fitt/demos` |
| FTC DV / FITTCORE | `172.168.1.247:3101` · gateway ที่ FBD ชี้ไป: `172.168.1.167:8080` |
| commit ที่ deploy | source ตามที่ pull ล่าสุด · **binary ที่รันอยู่คือ `e63014c`** (31 ส.ค. 17:02) ยังไม่ได้ compile ใหม่ — เช็คด้วย `/healthz` เสมอ อย่าเดาจากวันที่ commit |
| schema | `0011_concurrent_builds` ✅ |

`ssh macagents@172.168.1.171` เข้าได้ด้วยกุญแจแล้ว — **แต่ shell แบบ non-interactive ไม่มี PATH ของ docker** ต้อง `export PATH=/usr/local/bin:/opt/homebrew/bin:$PATH` ก่อนทุกครั้ง ไม่งั้นเจอ `command not found: docker`

---

## เสร็จแล้ว (2026-08-31 → 09-01)

**build ขนานได้** `91de383` + migration `0011` — ย้ายกฎ "1 build" จาก org → project, เพดาน `CRN_MAX_CONCURRENT_BUILDS=5` · เทสตาม [test-scripts/concurrent-builds.md](./test-scripts/concurrent-builds.md) **ยังไม่ได้รันจริงสักเคส**

**เลิกใส่ mock data** `1afb2d3` — seed เหลือแค่บัญชี login, `DEMO_SEED` → `SEED_LOGIN` · demo เก่าต้อง rebuild ถึงจะหาย

**เอกสารเทสติดไปกับทุก demo** `1834127` → `3efac43` → `82328a4` → `7c1246a` → `32d73e3` → `4c8ee7b` — เคสมาจาก BRD `AC-x`/`F-xx` และ PRD `Validation & Edge Cases` (ไม่ใช่เดาจากหน้าจอ), หน้าจอนับจาก wiring ไม่ใช่ file tree, id ต้อง prefix ชื่อเอกสาร

**ตัวตรวจหลัง build** `eb22875` — เตือนเมื่อ seed มี mock / ไม่มี `TEST_CASES.md` / checklist ไม่ครบ · เตือนอย่างเดียว ไม่ fail build

**skill library** `8ab3c89` + `4a6fa6a` — `thai-formatting` `data-tables` `charts` `printable-documents` (+ mirror `mail-service` ออกจาก DB) · อัปขึ้น .171 แล้วทั้งหมด enabled

**เครื่องมือย้ายเครื่อง** `6e95f73` — [moving-machines.md](./moving-machines.md)

---

## ค้างอยู่

| งาน | หมายเหตุ |
|---|---|
| **รัน test script ของ concurrency** | ยังไม่เคยพิสูจน์ว่าขนานได้จริงบนเครื่องจริงสักครั้ง |
| **`CRN_CLAUDE_MODEL` มีผลไปแล้ว** | ~~เข้าใจผิดตอนแรกว่ายังไม่มีผล~~ — `b313840` (default = `claude-opus-5`) เป็น **ancestor** ของ `e63014c` ที่รันอยู่ และ `.env` บน .171 ไม่ได้ตั้งทับ → **ทุก build ตั้งแต่ 31 ส.ค. 17:02 ใช้ Opus 5 แล้ว** · exposure จริงถึงตอนนี้ = **1 build (failed)** · ถ้าจะถอย ตั้ง `CRN_CLAUDE_MODEL=claude-sonnet-5` ใน `.env` แล้ว restart |
| rebuild dashboard | diff UI จาก `46f915b` ยังไม่รู้ว่า rebuild หรือยัง |
| E2E amd64 บนเครื่องลูกค้า | ค้างมานาน |
| destructive migration | `db push` ยังบล็อกอยู่ |
| skill ชุดถัดไป | `calendar-scheduling` `explainable-scoring` `role-gated-ui` `barcode-scanning` — contract อยู่ใน [skills/README.md](../skills/README.md) |

---

## กับดักที่เสียเวลาไปแล้ว (อย่าเหยียบซ้ำ)

**volume ของ compose ไม่ได้ชื่อตามที่เขียนใน yaml** — `crn_pgdata` จริง ๆ คือ `fitt-coderunner_crn_pgdata` (Compose เติมชื่อโปรเจกต์) ถ้า `docker run -v crn_pgdata:...` ตรง ๆ จะได้ volume เปล่าตัวใหม่ แล้ว backup ความว่างเปล่าออกมาโดยไม่มี error

**เพิ่มไฟล์ใน `skillassets/` เฉย ๆ ไม่พอ** — ต้องมี `//go:embed` + ใส่ใน `builtinSkillFiles` ด้วย ไม่งั้นไฟล์ไม่เคยถึง build เลย (เกิดขึ้นจริงกับ `test-cases.md` อยู่หลายวัน ตอนนี้มีเทสกันแล้ว)

**restart ก่อน pull = ลบของที่อัปไว้** — `EnsureBuiltinSkill` seed ทับจากไบนารี ถ้าไบนารีเก่ากว่าไฟล์ที่อัปเข้า DB ไว้ ของใหม่หาย

**`.env` ของ FBD hardcode IP ของ CRN ไว้** — ย้ายเครื่องแล้วไม่แก้ FBD จะบูตปกติ หน้าจอครบ **แต่กด generate เงียบ** ไม่มี error ให้เห็น

**Spotlight** — ต้องมี `.metadata_never_index` ไม่งั้น `ENOTEMPTY` ตอน rebuild

**อย่าเทียบว่า commit ไหนใหม่กว่าด้วยสายตา** — ผมเคยสรุปว่าไบนารีที่รันอยู่เก่ากว่า commit ที่เปลี่ยน default model ทั้งที่มันเป็นลูกหลานของกันและกัน ทำให้รายงานเรื่องค่าใช้จ่ายผิดไปคนละทาง ใช้ `git merge-base --is-ancestor A B` แล้วอ่านไฟล์ ณ commit นั้นตรง ๆ (`git show <rev>:<path>`)

---

## วิธีทำงานที่ใช้ได้ผลรอบนี้

มี session อีกตัวชื่อ `quote-v2-ma-payment-terms` ที่ดูฝั่ง FITT Builder อยู่ — ถามผ่าน `SendMessage` ได้ รอบที่ผ่านมามันช่วยแก้ harness ไป **5 commit** โดย 3 ตัวเป็นการแก้ของที่เขียนผิดไปแล้ว (เช่น negative case ที่ PRD เขียนไว้อยู่แล้วแต่เราสั่งให้ agent เดาเอง)

ถามเรื่องที่ต้องดูของจริงในโปรเจกต์ FBD ได้ผลกว่าเดาเองมาก — แต่ **ให้มันบอกเมื่อไม่รู้ อย่าให้เดา** และเช็คคำตอบซ้ำถ้ามันขัดกับสิ่งที่เราเห็นในโค้ด
