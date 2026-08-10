# เอกสารทดสอบ — Build หลายโปรเจคพร้อมกัน

| | |
|---|---|
| **รหัสเอกสาร** | TS-CRN-001 |
| **เรื่อง** | CRN รัน build ของหลายโปรเจคพร้อมกัน (จากเดิม 1 build ต่อ 1 org) |
| **commit ที่ทดสอบ** | `91de383` — *feat(jobs): run builds of different projects concurrently* |
| **migration** | `migrations/0011_concurrent_builds.sql` |
| **repo** | `fitt-coderunner` (CRN) · branch `dev` / `feat/feedback-panel` |
| **เวอร์ชันเอกสาร** | 1.0 · 2026-08-11 |
| **จำนวนเคส** | 11 (รอบ A = 7 เคส, รอบ B = 4 เคส) |
| **เวลาที่ใช้โดยประมาณ** | รอบ A ~20 นาที (ไม่เผา token) · รอบ B ~40–60 นาที (build จริง) |

---

## 1. ทำไมต้องเทส — สิ่งที่เปลี่ยนและความเสี่ยงที่ตามมา

### 1.1 ของเดิมเป็นยังไง

CRN ล็อกคิว build **ที่ระดับ org** สองชั้นซ้อนกัน

1. `pg_try_advisory_lock(hashOrg(orgID))` — ล็อกใน Go
2. unique index `uq_jobs_one_building_per_org ON project_jobs (org_id) WHERE status='building'` — ล็อกที่ DB

ผลคือ **ทั้ง org มี build วิ่งได้ทีละ 1 ตัวเท่านั้น** ลูกค้าที่มี 5 โปรเจคต้องรอเรียงกัน = เวลารวมเท่ากับผลบวกของทุก build (เพื่อนรายงานว่า "รันทีละโปรเจค นานมาก")

### 1.2 ของใหม่เปลี่ยนอะไร

ย้ายกฎ "1 build ที่วิ่งอยู่" จาก **org → project** แล้วคุมทรัพยากรด้วยเพดานจำนวนแทน

| ชั้น | กฎใหม่ | บังคับด้วย |
|---|---|---|
| ความถูกต้อง | 1 build ต่อ **1 โปรเจค** | `AcquireProjectLock` (advisory lock) + index `uq_jobs_one_building_per_project` |
| ทรัพยากร | รวมกันไม่เกิน **N ตัว** ทั้งเซิร์ฟเวอร์ | slot semaphore ใน `dispatch()` ตาม `CRN_MAX_CONCURRENT_BUILDS` (default 5) |

เหตุผลที่ทำได้: build คนละโปรเจค **ไม่แชร์อะไรกันเลย** — คนละ working dir (`projectsDir/<project_id>`), คนละ git repo/branch, คนละ image tag และ build path ไม่มี global state (ไม่มี `os.Chdir`, `os.Setenv`, `git config --global`; ทุก exec set `cmd.Dir` ของตัวเอง)

### 1.3 ความเสี่ยง → เคสที่ใช้กัน

ตารางนี้คือหัวใจของเอกสาร: แต่ละเคสมีไว้กันอะไร ไม่ใช่เทสเอาจำนวน

| # | ความเสี่ยง | ถ้าเกิดจะเห็นอะไร | เคสที่กัน |
|---|---|---|---|
| R1 | ลืมรัน migration → index เก่ายังอยู่ | build ตัวที่ 2 ตายทันทีด้วย `duplicate key value violates unique constraint "uq_jobs_one_building_per_org"` | TC-A1 |
| R2 | env ไม่ถูกอ่าน / ค่า default ผิด | ตั้ง 5 แต่รันได้ทีละตัว (หรือรันเกินจนเครื่องตาย) | TC-A2, TC-A5 |
| R3 | dispatcher ไม่ขนานจริง | ยิง 3 โปรเจค แต่ `building` ขึ้นทีละ 1 | **TC-A3** |
| R4 | ล็อกระดับโปรเจคหลุด → build โปรเจคเดียวกันทับกัน | workdir พัง, git conflict, image tag ชนกัน, ผลลัพธ์เพี้ยน | **TC-A4** |
| R5 | slot semaphore รั่ว (ไม่คืน slot) | รันไปเรื่อย ๆ แล้วค่อย ๆ ตัน จนไม่มี build ใหม่เริ่มเลย | TC-A5, TC-A7 |
| R6 | job ค้างคิวไม่มีใครปลุก (starvation) | `queued` ค้างทั้งที่เครื่องว่าง ต้องยิงซ้ำเอง | TC-A5, TC-A6 |
| R7 | restart กลางคิวแล้วงานหาย | job หาย/ค้าง `building` ตลอดกาล | TC-A6 |
| R8 | build ขนานกันแล้วเขียนทับไฟล์กัน | `ENOTEMPTY`, `Dockerfile not found`, demo ออกมาผิดโปรเจค | **TC-B2** |
| R9 | ผลลัพธ์ส่งออกสลับโปรเจค | FTC DV ได้ image ผิดตัว | **TC-B3** |
| R10 | เครื่องรับ 5 ตัวไม่ไหว (docker/QEMU) | build ช้าลงกว่าเดิม, swap, เครื่องหน่วงทั้งเครื่อง | TC-B4 |

> เคสที่ทำตัวหนา = ถ้าเคสนี้ไม่ผ่าน **ห้ามขึ้น production**

---

## 2. ขอบเขต

**อยู่ในขอบเขต**
- กลไกจ่ายงาน (dispatcher), ล็อกระดับโปรเจค, เพดาน slot, การกู้คิวหลัง restart
- ความถูกต้องของ migration `0011`
- การแยกทรัพยากรระหว่าง build ที่รันพร้อมกัน (workdir / branch / image)
- ผลกระทบต่อ throughput และภาระเครื่อง

