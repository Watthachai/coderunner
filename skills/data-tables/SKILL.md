---
name: data-tables
description: Build the list, table and search screens of a ported FITT demo — filtering, sorting and pagination pushed down into Postgres via Prisma, driven by URL searchParams, plus the Thai empty states every list needs because the delivered app starts on an EMPTY database. Use when porting any screen that shows rows — ตาราง, รายการ, ค้นหา, กรอง, เรียงลำดับ, แบ่งหน้า — a data grid, a search box, a filter dropdown, a "ทั้งหมด N รายการ" count, sortable column headers, or pagination controls.
---

# data-tables — list, table, search & pagination screens

Almost every ported demo has a screen that shows rows. This skill is **how to build the one the prototype already has** — it never tells you to add a control. No search box in the prototype → do not add one; no pagination → render the whole list. What the port changes is *where the work happens*: the prototype filtered a mock array in the browser, the demo asks Postgres.

`references/table-recipes.md` holds the full copy-pasteable code for everything below (params helper, page, empty states, client controls, table markup, skeleton, row actions). Read it before you write the first table.

## The shape of every list page

```tsx
// app/rolls/page.tsx
export const dynamic = "force-dynamic";           // required: next build has no DB

export default async function Page(props: PageProps<'/rolls'>) {
  const p = parseTableParams(await props.searchParams);  // Next 16: it is a Promise
  const where: Prisma.RollWhereInput = {
    ...(p.q ? { barcode: { contains: p.q, mode: "insensitive" } } : {}),
    ...(p.status ? { status: p.status as RollStatus } : {}),
  };

  // ONE round trip, ONE snapshot — rows and total can never disagree.
  const [rows, total] = await prisma.$transaction([
    prisma.roll.findMany({
      where,
      orderBy: [{ [p.sort]: p.dir }, { id: "asc" }],  // stable tiebreaker
      skip: (p.page - 1) * p.perPage,
      take: p.perPage,
    }),
    prisma.roll.count({ where }),
  ]);

  // Page 5 of a list that shrank is a bad page number, not an empty state.
  if (rows.length === 0 && total > 0) redirect(`/rolls?${toQuery(p, { page: 1 })}`);

  if (rows.length === 0) {
    if (!p.q && !p.status) return <RollsEmpty kind="no-data" />;
    // Filters are on and matched nothing — is the table empty, or just this query?
    const anyRow = await prisma.roll.findFirst({ select: { id: true } });
    return anyRow
      ? <RollsEmpty kind={p.q ? "no-search-match" : "no-filter-match"} q={p.q} />
      : <RollsEmpty kind="no-data" />;
  }

  return <RollTable rows={rows} total={total} params={p} />;
}
```

`PageProps<'/rolls'>` is a global type Next 16 generates — no import. **Never** `findMany()` the whole table and then `.filter()` / `.sort()` / `.slice()` it: the count goes wrong the moment the table grows, and every request drags the table into memory.

## The URL is the state — not `useState`

`?q=&status=&sort=&dir=&page=` is the only place table state lives. A link to a filtered view is shareable, the back button steps through filters, refresh survives, and the server can read it. `parseTableParams` (recipes §1) returns `{ q, status, sort, dir, page, perPage }` and is where safety lives: **whitelist the sort column** (`SORTABLE.includes(raw) ? raw : "created_at"`) — a raw string into `orderBy` is a runtime 500 on the first crafted URL — clamp `page` to `>= 1`, and fix `perPage` to whatever the prototype paginated by (20 if it did not say). Client controls push a new URL with `router.replace(..., { scroll: false })` inside `useTransition`, and **always delete `page`** when `q` or a filter changes.

## Search on Thai text

`mode: "insensitive"` is Postgres `ILIKE`. Thai has no upper/lower case, so it does **nothing** for Thai characters — it only matters for the Latin parts (codes, SKUs, emails), which is reason enough to keep it. It does not normalize: "สวัสดี" typed without the tone mark will not match, so search one or two short columns rather than promising fuzzy matching. Always `q.trim()`, and **an empty `q` means no filter at all, never `contains: ""`** — a blank search box must show every row, not zero.

## Three empty states, three different messages

