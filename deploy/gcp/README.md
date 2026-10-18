# ส่งแอปขึ้น cloud — UAT บน Cloud Run ขึ้นเองเมื่อส่งออกแบบ cloud

โปรเจกต์ที่ส่งออกจาก FITT Builder แบบ cloud จะขึ้น UAT บน Cloud Run เองหลัง build ผ่าน ใช้ image ตัวเดียวกับที่ส่งให้ลูกค้าแบบ local (digest เดียวกัน) ไม่มีการ build ซ้ำบน GCP · การลงทะเบียนรายลูกค้าเป็นอัตโนมัติ ไม่มีคนรันสคริปต์

```
Code Runner build ผ่านด่านตรวจ (image มี label: project_id, build_no, contract, schedule)
  → push เข้า registry หลัก → คัดลอกไป Artifact Registry (CRN_MIRROR_REGISTRY · digest เท่าเดิม)
      → topic "gcr" → trigger crn-uat-deploy ─┐
  Cloud Scheduler ทุก 5 นาที → topic "crn-sweep" → trigger crn-uat-sweep ─┤
                                                 ▼
                    uat-deploy.sh ถาม FITT Builder: โปรเจกต์นี้ local หรือ cloud
                    cloud → ลงทะเบียนครั้งแรก → deploy ด้วย digest → รอ /api/health → ย้าย traffic
                          → ตั้ง Cloud Scheduler ตาม schedule ของแอป → แจ้ง FITT Builder (ลิงก์ + รหัสผ่านครั้งแรก)
```

| ไฟล์ | หน้าที่ |
|---|---|
| `setup.sh` | ตั้งค่าครั้งแรกของโปรเจกต์ GCP · `DRY_RUN=1` แสดงคำสั่งโดยไม่รัน |
| `uat-deploy.sh` | สิ่งที่ trigger ทั้งสองรัน: อ่าน label ของ image ถาม FITT Builder ลงทะเบียน deploy และแจ้งสถานะ |
| `cloudbuild-uat.yaml` | config ของ trigger · ฝัง `uat-deploy.sh` ทั้งไฟล์ (test บังคับให้ตรงกัน) |
| `promote.sh` | คนในทีมรัน: เอา digest ที่ UAT ใช้อยู่ขึ้น production ของลูกค้ารายนั้น |

## 1. ตั้งค่าครั้งแรก (คนที่ดูแลโปรเจกต์ GCP)

ติดตั้ง gcloud แล้วล็อกอินด้วยบัญชีของตัวเองใน Terminal ของตัวเอง ไม่ใช่ผ่านแชท · ออก token ของ FITT Builder ไว้ก่อนด้วย `node scripts/deploy-binding.mjs create "Code Runner deploy"` ใน repo FITT Builder (สคริปต์จะถามตอนรัน)

```
gcloud auth login
PROJECT_ID=<project> DRY_RUN=1 ./deploy/gcp/setup.sh   # ดูก่อนว่าจะสร้างอะไร
PROJECT_ID=<project> ./deploy/gcp/setup.sh
```

ค่าเริ่มต้น: region `asia-southeast1` (สิงคโปร์) · repository `crn` (เก็บ 5 เวอร์ชันล่าสุดต่อ image) · Cloud SQL `crn-uat` ขนาด `db-custom-1-3840` (มีค่าใช้จ่ายทั้งเดือน ดูเอกสารค่าใช้จ่าย) · เปลี่ยนได้ด้วย `REGION`, `REPO`, `SQL_INSTANCE`, `SQL_TIER`, `FB_URL`

| service account | ใช้ทำอะไร | สิทธิ์ |
|---|---|---|
| `crn-pusher` | เครื่อง build ส่ง image | เขียนได้เฉพาะ repository `crn` |
| `crn-uat-deployer` | trigger ทั้งสอง | deploy Cloud Run · สร้างฐานข้อมูล ผู้ใช้ secret bucket และ Cloud Scheduler job · ให้สิทธิ์บนโปรเจกต์ได้แค่ `roles/cloudsql.client` · จัดการได้เฉพาะบัญชี `crn-uat-*` และ `crn-prod-*` · ใช้ได้เฉพาะใน Cloud Build |
| `crn-uat-<id8>` (สร้างตอนลงทะเบียน) | แอป UAT ของโปรเจกต์นั้นทำงานในนามนี้ | ต่อ Cloud SQL · อ่านได้เฉพาะ secret ของโปรเจกต์ตัวเอง · เขียนได้เฉพาะ bucket ของตัวเอง |

FITT Builder ต้องมี `FITT_DEPLOY_SECRET_KEY` (32 ไบต์สุ่ม base64) ใน env ของ server ไว้เก็บรหัสผ่านผู้ดูแลคนแรกแบบเข้ารหัส

## 2. เครื่อง build (เจ้าของเครื่อง)

`setup.sh` ไม่ออก key ให้ เพราะการออก credential เป็นขั้นที่ต้องตั้งใจทำ:

