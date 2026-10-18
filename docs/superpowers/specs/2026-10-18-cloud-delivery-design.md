# ส่งแอปแบบ local หรือ cloud จากปุ่มส่งออกของ FITT Builder — design

วันที่: 2026-10-18 · สถานะ: รอผู้ใช้ review · ต่อจาก branch `feature/gcp-mirror` (`603ee39`: ส่ง image ไป Artifact Registry ด้วย digest เดิม และขึ้น UAT บน Cloud Run เมื่อมีคนลงทะเบียนโปรเจกต์ด้วยมือ)

## ปัญหา

ตอนนี้แอปที่ CRN build ออกไปได้ทางเดียว คือไฟล์และ image ให้ลูกค้าติดตั้งเอง โค้ดฝั่ง GCP ใน `feature/gcp-mirror` ขึ้น UAT ได้ก็จริง แต่ทุกโปรเจกต์ต้องมีคนรัน `new-uat.sh` และรัน SQL 2 บรรทัดใน Cloud SQL Studio เอง และ FITT Builder ไม่รู้เลยว่าโปรเจกต์ไหนจะขึ้น cloud

## เป้าหมาย / ไม่ใช่เป้าหมาย

- **เป้าหมาย:** ตอนกดส่งออกใน FITT Builder เลือกได้ว่า local หรือ cloud · ถ้าเลือก cloud โค้ดของ CRN ลงทะเบียนโปรเจกต์ ขึ้น UAT บน Cloud Run และแจ้งลิงก์กับรหัสผ่านผู้ดูแลคนแรกกลับที่หน้าโปรเจกต์เอง ไม่มีคนรันสคริปต์รายลูกค้า · image เป็นตัวเดียวกันทั้งสองแบบ เปลี่ยนจาก local เป็น cloud ทีหลังได้โดยไม่ต้อง build ใหม่ · ทีมเราสั่งขึ้น production ด้วยคำสั่งเดียวหลังลูกค้ารับงาน
- **ไม่ใช่เป้าหมาย (รอบนี้):** build บน GCP (ยัง build บน MacMini) · ผูกโดเมนของลูกค้า (ใช้ `*.run.app` ไปก่อน) · คิดค่า hosting ใน FITT Billing · ลบทรัพยากรบน GCP อัตโนมัติ · แก้ Gateway ของ FITTCORE

## การตัดสินที่มีแล้ว (ผู้ใช้ 2026-10-18)

| เรื่อง | ตัดสิน |
|---|---|
| region | สิงคโปร์ `asia-southeast1` (ค่าเริ่มต้นในสคริปต์ตอนนี้ยังเป็น `asia-southeast3` ต้องเปลี่ยน) |
| โปรเจกต์ GCP | สร้างใหม่ ผู้ใช้เป็นเจ้าของ ผูก billing account ที่บริษัทมี |
| ฐานข้อมูล | UAT ใช้ Cloud SQL `crn-uat` ร่วมกัน แต่ละโปรเจกต์มีฐานและผู้ใช้ของตัวเองที่โปรเจกต์อื่นเข้าไม่ได้ · production แยก Cloud SQL ต่อลูกค้า |
| เปิด/หลับ | UAT หลับได้ (min instances 0) · production เลือกตามใบเสนอราคา |
| คนดูแล | ทีมเราทำทั้งหมดก่อน |
| คัดลอก image ไม่สำเร็จ | build ไม่ล้ม ขึ้นคำเตือน (โค้ดทำแบบนี้อยู่แล้ว) |
| ขึ้น production | ทีมเรารันคำสั่งหลังลูกค้ารับงาน |
| ตัวเลือก local/cloud | ไม่มีผลกับการ build · image เดียวกัน · ไม่มี skill แยกสำหรับ cloud |
| ลิงก์และรหัสผ่านผู้ดูแล | แสดงที่หน้าโปรเจกต์ใน FITT Builder · รหัสผ่านแสดงครั้งเดียว |
| แบบ | ตัวเลือกเก็บใน FITT Builder · ส่วน deploy ของ CRN รันใน GCP และถาม FITT Builder เอง (ไม่ส่งตัวเลือกผ่าน Gateway) |

