# On-prem Run Guide — รัน demo image (สำหรับ FITTCORE)

> CRN build เสร็จ → ได้ **1 app image (self-migrating)** → รันต่อ **Postgres ของคุณเอง** (ส่ง `DATABASE_URL` เข้าไป) → image apply migration เองตอน start → **ไม่มี source, DB อยู่นอก image**

---

## สิ่งที่ได้จาก 1 build (push ขึ้น GitLab registry)

| artifact | คืออะไร |
|---|---|
| `…/crn-demo-<slug>-<id8>:v<n>` | **app image** — Next standalone (compiled, ไม่มี `.tsx`) + prisma CLI + migration ที่ commit ไว้ + seed |
| `docker-compose.customer.yml` | compose แบบ app-only (ต่อ Postgres ภายนอกผ่าน `DATABASE_URL`) — commit ใน repo build |
| `INSTALL.md` | ขั้นตอนติดตั้ง |

- **build เป็น `linux/amd64`** → รันบนเครื่อง x86_64 ได้ (แม้ CRN build บน Mac arm64)
- รันเป็น user `node` ฟัง port `3000` ใน container
- **ไม่มี** migrate image แยก และ **ไม่มี** bundled postgres — DB คุณจัดหาเอง (1 database/แอป)

> ⚠️ **image รุ่นเก่า:** image ที่ build ก่อนมีระบบ bootstrap (รวม 8 แอปที่ใช้ล็อกอินแบบเก่า) ยังอ่าน `DEV_EMAIL`/`DEV_PASSWORD` และมีรหัสเริ่มต้นฝังอยู่ **ห้ามนำไปติดตั้งให้ลูกค้า** ต้อง build ใหม่ก่อน

---

## ตัวแปรที่ต้องส่ง

| ตัวแปร | บังคับ | หมายเหตุ |
|---|---|---|
| `DATABASE_URL` | ✅ | `postgresql://USER:PASSWORD@HOST:5432/DB?schema=public` |
| `AUTH_SECRET` | ✅ | สุ่มใหม่ต่อ deployment และ **ใช้ค่าเดิมตลอด** (restart/อัปเกรด) ไม่งั้น session เดิมใช้ไม่ได้ |
| `BOOTSTRAP_ADMIN_EMAIL` | ✅ ตอนติดตั้งครั้งแรก | อีเมลรูปแบบปกติ (ระบบตัดช่องว่างและแปลงเป็นตัวพิมพ์เล็ก) |
| `BOOTSTRAP_ADMIN_PASSWORD` | ✅ ตอนติดตั้งครั้งแรก | อย่างน้อย 16 ตัวอักษร ห้ามใช้ซ้ำข้ามลูกค้า |
| `FITT_FEEDBACK_URL` | – | ปลายทาง feedback widget (เฉพาะ build ที่ฝัง widget) |
| `DEV_EMAIL` / `DEV_PASSWORD` | – | **เลิกใช้แล้ว** image ปัจจุบันไม่อ่าน |

**admin คนแรก (bootstrap):**
- ติดตั้งครั้งแรกโดยไม่ส่ง `BOOTSTRAP_ADMIN_*` (หรืออีเมลผิดรูปแบบ / รหัสสั้นกว่า 16 ตัวอักษร) → `db:seed` ล้ม และ **container ไม่ start** (ถ้าตั้ง restart ไว้จะวน restart) — ไม่ใช่ start ขึ้นมาแบบไม่มี admin
- สร้าง **ครั้งเดียวต่อฐานข้อมูล** (advisory lock + marker ในตาราง `SystemBootstrap`) — รันพร้อมกันหลายตัวได้ admin คนเดียว · ฐานที่มี user อยู่แล้วจะไม่สร้างเพิ่ม · ลบ user แล้ว restart ไม่สร้างคืน
- หลังติดตั้งเสร็จ ระบบไม่อ่าน `BOOTSTRAP_ADMIN_*` อีก ถอดออกจาก deployment ได้ เปลี่ยนค่าภายหลังก็ไม่มีผล
- ระบบตั้งค่าว่าควรเปลี่ยนรหัสครั้งแรก **แต่ยังไม่บังคับ** — ให้เปลี่ยนเองทันทีหลังล็อกอินครั้งแรก

