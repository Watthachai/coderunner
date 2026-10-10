# กัน Claude ระหว่าง build ไม่ให้เข้าถึงค่าลับ ไฟล์ และเน็ตของเครื่อง build — design

วันที่: 2026-10-10 · สถานะ: รอผู้ใช้ review · ต่อจาก commit `ebb1f89` (ล้าง env ตอนเปิด Claude)

## ปัญหา

CRN รัน Claude แบบ `--dangerously-skip-permissions` ด้วย OS user เดียวกับ CRN และ prompt ของ edit build มีข้อความจากผู้ใช้ปลายทางปนอยู่ (FTCDV จะเปิดให้ผู้ใช้ส่งคำขอแก้) ข้อความที่แฝงคำสั่งจึงสั่ง Claude ได้ทุกอย่างที่ user นั้นทำได้

`ebb1f89` ปิดเรื่อง env แล้ว (รหัส DB, token callback และ ssh-agent ไม่ถึง Claude อีก) ที่ยังเปิดอยู่:

- อ่านไฟล์ได้ทั้งเครื่อง: `.env` ของ CRN, `~/.ssh`, credential ของ git, ประวัติ shell และ transcript ของ session อื่นใน `~/.claude`
- อ่านโค้ดของลูกค้ารายอื่นในโฟลเดอร์ projects ได้
- เขียนไฟล์นอกโฟลเดอร์ build ได้ เช่น ไฟล์เริ่มต้นของ shell หรือ `~/.claude/settings.json` ทำให้รอบถัดไปหลุดได้มากขึ้น
- ส่งข้อมูลออกเน็ตไปที่ไหนก็ได้

## เป้าหมาย / ไม่ใช่เป้าหมาย

- **เป้าหมาย:** Claude ระหว่าง build (และตอนปรับปรุงสกิล) อ่านไฟล์ได้เฉพาะที่งานต้องใช้ เขียนได้เฉพาะโฟลเดอร์ build กับ cache ที่ระบุ และออกเน็ตได้เฉพาะ domain ที่ build ต้องใช้ โดย build ปกติยังผ่านเหมือนเดิม
- **ไม่ใช่เป้าหมาย (รอบนี้):** ปิดช่อง docker (ดู "ช่องที่ยังเหลือ") · แยก OS user หรือรัน Claude ใน container · ใส่การยืนยันตัวตนให้ `/internal` (ชิ้น 5)

## ข้อเท็จจริงที่ลองแล้ว (CLI 2.1.295 บนเครื่อง dev, 2026-10-10)

ลองโดยส่ง `--settings` ทีละรอบคู่กับ `--dangerously-skip-permissions`:

| ลอง | ผล |
|---|---|
| tool Read อ่านไฟล์ที่อยู่ใน `permissions.deny` | ถูกกัน ("denied by your permission settings") — กฎ deny มีผลในโหมด bypass |
| Bash `cat` ไฟล์ใน `sandbox.filesystem.denyRead` | ถูกกัน ("Operation not permitted") |
| Bash เขียนไฟล์ใน home | ถูกกัน (เขียนได้แค่ cwd กับ temp ของ user ตามค่าเริ่มต้น) |
| Bash `curl` ไป domain ที่ไม่อยู่ใน `allowedDomains` | **ผ่าน** ถ้าไม่เปิด `strictAllowlist` (เอกสาร: โหมด bypass ปล่อย host อื่น) · ถูกกัน (403) เมื่อเปิด `strictAllowlist: true` |
| Bash `curl` ไป domain ที่อนุญาต | ผ่าน |
| `docker version` | ถูกกันตามค่าเริ่มต้น · ผ่านเมื่อใส่ socket ใน `network.allowUnixSockets` |

ข้อสรุปจากเอกสาร (sandboxing): sandbox คุมเฉพาะคำสั่ง shell ส่วน tool Read/Edit/Write/WebFetch ต้องคุมด้วย `permissions.deny` แยกต่างหาก · ต้องตั้ง `allowUnsandboxedCommands: false` ไม่งั้นในโหมด bypass Claude สั่งรันนอก sandbox ได้โดยไม่ถาม · การตั้งผ่าน `--settings` ทำให้ settings ใน repo ที่พยายามคลาย sandbox ถูกเมิน

## Design