## Design

### 1. ภาพรวม และใครถือสิทธิ์อะไร

```
FITT Builder ──ส่งออก──► Gateway (.167) ──► CRN บน MacMini ──build──► image (+label)
   ▲   │ บันทึก local/cloud ที่โปรเจกต์                        │ push ทุก image
   │   │                                                      ▼
   │   └──◄── GET ตัวเลือก / อีเมลผู้ดูแล ───────  ส่วน deploy ใน GCP (Cloud Build)
   └──────◄── POST สถานะ ลิงก์ รหัสผ่านครั้งแรก ──┘   ▲ image ใหม่ (Pub/Sub "gcr")
                                                       ▲ รอบเช็คทุก 5 นาที (Cloud Scheduler)
```

| ผู้ถือ | ถืออะไร | ทำอะไรได้ |
|---|---|---|
| MacMini | key ของ `crn-pusher` ใน keychain ของ docker | เขียน image เข้า repository `crn` อย่างเดียว |
| FITT Builder | ไม่มี credential ของ GCP | ตอบคำถามและรับสถานะผ่าน API ด้านล่าง |
| ส่วน deploy (`crn-uat-deployer`) | token ของ binding `CODE_RUNNER` ใน Secret Manager · สิทธิ์ใน GCP | สร้างบัญชีแอป ฐานข้อมูล รหัสลับ bucket และ Cloud Scheduler job · deploy Cloud Run · ใช้ได้เฉพาะใน Cloud Build |
| คนในทีม (production) | บัญชี GCP ของตัวเองใน Terminal ของตัวเอง | รันคำสั่งขึ้น production |

key ของ `crn-pusher` ไม่ได้เพิ่มความเสี่ยงจากเดิม แม้ Claude ตอน build จะใช้ docker ได้ เพราะ Claude เป็นคนเขียนแอปที่จะขึ้น UAT อยู่แล้ว

### 2. FITT Builder

**ข้อมูล** (migration ใหม่ ขอเลขจาก session ของ fitt-builder ก่อนใช้ · RLS เปิด ใช้ได้เฉพาะ service role)

- `fittbuilder_project_delivery`: `project_id` (PK), `target` (`local` | `cloud`), `admin_email`, `updated_by`, `updated_at`
- `fittbuilder_project_deploy_events`: `id`, `project_id`, `env` (`uat` | `production`), `stage` (`waiting_image` | `registering` | `deploying` | `ready` | `failed`), `build_no`, `digest`, `url`, `message`, `created_at`
- `fittbuilder_project_initial_passwords`: `project_id`, `env`, `ciphertext`, `created_at` · เข้ารหัส AES-256-GCM ด้วยกุญแจใน env ของ FITT Builder (ยังไม่มีตัวช่วยเข้ารหัสใน repo ต้องเขียนใหม่) · แถวถูกลบเมื่อแสดงแล้ว

**หน้าส่งออก** (`components/studio/FittcoreExportModal.tsx`): ตัวเลือก local (ค่าเริ่มต้น) / cloud · เลือก cloud ต้องกรอกอีเมลผู้ดูแลคนแรก · บันทึกลง `fittbuilder_project_delivery` ก่อนส่งงานไป Gateway · ส่งออกครั้งถัดไปตั้งค่าตามที่บันทึกไว้ · ข้อมูลที่ส่งไป Gateway ไม่เปลี่ยน

**กล่อง Cloud ที่หน้าโปรเจกต์:** แสดง event ล่าสุดของแต่ละ env (สถานะ, ลิงก์, build no, เวลา, ข้อความเมื่อล้มเหลว) · เปลี่ยน local↔cloud ได้ (cloud ต้องมีอีเมลผู้ดูแล) · cloud→local หยุดการอัปเดตเท่านั้น ไม่ลบอะไร · ปุ่ม "แสดงรหัสผ่านครั้งแรก" สำหรับคนที่แก้โปรเจกต์ได้ กดแล้วคืนรหัสผ่านครั้งเดียวและลบแถวทิ้ง กดซ้ำได้คำตอบว่าแสดงไปแล้ว

