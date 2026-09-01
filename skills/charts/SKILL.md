---
name: charts
description: Port a chart the prototype already has into Next.js so it survives the EMPTY database the delivered demo starts on. Use when converting a screen that renders a graph — dashboard, KPI tile, trend line, bar/donut/sparkline, `<ResponsiveContainer>`, `<LineChart>`, `<PieChart>`, a `<canvas>` graph, or a Thai screen labelled กราฟ / แดชบอร์ด / แนวโน้ม / สัดส่วน / สรุปยอด — when feeding one with Prisma `groupBy`/`aggregate`, or when a chart renders blank, crashes on `data[0]`, or shows NaN/Infinity on zero rows. Covers library choice, the server→client seam (Decimal/Date never cross it raw), zero-row and all-zero guards, ResponsiveContainer sizing, Thai axis/tooltip formatting, colors, and the text alternative every chart owes. Skip for screens with no chart (login, forms, tables, settings).
---

# charts — porting a prototype's charts onto an empty database

This is a **port**, not a redesign (`fitt-build` SKILL.md, step 4): render the chart the
prototype **already** has. Never add a chart it did not have, never upgrade a chart type
"while you're in there", never swap the palette.

The reason this guide exists: **the delivered app starts on an EMPTY database**
(prisma-setup.md §5 — mock rows are thrown away, not seeded). The prototype's charts were
never once rendered with zero rows, so every chart you port is about to meet a case its
author never wrote: a blank box, a crash on `data[0]`, an axis domain of
`[Infinity, -Infinity]`, `NaN%` from a donut dividing by zero. That is the customer's
first impression. Guard all of them and say "empty" **in Thai**; **never invent
placeholder points to make a chart look alive.** `references/chart-recipes.md` has the
longer recipes: trend spines, donut, KPI delta, sparkline, long-Thai-label axes, dark
treatment, non-Recharts libraries, verification greps.

## 1. Library — keep the prototype's

Read the prototype's imports, install **that** library (`npm i recharts`, latest — its pin
predates React 19), port the JSX prop for prop. Recharts is the common case; recipes for
Chart.js and friends are in the reference. **Hand-rolled charts stay hand-rolled** — bars
drawn as `<div style={{ width: "62%" }}>` or an inline `<svg>` are plain markup, so port
them verbatim rather than "upgrading" them to a library. **Only swap** a library that
genuinely cannot build on React 19 / Next 16, keeping chart type, colors and labels, and
declare it in `BUILD_NOTES.md` under "Not ported"; preference is not a reason. Either way
the chart file is **`"use client"`** — a chart library in a server component fails to build.

## 2. The server→client seam

Aggregate on the server, hand the client component **plain serializable data**. Prisma
`Decimal` is a class instance and never crosses the RSC boundary. `Date` technically
crosses, but formatting it client-side is a hydration bug — the container runs UTC, the
browser Asia/Bangkok, so one row lands on a different day label on each side; **bucket and
label dates on the server**. `BigInt` from `$queryRaw` does not serialize at all.

```tsx
// app/dashboard/page.tsx — server component (NO "use client")
export const dynamic = "force-dynamic";       // next build must pass with no DB running
const rows = await prisma.order.groupBy({ by: ["status"], _sum: { total: true } });
const data = rows.map((r) => ({ label: r.status, value: Number(r._sum.total ?? 0) }));
return <SalesByStatusChart data={data} />;    // [] on a fresh database — that is expected
```

`groupBy` on an empty table returns `[]`; `aggregate`'s `_sum`/`_avg`/`_max` come back
**`null`**, not `0`. Coalesce at the seam (`?? 0`), never in the JSX.

## 3. The empty state lives INSIDE the chart component

Guard in the chart itself, not in the page — the same component is rendered from several
places and only one of them will remember. Three cases, one gate: **no rows**, **one
row**, **rows whose values are all zero**.

