# Chart recipes — the shapes a ported dashboard actually uses

Companion to `SKILL.md`. Every recipe assumes its rules: the chart is a `"use client"`
component, the page that reads data is `force-dynamic`, the data crossing the boundary is
plain (`number` / `string`), and **the first render happens on an empty database**.

`formatTHB` / `formatDate` come from **`lib/format.ts`** (the one formatter module — see the
`thai-formatting` skill); `axisTHB`, `axisDay`, `pct`, `delta`, `shortLabel` are the
chart-only helpers in `lib/chart-format.ts` (SKILL.md §4).

## 1. Trend over time — build the date spine on the SERVER

A line chart over "the last 14 days" cannot come straight from `groupBy`: days with no
rows are simply absent, so the axis jumps from the 3rd to the 9th and the trend lies.
Build the spine, then fill it.

**A zero bucket is a fact — a day on which nothing was sold really is ฿0. An invented
value is a lie.** The two are not the same thing, and only the first is allowed. But when
the table is empty *entirely*, do not draw 14 zeros: that reads as "sales collapsed".
Return `[]` and let the chart's empty state speak.

```ts
// app/dashboard/page.tsx (server component)
export const dynamic = "force-dynamic";

const DAYS = 14;
const key = toDateInput;   // lib/format.ts — "2026-09-01", the BANGKOK day, not the UTC day.
                           // Do NOT use toISOString().slice(0,10): it buckets 02:00 ICT one day early.
const start = new Date();
start.setUTCHours(0, 0, 0, 0);
start.setUTCDate(start.getUTCDate() - (DAYS - 1));

const orders = await prisma.order.findMany({
  where: { createdAt: { gte: start } },
  select: { createdAt: true, total: true },
});

// No rows at all ⇒ empty state, NOT a flat line of zeros.
const data = orders.length === 0 ? [] : (() => {
  const spine = new Map<string, number>();
  for (let i = 0; i < DAYS; i++) {
    const d = new Date(start);
    d.setUTCDate(start.getUTCDate() + i);
    spine.set(key(d), 0);                            // a day with no orders is a real ฿0
  }
  for (const o of orders) {
    const k = key(o.createdAt);
    if (spine.has(k)) spine.set(k, spine.get(k)! + Number(o.total));  // Decimal -> number
  }
  return [...spine].map(([k, value]) => ({ label: axisDay(new Date(k)), value }));
})();

return <SalesTrendChart data={data} />;
```

```tsx
"use client";
export default function SalesTrendChart({ data }: { data: { label: string; value: number }[] }) {
  if (data.length === 0) {
    return <div className="flex h-64 items-center justify-center rounded-xl border border-dashed
      border-slate-300 text-sm text-slate-500">ยังไม่มีข้อมูลใน 14 วันที่ผ่านมา</div>;
  }
  const max = Math.max(...data.map((d) => d.value));            // safe: data is non-empty here
  return (
    <div className="h-64 min-w-0" role="img" aria-label={`แนวโน้มยอดขาย 14 วัน สูงสุด ${formatTHB(max)}`}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 8, right: 16, bottom: 8, left: 8 }}>
          <XAxis dataKey="label" minTickGap={24} />
          <YAxis width={72} domain={[0, max > 0 ? "auto" : 1]} tickFormatter={axisTHB} />
          <Tooltip formatter={(v: number) => formatTHB(v)} />
          <Line type="monotone" dataKey="value" stroke="#2563eb" strokeWidth={2} dot />
        </LineChart>                                {/* dot: one data point must still be visible */}
      </ResponsiveContainer>
    </div>
  );
}
```

## 2. Donut / proportion — the divide-by-zero

A `<Pie>` whose values are all `0` renders **nothing** — not an empty message, a silently
blank circle — and every `value / total` in the tooltip becomes `NaN%`.

```tsx
"use client";
const PALETTE = ["#2563eb", "#7c3aed", "#0891b2", "#65a30d"];   // the prototype's own hexes

export default function StatusDonut({ data }: { data: { label: string; value: number }[] }) {
  const total = data.reduce((s, d) => s + d.value, 0);
  if (total === 0) {                                             // covers [] and all-zero
    return <div className="flex h-64 items-center justify-center rounded-xl border border-dashed
      border-slate-300 text-sm text-slate-500">ยังไม่มีข้อมูลสำหรับแสดงสัดส่วน</div>;
  }
  return (
    <figure className="min-w-0">
      <div className="h-64" role="img" aria-label={`สัดส่วนตามสถานะ ทั้งหมด ${total} รายการ`}>
        <ResponsiveContainer width="100%" height="100%">
          <PieChart>
            <Pie data={data} dataKey="value" nameKey="label" innerRadius="60%" outerRadius="85%" paddingAngle={2}>
              {data.map((d, i) => <Cell key={d.label} fill={PALETTE[i % PALETTE.length]} />)}
            </Pie>
            <Tooltip formatter={(v: number, n) => [`${v} (${pct(v, total).toFixed(1)}%)`, n]} />
            <Legend />
          </PieChart>
        </ResponsiveContainer>
      </div>
      <figcaption className="sr-only">
        {data.map((d) => `${d.label} ${d.value} รายการ ${pct(d.value, total).toFixed(1)}%`).join(", ")}
      </figcaption>
    </figure>
  );
}
```