**อยู่นอกขอบเขต** (ไม่เปลี่ยนใน commit นี้)
- คุณภาพโค้ดที่ Claude generate (ดู `SKILL.md` / เอกสารเทส harness แยกต่างหาก)
- ขั้นตอน push GitHub / สร้าง repo ต่อโปรเจค
- ระบบ feedback widget, skill versioning, dashboard UI ส่วนอื่น
- Performance ของ `docker build` เอง (เทสแค่ว่ารันพร้อมกันแล้วเครื่องไหวไหม)

---

## 3. ทดสอบไปแล้วอะไรบ้าง (ฝั่ง dev) และเหลืออะไร

**ทำแล้ว — ผ่าน**

| สิ่งที่ทำ | คำสั่ง | ผล |
|---|---|---|
| คอมไพล์ทั้งโมดูล | `go build ./...` | ผ่าน ไม่มี error |
| static analysis | `go vet ./...` | ผ่าน ไม่มี warning |
| unit test ที่มีอยู่ | `go test ./...` | ผ่าน (หมายเหตุ: repo ยังไม่มีเทสของ jobs/store) |
| ตรวจ global state ใน build path | อ่านโค้ด: หา `os.Chdir` / `os.Setenv` / `git config --global` | **ไม่พบ** — ทุก exec set `cmd.Dir` เอง → ขนานได้ปลอดภัย |
| ตรวจว่ามี constraint ซ่อนอยู่ไหม | อ่าน `migrations/0001_init.sql` | **พบ** `uq_jobs_one_building_per_org` → จึงต้องเพิ่ม migration `0011` |
| ตรวจ dashboard รองรับหลาย build ไหม | อ่าน `NowBuilding.tsx` | รองรับอยู่แล้ว (`building.map`, `{building.length} active`) ไม่ต้องแก้ |

**ยังไม่ได้ทำ — คือทั้งหมดในเอกสารนี้**

| ยังไม่ได้ทดสอบ | เพราะ |
|---|---|
| SQL ตัวใหม่ (`NextRunnableJob`, `NextQueuedJobForProject`) กับ Postgres จริง | เครื่อง dev ไม่ได้เปิด Docker/Postgres ตอนพัฒนา |
| migration `0011` รันจริง | เหตุผลเดียวกัน |
| พฤติกรรมขนานจริงทั้งหมด (TC-A3…TC-B4) | ต้องมี CRN + DB ที่รันอยู่จริง |

> พูดตรง ๆ: commit นี้ **ผ่านแค่ระดับคอมไพล์กับการรีวิวโค้ด** การรันเอกสารนี้บน .171 คือการทดสอบจริงครั้งแรก

---

## 4. สภาพแวดล้อมที่ใช้ทดสอบ

| รายการ | ค่าที่ใช้ | กรอกจริง |
|---|---|---|
| เครื่อง CRN | Mac mini `172.168.1.171` (user `macagents`) | |
| CPU / RAM | | |
| CRN backend | `:8080` | |
| Dashboard | `:3001` | |
| Postgres | container `crn-postgres` host `:5433` | |
| commit | `91de383` | |
| `CRN_BUILD_IMAGE` | | |
| `CRN_GITHUB_OWNER` | (ถ้าตั้งไว้ → โปรเจคใหม่ = repo ใหม่ ดูข้อ 5.2) | |

---

## 5. การเตรียมก่อนทดสอบ

### SETUP-1 · Deploy ให้ถูกลำดับ

```bash
cd ~/fitt-coderunner
git pull origin dev
make migrate      # ← ต้องมาก่อน
make restart
```

> **ห้ามสลับลำดับ** ถ้า restart ก่อน migrate: binary ใหม่จะพยายามรัน 2 build พร้อมกัน แต่ index เก่าระดับ org ยังอยู่ → ตัวที่ 2 ตายด้วย unique violation (ความเสี่ยง R1)

### SETUP-2 · ยืนยันว่า deploy ลงจริง

```bash
export CRN=http://172.168.1.171:8080
curl -s $CRN/healthz | jq .
```

ต้องได้ (ตัวอย่าง)

```json
{
  "status": "ok",
  "build": { "revision": "91de383", "time": "2026-08-11T...Z", "modified": false }
}
```

- `revision` ต้องเป็น `91de383` — ถ้าเป็น `unknown` แปลว่ารันด้วย `make run` (`go run` ไม่ stamp) ให้ใช้ `make restart` แทน
- `modified: true` = build จาก working tree ที่ยังไม่ commit → อย่าเอาผลเทสไปอ้างอิง

### SETUP-3 · เครื่องมือ + shortcut

```bash
export CRN=http://172.168.1.171:8080
psql() { docker compose exec -T postgres psql -U crn -d crn "$@"; }   # รันในโฟลเดอร์ repo
```

ต้องมี `curl`, `jq`, `docker compose` บนเครื่องที่รันเทส

### SETUP-4 · สร้างโปรเจค QA ถาวร 5 ตัว (ทำครั้งเดียว เก็บไว้ใช้ตลอด)

อย่าปล่อยให้ระบบสุ่ม `project_id` ใหม่ทุกครั้งที่ยิงเทส — ถ้า `CRN_GITHUB_OWNER` ถูกตั้งไว้ **โปรเจคใหม่ = repo ใหม่บน GitHub** ยิงเทส 3 รอบก็ได้ repo ขยะ 15 อัน

```bash
for i in 1 2 3 4 5; do echo "export QA$i=$(uuidgen | tr 'A-Z' 'a-z')"; done
```

เอา 5 บรรทัดที่ได้ไปเก็บไว้ในไฟล์ `~/qa-projects.env` แล้ว `source ~/qa-projects.env` ทุกครั้งก่อนเทส

### SETUP-5 · helper

**ยิง build 1 ตัว**