---

## วิธีรัน

**1. login registry** (ถ้าต้อง — deploy token):
```bash
docker login 172.168.1.234:5050 -u <token-user> -p <token>
```
> registry ตอนนี้ยังเป็น HTTP → docker client ต้องตั้ง `"insecure-registries":["172.168.1.234:5050"]` (daemon.json / Docker Desktop) · TLS อยู่ระหว่างทำ (DIG-2347) ทีมที่ไม่ใช้ insecure registry ต้องรอข้อนี้

**2. รัน app ต่อ DB ของคุณ** (docker run):
```bash
docker run -d -p 127.0.0.1:4123:3000 \
  -e DATABASE_URL="postgresql://user:pass@db-host:5432/demo_<slug>_<id8>?schema=public" \
  -e AUTH_SECRET="<สุ่มยาว ๆ ต่อ deployment>" \
  -e BOOTSTRAP_ADMIN_EMAIL="admin@customer.example" \
  -e BOOTSTRAP_ADMIN_PASSWORD="<รหัสไม่ซ้ำ อย่างน้อย 16 ตัวอักษร>" \
  <registry>/crn-demo-<slug>-<id8>:v<n>
```

**หรือ compose:** ใส่ตัวแปรข้างบนในไฟล์ `.env` ข้าง `docker-compose.customer.yml` แล้ว
```bash
docker compose -f docker-compose.customer.yml up -d
```

ลำดับที่เกิดตอน start (entrypoint `set -eu` — ขั้นไหนล้ม container หยุดทันที):
1. `npm run db:deploy` → `prisma migrate deploy` apply migration ที่ commit ไว้ไปที่ `DATABASE_URL`
2. `npm run db:seed` → สร้าง admin คนแรกครั้งเดียว (ดูหัวข้อ bootstrap)
3. `node server.js` → แอปขึ้น

> ระหว่างข้อ 1–2 ยังไม่มีอะไรฟัง port `3000` — health check ที่ต่อไม่ได้ช่วงนี้แปลว่า "กำลังเริ่ม" ไม่ใช่ "พัง"

**เช็คว่าใช้ได้:** ดู `docker logs <container>` ว่าไม่มี error แล้วเปิดแอปและล็อกอินด้วย admin คนแรก

> แอปยังไม่มี health endpoint ที่กำหนดเป็นมาตรฐาน (บาง build มี `/api/health` แต่ยังไม่ได้บังคับทุก build)

---

## อัปเดตเป็นเวอร์ชันใหม่

สั่ง run/`up -d` image tag `:v<n>` ใหม่ โดย **ใช้ `DATABASE_URL` และ `AUTH_SECRET` เดิม** — `db:deploy` apply เฉพาะ migration ใหม่ที่ยังไม่เคยรัน

> migration ล้ม → **container ไม่ start** (ไม่ข้ามไปรันแอปต่อ) → ต้องแก้ก่อน
>
> **แนะนำ:** `pg_dump` ก่อนอัปเกรดทุกครั้ง เพื่อย้อนกลับได้

---

## สิ่งที่ยังไม่ได้ทดสอบ

- รันแบบ `--read-only` (จากการอ่านโค้ด แอปน่าจะเขียน `/tmp`, `/home/node` และ `/app/.next/cache`)
- รันแบบ `--cap-drop ALL` / `no-new-privileges` (จากโค้ดไม่มีอะไรต้องใช้ capability)
- ฐานข้อมูลที่ user ไม่ใช่ superuser (ขั้นตรวจตอน build ใช้ superuser)

---

## หลักการ (ทุกแอป)
- **DB อยู่นอก image** — FTC DV จัดหา Postgres + database + `DATABASE_URL`
- **image เป็นคน migrate ตัวเองตอน start** (`prisma migrate deploy` จาก migration ที่ commit ไว้)
- ข้อมูลที่ต้องหวงคือ data ที่คนกรอกตอน UAT → `pg_dump` snapshot ป้องกัน

## ต่อกับ build status (real-time)
รู้ว่า build เสร็จ/กำลังทำ + `image_ref` + `env` จาก `build_events` (DB) หรือ HTTP callback — ดู [crn-integration-contract.md](crn-integration-contract.md) §2/§3