## 3. KPI tile with "vs last period"

Growth against a previous period of `0` is not `Infinity` and not `100%` — it is
**unknown**, and a demo that prints `∞%` on day one loses the room.

```tsx
const d = delta(current, previous);          // null when previous === 0
<div>
  <p className="text-sm text-slate-500">ยอดขายเดือนนี้</p>
  <p className="text-2xl font-semibold tabular-nums">{formatTHB(current)}</p>
  {d === null
    ? <p className="text-sm text-slate-500">ไม่มีข้อมูลช่วงก่อนหน้าให้เปรียบเทียบ</p>
    : <p className={`text-sm tabular-nums ${d >= 0 ? "text-emerald-600" : "text-rose-600"}`}>
        {d >= 0 ? "▲" : "▼"} {Math.abs(d).toFixed(1)}% เทียบกับเดือนก่อน
      </p>}
</div>
```

## 4. Sparkline — needs two points

```tsx
if (data.length < 2) {
  return <span className="text-xs text-slate-500">ข้อมูลยังไม่พอแสดงแนวโน้ม</span>;
}
```
Then a 40px-high `<LineChart>` with no axes, no grid, no tooltip, `dot={false}`. Keep the
number it summarises rendered as text beside it — the sparkline is decoration, the figure
is the report.

## 5. Long Thai category labels

Thai has no word spaces, so labels never wrap: they overlap, or the axis clips them.

- **Vertical bars**: `<XAxis interval={0} angle={-35} textAnchor="end" height={56}
  tickFormatter={shortLabel} />` and raise the chart's bottom margin to match `height`.
- **Horizontal bars** (better for >6 long names — but only if the prototype already used
  them): `layout="vertical"`, `<XAxis type="number" />`, `<YAxis type="category"
  dataKey="label" width={140} />`. The default `width={60}` clips almost any Thai name.
- Always keep the **full** label in the `<Tooltip>`; only the tick is truncated.

## 6. Colors in both treatments

Do not hardcode greys if the prototype had a dark mode — they disappear. Two reliable
routes (CSS custom properties are not reliably substituted inside SVG *presentation
attributes*, so drive them from a real CSS rule or from `currentColor`):

```css
/* app/globals.css */
:root { --chart-grid: #e2e8f0; --chart-axis: #64748b; }
.dark { --chart-grid: #334155; --chart-axis: #94a3b8; }
.recharts-cartesian-grid line { stroke: var(--chart-grid); }
.recharts-cartesian-axis-tick text { fill: var(--chart-axis); }
```

or, per element, `stroke="currentColor" strokeOpacity={0.12}` with a Tailwind text-color
class on the wrapper. Keep ดี / เตือน / วิกฤต (green / amber / red) reserved for status;
never reuse them as ordinary series colors.

## 7. A library that is not Recharts

- **Chart.js** (`<canvas>` + a CDN `<script>` in the prototype): install locally —
  `npm i chart.js react-chartjs-2` — and keep the same `options` object. Chart.js v4 is
  tree-shaken, so **register what you use** once (`ChartJS.register(CategoryScale,
  LinearScale, BarElement, Tooltip, Legend)`) or nothing draws. Do not keep the CDN script.
- **ECharts / visx / nivo**: same rule — install the package the prototype imported.
- `dynamic(() => import(...), { ssr: false })` is not a default; reach for it only if the
  library touches `window` at import time, and call it from a client component (`ssr:
  false` is not allowed in a server component).
- Whatever the library, the guards do not change: empty gate first, no spreads over `[]`,
  no division by a zero total, a fixed-height wrapper, a Thai text alternative.

## 8. Verify before you tick the checklist

```bash
# crash shapes: an unguarded first element, a spread over a possibly-empty array, a raw ratio
grep -rn "data\[0\]\|Math\.max(\.\.\.\|Math\.min(\.\.\." app components
grep -rn "/ total\|/ previous\|/ prev" app components
# every file importing a chart library must be a client component
grep -rL '"use client"' $(grep -rl "recharts\|chart.js\|echarts" app components)
```

Then the only check that really counts: start the app against a **fresh, empty** database
and open every screen carrying a chart. Each must show its Thai empty text in a box the
size of the chart — no blank gap, no `NaN`, no `Infinity`, no console error. Add that walk
to `TEST_CASES.md`; it is the customer's first screen.