### 1. CRN สร้าง settings ทีละ build

ฟังก์ชันใหม่ใน `internal/claude` (ไฟล์ `sandbox.go`) รับ `workDir`, `projectsDir`, `runnerDir` (โฟลเดอร์ของ CRN เอง) และ `home` แล้วคืน JSON ที่ส่งเป็น `--settings '<json>'` แบบ inline (ไม่มีค่าลับในนั้น และไม่ต้องจัดการไฟล์ที่ Claude อาจแก้ได้)

**`permissions.deny`** (คุม tool ของ Claude ใช้รูปแบบ path `//absolute`):
- `WebFetch`, `WebSearch`
- `Read`/`Edit` ของ: `runnerDir` ทั้งโฟลเดอร์ · `~/.ssh` · `~/.aws` · `~/.config/gh` · `~/.git-credentials` · `~/.netrc` · `~/.docker/config.json` · `~/.zsh_history` · `~/.bash_history` · `~/Library/Keychains` · `~/.claude/projects` · `~/crn.log`
- `Read`/`Edit` ของโฟลเดอร์โปรเจกต์อื่นทุกตัวใน `projectsDir` (ไล่รายชื่อตอนเริ่ม build ยกเว้น `workDir`) — กฎ deny ชนะ allow เสมอ จึงใช้แบบ "ปิดทั้ง projects แล้วเปิดเฉพาะของตัวเอง" กับ tool ไม่ได้

**`sandbox`** (คุมคำสั่ง shell ใช้รูปแบบ path ปกติ):
- `enabled: true`, `failIfUnavailable: true`, `allowUnsandboxedCommands: false`
- `filesystem.denyRead`: path ลับชุดเดียวกับข้างบน + `projectsDir` · `filesystem.allowRead`: `workDir` (path ที่แคบกว่าชนะ จึงเปิดเฉพาะโฟลเดอร์ของ build นี้ได้)
- `filesystem.allowWrite`: cache ที่ build ใช้ — `~/.npm`, `~/Library/Caches/ms-playwright`, `~/.cache` (prisma) และ `~/Library/Caches/node-gyp` (รายการสุดท้ายยืนยันด้วย build ทดลอง)
- `network.allowedDomains`: `registry.npmjs.org`, `binaries.prisma.sh`, `cdn.playwright.dev`, `playwright.download.prss.microsoft.com`, `playwright.azureedge.net`, `github.com`, `codeload.github.com`, `objects.githubusercontent.com`, `nodejs.org`, `fonts.googleapis.com`, `fonts.gstatic.com` (รายการสุดท้ายยืนยันด้วย build ทดลอง) · `strictAllowlist: true`
- `network.allowUnixSockets`: socket ของ Docker (อ่านจาก `DOCKER_HOST` ถ้ามี ไม่งั้นใช้ `~/.docker/run/docker.sock`) — ขั้นตรวจที่ Claude รันเองต้องใช้ docker (`delivery-checks.md:12`)
- `credentials`: ตั้ง deny ให้ `CLAUDE_CODE_OAUTH_TOKEN` และ `ANTHROPIC_API_KEY` เพื่อให้คำสั่ง shell ไม่เห็น token ของ Claude เอง (ตัว CLI ยังใช้ได้ตามปกติ)

### 2. เปิดใช้ผ่านสวิตช์

- `CRN_CLAUDE_SANDBOX` = `off` (ค่าเริ่มต้นรอบแรก) | `on` · ค่าอื่นทำให้ CRN ไม่ยอม start (แบบเดียวกับ `CRN_CLAUDE_EFFORT`)
- เมื่อเป็น `on` ตอน start CRN เช็ค `claude --version` ถ้าต่ำกว่า **2.1.295** (version ที่ลองแล้ว) จะไม่ยอม start และบอกให้อัปเดต CLI เพราะ CLI รุ่นเก่าอาจไม่รู้จัก `strictAllowlist` แล้วเมินไปเฉยๆ ซึ่งเท่ากับเปิดเน็ต
- ใช้กับทั้ง `Run` (build/edit รวม `--resume`) และ `Improve` (ปรับปรุงสกิล)
- log ทุก build ว่ารันแบบ sandbox หรือไม่ และเก็บลง trace เพื่อให้รู้ย้อนหลังว่า build ไหนรันแบบไหน

