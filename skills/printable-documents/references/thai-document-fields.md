# Data, money and identity for a Thai business document

All of this runs on the **server**. The document page is a server component that turns Prisma rows into plain strings; the client only ever receives strings.

**Display formatting is not this skill's job.** Baht amounts, dates and เลขประจำตัวผู้เสียภาษี are formatted by `lib/format.ts` (`formatTHB`, `formatBahtUnit`, `formatDateLong`, `formatTaxId`) — create it as the `thai-formatting` skill describes if the build does not have one yet, and import it here. What follows is the part a *document* needs on top of that: the fetch, the arithmetic, the words, the numbering.

## 1. Fetch the whole document in ONE query

A document assembled from several awaits can render half of itself when a relation is missing — a quotation with a header, no customer and no lines still looks like a real quotation. One query, then one decision:

```tsx
// app/(print)/print/quotation/[id]/page.tsx
export const dynamic = "force-dynamic";   // required: next build must not touch the DB

import { notFound } from "next/navigation";
import { prisma } from "@/lib/prisma";

export default async function Page({ params }: PageProps<"/print/quotation/[id]">) {
  const { id } = await params;                       // Next 16: params is a Promise

  const doc = await prisma.quotation.findUnique({
    where: { id },
    include: { customer: true, items: { orderBy: { line_no: "asc" } }, issuer: true },
  });

  if (!doc) notFound();                              // → app/(print)/not-found.tsx (Thai)

  if (!doc.issuer) {
    return (
      <main className="p-10 text-center">
        <h1 className="text-lg font-bold">ยังไม่ได้ตั้งค่าข้อมูลผู้ออกเอกสาร</h1>
        <p className="mt-2">กรุณากรอกชื่อบริษัท ที่อยู่ และเลขประจำตัวผู้เสียภาษี ในหน้าตั้งค่า ก่อนพิมพ์เอกสาร</p>
      </main>
    );
  }
  if (!doc.customer) {
    return <main className="p-10 text-center">เอกสารนี้ยังไม่ได้ระบุลูกค้า จึงยังพิมพ์ไม่ได้</main>;
  }
  // zero line items is printable — the table body says so:
  //   <tr><td colSpan={5}>ยังไม่มีรายการในเอกสารนี้</td></tr>
  // never an empty grid of ruled rows.
  …
}
```

`app/(print)/not-found.tsx`:

```tsx
export default function NotFound() {
  return (
    <main className="p-10 text-center">
      <h1 className="text-lg font-bold">ไม่พบเอกสารที่ต้องการ</h1>
      <p className="mt-2">เอกสารนี้อาจถูกลบไปแล้ว หรือยังไม่ได้ถูกสร้างขึ้นในระบบ</p>
    </main>
  );
}
```

## 2. Money — one rounding order, used by screen and paper alike

Money is `Decimal @db.Decimal(12, 2)` in the schema (with `@default(0)`, per the fitt-build rule that every required column carries a default). Do the arithmetic with `Prisma.Decimal`, never with JS floats — `0.1 + 0.2` on an invoice is a support ticket.

```ts
// lib/money.ts
import { Prisma } from "@prisma/client";
const { Decimal } = Prisma;

const round2 = (d: Prisma.Decimal) => d.toDecimalPlaces(2, Decimal.ROUND_HALF_UP);

export const VAT_RATE = new Decimal("0.07");

/** Fixed order — change it and the printed total stops matching the screen. */
export function totals(
  items: { qty: Prisma.Decimal; unit_price: Prisma.Decimal }[],
  discount: Prisma.Decimal = new Decimal(0),
) {
  const lines = items.map((i) => round2(i.qty.mul(i.unit_price)));      // 1. round each line
  const subtotal = lines.reduce((a, b) => a.add(b), new Decimal(0));    // 2. sum rounded lines
  const base = round2(subtotal.sub(discount));                         // 3. minus discount
  const vat = round2(base.mul(VAT_RATE));                              // 4. VAT on the net base
  const grand = base.add(vat);                                         // 5. grand total
  return { lines, subtotal, discount, base, vat, grand };
}
```

Arithmetic only — no `Prisma` import ever reaches `lib/format.ts`, which client components also load. Render each of these with `formatTHB(...)`.

Print the block in this order and label it in Thai: `รวมเป็นเงิน` → `ส่วนลด` → `ราคาหลังหักส่วนลด` → `ภาษีมูลค่าเพิ่ม 7%` → `จำนวนเงินรวมทั้งสิ้น`.