**API สำหรับส่วน deploy** (`app/v1/integrations/deploy/...`) ใช้แบบเดียวกับ FITT Billing: Bearer token จาก `fittbuilder_integration_bindings` producer ใหม่ `CODE_RUNNER` ขอบเขตทั้งระบบ (org_id ว่าง), error envelope `fittbuilder.error.v1`, rate limit, ออกและยกเลิก token ด้วยสคริปต์ที่ผู้ใช้รันเอง

| method + path | ทำอะไร |
|---|---|
| `GET /v1/integrations/deploy/projects/{id}` | `{project_id, name, target, admin_email}` · ไม่มีแถว = `target: "local"` |
| `GET /v1/integrations/deploy/pending` | โปรเจกต์ที่ `target = cloud` ซึ่ง UAT ยังไม่เคย `ready` หรือ event ล่าสุดเป็น `failed` และล้มด้วย `build_no` เดิมไม่ถึง 3 ครั้งติด |
| `POST /v1/integrations/deploy/projects/{id}/events` | รับ event ตาม schema ข้างบน · `initial_admin_password` ไม่บังคับ ถ้ามีจะเข้ารหัสเก็บ และไม่เก็บใน event |

### 3. CRN บน MacMini

- **label ใน image** ใส่ตอน `docker build`: `org.opencontainers.image.revision` (commit), `fitt.crn.project_id`, `fitt.crn.job_id`, `fitt.crn.build_no`, `fitt.crn.contract=1` (image นี้ build โดย CRN ที่บังคับกฎด้านล่างแล้ว), `fitt.crn.schedule` (JSON ของงานตั้งเวลา ไม่มีงาน = `[]`) · FTCDV ได้ label ที่ขอไว้ไปด้วย
- **push ทุก image** ไป `CRN_MIRROR_REGISTRY` ตามโค้ดเดิม · Artifact Registry ตั้งให้ลบเองโดยเก็บแค่ 5 tag ล่าสุดต่อ image
- **กฎใหม่ใน skill หลัก** (`cmd/server/skillassets`) ใช้กับทุกแอป:
  1. ไฟล์ที่ผู้ใช้อัปโหลดเขียนลง `UPLOAD_DIR` (ค่าเริ่มต้น `/data/uploads`) เท่านั้น นอกจากนั้นเขียนได้แค่ `/tmp`
  2. งานตั้งเวลาประกาศใน `crn.schedule.json` (`[{"name","cron","path"}]`) มี handler ที่ `POST /api/cron/<name>` ซึ่งต้องมี `CRON_SECRET` ถูกต้อง · โมดูลจากแม่แบบเรียก handler ตามเวลาในตัวแอปเมื่อ `CRN_SCHEDULER=internal` (ค่าเริ่มต้น) · ห้ามใช้ตัวตั้งเวลาแบบอื่น
  3. ข้อมูลที่ต้องจำข้ามคำขอเก็บในฐานข้อมูล
  4. คำขอเดียวห้ามเกิน 5 นาที งานยาวให้แตกไปทำเบื้องหลังผ่านงานตั้งเวลา
- **Dockerfile และ compose:** สร้าง `/data/uploads` เป็นของ user `node` · `docker-compose.customer.yml` มี volume `uploads:/data/uploads` · ตั้ง npm ให้เขียน log ลง `/tmp`
- **ตรวจ image จริงก่อน push** (ขั้นใหม่หลัง `BuildImage`): รัน image คู่กับ PostgreSQL 16 ชั่วคราวด้วย `--read-only --tmpfs /tmp -v <ชั่วคราว>:/data/uploads --cap-drop ALL --security-opt no-new-privileges` · `/api/health` ต้องได้ 200 และ `GET /` ต่ำกว่า 500 · `docker diff` ของการรันแบบไม่ read-only ต้องไม่มีการเขียนนอก `/tmp` และ `/data/uploads` · ไม่ผ่าน = build ล้มพร้อมรายการ path · ต้นแบบคือสคริปต์ทดสอบให้ FTCDV เมื่อ 16 ต.ค.

