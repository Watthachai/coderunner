---
name: thai-formatting
description: Format money, dates, numbers, VAT and Thai text correctly in a Thai-language demo, and keep every formatter safe on an empty database. Use whenever a screen renders จำนวนเงิน/ราคา/ยอดรวม (฿, บาท, a Prisma Decimal), วันที่/เวลา (พ.ศ. vs ค.ศ., Asia/Bangkok), ภาษีมูลค่าเพิ่ม 7% (ราคารวม/ไม่รวมภาษี), เลขประจำตัวผู้เสียภาษี 13 หลัก, เลขที่เอกสาร, เบอร์โทร/รหัสไปรษณีย์/ที่อยู่ form fields, or a table or dropdown of Thai names, quantities or amounts — and when choosing fonts, line-height or line-breaking for Thai copy, or when a value renders as NaN, ฿NaN, Invalid Date, undefined, or one day off. Skip on an English-only UI with no money, dates or Thai text.
---

# thai-formatting — Thai money, dates, VAT and text that survive an empty database

A Thai customer spots these in the first ten seconds: `฿NaN` in a total, a date one day early, a VAT line that doesn't foot, Thai words chopped mid-word. Put every formatter in ONE module — `lib/format.ts` — and import it from every screen.

**This is HOW to render, not WHAT to render.** Format only what the prototype already shows. Never add a VAT row, a พ.ศ. date, or a column the prototype does not have — that is a redesign, and the port rule wins.

## Create `lib/format.ts` and import it everywhere

Keep this file free of `@prisma/client` imports — client components import it too, and pulling Prisma into the client bundle breaks the build. It types money structurally instead, so a `Decimal` passes without the import.

```ts
/** A Prisma Decimal, a number, a numeric string, or nothing at all. */
type Money = number | string | { toString(): string } | null | undefined;

const EMPTY = "—";                 // the one placeholder — never "", "-", "N/A", "undefined"
const TZ = "Asia/Bangkok";         // the container clock is UTC; never rely on it

/** null/undefined/NaN -> null. ZERO IS A REAL VALUE and stays 0. */
function toNum(v: Money): number | null {
  if (v === null || v === undefined || v === "") return null;
  const n = typeof v === "number" ? v : Number(v.toString());
  return Number.isFinite(n) ? n : null;
}

const nfBaht = new Intl.NumberFormat("th-TH", { style: "currency", currency: "THB",
  minimumFractionDigits: 2, maximumFractionDigits: 2 });
const nfPlain = new Intl.NumberFormat("th-TH", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** ฿1,234.50 · 0 -> ฿0.00 · null -> — */
export const formatTHB = (v: Money) => { const n = toNum(v); return n === null ? EMPTY : nfBaht.format(n); };

/** 1,234.50 บาท — for ใบแจ้งหนี้/ใบเสร็จ that spell the unit out */
export const formatBahtUnit = (v: Money) => { const n = toNum(v); return n === null ? EMPTY : `${nfPlain.format(n)} บาท`; };

/** Quantities: 1,234 · 12.5 · null -> — (0 -> "0") */
export function formatQty(v: Money, maxDp = 2): string {
  const n = toNum(v);
  return n === null ? EMPTY
    : new Intl.NumberFormat("th-TH", { maximumFractionDigits: maxDp }).format(n);
}

/** Takes a RATIO: 0.07 -> 7%. Passing 7 gives 700%. Divide by zero -> — */
export function formatPercent(ratio: number | null | undefined, dp = 0): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) return EMPTY;
  return new Intl.NumberFormat("th-TH", { style: "percent",
    minimumFractionDigits: dp, maximumFractionDigits: dp }).format(ratio);
}

const dfShort = new Intl.DateTimeFormat("th-TH", { timeZone: TZ, day: "2-digit", month: "2-digit", year: "numeric" });
const dfLong  = new Intl.DateTimeFormat("th-TH", { timeZone: TZ, dateStyle: "long" });
const dfStamp = new Intl.DateTimeFormat("th-TH", { timeZone: TZ, dateStyle: "medium", timeStyle: "short" });

function ok(d: Date | string | null | undefined): Date | null {
  if (!d) return null;                       // null, undefined, ""
  const x = d instanceof Date ? d : new Date(d);
  return Number.isNaN(x.getTime()) ? null : x;   // never "Invalid Date"
}
/** 15/01/2569 — Buddhist era, because th-TH's default calendar IS พ.ศ. */
export const formatDate = (d: Date | string | null | undefined) => { const x = ok(d); return x ? dfShort.format(x) : EMPTY; };
/** 15 มกราคม 2569 */
export const formatDateLong = (d: Date | string | null | undefined) => { const x = ok(d); return x ? dfLong.format(x) : EMPTY; };
/** 15 ม.ค. 2569 09:30 */
export const formatDateTime = (d: Date | string | null | undefined) => { const x = ok(d); return x ? dfStamp.format(x) : EMPTY; };

/** For <input type="date"> — ISO, Gregorian, Bangkok day. NOT toISOString(). */
const dfISO = new Intl.DateTimeFormat("en-CA", { timeZone: TZ, year: "numeric", month: "2-digit", day: "2-digit" });
export const toDateInput = (d: Date | string | null | undefined) => { const x = ok(d); return x ? dfISO.format(x) : ""; };
/** Back from the input: "2026-01-15" is a Bangkok day, not a UTC instant. */
export const fromDateInput = (v: string) => (v ? new Date(`${v}T00:00:00+07:00`) : null);

/** Thai dictionary order. Plain .sort() dumps every เ/แ/โ/ใ/ไ word at the end. */
const collator = new Intl.Collator("th", { numeric: true, sensitivity: "base" });
export const compareTh = (a?: string | null, b?: string | null) => collator.compare(a ?? "", b ?? "");
```