Two variants, only if the prototype had them:

- **VAT-inclusive quoting** (`ราคารวมภาษีมูลค่าเพิ่มแล้ว`): `base = round2(grand.div(new Decimal("1.07")))`, `vat = grand.sub(base)`. Say which convention the document uses on the document itself — the same numbers mean different things under the two.
- **หัก ณ ที่จ่าย 3%** on services: computed on `base` (never on the VAT), shown as a deduction below the grand total, producing `ยอดชำระสุทธิ`. It does not change `จำนวนเงินรวมทั้งสิ้น`.

## 3. บาทถ้วน — the amount in Thai words

Only for documents that already carry it (ใบเสร็จ, ใบกำกับภาษี, ใบแจ้งหนี้) — never add the line to a document that lacks it. **If the prototype has its own converter, port that one unchanged.** Use the implementation below only when the prototype printed the line from a hardcoded string and there is nothing to port. Input is the grand total as a `"1234.50"` string:

```ts
// lib/baht-text.ts
const DIGITS = ["", "หนึ่ง", "สอง", "สาม", "สี่", "ห้า", "หก", "เจ็ด", "แปด", "เก้า"];
const PLACES = ["", "สิบ", "ร้อย", "พัน", "หมื่น", "แสน"];

function readGroup(raw: string): string {
  const n = raw.replace(/^0+/, "");
  let out = "";
  for (let i = 0; i < n.length; i++) {
    const d = Number(n[i]);
    const place = n.length - i - 1;
    if (d === 0) continue;
    if (place === 1 && d === 1) out += "สิบ";              // สิบ, not หนึ่งสิบ
    else if (place === 1 && d === 2) out += "ยี่สิบ";       // ยี่สิบ, not สองสิบ
    else if (place === 0 && d === 1 && n.length > 1) out += "เอ็ด";  // ...เอ็ด
    else out += DIGITS[d] + PLACES[place];
  }
  return out;
}

function readInt(raw: string): string {
  const n = raw.replace(/^0+/, "");
  if (n === "") return "ศูนย์";
  if (n.length > 6) return readInt(n.slice(0, -6)) + "ล้าน" + readGroup(n.slice(-6));
  return readGroup(n);
}

export function bahtText(amount: string): string {
  const negative = amount.trim().startsWith("-");
  const [intPart, fracRaw = "0"] = amount.replace("-", "").trim().split(".");
  const satang = (fracRaw + "00").slice(0, 2);
  return (negative ? "ลบ" : "") + readInt(intPart) + "บาท" +
    (satang === "00" ? "ถ้วน" : readInt(satang) + "สตางค์");
}
```

`0.00` → `ศูนย์บาทถ้วน` · `21.00` → `ยี่สิบเอ็ดบาทถ้วน` · `1234.50` → `หนึ่งพันสองร้อยสามสิบสี่บาทห้าสิบสตางค์` · `1000000.00` → `หนึ่งล้านบาทถ้วน`.

## 4. Thai dates

Use `formatDateLong` from `lib/format.ts` — `th-TH`'s default calendar is พ.ศ. already, so no `+543` arithmetic belongs in your code:

```ts
new Intl.DateTimeFormat("th-TH", { timeZone: "Asia/Bangkok", dateStyle: "long" })
  .format(new Date("2026-01-31T00:00:00Z"));            // → "31 มกราคม 2569"
```

