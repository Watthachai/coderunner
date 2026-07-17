# How-To — On-prem Demo Image (CRN → GitLab → ลูกค้า)

> คู่มือทำจริงทีละ step — แต่ละหัวข้อ copy ไปแปะ **description ใน Jira** ได้เลย
> คู่กับ task list: `docs/on-prem-demo-image-tasks.md` · design: `docs/superpowers/specs/2026-07-16-on-prem-demo-image-design.md`

**ภาพรวม:** CRN build demo → `docker build` เป็น image opaque (ไม่มี source) → `docker push` ขึ้น GitLab registry (self-host) → ลูกค้า pull/load ไปรันวงแลนตัวเอง (data ไม่หลุด)

---

## HOWTO-A · ทดสอบ image build ใน local (ยังไม่ต้องมี GitLab)

**เป้าหมาย:** ยืนยันว่า CRN build image ออกได้ ก่อนต่อ registry

1. แก้ CRN `.env` บนเครื่องที่รัน CRN:
   ```bash
   CRN_BUILD_IMAGE=true
   CRN_IMAGE_REGISTRY=        # เว้นว่าง = build local เฉยๆ ไม่ push
   ```
2. เช็คว่ามี Docker daemon: `docker version` (ต้องได้ทั้ง Client + Server)
3. restart CRN: `make restart`
4. ยิง build demo (จาก studio) ให้ **สำเร็จถึง done**
5. ตรวจผล:
   ```bash
   docker images | grep crn-demo          # ควรเห็น crn-demo-<slug>:v<n>
   docker run --rm -p 3000:3000 crn-demo-<slug>:v<n>   # เปิด http://localhost:3000
   ```

**Done เมื่อ:** เห็น image `crn-demo-*` + `docker run` เปิด demo ได้
**หมายเหตุ:** `docker build` ใช้เวลา ~2-4 นาที (npm ci + next build); build ต้องเป็น Next app และสำเร็จก่อน

---

## HOWTO-B · เปิด GitLab Container Registry (INFRA-1)

**เป้าหมาย:** ให้ GitLab เก็บ docker image ได้ (บนเครื่องเรา ไม่ไป Docker Hub)
**ทำบน:** GitLab VM (172.168.1.234) เป็น root

1. แก้ `/etc/gitlab/gitlab.rb` — เพิ่ม registry (LAN ใช้ port 5050):
   ```ruby
   registry_external_url 'https://172.168.1.234:5050'
   ```
   > ถ้า GitLab เป็น HTTP/ไม่มี cert จริง ใช้ `http://172.168.1.234:5050` แทน (ต้องตั้ง insecure-registries ฝั่ง client ใน HOWTO-C)
2. apply:
   ```bash
   gitlab-ctl reconfigure
   gitlab-ctl restart registry
   ```
3. เปิดเว็บ GitLab → สร้าง **Group** (เช่น `fitt`) และ **Project** (เช่น `demos`) ไว้เก็บ image
4. เช็ค: เข้า Project → เมนู **Deploy → Container Registry** ต้องโผล่

**Done เมื่อ:** เมนู Container Registry ใช้งานได้ใน project

---

## HOWTO-C · CRN login + ชี้ registry (INFRA-2)

**เป้าหมาย:** ให้เครื่อง CRN push image ขึ้น GitLab ได้
**ทำบน:** เครื่อง CRN box

1. สร้าง token ใน GitLab: Project → **Settings → Repository → Deploy tokens** → New token
   - scopes: **`read_registry` + `write_registry`**
   - จด `username` + `token` ที่ได้
2. (ถ้า registry เป็น HTTP/self-signed) ตั้ง insecure-registries — แก้ `/etc/docker/daemon.json`:
   ```json
   { "insecure-registries": ["172.168.1.234:5050"] }
   ```
   แล้ว `sudo systemctl restart docker` (Linux) / restart Docker Desktop (mac)
3. login:
   ```bash
   docker login 172.168.1.234:5050 -u <deploy-token-username> -p <deploy-token>
   ```
4. ตั้ง CRN `.env`:
   ```bash
   CRN_BUILD_IMAGE=true
   CRN_IMAGE_REGISTRY=172.168.1.234:5050/fitt/demos
   ```
5. `make restart` → ยิง build → เช็คว่า push ขึ้น:
   ```bash
   # ในหน้า GitLab: Project → Deploy → Container Registry ต้องเห็น crn-demo-<slug>:v<n>
   ```

**Done เมื่อ:** image โผล่ใน GitLab Container Registry หลัง build

---

## HOWTO-D · ลูกค้าติดตั้ง demo (ส่งมอบ)

**เป้าหมาย:** ลูกค้ารัน demo ในวงแลนตัวเอง (ไม่เห็น source, data local)
**ทำบน:** เครื่องลูกค้า

**กรณีลูกค้าต่อ registry เราได้ (VPN/LAN เชื่อม):**
1. login registry: `docker login 172.168.1.234:5050 -u <read-token-user> -p <read-token>`
2. `docker compose -f docker-compose.customer.yml up -d`
3. เปิด `http://<customer-host>:3000`

**กรณีลูกค้า air-gap (ต่อ registry เราไม่ได้):**
1. ฝั่งเรา: `docker save 172.168.1.234:5050/fitt/demos/crn-demo-<slug>:v<n> | gzip > demo.tar.gz`
2. ส่งไฟล์ `demo.tar.gz` + `docker-compose.customer.yml` + `INSTALL.md` ให้ลูกค้า
3. ลูกค้า: `docker load < demo.tar.gz` → `docker compose up -d`

**Done เมื่อ:** demo เปิดได้บนเครื่องลูกค้า, `docker exec ... ls` ไม่เห็นไฟล์ `.tsx`, data อยู่ postgres volume ในเครื่องเขา

---

## HOWTO-E · ย้าย git remote → GitLab (CRN-9)

**เป้าหมาย:** source อยู่บน GitLab เรา (เลิกใช้ GitHub)
1. สร้าง project สำหรับ source ใน GitLab
2. CRN `.env`: `CRN_GIT_REMOTE=https://172.168.1.234/fitt/<repo>.git`
3. ตั้ง git credential ให้ CRN (deploy token scope `write_repository` หรือ PAT)
4. `make restart` → ยิง build → เช็คว่า commit ขึ้น GitLab

**Done เมื่อ:** build push source ขึ้น GitLab, ไม่มีอะไรไป GitHub

---

## Troubleshoot ที่เจอบ่อย
- `docker build` ล้ม → build ยังไม่ถึง done / ไม่ใช่ Next app / `docker version` ไม่มี Server (daemon ไม่รัน)
- `docker push` ล้ม `unauthorized` → ยังไม่ `docker login` / token หมด scope
- `push` ล้ม `http: server gave HTTP response to HTTPS client` → registry เป็น HTTP แต่ไม่ได้ตั้ง `insecure-registries` (HOWTO-C ข้อ 2)
- ลูกค้า pull ล้ม → ต่อ registry เราไม่ได้ → ใช้ทาง tarball (HOWTO-D air-gap)
