# เลือก model / effort ของ build จากหน้าเว็บ

> สถานะ: design อนุมัติแล้ว 2026-09-23 · รอ review spec ก่อนวางแผน

## เป้าหมาย

1. **เห็น** ว่า build ใช้ model และ effort อะไร ใน 3 จุด:
   ค่าที่ build ถัดไปจะใช้ · build ที่กำลังวิ่ง · build ในประวัติ
2. **เปลี่ยน** model และ effort จากหน้าเว็บ โดยไม่ต้องแก้ `.env` หรือ restart CRN

## ไม่อยู่ในขอบเขต

- ตั้งค่าแยกต่อโปรเจกต์ — มีค่าเดียวทั้งระบบ
- รหัสผ่าน / auth — user เลือกแล้วว่า "ไม่ต้องใส่รหัส เลือกได้ทุกตัว" เหมือน `/internal/*` ที่เหลือ
- เปลี่ยนค่าของ build ที่กำลังวิ่ง
- ย้อนเติม model/effort ให้ build เก่า (ข้อมูลไม่เคยถูกบันทึก)

## สภาพปัจจุบัน

- model มาจาก `CRN_CLAUDE_MODEL` (ค่าตั้งต้น `claude-opus-5`) และถูกฝังใน runner ครั้งเดียวตอน process เริ่ม (`cmd/server/main.go:92`) เปลี่ยนได้ทางเดียวคือแก้ env แล้ว restart
- **ไม่ได้ส่ง `--effort` เลย** CLI ใช้ค่าตั้งต้นของตัวเอง
- `~/.claude/settings.json` บนเครื่อง build ตั้ง `"model": "sonnet"` ไว้ ตอนนี้ไม่มีผลเพราะ CRN ส่ง `--model` ทุกครั้ง **ต้องรักษาคุณสมบัตินี้ไว้: ทุกครั้งที่เรียก Claude ต้องส่งทั้ง `--model` และ `--effort`** ไม่งั้นค่าใน settings จะหลุดเข้ามาเงียบๆ
- `project_jobs` และ `job_traces` ไม่มีคอลัมน์ model/effort
- Claude ถูกเรียกแค่ 2 ที่: `runJob` → `m.runner.Run` (`internal/jobs/jobs.go` ~611) และ improve skill → `claude.Improve` (`internal/api/api.go:569`)
- CLI บนเครื่อง build: 2.1.280 รองรับ `--effort low|medium|high|xhigh|max`

## Design

### 1. เก็บค่า — `migrations/0012_build_settings.sql`

```sql
CREATE TABLE IF NOT EXISTS build_settings (
  id            SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  claude_model  TEXT NOT NULL,
  claude_effort TEXT NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_from  TEXT NOT NULL DEFAULT ''
);

ALTER TABLE project_jobs ADD COLUMN IF NOT EXISTS claude_model  TEXT;
ALTER TABLE project_jobs ADD COLUMN IF NOT EXISTS claude_effort TEXT;
ALTER TABLE job_traces   ADD COLUMN IF NOT EXISTS claude_model  TEXT;
ALTER TABLE job_traces   ADD COLUMN IF NOT EXISTS claude_effort TEXT;
```

- `CHECK (id = 1)` บังคับให้มีแถวเดียว
- **ไม่ seed แถว** — ยังไม่มีแถว = ใช้ค่าจาก env · กดบันทึกจากเว็บครั้งแรกถึงจะสร้างแถว
- `updated_from` เก็บ IP ของคนที่เปลี่ยนล่าสุด — ไม่มี auth เลยใช้เป็นร่องรอยว่าใครเปลี่ยน
- คอลัมน์ใหม่เป็น NULL ได้ แถวเก่าเป็น NULL → หน้าเว็บแสดง "—"
- **ไม่ใส่ CHECK ค่า model ใน DB** — รายการ model อยู่ในโค้ด Go ที่เดียว เพราะ model ใหม่ออกบ่อย ไม่ควรต้องเขียน migration ทุกครั้ง และคนเขียนค่ามีแค่ PUT handler ตัวเดียว
- **เข้ากันได้กับ binary ตัวเก่า** — ตารางและคอลัมน์ใหม่ไม่ถูกอ่านโดยโค้ดเก่า จึงรัน migrate ก่อน restart ได้ปลอดภัย