```bash
fire() {  # fire <project_id> <name>
  curl -sS -X POST "$CRN/internal/projects" -H 'Content-Type: application/json' \
    -d "{\"project_id\":\"$1\",\"name\":\"$2\",\"tag\":\"alpha-test\",\"idea\":\"QA concurrency probe\",\"prompts\":[\"หน้าเดียว แสดงข้อความ $2\"]}" \
  | jq -c '{project_id,job_id,build_no,status}'
}
```

ต้องตอบ HTTP 202 พร้อม body ประมาณนี้

```json
{"project_id":"...","job_id":"...","build_no":1,"status":"queued"}
```

**ดูสถานะสด (เปิดค้างไว้อีกหน้าต่าง)**

```bash
while :; do
  curl -s "$CRN/internal/dashboard" \
    | jq -c '{t:(now|todate), building:.vitals.building, queued:.vitals.queued, now:[.building[].project_name]}'
  sleep 2
done
```

**ล้างสนามก่อนเริ่มแต่ละเคส** — จดเวลาเริ่มไว้ใช้กรอง SQL

```bash
export T0=$(date -u +%Y-%m-%dT%H:%M:%SZ); echo "เริ่มเคสเวลา $T0"
```

---

## 6. ตารางสรุปเคสทั้งหมด

| เคส | หัวข้อ | ประเภท | กันความเสี่ยง | รอบ | ระดับ |
|---|---|---|---|---|---|
| TC-A1 | migration สลับ index จริง | สถานะระบบ | R1 | A | **บังคับ** |
| TC-A2 | `CRN_MAX_CONCURRENT_BUILDS` ถูกอ่านจริง | config | R2 | A | **บังคับ** |
| TC-A3 | คนละโปรเจครันขนานกันจริง | ฟังก์ชันหลัก | R3 | A | **บังคับ** |
| TC-A4 | โปรเจคเดียวกันห้ามทับกัน | ฟังก์ชันหลัก | R4 | A | **บังคับ** |
| TC-A5 | slot เต็ม → คิวไม่หาย และไม่เกินเพดาน | ฟังก์ชันหลัก | R2, R5, R6 | A | **บังคับ** |
| TC-A6 | restart กลางคิวแล้วงานไม่หาย | ความทนทาน | R6, R7 | A | ควรทำ |
| TC-A7 | ตั้ง 1 = พฤติกรรมเดิม | regression | R5 | A | ควรทำ |
| TC-B1 | 3 build จริงพร้อมกันจนจบ | end-to-end | R3 | B | **บังคับ** |
| TC-B2 | แต่ละ build แยกทรัพยากรกันเด็ดขาด | end-to-end | R8 | B | **บังคับ** |
| TC-B3 | ผลลัพธ์ส่งออกครบ ไม่สลับโปรเจค | integration | R9 | B | **บังคับ** |
| TC-B4 | เครื่องรับไหวที่ค่า N | ทรัพยากร | R10 | B | **บังคับ** |

**รอบ A** = เทสเฉพาะตัวจ่ายงาน โดยตั้ง `CRN_RUN_CLAUDE=false` → build จบใน ~วินาที **ไม่เผา token** ยิงซ้ำได้เยอะ เห็นพฤติกรรมคิวชัด
**รอบ B** = ของจริง เปิด Claude + docker ครบ ใช้ export จริงจาก FBD

> **สำคัญ:** รอบ A ต้องแก้ `.env` บนเครื่องจริง — แจ้งทีมก่อน และ **สำรองไฟล์เดิมไว้** `cp .env .env.bak` แล้ว `cp .env.bak .env` เมื่อจบรอบ A

---

## 7. รายละเอียดรายเคส

### รอบ A — ทดสอบตัวจ่ายงาน (ไม่เผา token)

**เตรียมรอบ A**

```bash
cp .env .env.bak
# แก้ .env: CRN_RUN_CLAUDE=false
make restart
```

---

#### TC-A1 · migration สลับ index จริง

| | |
|---|---|
| **จุดประสงค์** | ยืนยันว่า index ระดับ org ถูกถอดออก และ index ระดับ project ถูกสร้าง |
| **กันความเสี่ยง** | R1 — build ตัวที่ 2 ตายด้วย unique violation |
| **Pre-condition** | รัน `make migrate` แล้ว |

**ขั้นตอน**

1. ตรวจว่า migration ถูกบันทึกว่า apply แล้ว

```bash
psql -tAc "select version, applied_at from schema_migrations order by version desc limit 3"
```

2. ตรวจ index จริงในตาราง

```bash
psql -c "select indexname, indexdef from pg_indexes where tablename='project_jobs' and indexname like 'uq_jobs%'"
```

**ผลที่คาด**

- ข้อ 1: มีบรรทัด `0011_concurrent_builds|<เวลา>`
- ข้อ 2: มี `uq_jobs_one_building_per_project` ที่ `indexdef` มี `WHERE (status = 'building'::text)` และ **ไม่มี** `uq_jobs_one_building_per_org`

**เกณฑ์ผ่าน** ครบทั้ง 2 ข้อ · **หลักฐาน** วาง output ทั้ง 2 คำสั่ง

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน — ________________________________

---

#### TC-A2 · `CRN_MAX_CONCURRENT_BUILDS` ถูกอ่านจริง

| | |
|---|---|
| **จุดประสงค์** | ยืนยันว่าค่า env ถึงตัว manager จริง ไม่ใช่ hardcode |
| **กันความเสี่ยง** | R2 |

**ขั้นตอน**

1. ไม่ต้องตั้งอะไรใน `.env` → `make restart` → ดู log ตอน boot
2. ตั้ง `CRN_MAX_CONCURRENT_BUILDS=2` → `make restart` → ดู log อีกครั้ง
3. ตั้งค่าเป็น `0` → `make restart`
4. ลบค่าที่ตั้งผิดออก กลับเป็น `5`

