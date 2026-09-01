---
name: printable-documents
description: Build the printable/exportable business documents a ported prototype already has — as a real HTML route styled for A4 print plus window.print(), with Thai typography, VAT 7% totals, ต้นฉบับ/สำเนา marking and a demo watermark. Use when porting or fixing any screen that produces a paper document — ใบเสนอราคา, ใบแจ้งหนี้, ใบกำกับภาษี, ใบเสร็จรับเงิน, ใบสั่งซื้อ (PO), ใบส่งของ, ใบเบิกของ, สัญญา, or a printed summary report — and whenever the prototype has a button labelled พิมพ์ / ปริ้น / Print / ดาวน์โหลด PDF / Export / ส่งออก Excel, or a layout shaped like A4 or กระดาษหัวจดหมาย. Read it BEFORE adding any PDF or spreadsheet library.
---

# printable-documents — เอกสารที่ต้องพิมพ์หรือส่งออก

Read these before writing the document:
- `references/print-css.md` — the print stylesheet, page mechanics, fonts, the watermark, the print button.
- `references/thai-document-fields.md` — one-query fetch, VAT/rounding order, บาทถ้วน, Thai dates, running numbers, CSV export.

**This is still a port.** Build only the documents the prototype already produces. If it has a "พิมพ์ใบเสนอราคา" button, build that. Do NOT add a ใบกำกับภาษี, a ใบส่งของ or a signature block because the domain "should" have one — that is a redesign, and it puts a tax document in a demo nobody asked for.

## 1. The renderer is decided by the container, not by taste

The image ships `.next/standalone` on a slim Node base and `next build` runs with no database. That rules most PDF stacks out:

| approach | survives the image? |
|---|---|
| **HTML route + `@media print` + `window.print()`** | **yes — the default. No dependency, no binary; the dialog's "Save as PDF" is the PDF path, shaped correctly for Thai.** |
| puppeteer / playwright / chromium / wkhtmltopdf | **no** — needs a ~300 MB browser binary and system libs the base image lacks |
| pdfkit / jsPDF / pdf-lib (pure JS) | runs, but **renders Thai wrong** — no OpenType shaping, so สระ/วรรณยุกต์ land in the wrong place; do not use for Thai |
| exceljs (pure JS, xlsx) | yes — traced into standalone; only if the prototype really exported Excel |

So: **the document is a real page in the app, and printing is the browser's job.** The same React component the user reads on screen is what goes on paper — one code path, no drift between screen totals and printed totals.

## 2. Give the document its own route with a bare layout

Do not hide the app shell with a giant `.no-print` list. Put printable documents in a route group whose layout has no nav, no sidebar and no toolbar — `app/(print)/layout.tsx` returning a bare `<html><body>{children}</body></html>`, with the document at `app/(print)/print/quotation/[id]/page.tsx` and `export const dynamic = "force-dynamic"`.

Keep the prototype's entry point (its button/link) and point it at that route: `<Link href={`/print/quotation/${id}`} target="_blank">พิมพ์</Link>`. Never build the document by `window.open()` + `document.write()` of hand-assembled HTML — that window has none of the app's CSS or fonts, and Thai comes out in a fallback face.

## 3. One query, and format on the server

```tsx
const doc = await prisma.quotation.findUnique({ where: { id }, include: { customer: true, items: true, issuer: true } });
```

The page is a **server component**. Prisma `Decimal` is not serializable across the server→client boundary, and `toLocaleDateString` on the client after the server already rendered it is a hydration mismatch. So turn rows into plain strings on the server with `lib/format.ts` (`formatTHB`, `formatDateLong`) and pass strings down. The only client component here is the print button.

## 4. An empty database is the normal case, not an error

The delivered app starts empty. A document screen with no record must **say so in Thai** — never render a blank invoice skeleton with headings, an empty table and `0.00` totals, which reads as a real document for a real order of nothing.

```tsx
if (!doc) notFound();   // → app/(print)/not-found.tsx, in Thai
if (!doc.issuer) return <p className="p-8">ยังไม่ได้ตั้งค่าข้อมูลผู้ออกเอกสาร (ชื่อบริษัท ที่อยู่ เลขประจำตัวผู้เสียภาษี) — กรุณาตั้งค่าก่อนพิมพ์เอกสาร</p>;
```

`app/(print)/not-found.tsx`: `ไม่พบเอกสารที่ต้องการ` + a link back. The list page with zero rows: `ยังไม่มีใบเสนอราคาในระบบ` — not an empty table styled like paper.

## 5. Print CSS — the load-bearing part

