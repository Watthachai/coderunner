# On-prem Run Guide — รัน demo image (สำหรับ FITTCORE)

> CRN build เสร็จ → ได้ **1 app image (self-migrating)** → รันต่อ **Postgres ของคุณเอง** (ส่ง `DATABASE_URL` เข้าไป) → image สร้าง schema เองตอน start → **ไม่มี source, DB อยู่นอก image**

---

## สิ่งที่ได้จาก 1 build (push ขึ้น GitLab registry)

| artifact | คืออะไร |
|---|---|
| `…/crn-demo-<slug>-<id8>:v<n>` | **app image** — Next standalone (compiled, ไม่มี `.tsx`) + **self-migrate** (prisma CLI + schema + seed baked) |
| `docker-compose.customer.yml` | compose แบบ app-only (ต่อ Postgres ภายนอกผ่าน `DATABASE_URL`) — commit ใน repo build |
| `INSTALL.md` | ขั้นตอนติดตั้ง |

- **build เป็น `linux/amd64`** → รันบนเครื่อง x86_64 ได้ (แม้ CRN build บน Mac arm64)
- **self-migrate-on-start:** ตอน container start entrypoint รัน `prisma db push` (data-safe) ยิงไป `DATABASE_URL` แล้วค่อย `node server.js`. seed เฉพาะเมื่อ `DEMO_SEED=1`
- **ไม่มี** migrate image แยก และ **ไม่มี** bundled postgres — DB คุณจัดหาเอง (central `ftc-demo-db` + 1 database/แอป)

---

## วิธีรัน

**1. login registry** (ถ้าต้อง — deploy token):
```bash
docker login 172.168.1.234:5050 -u <token-user> -p <token>
```
> registry เป็น HTTP → docker client ตั้ง `"insecure-registries":["172.168.1.234:5050"]` (daemon.json / Docker Desktop) ด้วย

**2. รัน app ต่อ DB ของคุณ** (docker run):
```bash
docker run -d -P \
  -e DATABASE_URL="postgresql://user:pass@ftc-demo-db:5432/demo_<slug>_<id8>" \
  <registry>/crn-demo-<slug>-<id8>:v<n>
```
- `-P` publish port → หา URL ด้วย `docker port <container> 3000`
- อยาก seed data ตัวอย่าง: เพิ่ม `-e DEMO_SEED=1`

**หรือ compose:**
```bash
DATABASE_URL="postgresql://user:pass@ftc-demo-db:5432/demo_<slug>_<id8>" \
  docker compose -f docker-compose.customer.yml up -d
```

ลำดับที่เกิดตอน start:
1. `prisma db push` → สร้าง/sync schema ใน DB ที่ส่งให้ (DB เปล่า → สร้าง table; DB มี data → additive apply)
2. (ถ้า `DEMO_SEED=1`) `prisma db seed`
3. `node server.js` → demo ขึ้น

---

## อัปเดตเป็นเวอร์ชันใหม่ (v2)
สั่ง run/`up -d` image tag `:v<n>` ใหม่ **ชี้ `DATABASE_URL` เดิม** — schema sync ให้เอง

> **additive** (เพิ่ม table/column): `db push` apply delta ให้เอง **data เดิมอยู่ครบ** ✅
>
> ⚠️ **destructive** (ลบ/rename column, narrow type, เพิ่ม NOT NULL บน table ที่มี row): `db push` **error → container ไม่ start** (ไม่ได้ส่ง `--accept-data-loss`) เพื่อกันข้อมูลหาย ไม่ drop เงียบ ๆ → operator ต้อง migrate มือ / ตัดสินใจ reset
>
> **แนะนำฝั่ง FTC DV:** `pg_dump` snapshot ก่อน (re)start ทุกครั้ง → ต่อให้ reset ก็มี dump กู้ได้ (image ไม่ต้องรู้เรื่องนี้ — แยก concern)

---

## หลักการ (ทุกแอป)
- **DB อยู่นอก image** — FTC DV จัดหา Postgres + database + `DATABASE_URL`
- **image เป็นคน migrate ตัวเองตอน start** (`db push`, data-safe)
- seed data ไม่ใช่ของมีค่า (regenerate ได้) — ที่ต้องหวงคือ data ที่คนกรอกตอน UAT → `pg_dump` snapshot ป้องกัน

## ต่อกับ build status (real-time)
รู้ว่า build เสร็จ/กำลังทำ + `image_ref` + `env` (DATABASE_URL/PORT) จาก `build_events` (DB) หรือ HTTP callback — ดู [crn-integration-contract.md](crn-integration-contract.md) §2/§3