**ผลที่คาด**

| ขั้น | ต้องเห็น |
|---|---|
| 1 | `build concurrency max_concurrent_builds=5` (ค่า default) |
| 2 | `build concurrency max_concurrent_builds=2` |
| 3 | server **ไม่ start** พร้อม error `config: CRN_MAX_CONCURRENT_BUILDS must be >= 1, got 0` |

**เกณฑ์ผ่าน** ครบ 3 ขั้น (ขั้น 3 คือความจงใจให้ล้มเร็ว ไม่ใช่บั๊ก)

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน — ________________________________

---

#### TC-A3 · คนละโปรเจครันขนานกันจริง ★

| | |
|---|---|
| **จุดประสงค์** | เคสหลักของงานนี้ — 3 โปรเจคต้องวิ่งพร้อมกัน ไม่ใช่ต่อคิว |
| **กันความเสี่ยง** | R3 |
| **Pre-condition** | `CRN_MAX_CONCURRENT_BUILDS=5`, `CRN_RUN_CLAUDE=false`, restart แล้ว |

**ขั้นตอน**

1. `source ~/qa-projects.env` และจดเวลา `export T0=$(date -u +%Y-%m-%dT%H:%M:%SZ)`
2. เปิดหน้าต่างดูสถานะสด (SETUP-5)
3. ยิง 3 โปรเจคติดกันภายใน 5 วินาที

```bash
fire $QA1 qa-conc-1; fire $QA2 qa-conc-2; fire $QA3 qa-conc-3
```

4. จับภาพหน้าต่างสถานะสดตอนที่ `building` ขึ้นสูงสุด
5. เปิด dashboard `http://172.168.1.171:3001` ดูแผง **Now building**
6. หลังทุกตัวจบ รัน SQL พิสูจน์การทับซ้อนของช่วงเวลา

```sql
WITH w AS (SELECT * FROM project_jobs WHERE queued_at > now() - interval '30 minutes')
SELECT count(*) AS overlapping_pairs
FROM w a JOIN w b ON a.project_id <> b.project_id AND a.id < b.id
WHERE a.started_at < COALESCE(b.finished_at, now())
  AND b.started_at < COALESCE(a.finished_at, now());
```

**ผลที่คาด**

- ทั้ง 3 คำสั่งตอบ 202 status `queued`
- `.vitals.building` ขึ้นถึง **3** และ `.building[]` มี `project_id` ต่างกัน 3 ตัว
- dashboard แสดง "3 active" พร้อมการ์ด 3 ใบ
- SQL: `overlapping_pairs >= 1` — **ของเดิมจะได้ `0` เสมอ** ตัวเลขนี้คือหลักฐานว่าขนานจริง

**เกณฑ์ผ่าน** `overlapping_pairs >= 1` **และ** เห็น building > 1 ด้วยตา

> ถ้า build จบเร็วมากจนตาไม่ทัน ให้เชื่อผล SQL เป็นหลัก (เป็นหลักฐานย้อนหลังที่แม่นกว่า)

**หลักฐาน** output ของ `fire` ทั้ง 3 · screenshot dashboard · ผล SQL

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน · `overlapping_pairs = ____` — ________________________________

---

#### TC-A4 · โปรเจคเดียวกันห้ามทับกัน ★

| | |
|---|---|
| **จุดประสงค์** | ยืนยันว่าการเปิดให้ขนาน **ไม่ได้** ทำให้ build โปรเจคเดียวกันชนกัน |
| **กันความเสี่ยง** | R4 — เคสที่อันตรายที่สุด (workdir/repo/image ทับกัน) |

**ขั้นตอน**

1. ยิงโปรเจค **เดียวกัน** 3 ครั้งรวดในคำสั่งเดียว

```bash
fire $QA1 qa-conc-1; fire $QA1 qa-conc-1; fire $QA1 qa-conc-1
```

2. ดูสถานะสดระหว่างนั้น
3. รอจนจบทั้งหมด แล้วรัน SQL หาคู่ที่เวลาทับกันในโปรเจคเดียวกัน

```sql
WITH w AS (SELECT * FROM project_jobs
           WHERE project_id = '<QA1>' AND queued_at > now() - interval '30 minutes')
SELECT a.build_no AS build_a, b.build_no AS build_b,
       a.started_at, a.finished_at, b.started_at, b.finished_at
FROM w a JOIN w b ON a.id < b.id
WHERE a.started_at < COALESCE(b.finished_at, now())
  AND b.started_at < COALESCE(a.finished_at, now());
```

4. ดูลำดับ build

```bash
psql -c "select build_no, status, queued_at, started_at, finished_at from project_jobs where project_id='<QA1>' order by build_no"
```

5. ตรวจ log ว่าไม่มี unique violation

```bash
grep -iE "duplicate key|unique constraint" <ไฟล์ log ที่รัน backend>
```

**ผลที่คาด**

- ระหว่างทาง: `building` ของโปรเจคนี้ = 1 เสมอ ที่เหลืออยู่ `queued`
- SQL ข้อ 3: **0 แถว** ← เกณฑ์ตัดสิน
- ข้อ 4: `build_no` เดินเรียง 1, 2, 3 และช่วงเวลาไม่คาบเกี่ยวกัน (`finished_at` ของตัวก่อน ≤ `started_at` ของตัวถัดไป)
- ข้อ 5: ไม่พบ

**เกณฑ์ผ่าน** SQL ได้ 0 แถว และไม่มี unique violation ใน log

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน · จำนวนแถวที่ทับกัน = `____` — ________________________________

---

#### TC-A5 · slot เต็ม → คิวไม่หาย และไม่เกินเพดาน ★

