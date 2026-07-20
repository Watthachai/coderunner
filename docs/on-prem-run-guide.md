# On-prem Run Guide — รัน demo image บนเครื่องลูกค้า (สำหรับ FITTCORE)

> CRN build เสร็จ → ได้ **2 image + compose** ที่ **self-contained** เอาไปรันเครื่องไหนก็ได้ (amd64) → schema+seed ตั้งเอง → **ไม่มี source, data อยู่ในเครื่องลูกค้า**

---

## สิ่งที่ได้จาก 1 build (push ขึ้น GitLab registry)

| artifact | คืออะไร |
|---|---|
| `…/crn-demo-<slug>-<id8>:v<n>` | **app image** (Next standalone, compiled — ไม่มี `.tsx`) |
| `…/crn-demo-<slug>-<id8>-migrate:v<n>` | **migrate image** (prisma CLI + schema + init migration + seed — ไม่มี app source) |
| `docker-compose.customer.yml` | compose ที่ต่อ postgres + migrate + app (commit ใน repo build) |
| `INSTALL.md` | ขั้นตอนติดตั้ง |

- **build เป็น `linux/amd64`** → รันบนเครื่อง x86_64 (Linux/customer) ได้ (แม้ CRN จะ build บน Mac arm64)
- **migrate-on-start:** service `migrate` รัน `prisma migrate deploy` + `prisma db seed` ครั้งเดียวก่อน app start

---

## วิธีรัน (เครื่องลูกค้า / เครื่องเพื่อน)

**1. login registry** (ถ้าต้อง — deploy token):
```bash
docker login 172.168.1.234:5050 -u <token-user> -p <token>
```
> registry เป็น HTTP → docker client ตั้ง `"insecure-registries":["172.168.1.234:5050"]` (daemon.json / Docker Desktop) ด้วย

**2. รันทั้ง stack:**
```bash
docker compose -f docker-compose.customer.yml up -d
```
ลำดับที่เกิด:
1. `db` — Postgres start (data ใน volume `dbdata`)
2. `migrate` — `migrate deploy` (สร้าง schema) + `seed` → exit
3. `app` — start หลัง migrate สำเร็จ (`service_completed_successfully`)

**3. เปิด:** `http://localhost:<port>` (port อยู่ใน compose, ต่อ project)

---

## อัปเดตเป็นเวอร์ชันใหม่ (v2)
เปลี่ยน tag `:v<n>` ใน `docker-compose.customer.yml` แล้ว `up -d` ใหม่ — **data ใน `dbdata` อยู่ครบ**

> ⚠️ **caveat:** CRN regenerate schema ใหม่ทุก build (ไม่ใช่ incremental) → ถ้า **schema เปลี่ยน + ลูกค้ามี data แล้ว**, `migrate deploy` ของ v2 (init migration คนละ checksum) อาจ **fail checksum mismatch**. เคสนี้ต้อง migration แบบ incremental จริง (delta tracking) = งาน future. **First deploy (DB เปล่า) ทำงานปกติ**

---

## ⚠️ ห้าม
- `docker compose down -v` — `-v` ลบ volume = **ล้าง data ลูกค้าทิ้ง**

## ต่อกับ build status (real-time)
รู้ว่า build เสร็จ/กำลังทำ + image_ref จาก `build_events` (DB) — ดู [fittcore-consumer-guide.md](fittcore-consumer-guide.md)
