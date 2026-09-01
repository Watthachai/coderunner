# Thai dates, text and form fields

## 1. พ.ศ. vs ค.ศ. — `th-TH` already gives you Buddhist era

`Intl.DateTimeFormat("th-TH", …)` uses the **Buddhist calendar by default**. `formatDate()` from `lib/format.ts` therefore prints `15/01/2569` with no arithmetic from you.

**Never write `year + 543` on top of it.** `new Date().toLocaleDateString("th-TH")` is already 2569; adding 543 gives 3112. If you ever see a year near 3100, this is why.

| Where | Era | How |
|---|---|---|
| Screen labels, tables, ใบแจ้งหนี้/ใบเสร็จ/รายงาน | พ.ศ. | `formatDate` / `formatDateLong` |
| `<input type="date">` value, CSV export, an id or slug | ค.ศ. (ISO) | `toDateInput` |
| A ค.ศ. date shown deliberately (import files, API echoes) | ค.ศ. | `new Intl.DateTimeFormat("th-TH-u-ca-gregory", { timeZone: "Asia/Bangkok", … })` |
| Anything sorted or compared | neither | compare `Date`/`DateTime` values, never formatted strings |

## 2. The off-by-one day

The container runs on UTC. Postgres stores the instant, Prisma hands back a UTC `Date`, and a server component formats it in the server's zone:

- Row created **15 Jan 2026, 02:00 ICT** → stored `2026-01-14T19:00Z` → rendered without `timeZone` as **14/01/2569**. Wrong day, every time, for anything entered before 07:00.
- Worse, the client re-renders in the user's zone and React reports a hydration mismatch.

Fix: every `Intl.DateTimeFormat` in the app passes `timeZone: "Asia/Bangkok"` — which is why they all live in `lib/format.ts` and nothing calls `toLocaleDateString()` inline. Also never use `date.toISOString().slice(0, 10)` for a date input: it is the UTC day, so it drops back one day for the same reason. Use `toDateInput` / `fromDateInput`.

Date-only fields (วันที่ครบกำหนด, วันเกิด) still store a `DateTime`; `fromDateInput` anchors them to `T00:00:00+07:00` so the day survives the round trip.

## 3. Thai text has no spaces

Consequences, in order of how often they bite:

- **`break-all` / `break-words` mangle Thai.** The whole sentence is one unbroken run, so those utilities cut it at an arbitrary character and it reads as nonsense. Drop them from any element holding Thai copy.
- **`<html lang="th">` is what enables the browser's Thai dictionary line-breaker.** Without it the browser guesses, and long Thai runs overflow their column instead of wrapping at word boundaries.
- **Truncate with CSS, not `String.slice`.** `slice` cuts between a consonant and its combining สระ/วรรณยุกต์: the head silently loses the mark (a different word), and any remainder starts with an orphaned one — `"สวัสดี".slice(2)` is `"ัสดี"`, which renders as a floating mark on a dotted circle. Use `truncate` (needs a width or `min-w-0` in a flex row) or `line-clamp-2`. If a string genuinely must be shortened in JS:

```ts
export function truncateTh(s: string, max: number): string {
  const g = [...new Intl.Segmenter("th", { granularity: "grapheme" }).segment(s)];
  return g.length <= max ? s : g.slice(0, max).map((x) => x.segment).join("") + "…";
}
```

- **Sorting.** Code-point order puts every word starting with เ แ โ ใ ไ after ฮ, so a dropdown of Thai names looks shuffled. Use `compareTh` from `lib/format.ts`. Postgres's own collation is not Thai either, so `orderBy: { name: "asc" }` has the same defect — for a user-visible list of Thai names, fetch and then sort in the server component:

```ts
const rows = await prisma.customer.findMany();
rows.sort((a, b) => compareTh(a.name, b.name));
```

Keep `orderBy` in the query for dates, numbers and ids — it is only Thai text that needs the collator.

**Do not break pagination to get this.** Sorting in JS only sorts the rows you fetched, so it is correct for a full list at demo scale and WRONG for a paginated query with `skip`/`take`. If the screen pages through the DB, leave the sort in Postgres and accept its collation; if the list is small and fully fetched, use `compareTh`.

## 4. Fonts, line-height, print

Latin faces have no Thai glyphs, so a Thai UI on a machine without a Thai font renders tofu (▯▯▯).

```tsx
// app/layout.tsx
import { Noto_Sans_Thai } from "next/font/google";
const thai = Noto_Sans_Thai({ subsets: ["thai", "latin"], display: "swap", variable: "--font-thai" });

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="th" className={thai.variable}>
      <body className="leading-7">{children}</body>
    </html>
  );
}
```

```css
/* app/globals.css — Tailwind v4 */
@theme { --font-sans: var(--font-thai), ui-sans-serif, system-ui, sans-serif; }
```

- `Noto_Sans_Thai` is a variable font, so omit `weight`. A static Thai face (`Sarabun`, `IBM_Plex_Sans_Thai`, `Prompt`) requires an explicit `weight: ["400", "500", "700"]`. `Kanit`/`Prompt` are display faces — headings only; keep tables on a text face.
- Match the prototype's own font when it names one; this is styling, and the port rule applies.
- `next/font/google` downloads at build time. The build already has network for `npm ci`, so it works — do not add a CDN `<link>` as a backup path.
- **Line-height ≥ 1.5** (`leading-7`, or `leading-6` at `text-sm`). Thai stacks vowels above and below the baseline; `leading-tight` clips them in dense table rows.
- **Print / PDF:** if the prototype has a print button, keep `window.print()` plus a `@media print` block — and do **not** reset `font-family` to `serif` there, which is the usual way Thai turns into tofu on the printed page. Do not add server-side PDF generation: it is a new feature, and the standalone image ships no Thai system fonts, so it would print boxes.

## 5. Field shapes as Thai forms actually have them

Only mirror the fields the prototype already has — this section is about their **shape**, not about adding any.

| Field | Type | Rules |
|---|---|---|
| เบอร์โทรศัพท์ | `String` | Store digits only; `inputMode="tel"`, `maxLength={10}`. Format on display. |
| รหัสไปรษณีย์ | `String` | Exactly 5 digits; `inputMode="numeric"`, `maxLength={5}`. Never `Int`. |
| เลขประจำตัวผู้เสียภาษี | `String` | 13 digits — see `money-and-vat.md` §5. |
| ที่อยู่ | separate columns | ตำบล/แขวง, อำเภอ/เขต, จังหวัด, รหัสไปรษณีย์ stay separate if the prototype separates them. Do not fold them into a Western `city/state/zip`. |
| ชื่อ | as the prototype has it | Thai has no middle name and คำนำหน้า (นาย/นาง/นางสาว) is usually its own field. Do not split one name field into first/last. |

```ts
const EMPTY = "—";
/** 0812345678 -> 081-234-5678 · 021234567 -> 02-123-4567 · 053123456 -> 053-123-456 */
export function formatPhone(v?: string | null): string {
  const d = (v ?? "").replace(/\D/g, "");
  if (d.length === 10) return `${d.slice(0, 3)}-${d.slice(3, 6)}-${d.slice(6)}`;
  if (d.length === 9)
    return d.startsWith("02")
      ? `${d.slice(0, 2)}-${d.slice(2, 5)}-${d.slice(5)}`   // Bangkok landline
      : `${d.slice(0, 3)}-${d.slice(3, 6)}-${d.slice(6)}`;  // provincial landline
  return v?.trim() || EMPTY;
}
```

Every one of these formatters returns `—` rather than `undefined` when the column is empty, because on a fresh database most of them are.