| | |
|---|---|
| **จุดประสงค์** | ยืนยัน 3 อย่างพร้อมกัน: เพดานทำงาน, คิวไม่หาย, และคิวถูกหยิบต่อเองโดยไม่ต้อง trigger ซ้ำ |
| **กันความเสี่ยง** | R2, R5 (slot รั่ว), R6 (starvation) |

**ขั้นตอน**

1. ตั้ง `CRN_MAX_CONCURRENT_BUILDS=2` → `make restart` → ยืนยัน log `max_concurrent_builds=2`
2. ยิง 5 โปรเจครวด

```bash
fire $QA1 qa-conc-1; fire $QA2 qa-conc-2; fire $QA3 qa-conc-3; fire $QA4 qa-conc-4; fire $QA5 qa-conc-5
```

3. ดูหน้าต่างสถานะสด **โดยไม่ยิงอะไรเพิ่มอีกเลย**
4. รอจนคิวว่าง แล้วนับ peak concurrency ด้วย SQL (นับ ณ วินาทีที่แต่ละ build เริ่ม ว่ามีกี่ตัววิ่งอยู่ — จุดสูงสุดจะเกิดที่จังหวะเริ่มเสมอ)

```sql
WITH w AS (SELECT * FROM project_jobs
           WHERE started_at IS NOT NULL AND queued_at > now() - interval '30 minutes')
SELECT max(c) AS peak FROM (
  SELECT (SELECT count(*) FROM w b
          WHERE b.started_at <= a.started_at
            AND (b.finished_at IS NULL OR b.finished_at > a.started_at)) AS c
  FROM w a
) t;
```

5. ตรวจว่าไม่มีงานตกค้าง

```sql
SELECT status, count(*) FROM project_jobs
WHERE queued_at > now() - interval '30 minutes' GROUP BY status;
```

**ผลที่คาด**

| ช่วง | ต้องเห็น |
|---|---|
| ทันทีหลังยิง | `building: 2`, `queued: 3` |
| ระหว่างทาง | ตัวหนึ่งจบ → ตัวถัดไปขึ้นมาเอง **ไม่ต้องยิง/ไม่ต้อง trigger** |
| จบ | `building: 0`, `queued: 0` |
| SQL ข้อ 4 | `peak = 2` — ห้ามเกินค่าที่ตั้ง |
| SQL ข้อ 5 | มีแต่ `done`/`released` (หรือ `failed` ถ้า env รอบ A ทำให้ push ไม่ผ่าน — ยอมรับได้ ขอแค่ไม่ค้าง `queued`/`building`) |

**เกณฑ์ผ่าน** `peak = 2` **และ** ไม่มี job ค้าง `queued`

> ถ้า `peak > 2` = slot semaphore ไม่ทำงาน · ถ้ามี job ค้าง `queued` ทั้งที่ว่าง = slot รั่ว/dispatcher ไม่ถูกปลุก ทั้งสองแบบคือของต้องแก้ก่อนขึ้น production

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน · `peak = ____` · ค้างคิว `____` งาน — ________________________________

---

#### TC-A6 · restart กลางคิวแล้วงานไม่หาย

| | |
|---|---|
| **จุดประสงค์** | ยืนยันว่ากลไกกู้คืน (reconcile orphan + resume queued) ยังทำงานหลังเปลี่ยนมาเป็น dispatcher แบบใหม่ |
| **กันความเสี่ยง** | R6, R7 |

**ขั้นตอน**

1. `CRN_MAX_CONCURRENT_BUILDS=2` (ยังคงจากเคสก่อน) ยิง 5 โปรเจค
2. ระหว่างที่ยังมี `queued` เหลือ สั่ง `make restart` ทันที
3. ดู log ตอน boot
4. **ไม่ยิงอะไรเพิ่ม** รอดูว่าคิวเดินต่อเองไหม
5. ตรวจสถานะสุดท้าย

```sql
SELECT status, count(*) FROM project_jobs
WHERE queued_at > now() - interval '30 minutes' GROUP BY status;
```

**ผลที่คาด**

- job ที่ค้าง `building` ตอนถูกฆ่า → ถูก flip เป็น `failed` อัตโนมัติ (reconcile ตอน boot)
- job ที่ค้าง `queued` → **เริ่มวิ่งเองหลัง boot** โดยไม่ต้องยิงซ้ำ
- ปิดท้าย: ไม่มีแถวไหนค้าง `queued` หรือ `building`

**เกณฑ์ผ่าน** ไม่มีงานค้าง และไม่ต้องแทรกแซงด้วยมือ

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน — ________________________________

---

#### TC-A7 · ตั้ง 1 = พฤติกรรมเดิม (regression)

| | |
|---|---|
| **จุดประสงค์** | ให้มีทางถอย: ถ้าเครื่องไม่ไหว ตั้ง 1 ต้องได้พฤติกรรมเดิมเป๊ะ |
| **กันความเสี่ยง** | R5 |

**ขั้นตอน**

1. `CRN_MAX_CONCURRENT_BUILDS=1` → `make restart` → ยืนยัน log
2. ยิง 3 โปรเจคคนละตัว
3. รัน SQL peak (เหมือน TC-A5 ข้อ 4)

**ผลที่คาด** `peak = 1` · ทุก job จบครบ ไม่มีค้าง

**เกณฑ์ผ่าน** `peak = 1`

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน · `peak = ____`

**ปิดรอบ A**

```bash
cp .env.bak .env      # คืน CRN_RUN_CLAUDE=true และค่าอื่น ๆ
# ตั้ง CRN_MAX_CONCURRENT_BUILDS=5
make restart
```

---

### รอบ B — ของจริง (Claude + docker)

ใช้ export จริงจาก FBD 3 ชุด (ขนาดต่างกันได้ยิ่งดี) ยิงติดกันภายใน 1 นาที ผ่าน FBD หรือผ่าน `POST /internal/projects` พร้อม `zip_base64`/`zip_uri`

