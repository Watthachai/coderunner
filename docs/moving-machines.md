# ย้าย CRN ไปเครื่องใหม่

> **สรุปสั้น:** copy โฟลเดอร์เฉย ๆ **ไม่พอ** — ฐานข้อมูลไม่ได้อยู่ในโฟลเดอร์
> ใช้ `scripts/pack-for-move.sh` แล้วได้ไฟล์เดียวที่ครบจริง

---

## ทำไม copy folder ไม่พอ

| ✅ อยู่ในโฟลเดอร์ ยกไปด้วยได้ | ❌ ไม่ได้อยู่ในโฟลเดอร์ |
|---|---|
| โค้ด + `migrations/` | **Postgres** (docker volume) — โปรเจกต์ ประวัติ build **skill ทุกตัว** |
| `.env` (แค่ gitignore ไม่ได้หายไปไหน) | **Mongo** (docker volume) |
| `.crn-workspaces/` | docker images |
| | login: `claude`, `gh`, `docker` |

เปิดเครื่องใหม่หลัง copy โฟลเดอร์อย่างเดียว = CRN ที่ **ดูปกติทุกอย่าง** แต่ไม่มีโปรเจกต์ ไม่มีประวัติ และมีแค่ skill built-in ตัวเดียว

> ⚠️ ชื่อ volume ใน `docker-compose.yml` (`crn_pgdata`) **ไม่ใช่ชื่อจริง** — Compose เติมชื่อโปรเจกต์ให้เป็น `fitt-coderunner_crn_pgdata` ถ้าไป `docker run -v crn_pgdata:/data ...` ตรง ๆ docker จะ**สร้าง volume เปล่าตัวใหม่แล้ว backup ความว่างเปล่า** ได้ไฟล์ที่ดูเหมือนสำเร็จ สคริปต์เลยถามจาก container ที่รันอยู่จริงแทนการเดาชื่อ

---

## เครื่องเก่า — แพ็ค

```bash
cd ~/fitt-coderunner
./scripts/pack-for-move.sh
```

ได้ `../crn-move-<วันเวลา>.tar.gz` ไฟล์เดียว มีทั้งโค้ด `.env` และ snapshot ของ Postgres/Mongo

สคริปต์ **หยุด postgres/mongo ชั่วคราว** ก่อน tar (tar ทับ DB ที่กำลังเขียนอยู่ = ไฟล์ที่ดูดีแต่พังเป็นครั้งคราว) แล้วสตาร์ทกลับให้เอง — อย่ารันตอนมี build ค้างอยู่ เช็คก่อนที่หน้า dashboard ว่า `building: 0`

### 🔐 ไฟล์นี้มีความลับอยู่ข้างใน

`.env` มีรหัสผ่าน DB, callback token และอื่น ๆ — ส่งผ่าน **AirDrop หรือสาย** เท่านั้น อย่าพักไว้บน Google Drive/LINE/อีเมล และ **ลบทิ้งหลังย้ายเสร็จ**

---

## เครื่องใหม่ — แตก

```bash
tar xzf crn-move-<วันเวลา>.tar.gz -C ~
cd ~/fitt-coderunner
docker compose up -d          # สร้าง volume ใหม่ (initdb จะรัน 0001-0008)
docker compose stop postgres mongo
```

ยัด snapshot ทับ volume ที่เพิ่งสร้าง — **ขั้นนี้ลบข้อมูลใน volume ปลายทางทิ้ง** จงใจไม่ทำเป็นสคริปต์ ให้พิมพ์เอง

```bash
PG=$(docker inspect crn-postgres --format \
  '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
docker run --rm -v "$PG":/data -v "$PWD":/b alpine \
  sh -c 'rm -rf /data/* /data/..?* && tar xzf /b/pgdata.tgz -C /data'

MG=$(docker inspect crn-mongo --format \
  '{{range .Mounts}}{{if eq .Destination "/data/db"}}{{.Name}}{{end}}{{end}}')
docker run --rm -v "$MG":/data -v "$PWD":/b alpine \
  sh -c 'rm -rf /data/* /data/..?* && tar xzf /b/mongodata.tgz -C /data'

docker compose start postgres mongo
```

แล้วปิดท้าย

```bash
cd frontend && npm install && cd ..
make restart
```

**ไม่ต้อง `make migrate`** — PGDATA ที่ยกมามี schema ครบแล้ว รวมตาราง `schema_migrations` (ถ้าเครื่องเก่ายังไม่ได้ apply migration ตัวไหน เครื่องใหม่ก็ยังไม่มีเหมือนกัน — ตรวจด้วย `make migrate` ได้ มันจะข้ามตัวที่ลงแล้วเอง)

---

## ยกไฟล์ PGDATA ดิบ ๆ ใช้ได้เมื่อไหร่

วิธีนี้เร็วและครบ แต่ผูกกับ 2 อย่าง

| เงื่อนไข | ตอนนี้ |
|---|---|
| Postgres **major version เดียวกัน** | ✅ compose pin `postgres:16-alpine` ตายตัว |
| **สถาปัตยกรรมเดียวกัน** | ✅ Apple Silicon → Apple Silicon (container arm64 ทั้งคู่) |

ถ้าวันหนึ่งข้ามไปเครื่อง Intel หรืออัป Postgres เป็น 17 → **ห้ามใช้วิธีนี้** ให้ใช้ dump แทน ซึ่ง portable กว่าแต่ช้ากว่า

```bash
# เครื่องเก่า
docker compose exec -T postgres pg_dump -U crn -d crn --clean --if-exists > crn.sql
# เครื่องใหม่ (หลัง docker compose up -d)
docker compose exec -T postgres psql -U crn -d crn < crn.sql
make migrate
```

---

## ที่เหลือต้องทำเอง (ไม่มีทางลัด)

| อะไร | ทำไม |
|---|---|
| `claude` login | credential ส่วนตัว |
| `gh auth login` | ต้องมีถ้าใช้โมเดล repo-ต่อ-โปรเจกต์ (`CRN_GITHUB_OWNER`) |
| `docker login <registry>` | ต้องมีถ้า `CRN_IMAGE_REGISTRY` ตั้งไว้ |
| เช็ค `CRN_CLAUDE_BIN` ใน `.env` | path ต่างกันระหว่าง Apple Silicon (`/opt/homebrew/bin/claude`) กับ Intel (`/usr/local/bin/claude`) |
| แตะ `.metadata_never_index` | กัน Spotlight แย่ง lock ตอน rebuild → `ENOTEMPTY` |

## ถ้า CRN ย้าย IP — ฝั่ง FBD ต้องตามแก้

`fitt-builder-v2` เก็บที่อยู่ CRN ไว้ใน `.env.local` แบบตายตัว

```
FITTCORE_GATEWAY_URL=http://<ip>:8080
FITTCORE_DIRECT_CRN_URL=http://localhost:8080
```

ถ้าไม่แก้ FBD จะ **บูตปกติ หน้าจอครบ แต่กด generate แล้วเงียบ** เพราะยิงไปหาเครื่องที่ไม่มีใครฟัง — ไม่มี error ให้เห็น ดูรายละเอียด preset ที่ [deployment-config.md](./deployment-config.md)
