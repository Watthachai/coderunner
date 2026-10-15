# UAT บน GCP — ขึ้น Cloud Run เองหลัง build ผ่าน

แอปที่ Code Runner build ผ่านด่านตรวจแล้วจะขึ้น UAT บน Cloud Run เอง โดยใช้ image ตัวเดียวกับที่ส่งให้ลูกค้า (digest เดียวกัน) ไม่มีการ build ซ้ำบน GCP

```
Code Runner build ผ่านด่านตรวจ
  → push เข้า registry หลัก
  → คัดลอกไป Artifact Registry (CRN_MIRROR_REGISTRY · digest เท่าเดิม)
      → Artifact Registry แจ้ง Pub/Sub topic "gcr"
          → Cloud Build trigger "crn-uat-deploy" (cloudbuild-uat.yaml · deploy อย่างเดียว)
              → gcloud run deploy uat-<slug>-<id8> --image …@sha256:<digest>
```

Production ใช้ digest เดียวกับที่ผ่าน UAT เสมอ ไม่ build ใหม่ (ยังไม่ได้ทำส่วนนี้ ตัดสินในนัดคุย GCP)

| ไฟล์ | หน้าที่ |
|---|---|
| `setup.sh` | ตั้งค่าครั้งแรกของโปรเจกต์ GCP · `DRY_RUN=1` แสดงคำสั่งโดยไม่รัน |
| `new-uat.sh` | ลงทะเบียนโปรเจกต์ลูกค้า 1 ราย: ฐานข้อมูล ผู้ใช้ และ secret |
| `uat-deploy.sh` | สิ่งที่ trigger รัน: deploy digest ที่ได้จาก event ถ้าโปรเจกต์ลงทะเบียนแล้ว |
| `cloudbuild-uat.yaml` | config ของ trigger · ฝัง `uat-deploy.sh` ทั้งไฟล์ (test บังคับให้ตรงกัน) |

## 1. ตั้งค่าครั้งแรก (คนที่ดูแลโปรเจกต์ GCP)

ติดตั้ง gcloud แล้วล็อกอินด้วยบัญชีของตัวเองใน Terminal ของตัวเอง ไม่ใช่ผ่านแชท

```
gcloud auth login
PROJECT_ID=<project> DRY_RUN=1 ./deploy/gcp/setup.sh   # ดูก่อนว่าจะสร้างอะไร
PROJECT_ID=<project> ./deploy/gcp/setup.sh
```

ค่าเริ่มต้น: region `asia-southeast3` (กรุงเทพ) · repository `crn` · Cloud SQL `crn-uat` ขนาด `db-custom-1-3840` (มีค่าใช้จ่ายทั้งเดือน ดูเอกสารค่าใช้จ่าย) · เปลี่ยนได้ด้วย `REGION`, `REPO`, `SQL_INSTANCE`, `SQL_TIER`

service account ที่สร้าง มีสิทธิ์เท่าที่ต้องใช้:

| service account | ใช้ทำอะไร | สิทธิ์ |
|---|---|---|
| `crn-pusher` | เครื่อง build ส่ง image | เขียนได้เฉพาะ repository `crn` |
| `crn-uat-deployer` | trigger deploy UAT | deploy Cloud Run, ดูชื่อ secret, เขียน log, อ่าน image, ทำงานในนาม `crn-uat-runtime` |
| `crn-uat-runtime` | แอป UAT ทำงานในนามนี้ | ต่อ Cloud SQL · อ่าน secret ของโปรเจกต์ตัวเอง (ให้ทีละ secret ตอนลงทะเบียน) |

## 2. เครื่อง build (เจ้าของเครื่อง)

`setup.sh` ไม่ออก key ให้ เพราะการออก credential เป็นขั้นที่ต้องตั้งใจทำ:

1. คนที่ดูแลโปรเจกต์ออก key ของ `crn-pusher` แล้วส่งให้เจ้าของเครื่อง build ทางช่องทางลับ (ถ้าองค์กรห้ามออก key ให้คุยทางเลือกในนัด)
2. ที่เครื่อง build เอง (ไม่ผ่าน ssh เพราะ keychain): `docker login -u _json_key --password-stdin https://asia-southeast3-docker.pkg.dev < key.json` แล้วลบไฟล์ key
3. ใส่ `CRN_MIRROR_REGISTRY=asia-southeast3-docker.pkg.dev/<project>/crn` ใน `.env` แล้ว restart Code Runner

## 3. ลงทะเบียนโปรเจกต์ลูกค้า

```
PROJECT_ID=<project> BOOTSTRAP_ADMIN_EMAIL=<ผู้ดูแลคนแรก> ./deploy/gcp/new-uat.sh crn-demo-<slug>-<id8>
```

สร้างฐานข้อมูลและผู้ใช้บน `crn-uat` และ secret `uat-<slug>-<id8>-database-url`, `-auth-secret`, `-bootstrap-admin-email`, `-bootstrap-admin-password` (และ `-license-key` ถ้าพิมพ์ใส่ตอนถาม) · ค่าลับส่งเข้า Secret Manager ทาง stdin ไม่อยู่ใน argument · รหัสผ่านผู้ดูแลคนแรกแสดงครั้งเดียวใน Terminal ของคนที่รัน ส่งต่อทางช่องทางลับ

หลังลงทะเบียน image ถัดไปของโปรเจกต์นี้ที่ไปถึง Artifact Registry จะขึ้น UAT เอง · โปรเจกต์ที่ไม่ได้ลงทะเบียน trigger จะข้าม ไม่ error

## ข้อควรรู้

- **ยังไม่ได้รันกับ GCP จริง** สคริปต์ทดสอบกับ gcloud จำลองเท่านั้น (`node --test tests/gcp-uat-deploy.test.mjs`) รอบแรกให้รัน `DRY_RUN=1` ก่อนแล้วดูผลทีละขั้น
- UAT เปิดให้เข้าได้โดยไม่ต้องล็อกอิน Google (`--allow-unauthenticated`) เพราะแอปมีระบบล็อกอินของตัวเอง ถ้า organization policy ห้าม allUsers คำสั่ง deploy จะไม่ผ่าน
- region กรุงเทพผูกโดเมนของลูกค้ากับ Cloud Run ตรงๆ ไม่ได้ ต้องใช้ load balancer (ดูเอกสารค่าใช้จ่าย) · ใช้ URL `*.run.app` สำหรับ UAT ได้เลย
- แอปเริ่มแต่ละครั้งรัน migration และ bootstrap ก่อนรับ request ครั้งแรกหลังหลับจึงช้ากว่าปกติ
- สวิตช์ระงับของ FITT Billing ใช้ได้บน Cloud Run เลย: trigger ตั้ง `FITT_STATUS_URL` ให้ และใส่ `FITT_LICENSE_KEY` ถ้าโปรเจกต์มี secret `-license-key`