---

#### TC-B1 · 3 build จริงพร้อมกันจนจบ ★

| | |
|---|---|
| **จุดประสงค์** | end-to-end: ขนานได้จริงในสภาพจริง ไม่ใช่แค่ dispatcher |
| **กันความเสี่ยง** | R3 |

**ขั้นตอน**

1. จดเวลาเริ่ม `T0`
2. ยิง 3 โปรเจคจาก export จริง ภายใน 1 นาที
3. ดู dashboard ตลอด ไล่ดู phase ของแต่ละตัว (repo → materialize → claude → git → push → docker)
4. เปิด console log ของแต่ละ build เทียบกัน
5. จดเวลาที่แต่ละตัวจบ

**ผลที่คาด**

- dashboard แสดง 3 การ์ดพร้อมกัน ("3 active")
- ทั้ง 3 ไปถึงสถานะ `released`
- log ของแต่ละ job แยกกัน ไม่มีบรรทัดของโปรเจคอื่นปนเข้ามา
- `finished_at - started_at` ของแต่ละตัวไม่ยาวผิดปกติเกิน ~2 เท่าของตอนรันเดี่ยว

**เกณฑ์ผ่าน** ทั้ง 3 `released` และ log ไม่ปนกัน

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน · เวลารวม `____` นาที (เทียบรันทีละตัว `____` นาที)

---

#### TC-B2 · แต่ละ build แยกทรัพยากรกันเด็ดขาด ★

| | |
|---|---|
| **จุดประสงค์** | จุดเสี่ยงจริงของการรันขนาน — ไฟล์/branch/image ต้องไม่เขียนทับกัน |
| **กันความเสี่ยง** | R8 |

**ขั้นตอน + ผลที่คาด**

| # | ตรวจ | คำสั่ง | ต้องได้ |
|---|---|---|---|
| 1 | working dir | `ls -la $CRN_PROJECTS_DIR` | 1 โฟลเดอร์ต่อ 1 `project_id` · **ไม่มี** `.stale-*` ค้าง |
| 2 | git branch | `git ls-remote <CRN_GIT_REMOTE> \| grep crn/` | branch `crn/<slug>-<id8>` ต่างกันครบ 3 |
| 3 | image tag | `docker images \| grep crn-demo` | tag `<reg>/crn-demo-<slug>:v<n>` ของใครของมัน ไม่ทับกัน |
| 4 | log สะอาด | `grep -iE "ENOTEMPTY\|directory not empty\|Dockerfile not found\|no such file" <log>` | ไม่พบ |
| 5 | เนื้อ demo | เปิด demo ทั้ง 3 | เนื้อหาตรงกับ export ของตัวเอง ไม่สลับกัน |

**เกณฑ์ผ่าน** ผ่านครบทั้ง 5 ข้อ · ข้อ 5 คือข้อที่สำคัญที่สุด

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน — ________________________________

---

#### TC-B3 · ผลลัพธ์ส่งออกครบ ไม่สลับโปรเจค ★

| | |
|---|---|
| **จุดประสงค์** | ฝั่ง FTC DV ต้องได้ผลครบทุก build และ mapping ถูกตัว |
| **กันความเสี่ยง** | R9 |

**ขั้นตอน**

1. ดู `build_events`

```sql
SELECT project_id, job_id, status, created_at
FROM build_events WHERE created_at > now() - interval '2 hours'
ORDER BY created_at;
```

2. ถ้าตั้ง `CRN_FTC_DV_CALLBACK_URL` ไว้ ให้ฝั่ง FTC DV ยืนยันว่าได้ callback ครบ
3. เทียบ `image_ref` ของแต่ละ job กับ tag จริงใน registry

**ผลที่คาด**

- แต่ละ job มีทั้ง `building` และ `released` ครบ ไม่มีตัวไหนขาด
- `project_id` ↔ `image_ref` ตรงกันทุกคู่ ไม่มีการสลับ
- ไม่มี event ซ้ำผิดปกติ

**เกณฑ์ผ่าน** ครบ + ไม่สลับ

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน — ________________________________

---

#### TC-B4 · เครื่องรับไหวที่ค่า N ★

| | |
|---|---|
| **จุดประสงค์** | ตัดสินว่าเลข `CRN_MAX_CONCURRENT_BUILDS` ที่จะใช้จริงคือเท่าไร |
| **กันความเสี่ยง** | R10 |
| **หมายเหตุ** | ช่วงที่หนักสุดคือ **step docker** เพราะ build เป็น `--platform linux/amd64` บนเครื่อง ARM = emulate ผ่าน QEMU |

**ขั้นตอน**

1. ระหว่างที่ทั้ง 3 อยู่ช่วง docker พร้อมกัน จดค่าทุก 30 วินาที (3 ครั้ง)

```bash
uptime          # load average
vm_stat | head  # memory / swap (macOS)
```

2. เทียบเวลา build เฉลี่ยกับตอนรันทีละตัว
3. ถ้าไหวสบาย ลองยิงเพิ่มเป็น 5 โปรเจคพร้อมกัน แล้ววัดซ้ำ

**ผลที่คาด / เกณฑ์**

| วัด | เกณฑ์ |
|---|---|
| load average (1 นาที) | ไม่ควรเกินจำนวน core ของเครื่อง |
| swap | ไม่พุ่งขึ้นต่อเนื่อง |
| เวลาต่อ build | ช้าลงได้ แต่ไม่ควรเกิน ~2 เท่าของตอนรันเดี่ยว |
| การใช้งานอื่น | ssh/dashboard ยังตอบสนอง ไม่หน่วงจนใช้ไม่ได้ |

**สรุปเลขที่ควรใช้จริง:** `CRN_MAX_CONCURRENT_BUILDS = ______` (เหตุผล: ______________________)