The delivered app starts on an EMPTY database, so the first render of every list has zero rows. That screen is the product, not an edge case. The three cases tell the user to do three different things (full markup in recipes §3):

- `no-data` — "ยังไม่มีข้อมูลม้วนผ้า" / "เริ่มต้นด้วยการเพิ่มม้วนผ้าม้วนแรกเข้าระบบ" + the button that actually fills it (`เพิ่มม้วนผ้า`). Wire it to this app's real write path — a create form, or the flow whose side effect writes these rows.
- `no-search-match` — "ไม่พบรายการที่ตรงกับ "{q}"" / "ลองตรวจสอบตัวสะกด หรือค้นหาด้วยคำที่สั้นลง" + "ล้างคำค้นหา".
- `no-filter-match` — "ไม่มีรายการที่ตรงกับตัวกรอง" / "มีข้อมูลอยู่ในระบบ แต่ไม่ตรงกับเงื่อนไขที่เลือก" + "ล้างตัวกรองทั้งหมด".

Never a blank page, never a spinner that never resolves, and never example rows to "show what it will look like".

## Sorting that stays correct across pages

- **Always append `{ id: "asc" }`** to `orderBy`. Without a tiebreaker Postgres may order ties differently per query, and a row on page 1 disappears from page 2.
- The demo's database is created with the image's default collation, not a Thai one, so `orderBy` on a Thai column sorts by code point: words starting with เ แ โ ใ ไ (U+0E40–44) land after *every* consonant-initial word. That is not dictionary order.
- Do **not** patch that with `localeCompare("th")` on the fetched page — you would reorder 20 rows out of N while the page boundaries stay wrong. Sorting must live where pagination lives. `localeCompare(b, "th")` is correct only for a list the prototype shows in full with no pagination.
- Nullable sort key → `orderBy: { shipped_at: { sort: "desc", nulls: "last" } }`, so empty rows sink instead of filling page 1.

## Markup that holds its shape

`table-fixed` + a shared `COLS` array driving `<colgroup>` (recipes §4) — the same array feeds the skeleton, so nothing shifts when data lands. Numbers get `text-right tabular-nums` or columns of digits will not line up. Long Thai text gets `truncate` plus `title={value}`. Sticky header is `sticky top-0 z-10 bg-*` on `<th>` (not on `<thead>`). Wrap the table in `<div class="overflow-x-auto">` whose **parent flex/grid child carries `min-w-0`** — without it that child's `min-width: auto` refuses to shrink and the whole page scrolls sideways instead of the table.

## Row actions

Destructive actions confirm inline — the button turns into "ยืนยันลบ" for a few seconds — never `window.confirm`, which blocks the thread and cannot be styled. The action is a server action that ends in `revalidatePath`. Two failures are normal, not 500s: the row is already gone (`P2025` → treat as done, "รายการนี้ถูกลบไปแล้ว") and the row is still referenced (`P2003` → "ลบไม่ได้ เพราะมีข้อมูลอื่นอ้างถึงรายการนี้อยู่"). Because foreign keys in this pipeline are optional, an `include`d relation can be `null`: render `{row.customer?.name ?? "— (ถูกลบแล้ว)"}` and never silently drop the row.

## Loading

The table body is an `async` child inside `<Suspense key={qs} fallback={<TableSkeleton />}>` so the page shell, header and filters paint immediately. The `key` (the serialized query string) is what makes the skeleton reappear when filters change instead of showing stale rows. The skeleton renders the same `COLS` widths with grey bars — **never placeholder text or sample rows**.

## Hard rules

- Port, don't design: build the controls the prototype has, no more. Extra columns, extra filters and "while I'm here" sorting are out of scope.
- `where` / `orderBy` / `skip` / `take` in Prisma. No `.filter()` over a full `findMany()`.
- `await props.searchParams` — Next 16 makes it a Promise; `PageProps<'/route'>` types it.
- Every list page stays `export const dynamic = "force-dynamic"` so `next build` passes with no database.
- Zero rows is a designed screen in Thai with the action that fills it — and it is the FIRST thing anyone sees on the delivered demo.
- No fake data anywhere: not in the empty state, not in the skeleton, not as a "sample" row.
- Decide defaults yourself (sort key, page size, which columns search) from the prototype and the PRD. The build is unattended — never stop to ask.