Exact ICU output shifts a little between Node versions — display these strings, never parse them back.

## Rules

- **Zero is not null.** `if (!value) return "—"` erases a legitimate `฿0.00` and a `0 ชิ้น` stock count. Test `=== null`, as above.
- **A Prisma `Decimal` is not a number.** `a + b` concatenates strings and `_sum` is `null` on an empty table; a Decimal also cannot cross into a `"use client"` component. See `references/money-and-vat.md` — read it before writing any total.
- **Round VAT once, on the invoice, and derive the third number by subtraction** so ยอดรวม always foots. Per-line rounding disagrees by satang. Same reference.
- **Always pass `timeZone: "Asia/Bangkok"`.** The server renders in UTC, so a row created 02:00 ICT prints as the previous day, and the client then hydrates a different string.
- **`<html lang="th">` in `app/layout.tsx`.** It is what turns on the browser's Thai line-breaker and correct font selection.
- **Never `break-all`/`break-words` on Thai.** Thai has no spaces, so the whole sentence is one "word" and those utilities cut it anywhere. Clamp with CSS (`truncate`, `line-clamp-2`), not `String.slice`.
- **Money and quantity columns: `text-right tabular-nums whitespace-nowrap`.** Digits must line up and `฿1,234.50` must not wrap.
- **Thai body text needs `leading-7` or looser.** `leading-tight` crowds the stacked สระ/วรรณยุกต์ in table rows.
- **Ship a Thai font** (`next/font/google`, `subsets: ["thai", "latin"]`) or the UI renders tofu on machines without one. Details, plus the print/PDF trap, in `references/thai-text-and-input.md`.
- **13-digit tax IDs and 5-digit postcodes are `String`, never `Int`** — `Int` overflows and eats the leading `0` of `0105545…`.
- **Copy validation messages verbatim from the PRD's Validation & Edge Cases** — do not invent Thai error text.

## References

- `references/money-and-vat.md` — the Prisma `Decimal` path end to end, VAT 7% inclusive vs exclusive, rounding order with a worked example, เลขประจำตัวผู้เสียภาษี.
- `references/thai-text-and-input.md` — พ.ศ. vs ค.ศ. decision table, the timezone off-by-one, line breaking, truncation, sorting, fonts and print, phone/postcode/address/name field shapes.

This skill owns the **values**: the arithmetic and the formatted string. Whatever owns the **layout** — a table screen, a printable ใบกำกับภาษี, a chart axis — calls into `lib/format.ts` rather than re-deriving totals or re-implementing a date format.

## Verify before you finish

Against an **empty** database, then with ONE row you entered through the app's own form (no seeded sample rows): totals show `฿0.00`, empty columns show `—`, a `0` quantity still shows `0`, a Thai name list is in dictionary order, and `1234567.5` shows `฿1,234,567.50`. No `NaN`, `Invalid Date`, `undefined`, or off-by-one day anywhere.