### 4. ส่วน deploy ใน GCP

ต่อยอด `deploy/gcp/uat-deploy.sh` ที่ trigger `crn-uat-deploy` รันอยู่ แบ่งงานเป็นขั้นที่รันซ้ำได้ทุกขั้น (สร้างเฉพาะที่ยังไม่มี)

**ถูกเรียกเมื่อ:** (ก) Artifact Registry ประกาศ image ใหม่บน topic `gcr` (มีแล้ว) · (ข) Cloud Scheduler ส่งข้อความเข้า topic ใหม่ `crn-sweep` ทุก 5 นาที แล้วทำทีละโปรเจกต์จาก `GET /pending` โดยหา image ล่าสุดของโปรเจกต์นั้นใน Artifact Registry เอง

**ขั้นตอนต่อหนึ่งโปรเจกต์:**

1. อ่าน label จาก config ของ image ตาม digest · ไม่มี `fitt.crn.project_id` หรือ `fitt.crn.contract` < 1 → ถ้าโปรเจกต์เลือก cloud ส่ง `failed` "image รุ่นเก่า ต้อง build ใหม่หนึ่งครั้ง" แล้วจบ
2. `GET /projects/{id}` · `target = local` → จบเงียบๆ
3. ยังไม่ลงทะเบียน → ส่ง `registering` แล้วทำสิ่งที่ `new-uat.sh` ทำอยู่: บัญชี `crn-uat-<id8>`, ฐานและผู้ใช้บน `crn-uat`, secret `uat-<slug>-<id8>-*` (`AUTH_SECRET`, `CRON_SECRET`, รหัสผ่านผู้ใช้ฐาน และรหัสผ่านผู้ดูแลแบบสุ่ม 24 ตัว) · **ขั้น SQL ที่ตอนนี้ทำด้วยมือ** (โอนฐานให้ผู้ใช้ของโปรเจกต์ และถอน CONNECT จาก PUBLIC) รันผ่าน Cloud SQL Auth Proxy ใน step ของ Cloud Build ด้วยรหัสผ่าน `postgres` จาก Secret Manager ที่ `setup.sh` สร้างไว้ · bucket `crn-uat-<id8>-uploads` ที่บัญชีของโปรเจกต์นั้นเขียนได้คนเดียว
4. ส่ง `deploying` · deploy `uat-<slug>-<id8>` ด้วย digest แบบ `--no-traffic --tag candidate`, port 3000, execution environment gen2, ผูก bucket ที่ `/data/uploads`, `CRN_SCHEDULER=external`, `BOOTSTRAP_ADMIN_EMAIL` จาก FITT Builder, min instances 0
5. เรียก `/api/health` ของ revision ใหม่ผ่าน tag URL จนได้ 200 (นานสุด 4 นาที) · ผ่าน → ย้าย traffic ทั้งหมดมา revision ใหม่ · ไม่ผ่าน → traffic อยู่ที่ revision เดิม ส่ง `failed` พร้อมบรรทัดท้ายของ log
6. สร้างหรือปรับ Cloud Scheduler job ตาม `fitt.crn.schedule` (ใส่ `CRON_SECRET` เป็น header) และลบ job ที่ไม่อยู่ใน label แล้ว
7. ส่ง `ready` พร้อม url, digest, build_no · ครั้งแรกแนบ `initial_admin_password` ด้วย

**นับครั้งที่ล้ม:** FITT Builder นับ `failed` ติดกันที่มี `build_no` เดียวกัน · ครบ 3 ครั้ง `/pending` จะไม่คืนโปรเจกต์นั้นอีก และกล่อง Cloud ขึ้นว่า "ต้องให้ทีมดู" · event ของ `build_no` ใหม่ (จาก image ใหม่) เริ่มนับใหม่