```tsx
"use client";
import { ResponsiveContainer, BarChart, Bar, XAxis, YAxis, Tooltip } from "recharts";
import { formatTHB } from "@/lib/format";
import { axisTHB, shortLabel } from "@/lib/chart-format";

export default function SalesByStatusChart({ data }: { data: { label: string; value: number }[] }) {
  const total = data.reduce((s, d) => s + d.value, 0);
  const max = data.length ? Math.max(...data.map((d) => d.value)) : 0;   // never spread []

  if (data.length === 0 || total === 0) {        // h-72 = the chart's height: no layout jump
    return <div className="flex h-72 items-center justify-center rounded-xl border border-dashed
      border-slate-300 text-sm text-slate-500">ยังไม่มีข้อมูลสำหรับแสดงกราฟ — เพิ่มรายการแรกเพื่อดูสรุปยอด</div>;
  }
  return (
    <figure className="min-w-0">                               {/* flex/grid child: min-w-0 */}
      <div className="h-72" role="img" aria-label={`กราฟยอดขายตามสถานะ รวม ${formatTHB(total)}`}>
        <ResponsiveContainer width="100%" height="100%">       {/* % height needs the h-72 */}
          <BarChart data={data} margin={{ top: 8, right: 8, bottom: 40, left: 8 }}>
            <XAxis dataKey="label" interval={0} angle={-35} textAnchor="end" height={56} tickFormatter={shortLabel} />
            <YAxis width={72} tickFormatter={axisTHB} domain={[0, max > 0 ? "auto" : 1]} /> {/* no collapse */}
            <Tooltip formatter={(v: number) => formatTHB(v)} />
            <Bar dataKey="value" fill="#2563eb" />             {/* the prototype's own hex */}
          </BarChart>
        </ResponsiveContainer>
      </div>
      <figcaption className="mt-2 text-sm tabular-nums text-slate-600">รวม {formatTHB(total)} จาก {data.length} รายการ</figcaption>
    </figure>
  );
}
```

- **One row**: a `<Line>` through one point draws nothing — keep `dot` on (`<Line dot />`)
  so a single day of data is visible. Do not switch chart type to compensate.
- **Percentages / donuts**: `part / total` with `total === 0` is `NaN` and prints `NaN%`.
  Go through `pct()` below; let the zero-total gate render the empty state, not a donut of
  zero slices.
- **Change vs last period**: a previous period of `0` makes growth *unknown*, not
  `Infinity` — render `—` with `ไม่มีข้อมูลช่วงก่อนหน้า`, never a number.
- **The height trap** (a blank chart that is NOT an empty-data bug): `ResponsiveContainer`
  measures its parent, so `height="100%"` under a parent with no resolved height is 0px.
  Keep the one shape above — fixed height class on the wrapper — and add **`min-w-0`**
  (`min-h-0` in a `flex-col`) on a flex/grid child, whose `min-*` defaults to `auto` and
  collapses it. Never put a chart in a parent that sizes to its own content.

## 4. Thai formatting

Tooltips and captions use `formatTHB` / `formatDate` from **`lib/format.ts`** unchanged —
the same strings the tables show. Only the **axis tick** earns a chart-specific short form,
because full-width ticks collide:

```ts
// lib/chart-format.ts — chart-ONLY helpers, layered on lib/format.ts. Never redefine money
// or dates here: an axis reading ค.ศ. 2026 beside a table reading พ.ศ. 2569 IS the bug.
export const axisTHB = (v: number) =>
  `฿${v.toLocaleString("th-TH", { notation: "compact" })}`;   // ฿1.2M · ฿13K
export const axisDay = (d: Date) =>          // SERVER-side (§2); Asia/Bangkok + พ.ศ., like everything else
  new Intl.DateTimeFormat("th-TH", { timeZone: "Asia/Bangkok", day: "numeric", month: "short" }).format(d);
export const pct = (part: number, whole: number) => (whole > 0 ? (part / whole) * 100 : 0);
export const delta = (cur: number, prev: number) => (prev === 0 ? null : ((cur - prev) / prev) * 100);
export const shortLabel = (s: string) => (s.length > 12 ? `${s.slice(0, 12)}…` : s);
```

Thai has no word spaces, so a long category label never wraps — it overlaps its neighbour
or is clipped. Truncate the **tick** with `shortLabel`, keep the full name in the
`<Tooltip>`, and on horizontal bars raise `<YAxis width={…}>` past its default `60`. Put
`tabular-nums` on every number rendered as text so digits stop jittering.

## 5. Colors, and the text alternative

Take chart colors **from the ported design** — the prototype's own hex or Tailwind classes,
copied across. Keep the status palette (ดี / เตือน / วิกฤต) distinct from the series palette,
or a neutral series reads as an alarm; if the prototype had a dark treatment, draw grid and
axis strokes with `currentColor` + `strokeOpacity` (hardcoded greys vanish in it). **Every
chart owes a text alternative** — an SVG says nothing to a screen reader and a screenshot
of a chart is not a report. The `role="img"` + Thai `aria-label` and the `<figcaption>`
carrying the real total, above, are the floor: on every chart, empty state included.

## Done when

- The chart file is `"use client"` and imports no Prisma; the page reading data is
  `force-dynamic`; `npx next build` passes with **no database running**.
- Against an **empty** database every chart shows its Thai empty text in a box the same
  size as the chart — no blank space, no console error, no `NaN`, no `Infinity`.
- Chart type, colors and labels still match the prototype; no chart exists that the
  prototype did not have.
- `TEST_CASES.md` carries a case for the fresh-database dashboard — that empty text is the
  customer's literal first impression, so it gets tested like any other screen.