**ผลจริง:** ☐ ผ่าน ☐ ไม่ผ่าน · load สูงสุด `____` · เวลาเฉลี่ย `____` นาที

---

## 8. บันทึกผลการทดสอบ

### 8.1 สรุปรอบทดสอบ

| ครั้งที่ | วันที่ | ผู้ทดสอบ | commit | เครื่อง | ผ่าน/ทั้งหมด | สรุป |
|---|---|---|---|---|---|---|
| 1 | | | `91de383` | | ___ / 11 | ☐ ผ่าน ☐ ไม่ผ่าน |
| 2 | | | | | ___ / 11 | ☐ ผ่าน ☐ ไม่ผ่าน |

### 8.2 ผลรายเคส

| เคส | หัวข้อ | ผล | ค่าที่วัดได้ | หลักฐาน |
|---|---|---|---|---|
| TC-A1 | migration สลับ index | ☐ผ่าน ☐ไม่ผ่าน | | |
| TC-A2 | env ถูกอ่านจริง | ☐ผ่าน ☐ไม่ผ่าน | | |
| TC-A3 | คนละโปรเจคขนานกัน | ☐ผ่าน ☐ไม่ผ่าน | `overlapping_pairs=` | |
| TC-A4 | โปรเจคเดียวกันไม่ทับ | ☐ผ่าน ☐ไม่ผ่าน | แถวที่ทับ `=` | |
| TC-A5 | คิวไม่หาย + ไม่เกินเพดาน | ☐ผ่าน ☐ไม่ผ่าน | `peak=` | |
| TC-A6 | restart แล้วงานไม่หาย | ☐ผ่าน ☐ไม่ผ่าน | | |
| TC-A7 | ตั้ง 1 = เดิม | ☐ผ่าน ☐ไม่ผ่าน | `peak=` | |
| TC-B1 | 3 build จริงจนจบ | ☐ผ่าน ☐ไม่ผ่าน | เวลารวม | |
| TC-B2 | isolation | ☐ผ่าน ☐ไม่ผ่าน | | |
| TC-B3 | ส่งผลครบ ไม่สลับ | ☐ผ่าน ☐ไม่ผ่าน | | |
| TC-B4 | เครื่องรับไหว | ☐ผ่าน ☐ไม่ผ่าน | load / เวลา | |

### 8.3 ปัญหาที่เจอ (defect log)

| # | เคส | อาการ | หลักฐาน (log/SQL) | ความรุนแรง | สถานะ |
|---|---|---|---|---|---|
| 1 | | | | ☐blocker ☐major ☐minor | |
| 2 | | | | ☐blocker ☐major ☐minor | |

### 8.4 สรุปตัดสิน

☐ **ผ่าน — ใช้ได้บน production** (เคสบังคับผ่านครบ: A1, A2, A3, A4, A5, B1, B2, B3, B4)
☐ **ผ่านแบบมีเงื่อนไข** — ใช้ค่า `CRN_MAX_CONCURRENT_BUILDS = ____` เพราะ ____________
☐ **ไม่ผ่าน** — ต้องแก้ก่อน: ____________

ผู้อนุมัติ: ______________ วันที่: __________

---

## 9. Regression — สิ่งที่ต้องไม่พังจากงานนี้

ตรวจแบบเร็ว ๆ หลังจบรอบ B

| ตรวจ | วิธี | ต้องได้ |
|---|---|---|
| หน้า status ของโปรเจค | `GET /v1/projects/{id}/status` | คืนค่า job ปัจจุบัน + คิวถัดไปของ **โปรเจคนั้น** ถูกต้อง (โค้ดส่วนนี้เปลี่ยนมาใช้ `NextQueuedJobForProject`) |
| cancel build | กด cancel บน dashboard ระหว่าง build | build หยุดจริง, status `cancelled`, slot ถูกคืน (ยิงตัวใหม่แล้วเริ่มได้ทันที) |
| edit build | `POST /internal/projects/{id}/edit` | ทำงานปกติ และยัง serialize กับ build ปกติของโปรเจคเดียวกัน |
| rollback | `POST /v1/projects/{id}/rollback/{build_no}` | ทำงานปกติ |
| ยิงซ้ำระหว่าง build | ยิงโปรเจคที่กำลัง build อยู่ | ตอบ 202 `{"status":"project busy, queued"}` ไม่ใช่ error |

---

## 10. ถ้าไม่ผ่าน — ไล่หาสาเหตุตรงไหนก่อน

| อาการ | สาเหตุที่น่าจะเป็น | ตรวจที่ |
|---|---|---|
| build ตัวที่ 2 error `duplicate key ... uq_jobs_one_building_per_org` | ยังไม่ migrate (หรือ restart ก่อน migrate) | `make migrate` แล้ว restart ใหม่ · TC-A1 |
| ยิง 3 โปรเจค แต่ `building` ขึ้นแค่ 1 | binary ยังเป็นตัวเก่า | `/healthz` → `revision` ต้อง `91de383` |
| `building` ไม่เคยเกิน N ทั้งที่คิวยาว | ค่า env ต่ำกว่าที่คิด | log boot `build concurrency` · TC-A2 |
| job ค้าง `queued` ทั้งที่เครื่องว่าง | dispatcher ไม่ถูกปลุก / slot รั่ว | log `all build slots busy, leaving queued`, `project busy, leaving queued`; restart แล้ว `ResumeQueued` ควรกู้ให้ |
| build โปรเจคเดียวกันทับกัน | advisory lock ไม่ทำงาน | ระหว่าง build: `select * from pg_locks where locktype='advisory'` ต้องมีแถวต่อ 1 build |
| `ENOTEMPTY` / `directory not empty` | workspace reset ชนกับ Spotlight | มี `.metadata_never_index` ในโฟลเดอร์ workspaces ไหม |
| เครื่องหน่วง/ค้างช่วง docker | slot มากไปสำหรับเครื่องนี้ | ลดเป็น 3 หรือ 2 → `make restart` (ไม่ต้อง rebuild) |