### 5. ขึ้น production

`deploy/gcp/promote.sh <project_id> [--always-on] [--tier <tier>]` คนในทีมรันด้วยบัญชีของตัวเอง:
Cloud SQL `crn-prod-<id8>` (ปิด CONNECT จาก PUBLIC เหมือน UAT) · บัญชี `crn-prod-<id8>` · secret `prod-<slug>-<id8>-*` ชุดใหม่ รวมรหัสผ่านผู้ดูแลชุดใหม่ · bucket `crn-prod-<id8>-uploads` · deploy `prod-<slug>-<id8>` ด้วย digest ที่ UAT ใช้อยู่ตอนนั้น (ใช้วิธีเช็ค health แล้วค่อยย้าย traffic เหมือนข้อ 4) · `CRN_SCHEDULER=external` และ Cloud Scheduler job แบบเดียวกับ UAT · `--always-on` = min instances 1 · ถาม `FITT_LICENSE_KEY` ทาง stdin (เว้นว่างได้) · ตั้ง `FITT_STATUS_URL` · ส่ง event `env: production` พร้อมรหัสผ่านไป FITT Builder · build ครั้งต่อไปไม่แตะ production

### 6. เมื่อมีอะไรผิดพลาด

| เหตุ | ผล |
|---|---|
| ลงทะเบียนพังกลางทาง | ส่ง `failed` · รอบเช็คถัดไปทำต่อจากขั้นที่ค้าง · ครบ 3 ครั้ง = "ต้องให้ทีมดู" |
| revision ใหม่ไม่ผ่าน health | UAT ยังใช้ revision เดิม · กล่อง Cloud บอกว่า build ใหม่ขึ้นไม่สำเร็จ |
| FITT Builder ติดต่อไม่ได้ | ไม่ทำอะไร · log ใน Cloud Build · รอบเช็คถัดไปลองใหม่ |
| ส่ง image ขึ้น GCP ไม่สำเร็จ | build ไม่ล้ม มีคำเตือน · กล่อง Cloud ขึ้น `waiting_image` จากรอบเช็ค |
| image ที่ไม่มี label | ไม่ deploy · แจ้ง "ต้อง build ใหม่หนึ่งครั้ง" |
| token ของ `CODE_RUNNER` ถูกยกเลิก | API ตอบ 401 · ทุกขั้นหยุด · log บอกให้ออก token ใหม่ |

### 7. การทดสอบ

- **CRN (Go):** label ครบใน image · ขั้นตรวจ image จับการเขียนนอกที่อนุญาตได้ (ทดสอบกับ image ที่เขียน `/app/x` แล้วต้องล้ม) · compose มี volume `uploads` · Dockerfile สร้าง `/data/uploads` · test จริงกับ Docker เปิดด้วย env เหมือน `CRN_MIRROR_TEST`
- **skill:** แม่แบบโมดูลตั้งเวลา (`internal` เรียก handler ตามเวลา, `external` ไม่เรียกเลย, `CRON_SECRET` ผิดได้ 401)
- **ส่วน deploy:** `node --test` แบบเดียวกับ `tests/gcp-uat-deploy.test.mjs` คือใช้ gcloud จำลองและ FITT Builder จำลอง · กรณี local จบเงียบ, ลงทะเบียนครั้งแรก, ลงทะเบียนพังแล้วทำต่อ, health ไม่ผ่านแล้ว traffic อยู่ที่เดิม, image ไม่มี label, รอบเช็ค
- **FITT Builder (vitest):** token ผิด/ถูกยกเลิก 401, producer ผิด 403, envelope, `/pending` ตามกติกาการนับ, รับ event, รหัสผ่านแสดงครั้งเดียว, หน้าส่งออกบันทึกตัวเลือก
- **ทดสอบจริงหนึ่งครั้ง:** แอป Next.js Hello World ที่มีหน้าอัปโหลด 1 หน้าและงานตั้งเวลา 1 งาน ส่งออกแบบ cloud แล้วดูว่ากล่อง Cloud ได้ลิงก์ ล็อกอินได้ ไฟล์อัปโหลดอยู่ใน bucket และ Cloud Scheduler เรียกงานได้ · จากนั้นเปลี่ยนอีกโปรเจกต์จาก local เป็น cloud แล้วดูว่าขึ้นภายใน 5 นาทีโดยไม่ build ใหม่

