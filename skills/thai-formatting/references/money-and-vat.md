# Money, Prisma Decimal, and VAT 7%

Everything here is about numbers that must ADD UP on screen. Format with `lib/format.ts`; do the arithmetic here, on the server, before the value reaches a component.

## 1. `Decimal` is a class, not a number

`Decimal @db.Decimal(12, 2)` comes back as a `Prisma.Decimal` (decimal.js). Three things break:

```ts
import { Prisma } from "@prisma/client";   // server files ONLY, never lib/format.ts

const lines = await prisma.invoice_line.findMany({ where: { invoice_id: id } });

// ❌ Decimal.valueOf() returns a STRING, so + concatenates:
//    three ฿333.33 lines reduce to "0333.33333.33333.33", and Number() of that is NaN → ฿NaN.
const wrong = lines.reduce((s, l) => s + l.amount, 0);

// ✅ Decimal arithmetic. Its default rounding is HALF-UP, which is the satang convention.
const subtotal = lines.reduce((s, l) => s.plus(l.amount), new Prisma.Decimal(0)).toDecimalPlaces(2);
```

`Decimal.toString()` is **not** display-ready — it drops trailing zeros, so `฿70.00` renders as `70`. Always go through `formatTHB()`.

**`_sum` is `null` on an empty table** — the delivered app starts empty, so this is the normal path, not an edge case:

```ts
const agg = await prisma.invoice.aggregate({ _sum: { grand_total: true }, _count: true });
const grand = agg._sum.grand_total ?? new Prisma.Decimal(0);   // null -> zero, never NaN
const average = agg._count === 0 ? null : grand.div(agg._count);  // never divide by 0
```

**A `Decimal` cannot cross into a client component.** React throws *"Only plain objects … can be passed to Client Components"*. Convert at the boundary:

```tsx
// app/invoices/[id]/page.tsx  (server)
export const dynamic = "force-dynamic";
<InvoiceView subtotal={subtotal.toNumber()} vatText={formatTHB(vat)} />
```

Pass `.toNumber()` (safe at demo magnitudes), or pass the already-formatted string. Never `JSON.parse(JSON.stringify(row))`, and never spread a raw Prisma row with Decimal columns into a `"use client"` prop.

## 2. VAT 7% — exclusive vs inclusive

Which one the app uses is the prototype's decision, taken from the BRD/PRD. Do not switch it, and do not add a VAT line to a screen that has none.

| ราคาไม่รวมภาษี (exclusive) | ราคารวมภาษีแล้ว (inclusive) |
|---|---|
| `vat = subtotal × 0.07` | `base = total ÷ 1.07` |
| `total = subtotal + vat` | `vat = total − base` |

```ts
const RATE = new Prisma.Decimal("0.07");

/** ราคาไม่รวมภาษี: subtotal is the trusted number. */
export function vatExclusive(subtotal: Prisma.Decimal) {
  const base = subtotal.toDecimalPlaces(2);
  const vat = base.mul(RATE).toDecimalPlaces(2);
  return { base, vat, total: base.plus(vat) };          // base + vat === total, exactly
}

/** ราคารวมภาษีแล้ว: the total is the trusted number — derive vat by SUBTRACTION. */
export function vatInclusive(total: Prisma.Decimal) {
  const gross = total.toDecimalPlaces(2);
  const base = gross.div(new Prisma.Decimal("1.07")).toDecimalPlaces(2);
  return { base, vat: gross.minus(base), total: gross };  // base + vat === total, exactly
}
```

**Derive the third number by subtraction.** If you round all three independently, `฿93.46 + ฿6.54` will sometimes not equal `฿100.00`, and the customer's tester will file it as a bug.

## 3. Rounding order — round ONCE, at the invoice

Three lines at `฿333.33`:

- **Per line:** `333.33 × 0.07 = 23.3331 → 23.33`, ×3 = `฿69.99`. Total `฿1,069.98`.
- **Per invoice:** subtotal `฿999.99`, `× 0.07 = 69.9993 → ฿70.00`. Total `฿1,069.99`.

One satang apart, and the printed ภาษีมูลค่าเพิ่ม line no longer equals 7% of ยอดรวมก่อนภาษี. Rule:

1. Round each **line amount** to 2 dp (`qty × unit_price`) — that number is printed per row.
2. Sum the rounded line amounts → `subtotal`.
3. Compute VAT **once** from `subtotal`, round once.
4. `grand_total = subtotal + vat`.

Store `subtotal`, `vat_amount` and `grand_total` as their own `Decimal @db.Decimal(12, 2) @default(0)` columns when the prototype shows them — recomputing on each render lets a later price edit silently change an issued document.

## 4. Money in and out of forms

```ts
// Server action: "" must NOT become 0 — Number("") === 0.
const raw = String(formData.get("amount") ?? "").trim();
if (raw === "") return { error: "กรุณากรอกจำนวนเงิน" };
const amount = Number(raw);
if (!Number.isFinite(amount) || amount < 0) return { error: "จำนวนเงินไม่ถูกต้อง" };
await prisma.expense.create({ data: { amount: new Prisma.Decimal(amount.toFixed(2)) } });
```

- Input: `inputMode="decimal"`, `step="0.01"`, `min="0"`. Never bind a money input to `parseFloat(x) || 0` — that turns a typo into a silent zero.
- Take the exact Thai error text from the PRD's Validation & Edge Cases section; the wording above is a placeholder shape only.
- Money columns are `Decimal @db.Decimal(12, 2) @default(0)` — never `Float` (0.1 + 0.2 problems), never `Int` unless the schema genuinely stores satang.

## 5. เลขประจำตัวผู้เสียภาษี (13-digit tax ID) and document numbers

Store as `String` — 13 digits overflow `Int`, and the leading `0` of `0105545…` is significant. Display grouped `1-4-5-2-1`. Append to `lib/format.ts`:

```ts
const EMPTY = "—";
/** 0105545000000 -> 0-1055-45000-00-0 */
export function formatTaxId(v?: string | null): string {
  const d = (v ?? "").replace(/\D/g, "");
  if (d.length !== 13) return v?.trim() || EMPTY;        // show what's there; never "undefined"
  return `${d[0]}-${d.slice(1, 5)}-${d.slice(5, 10)}-${d.slice(10, 12)}-${d[12]}`;
}
```

Document numbers (`INV-2569-0001`, `PO-0007`) are `String` too, zero-padded and sorted with `compareTh(a, b)` (its `numeric: true` puts `PO-2` before `PO-10`). Generate the running number inside the create action — never from `Date.now()` or a random suffix. Do NOT derive it from `count()`/`max()`: two people submitting at once read the same value and the second insert dies on the `@unique`. The `printable-documents` skill owns that generator (a counter row incremented inside `$transaction`); this skill only formats and sorts what it produces.

If the prototype prints an amount in Thai words (`…บาทถ้วน`), port its existing implementation as-is. Do not write a new one; do not add the line to a document that lacks it.