1. คนที่ดูแลโปรเจกต์ออก key ของ `crn-pusher` แล้วส่งให้เจ้าของเครื่อง build ทางช่องทางลับ (ถ้าองค์กรห้ามออก key ต้องยกเว้นนโยบายนั้นเฉพาะโปรเจกต์นี้)
2. ที่เครื่อง build เอง (ไม่ผ่าน ssh เพราะ keychain): `docker login -u _json_key --password-stdin https://asia-southeast1-docker.pkg.dev < key.json` แล้วลบไฟล์ key
3. ใส่ `CRN_MIRROR_REGISTRY=asia-southeast1-docker.pkg.dev/<project>/crn` ใน `.env` แล้ว restart Code Runner

## 3. ลูกค้าแต่ละราย — ไม่ต้องทำอะไร

กดส่งออกใน FITT Builder แบบ cloud พร้อมอีเมลผู้ดูแลคนแรก · image ถัดไปของโปรเจกต์นั้นที่ไปถึง Artifact Registry จะลงทะเบียนและขึ้น UAT เอง · เปลี่ยนโปรเจกต์ที่ build ไว้แล้วจาก local เป็น cloud ก็ขึ้นภายใน 5 นาทีจาก image ล่าสุด ไม่ต้อง build ใหม่ · ลิงก์ สถานะ และรหัสผ่านครั้งแรก (แสดงครั้งเดียว) อยู่ในหน้าส่งออกของโปรเจกต์

ลงทะเบียนครั้งแรกสร้าง: บัญชี `crn-uat-<id8>` · ฐานข้อมูลและผู้ใช้บน `crn-uat` ที่โอนให้ผู้ใช้ของโปรเจกต์แล้วถอน CONNECT จาก PUBLIC (โปรเจกต์อื่นเข้าไม่ได้ · ทดสอบลำดับนี้กับ PostgreSQL 16 ที่จำลองสิทธิ์แบบ Cloud SQL แล้ว) · secret `uat-<slug>-<id8>-database-url`, `-auth-secret`, `-cron-secret`, `-bootstrap-admin-email`, `-bootstrap-admin-password` · bucket `<project>-uat-<id8>-uploads` ผูกที่ `/data/uploads` · ค่าลับส่งเข้า Secret Manager ทาง stdin (ยกเว้นรหัสผ่านผู้ใช้ฐานข้อมูล ซึ่ง gcloud รับทาง flag เท่านั้น)

ถ้าลงทะเบียนหรือ deploy ล้ม รอบเช็คทุก 5 นาทีจะทำต่อจากขั้นที่ค้าง · ล้ม 3 ครั้งติดใน build เดียวกัน FITT Builder จะหยุดส่งโปรเจกต์นั้นมาและขึ้นว่า "ต้องให้ทีมดู" · revision ใหม่ที่ `/api/health` ไม่ตอบ 200 จะไม่ได้ traffic

## 4. ขึ้น production (คนในทีม หลังลูกค้ารับงาน)

```
PROJECT_ID=<project> ./deploy/gcp/promote.sh <project_id ของ FITT Builder> [--always-on] [--tier db-custom-1-3840]
```

สร้าง Cloud SQL `crn-prod-<id8>` ของลูกค้ารายนั้น บัญชี secret และ bucket ชุดใหม่ แล้วเอา digest ที่ UAT ใช้อยู่ขึ้น `prod-<slug>-<id8>` · ถาม license key ของ FITT Billing (เว้นว่างได้) · รหัสผ่านผู้ดูแลชุดใหม่แสดงครั้งเดียวในหน้าส่งออก · build ครั้งต่อไปไม่แตะ production

## ข้อควรรู้

- **ยังไม่ได้รันกับ GCP จริง** สคริปต์ทดสอบกับ gcloud และ FITT Builder จำลองเท่านั้น (`node --test tests/gcp-*.test.mjs`) รอบแรกให้รัน `DRY_RUN=1` ก่อนแล้วดูผลทีละขั้น
- ต้องยืนยันตอนรันจริง: การผูก Cloud Storage เป็น volume ของ Cloud Run (`--add-volume type=cloud-storage`, gen2) · เงื่อนไข IAM ที่จำกัดสิทธิ์ของ `crn-uat-deployer` (`modifiedGrantsByRole`, `resource.name.extract`) · Cloud Build ต่อ Cloud SQL ผ่าน Auth Proxy (`PROXY_VERSION` ค่าเริ่มต้น `v2.11.0`)
- UAT เปิดให้เข้าได้โดยไม่ต้องล็อกอิน Google (`--allow-unauthenticated`) เพราะแอปมีระบบล็อกอินของตัวเอง ถ้า organization policy ห้าม allUsers คำสั่ง deploy จะไม่ผ่าน
- ช่วงแรกใช้ URL `*.run.app` · การผูกโดเมนของลูกค้าทำทีหลัง
- แอปเริ่มแต่ละครั้งรัน migration และ bootstrap ก่อนรับ request ครั้งแรกหลังหลับจึงช้ากว่าปกติ
- image ที่ build ก่อน CRN มี label จะไม่ขึ้น cloud: ต้อง build ใหม่หนึ่งครั้ง