Two things a printed document depends on: **pin `timeZone: "Asia/Bangkok"`** (the container clock is UTC, so a document issued at 21:00 Bangkok otherwise prints yesterday's date), and **call it on the server**, passing the finished string down — the same call on the client after a server render is a hydration mismatch. Same function for `วันที่ออกเอกสาร`, `ยืนราคาถึงวันที่`, `ครบกำหนดชำระ`.

## 5. Running document numbers

Generate the number **when the document is created**, in the create action — never from `Date.now()` or a random suffix. Derive it from a counter row rather than `count() + 1` or `max + 1`: two people submitting at once read the same count, and the second insert dies on the `@unique` with a stack trace in the middle of a demo. Incrementing a row inside a transaction takes the row lock and serialises them:

```ts
// prisma: model doc_counter { key String @id  last_no Int @default(0) }
export async function nextDocNumber(prefix: string) {           // "QT" | "INV" | "PO" | "DO"
  const year = new Date().getFullYear() + 543;                   // 2569
  const key = `${prefix}-${year}`;
  const { last_no } = await prisma.doc_counter.upsert({
    where: { key },
    update: { last_no: { increment: 1 } },
    create: { key, last_no: 1 },
    select: { last_no: true },
  });
  return `${prefix}-${year}-${String(last_no).padStart(4, "0")}`;  // QT-2569-0001
}
```

Store it as `doc_number String @unique @default("")` and generate it when the document is created, not when it is printed — a printed number that changes on reload is not a document number.

## 6. The identity block

Fields Thai business documents are expected to carry. Include the ones the prototype's document showed; the values all come from data the customer enters.

- **ผู้ออกเอกสาร**: ชื่อบริษัท / ที่อยู่ / โทรศัพท์ / เลขประจำตัวผู้เสียภาษี (13 digits) / สาขา (`สำนักงานใหญ่` or `สาขาที่ 00001`).
- **ผู้รับ (ลูกค้า/ผู้ขาย)**: ชื่อ / ที่อยู่ / เลขประจำตัวผู้เสียภาษี / ผู้ติดต่อ.
- **หัวเอกสาร**: document name (`ใบเสนอราคา / QUOTATION`), `เลขที่`, `วันที่`, and the document's own date field (`ยืนราคาถึง`, `ครบกำหนดชำระ`, `วันที่ส่งของ`).
- **ท้ายเอกสาร**: `จำนวนเงินรวมทั้งสิ้น` + `(ตัวอักษร)` บาทถ้วน, `หมายเหตุ / เงื่อนไขการชำระเงิน`, the signature block.

Render the tax ID with `formatTaxId` from `lib/format.ts` (`0105545000000` → `0-1055-45000-00-0`); it is a 13-digit `String` column, never an `Int` — the leading zero is significant.

**ต้นฉบับ / สำเนา**: render both copies from the same component, one `[data-print-page]` each, so a single print run produces the pair:

```tsx
{(["ต้นฉบับ", "สำเนา"] as const).map((copy) => (
  <section key={copy} data-print-page>
    <span className="float-right border px-2 py-0.5 text-sm">{copy}</span>
    <QuotationBody doc={view} />
  </section>
))}
```

Never seed issuer details, a sample customer or demo line items — the fitt-build seed creates the login account and nothing else, and a document filled with plausible fake company data is exactly what must not leave this pipeline.

## 7. CSV export (when the prototype had one)

Route Handler, UTF-8 **with BOM** — without it Excel on Windows opens Thai as `à¸ˆ...`:

```ts
// app/api/export/quotations/route.ts
export const dynamic = "force-dynamic";
import { prisma } from "@/lib/prisma";
import { formatDate } from "@/lib/format";

const cell = (v: unknown) => `"${String(v ?? "").replace(/"/g, '""')}"`;

export async function GET() {
  const rows = await prisma.quotation.findMany({ include: { customer: true }, orderBy: { doc_number: "asc" } });
  const head = ["เลขที่", "วันที่", "ลูกค้า", "ยอดรวมทั้งสิ้น"];
  const csv = [head, ...rows.map((r) => [r.doc_number, formatDate(r.issue_date), r.customer?.name ?? "", r.grand_total.toFixed(2)])]
    .map((cells) => cells.map(cell).join(","))
    .join("\r\n");

  return new Response("\uFEFF" + csv, {   // BOM — mandatory for Excel
    headers: {
      "Content-Type": "text/csv; charset=utf-8",
      "Content-Disposition": `attachment; filename="quotations.csv"; filename*=UTF-8''${encodeURIComponent("ใบเสนอราคา.csv")}`,
    },
  });
}
```

An empty table exports the header row only — that is the honest result, not an error.

**XLSX**: only if the prototype clearly exported Excel. `exceljs` is pure JS and gets traced into `.next/standalone`, so it works; write the buffer from a Route Handler with `Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`.

**PDF**: there is no server-side PDF path in this image. A headless browser is not installed and cannot be; the pure-JS writers (`pdfkit`, `jsPDF`, `pdf-lib`) run but have no OpenType shaping, so Thai สระ and วรรณยุกต์ are placed as if they were Latin letters — the output is unreadable. The delivered path is the print dialog's **บันทึกเป็น PDF**, which produces a correct PDF using the user's own browser. Record this in `BUILD_NOTES.md` under the prototype's "ดาวน์โหลด PDF" button, and in `TEST_CASES.md` under `สิ่งที่ demo นี้ยังไม่รองรับ` if the brief asked for server-generated PDF files.