Full stylesheet in `references/print-css.md`. The rules that actually decide whether it prints correctly:

```css
@page { size: A4; margin: 14mm 12mm 18mm; }

@media print {
  #fitt-feedback-host, [data-print-hide] { display: none !important; }  /* CRN's 🐞 widget + buttons */
  .overflow-x-auto, .overflow-auto { overflow: visible !important; }     /* a scroll wrapper breaks pagination */
  [data-print-root] { width: auto; min-height: 0; padding: 0; box-shadow: none; }
  thead { display: table-header-group; }        /* repeat the header on every page */
  tr, [data-print-keep] { break-inside: avoid; }
  * { print-color-adjust: exact; -webkit-print-color-adjust: exact; }   /* or backgrounds drop out */
}
```

Tailwind's `print:hidden` covers one-off buttons. Two traps: **totals go after the table, never in `<tfoot>`** (a `tfoot` repeats on every page); and **browsers cannot render your own page numbers** — `@page { @bottom-center { content: counter(page) } }` is ignored by Chrome and Firefox. Rely on the browser's own footer and do not promise page numbers in the UI.

## 6. Thai text must survive the print preview

Keep the prototype's font family, but self-host it and **request the Thai subset explicitly** — `Noto_Sans_Thai({ subsets: ["thai", "latin"] })`. `subsets: ["latin"]` on a Thai family silently ships a file with no Thai glyphs. Always leave OS fallbacks in the stack: `"Noto Sans Thai", "Leelawadee UI", Tahoma, Thonburi, sans-serif`.

The classic "fine on screen, wrong in the print preview": printing before the webfont finished loading. Await it:

```tsx
"use client";  // the only client component on the page
export const PrintButton = () => <button data-print-hide onClick={() => document.fonts.ready.then(() => window.print())}>พิมพ์เอกสาร / บันทึกเป็น PDF</button>;
```

## 7. The document must not pretend to be a real one

A demo may not emit something that could pass as a filed tax document.

- The issuer's name, address and เลขประจำตัวผู้เสียภาษี come from a record **the customer fills in** — never hardcoded, never seeded. No issuer record → the message in §4, not a document with blanks.
- Every printable document carries the marking `เอกสารตัวอย่างจากระบบสาธิต — ไม่ใช่เอกสารทางภาษี` as **real text** in the header or footer, plus a repeating `DEMO` background watermark (`references/print-css.md`). Text, because the print dialog can switch background graphics off; the watermark, because text alone is easy to miss.
- `ต้นฉบับ` / `สำเนา` is a label on the page, driven by data — print both copies as two `break-after: page` sections rather than asking anyone which one they want.
- Laying the fields out like a Thai document is not a claim of legal compliance. Do not write "ถูกต้องตามกรมสรรพากร" anywhere.

## 8. Money and identity

`references/thai-document-fields.md` has the code. The order is fixed so screen and paper agree: round each line total to 2 decimals → sum to `subtotal` → subtract `discount` → `vat = round(base × 0.07, 2)` → `grand = base + vat`. Store money as `Decimal @db.Decimal(12, 2)`, do the arithmetic server-side, and print the same string both places. Document numbers come from a counter row incremented inside `$transaction` (`QT-2569-0001`, พ.ศ. = ค.ศ. + 543) — never `count() + 1`. Dates are `31 มกราคม 2569`, formatted on the server at `Asia/Bangkok`.

## 9. When a real file download is required

Only if the prototype had one. CSV from a Route Handler, **with a UTF-8 BOM** — without it Excel opens Thai as garbage:

```ts
return new Response("\uFEFF" + csv, {        // the UTF-8 BOM is not optional
  headers: { "Content-Type": "text/csv; charset=utf-8", "Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent("ใบเสนอราคา.csv")}` },
});
```

For PDF, the honest answer is the print dialog's "บันทึกเป็น PDF" — say so in `BUILD_NOTES.md` if the prototype's button said "ดาวน์โหลด PDF", and label the button พิมพ์ / บันทึกเป็น PDF rather than shipping a Thai-mangling PDF library.

## Hard rules
- Only the documents the prototype already has. Never add one because the domain expects it.
- HTML route + `window.print()` is the one path. No headless browser, no pure-JS PDF writer for Thai text.
- No record → a Thai message. Never an empty document skeleton, never invented line items or a sample customer.
- Issuer identity is data the customer enters; the DEMO marking and the "ไม่ใช่เอกสารทางภาษี" line ship on every printable document.
- Server component, one query, strings across the boundary; the route is `force-dynamic` so `next build` needs no database.