### 2. รายการตัวเลือก — package ใหม่ `internal/buildsettings`

แหล่งความจริงเดียวของตัวเลือกที่เลือกได้:

| id | label | input $/MTok | output $/MTok |
|---|---|---|---|
| `claude-opus-5-5` | Opus 5.5 | 4 | 20 |
| `claude-opus-5` | Opus 5 | 5 | 25 |
| `claude-sonnet-5` | Sonnet 5 | 2 | 10 |
| `claude-fable-5-1` | Fable 5.1 | 10 | 50 |

effort: `low` `medium` `high` `xhigh` `max`

- `Validate(model, effort) error` — รับเฉพาะค่าในรายการ ตรงตัว (case-sensitive)
- `Resolve(ctx, store, defaults) Settings` — มีแถวใน DB ใช้แถว · ไม่มีแถวใช้ env · อ่าน DB ไม่ได้ใช้ env แล้ว log warn **build ต้องไม่พังเพราะอ่านค่าตั้งไม่ได้**
- ราคาใช้แสดงผลอย่างเดียว ไม่มีผลกับการคิดเงินจริง อ้างอิงราคา Anthropic ณ ก.ย. 2026 ต้องแก้มือเมื่อราคาเปลี่ยน

**ค่าจาก env:**
- `CRN_CLAUDE_MODEL` — เหมือนเดิม ไม่ตรวจกับรายการ เพราะ config เดิมอนุญาตให้ใช้ alias เช่น `opus`
- `CRN_CLAUDE_EFFORT` (**ใหม่**, ค่าตั้งต้น `xhigh`) — ตรวจตอนเริ่ม ถ้าไม่อยู่ใน 5 ค่า **CRN ไม่ยอมเริ่มและบอกเหตุผลชัดเจน** เพราะค่าผิดจะทำให้ build พังทุกตัว

### 3. build ใช้ค่ายังไง — `internal/jobs`

ที่จุดเรียก Claude ของ build (มีจุดเดียวใน `runJob` ก่อนสร้าง `RunSpec`):

1. `Resolve` ค่าครั้งเดียว
2. บันทึกลง job: `store.SetJobClaudeConfig(ctx, jobID, model, effort)` → dashboard เห็นตอนกำลังวิ่ง
3. ใส่ใน `traceMeta` → `saveTrace` บันทึกลง `job_traces` ทุกเส้นทางจบ (done / failed / cancelled)
4. ใส่ใน `RunSpec.Model` / `RunSpec.Effort` → runner

ผลที่ตามมา:
- **เปลี่ยนค่าตอน build วิ่งอยู่ ตัวที่วิ่งอยู่ใช้ค่าเดิมจนจบ** · หลาย build พร้อมกัน แต่ละตัวจับค่าตอนเริ่มเรียก Claude ของตัวเอง
- build ที่ **ไม่ได้เรียก Claude** (reuse image จาก cache ผ่าน `finishReused`, หรือ `runClaude=false`) ไม่ผ่านจุดนี้ จึงไม่มี model บันทึก → "—" **ถูกต้องตามจริง เพราะไม่มี model ไหนรัน**
- บันทึกลง job ไม่สำเร็จ → log warn แล้วทำ build ต่อ (กระทบแค่การแสดงผล)

### 4. Runner — `internal/claude`

- `domain.RunSpec` เพิ่ม `Model string`, `Effort string`
- `Run`: model = `spec.Model` ถ้ามี ไม่งั้นใช้ `r.model` · ส่ง `--effort spec.Effort` ถ้ามี
- **`Resolve` คืนค่าครบทั้งคู่เสมอ** (อย่างแย่ที่สุดคือค่าจาก env) งานทั้งสองทางจึงส่ง `--model` และ `--effort` ทุกครั้ง การ fallback ใน runner มีไว้กันพลาดเท่านั้น
- `Improve(ctx, bin, model, effort, body, logger)` เพิ่มพารามิเตอร์ effort · handler ของ improve เรียก `Resolve` แล้วส่งทั้งคู่

### 5. API