### 8. ลำดับงาน

1. เปลี่ยน region เริ่มต้นเป็น `asia-southeast1` · ผู้ใช้สร้างโปรเจกต์ GCP · `setup.sh` (DRY_RUN ก่อน) · ผู้ใช้ออก key และ `docker login` ที่หน้าเครื่อง MacMini · รวม `feature/gcp-mirror` เข้า dev · deploy CRN · ลอง build 1 ครั้งแล้วเทียบ digest (ปิด DIG-2923)
2. CRN: label, กฎใน skill, Dockerfile/compose, ขั้นตรวจ image
3. FITT Builder: migration, API, หน้าส่งออก, กล่อง Cloud, สคริปต์ออก token · rebase บน main และ bump `lib/changelog.ts` · push main ต้องให้ผู้ใช้อนุมัติ
4. ส่วน deploy: ลงทะเบียนอัตโนมัติรวมขั้น SQL, bucket, health ก่อนย้าย traffic, Cloud Scheduler, แจ้ง event, รอบเช็ค · `setup.sh` เพิ่ม topic `crn-sweep`, Cloud Scheduler, secret ของ token และรหัสผ่าน `postgres`, สิทธิ์ของ `crn-uat-deployer`
5. ทดสอบจริงด้วย Hello World
6. `promote.sh` (DIG-2924 ภายใน 31 ธ.ค.)

งานส่งไฟล์ให้ FTCDV รอบ 20 และ 24 ต.ค. มาก่อนทั้งหมด

## ต้องยืนยันตอนทำ

- Cloud Run ผูก Cloud Storage เป็น volume ได้ใน `asia-southeast1` (ต้องใช้ gen2) · ถ้าไม่ได้ ต้องให้แอปเขียนไฟล์ผ่าน API ของ Cloud Storage และกฎข้อ 1 จะเปลี่ยน
- องค์กร GCP ห้ามออก key ของ service account ไหม (ค่าเริ่มต้นขององค์กรที่สร้างใหม่มักห้าม) · ถ้าห้าม ต้องยกเว้นเฉพาะโปรเจกต์นี้
- Cloud Build ต่อ Cloud SQL ผ่าน Auth Proxy ได้ด้วย IP สาธารณะของ instance โดยไม่ต้องเปิด network เพิ่ม
- งานตั้งเวลาในแอปที่ build ไปแล้วมีแบบไหนบ้าง (เพื่อดูว่ากฎข้อ 2 ครอบคลุมพอ)

## ช่องที่ยังเหลือ

- ขั้นตรวจ image จับได้แค่การเขียนไฟล์ตอนเริ่มแอป ถ้าโค้ดอัปโหลดเขียนผิดที่ จะเจอตอนมีคนใช้จริง · ลดได้ด้วยการทดสอบอัปโหลดใน UAT ก่อนลูกค้ารับงาน
- UAT ทุกโปรเจกต์อยู่บน Cloud SQL เครื่องเดียว ถ้าโปรเจกต์หนึ่งใช้หนักจะกระทบโปรเจกต์อื่น
- รหัสผ่านผู้ดูแลคนแรกผ่าน FITT Builder ในรูปเข้ารหัสจนกว่าจะมีคนกดดู ถ้ากุญแจใน env ของ FITT Builder หลุดพร้อมฐานข้อมูล รหัสที่ยังไม่มีคนดูจะหลุดด้วย