---

## 11. ภาคผนวก

### A · SQL ที่ใช้ทั้งหมด (รวมไว้ที่เดียว)

```sql
-- A1: index ปัจจุบัน
SELECT indexname, indexdef FROM pg_indexes
WHERE tablename='project_jobs' AND indexname LIKE 'uq_jobs%';

-- A1: migration ที่ apply แล้ว
SELECT version, applied_at FROM schema_migrations ORDER BY version DESC LIMIT 5;

-- A3: มีคู่ build ข้ามโปรเจคที่เวลาทับกันไหม (ต้อง >= 1)
WITH w AS (SELECT * FROM project_jobs WHERE queued_at > now() - interval '30 minutes')
SELECT count(*) AS overlapping_pairs
FROM w a JOIN w b ON a.project_id <> b.project_id AND a.id < b.id
WHERE a.started_at < COALESCE(b.finished_at, now())
  AND b.started_at < COALESCE(a.finished_at, now());

-- A4: คู่ build ในโปรเจคเดียวกันที่เวลาทับกัน (ต้อง = 0 แถว)
WITH w AS (SELECT * FROM project_jobs WHERE queued_at > now() - interval '30 minutes')
SELECT a.project_id, a.build_no, b.build_no
FROM w a JOIN w b ON a.project_id = b.project_id AND a.id < b.id
WHERE a.started_at < COALESCE(b.finished_at, now())
  AND b.started_at < COALESCE(a.finished_at, now());

-- A5/A7: peak concurrency ในช่วงเทส (ต้อง = ค่าที่ตั้ง)
WITH w AS (SELECT * FROM project_jobs
           WHERE started_at IS NOT NULL AND queued_at > now() - interval '30 minutes')
SELECT max(c) AS peak FROM (
  SELECT (SELECT count(*) FROM w b
          WHERE b.started_at <= a.started_at
            AND (b.finished_at IS NULL OR b.finished_at > a.started_at)) AS c
  FROM w a
) t;

-- A5/A6: มีงานค้างไหม
SELECT status, count(*) FROM project_jobs
WHERE queued_at > now() - interval '30 minutes' GROUP BY status;

-- A4: ไทม์ไลน์ของโปรเจคเดียว
SELECT build_no, status, queued_at, started_at, finished_at
FROM project_jobs WHERE project_id = '<uuid>' ORDER BY build_no;

-- B3: event ที่ส่งออก
SELECT project_id, job_id, status, created_at FROM build_events
WHERE created_at > now() - interval '2 hours' ORDER BY created_at;

-- ตรวจ advisory lock ระหว่าง build
SELECT * FROM pg_locks WHERE locktype = 'advisory';
```

### B · โค้ดที่เกี่ยวข้อง (ไว้ debug)

| ไฟล์ | จุดสำคัญ |
|---|---|
| `migrations/0011_concurrent_builds.sql` | drop index ราย org → สร้าง index ราย project |
| `internal/jobs/jobs.go:111` | `slots chan struct{}` — semaphore |
| `internal/jobs/jobs.go:331` | `lockMissLimit = 3` — กันวนฟรีตอนแย่งล็อก |
| `internal/jobs/jobs.go:345` | `dispatch()` — ตัวจ่ายงาน: จอง slot → อ่านคิว → ล็อกโปรเจค → รัน |
| `internal/jobs/jobs.go:404` | `freeSlot()` — คืน slot |
| `internal/jobs/jobs.go:1072` | `ResumeQueued()` — กู้คิวหลัง restart |
| `internal/store/store.go:375` | `NextRunnableJob()` — FIFO ทั้งระบบ + `NOT EXISTS` ข้ามโปรเจคที่ build อยู่ |
| `internal/store/store.go:403` | `NextQueuedJobForProject()` — ใช้กับหน้า status |
| `internal/store/store.go:1253` | `AcquireProjectLock()` — `pg_try_advisory_lock(hashUUID(projectID))` |
| `internal/config/config.go:185` | อ่าน `CRN_MAX_CONCURRENT_BUILDS` (default 5, `< 1` = start ไม่ขึ้น) |
| `cmd/server/main.go:102` | log `build concurrency` ตอน boot |

### C · ทำไมต้องเป็น FIFO ทั้งระบบ ไม่ใช่ต่อคิวราย org

design แรกให้แต่ละ org ไล่คิวของตัวเอง แต่เมื่อ slot เป็นของกลาง org ที่ไม่ได้จบ build ล่าสุดจะไม่มีใครปลุก → คิวค้างถาวร (starvation) จึงเปลี่ยนเป็นอ่านคิวรวมทั้งระบบเรียงตาม `queued_at` ซึ่งได้ผลถูกต้องไม่ว่า FTC DV จะส่ง `org_id` แยกต่อลูกค้าหรือใช้ org เดียวร่วมกัน — **TC-A5 คือเคสที่ตรวจเรื่องนี้**

### D · คำศัพท์

| คำ | ความหมาย |
|---|---|
| slot | สิทธิ์รัน build 1 ตัว มีทั้งหมด `CRN_MAX_CONCURRENT_BUILDS` ใบ |
| advisory lock | ล็อกใน Postgres ที่แอปขอเอง (ไม่ผูกกับตาราง) ใช้กันไม่ให้ 2 process ทำงานเดียวกัน |
| peak concurrency | จำนวน build ที่วิ่งพร้อมกันสูงสุดในช่วงเวลาหนึ่ง |
| starvation | งานที่ไม่มีวันได้รัน เพราะไม่มีใครหยิบขึ้นมา |
| orphan build | job ที่ค้างสถานะ `building` เพราะ process ตายกลางคัน |