**`GET /internal/settings/build`**
```json
{
  "model": "claude-opus-5",
  "effort": "xhigh",
  "source": "env",
  "updated_at": null,
  "updated_from": "",
  "options": {
    "models": [
      { "id": "claude-opus-5-5", "label": "Opus 5.5",
        "input_usd_per_mtok": 4, "output_usd_per_mtok": 20 }
    ],
    "efforts": ["low", "medium", "high", "xhigh", "max"]
  }
}
```
`source` = `"env"` (ยังไม่เคยบันทึกจากเว็บ) หรือ `"saved"`

**`PUT /internal/settings/build`** body `{"model": "...", "effort": "..."}`
- ผ่าน `Validate` → upsert (บันทึก `updated_from` = ส่วน host ของ `r.RemoteAddr` ไม่เอา port) → ตอบกลับรูปเดียวกับ GET
- ค่าไม่อยู่ในรายการ → **400** พร้อมเหตุผล ไม่เขียนอะไร · ฟิลด์เกินถูกปฏิเสธ (`decodeJSON` เดิมทำอยู่แล้ว)
- log ทุกครั้งที่เปลี่ยน: ค่าเก่า → ค่าใหม่ + IP

**ฟิลด์ที่เพิ่มในข้อมูลเดิม** (เพิ่มอย่างเดียว ไม่เปลี่ยนของเดิม):
- `/internal/dashboard` → `building[]` เพิ่ม `claude_model`, `claude_effort`
- trace ทุก endpoint (`/internal/traces`, `/internal/jobs/{id}/trace`, `/internal/projects/{id}/builds`) เพิ่ม `claude_model`, `claude_effort`
- **FITT Builder ที่อ่าน `/internal/projects/{id}/builds` อยู่ไม่ได้รับผลกระทบ** เพราะเป็นฟิลด์ใหม่เท่านั้น

### 6. หน้าเว็บ — `frontend/`

| ไฟล์ | สิ่งที่เปลี่ยน |
|---|---|
| `lib/config.ts` | `buildSettingsUrl()` |
| `lib/types.ts` | `BuildSettings`, `ModelOption` · เพิ่ม `claude_model?`, `claude_effort?` ใน `BuildingJob` และ `BuildTrace` |
| `lib/useBuildSettings.ts` (ใหม่) | โหลด + บันทึก คืน `{ data, error, saving, save }` |
| `components/BuildSettingsPanel.tsx` (ใหม่) | แผงบนหน้าแรก |
| `components/NowBuilding.tsx` | ป้าย `Opus 5.5 · xhigh` ข้าง `build #N` |
| `components/TraceSpanGraph.tsx` | ป้าย model/effort ข้าง cost ในประวัติ build · ไม่มีค่าแสดง "—" |

**แผง `BuildSettingsPanel`** — แถวกะทัดรัดเต็มความกว้าง อยู่ใต้ `VitalsRail` ก่อนแถว NowBuilding:
- ค่าปัจจุบัน + ที่มา: "ค่าตั้งต้นจาก env" หรือ "บันทึกจากเว็บเมื่อ <เวลา> จาก <IP>"
- dropdown model ที่แสดงราคาในชื่อ เช่น `Opus 5.5 · $4 / $20 ต่อ 1M token`
- dropdown effort
- ปุ่มบันทึก กดได้เฉพาะเมื่อค่าเปลี่ยน
- ข้อความกำกับ: "มีผลกับ build ถัดไป — build ที่กำลังวิ่งใช้ค่าเดิมจนจบ"
- บันทึกไม่ผ่าน → แสดง error และยังแสดงค่าเดิม
- ค่าปัจจุบันที่ไม่อยู่ในรายการ (เช่น env เป็น alias `opus`) → แสดงตามจริงเป็นข้อความ

ป้ายใน NowBuilding และประวัติ build แปลง id เป็น label ด้วยฟังก์ชันเดียว (`claude-opus-5-5` → `Opus 5.5`, `claude-fable-5-1` → `Fable 5.1`) จึงไม่ต้องโหลด options ในทุกหน้า · id ที่แปลงไม่ได้แสดง id ดิบ · รายการที่เลือกได้และราคายังมาจาก API ที่เดียว

## การจัดการ error (สรุป)