### 3. ช่องที่ยังเหลือ (ต้องรู้ก่อนเปิดให้ผู้ใช้ปลายทาง)

**docker:** เมื่อเปิด socket ให้ ข้อความที่แฝงคำสั่งยังทำได้ 2 อย่าง: สั่ง container mount ไฟล์ในเครื่องออกมาอ่าน และให้ container ส่งข้อมูลออกเน็ต (container ไม่ผ่าน proxy ของ sandbox)

- **ลดได้ทันที:** ตั้ง Docker Desktop บนเครื่อง build ให้แชร์ไฟล์ได้เฉพาะ `projectsDir` และ temp (เดิมแชร์ทั้ง `/Users`) แล้วลองยืนยันว่า `docker run -v <runnerDir>:/x` ล้ม — ทำบนเครื่อง build โดยผู้ใช้อนุมัติ
- **ปิดจริง (spec แยก รอบถัดไป):** เอา docker ออกจากมือ Claude ให้ Claude ตรวจเฉพาะส่วนที่ไม่ใช้ docker แล้วให้ CRN รันขั้นตรวจ docker เอง (CRN รันซ้ำอยู่แล้วก่อน export) ถ้าล้มค่อยส่งผลกลับให้ Claude แก้ต่อด้วย `--resume`

ระหว่างที่ช่อง docker ยังอยู่ **ยังไม่ควรเปิดให้ผู้ใช้ปลายทางส่งคำขอแก้ตรงถึง CRN** ให้มีคนอ่านและยืนยันก่อนตามที่ FTCDV วางไว้

### 4. Error handling

- sandbox start ไม่ได้ → CLI ออกเอง (`failIfUnavailable`) → build ล้มพร้อมข้อความชัด ไม่รันแบบไม่มี sandbox แทน
- คำสั่งถูก sandbox กัน → Claude เห็นข้อความ (เช่น host ที่ถูกปฏิเสธ) ใน output → ถ้าทำให้ build ล้ม จะเห็นใน log และ trace แล้วนำมาเพิ่มใน allowlist ด้วยการแก้โค้ด (ไม่ให้ตั้งจาก request)

## Testing

- **Unit:** สร้าง settings ได้ JSON ที่มี deny ครบ, รวมโฟลเดอร์โปรเจกต์อื่นแต่ไม่รวม `workDir`, ไม่มีค่าจาก env ของ CRN และ path ใช้รูปแบบถูก (`//abs` สำหรับกฎ tool, absolute สำหรับ sandbox)
- **Version guard:** แยกเลข version ได้ และไม่ยอม start เมื่อต่ำกว่าที่กำหนด
- **Integration (เปิดด้วย env เท่านั้น เพราะเสียค่า model):** รัน Claude (Haiku) ด้วย settings จริง แล้วยืนยัน 6 ข้อแบบที่ลองไว้ข้างบน + อ่านโฟลเดอร์โปรเจกต์อื่นไม่ได้ + อ่านไฟล์ใน `workDir` ได้
- **Build ทดลอง 1 รอบบนเครื่อง build** ด้วยสวิตช์ `on` (~$10–40 ต้องอนุมัติ) เพื่อเก็บ domain และ path ที่ขาด แล้วค่อยเปลี่ยนค่าเริ่มต้นเป็น `on`

## ลำดับการปล่อย

1. เขียนโค้ด + test (สวิตช์ `off` = พฤติกรรมเดิม)
2. อัปเดต Claude CLI บนเครื่อง build เป็น ≥ 2.1.295
3. deploy (รวมกับ commit ที่ค้างอยู่ ต้องอนุมัติ)
4. ตั้ง Docker Desktop ให้แชร์ไฟล์เฉพาะที่จำเป็น แล้วลองยืนยัน
5. build ทดลองด้วย `on` → ปรับ allowlist → เปลี่ยนค่าเริ่มต้นเป็น `on`

## คำถามที่ต้องเช็คบนเครื่อง build ก่อนลงมือ

- `CRN_PROJECTS_DIR` อยู่ที่ไหน และ Claude ล็อกอินด้วยอะไร (keychain หรือ `CLAUDE_CODE_OAUTH_TOKEN`)
- Docker Desktop ตอนนี้แชร์ไฟล์ path ไหนบ้าง