| เหตุการณ์ | ผล |
|---|---|
| อ่าน `build_settings` ไม่ได้ | ใช้ env + log warn · build ทำต่อ |
| บันทึก model/effort ลง job ไม่ได้ | log warn · build ทำต่อ |
| PUT ค่าไม่อยู่ในรายการ | 400 + เหตุผล · ไม่เขียน |
| PUT แล้ว DB เขียนไม่ได้ | 500 · หน้าเว็บแสดง error คงค่าเดิม |
| `CRN_CLAUDE_EFFORT` ผิด | CRN ไม่เริ่ม + บอกเหตุผล |

## การทดสอบ

- **`internal/buildsettings`** — `Validate` รับทุกค่าในรายการ ปฏิเสธ model/effort แปลกปลอมและตัวพิมพ์ผิด · `Resolve` ไม่มีแถว → env · มีแถว → แถว · store error → env
- **`internal/claude`** — stub binary `claude` (แบบเดียวกับ `stubDocker` ใน `dockerpush_test.go`) ตรวจว่า args มี `--model X --effort Y` · `spec.Model` ว่าง → ใช้ `r.model`
- **`internal/jobs`** — ขยาย `traceStore` ใน `trace_test.go` ให้ trace ที่จบแล้วมี `claude_model` / `claude_effort` จาก meta ทั้ง failed และ cancelled
- **`internal/api`** — GET ตอนไม่มีแถวได้ `source: env` · PUT ถูก → 200 และอ่านกลับได้ค่าใหม่ · PUT ผิด → 400 และไม่มีการเขียน
- **config** — `CRN_CLAUDE_EFFORT` ผิดทำให้โหลด config ล้มพร้อมข้อความชัดเจน
- **หน้าเว็บ** — `npx tsc --noEmit` + `npm run build` (`npm run lint` ใช้ไม่ได้อยู่แล้วตั้งแต่ Next 16 ถอด `next lint`)
- **ลองจริงบนเครื่อง build** — ตั้ง Opus 5.5 · xhigh บนเว็บ → สั่ง build → เห็นป้ายตอนวิ่ง → trace บันทึก model/effort คู่กับ cost

## การ deploy (ลำดับสำคัญ)

1. บนเครื่อง build: `git pull` → `make build`
2. `make migrate` — ใส่ 0012 · **รันตอน binary เก่ายังวิ่งได้** เพราะเพิ่มอย่างเดียว
3. **รอให้ไม่มี build วิ่งอยู่** (restart ฆ่า build ที่กำลังวิ่ง)
4. restart แบบปลอดภัย — **ห้าม `make restart` ผ่าน ssh**:
   `make stop && ( set -a; . ./.env; set +a; nohup ./bin/crn-server > ~/crn.log 2>&1 & )`
   แล้วตัด ssh ก่อนเช็ค `/healthz` ว่าเป็น commit ใหม่
   — ผลพลอยได้: log จะกลับมาเขียนลง `~/crn.log` (ตอนนี้ผูกกับ terminal ไฟล์หยุดตั้งแต่ 10 ก.ย.)
5. หน้าเว็บ: บนเครื่อง build รันเป็น `next dev` (`make frontend-dev`, port 3000) **รับโค้ดใหม่เองหลัง `git pull`** ไม่ต้อง restart เพราะไม่ได้เพิ่ม dependency

## ความเสี่ยง

- **resume ข้าม model** — edit build `--resume` session เดิม ถ้า session นั้นสร้างด้วย model อื่น thinking block ของ model เก่าจะถูกทิ้ง Claude Code รองรับการเปลี่ยน model กลาง session แต่ควรเฝ้า edit build ตัวแรกหลังสลับ model · trace จะบอก model ที่ใช้ ทำให้ไล่ย้อนได้
- **ไม่มี auth** (user ตัดสินใจแล้ว) — ใครในวง LAN ก็สลับทุก build ไป Fable 5.1 · max ได้ สิ่งที่ช่วยคือ ราคาอยู่ตรงหน้าตอนเลือก · ทุกการเปลี่ยนถูก log พร้อม IP และแผงแสดงว่าใครเปลี่ยนล่าสุดเมื่อไหร่
- **ราคาในโค้ดจะเก่า** — ใช้แสดงผลอย่างเดียว
- ค่าตั้งต้น effort ฝั่ง API ของ Opus 5.5 (`medium`) **ไม่มีผล** เพราะ CRN ส่ง `--effort` เองทุกครั้ง
