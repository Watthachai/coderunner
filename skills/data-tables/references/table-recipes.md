# Table recipes — copy-pasteable code for list screens

Rename `Roll` / `roll` / `/rolls` to the real model and route. Every user-facing
string here is Thai on purpose — replace the wording with the prototype's own
labels, not with English.

## 1. `lib/table-params.ts` — the URL is the state

One helper, used by the page and by every control. It is also the security
boundary: a sort column that is not on the whitelist is a runtime 500 the first
time someone edits the URL.

```ts
// lib/table-params.ts
export const SORTABLE = ["created_at", "barcode", "current_length"] as const;
export type SortKey = (typeof SORTABLE)[number];

export type TableParams = {
  q: string;
  status: string;      // "" = ทั้งหมด
  sort: SortKey;
  dir: "asc" | "desc";
  page: number;        // 1-based
  perPage: number;
};

type Raw = Record<string, string | string[] | undefined>;
const one = (v: string | string[] | undefined) => (Array.isArray(v) ? v[0] : v) ?? "";

export function parseTableParams(sp: Raw): TableParams {
  const rawSort = one(sp.sort);
  const page = Number.parseInt(one(sp.page), 10);
  return {
    q: one(sp.q).trim(),
    status: one(sp.status).trim(),
    // widen to string[] so an arbitrary URL value can be tested without lying about its type
    sort: (SORTABLE as readonly string[]).includes(rawSort) ? (rawSort as SortKey) : "created_at",
    dir: one(sp.dir) === "asc" ? "asc" : "desc",
    page: Number.isFinite(page) && page > 0 ? page : 1,
    perPage: 20, // whatever the prototype paginated by
  };
}

/** Same params with some keys replaced — for links and for redirects. */
export function toQuery(p: TableParams, patch: Partial<TableParams> = {}) {
  const next = { ...p, ...patch };
  const qs = new URLSearchParams();
  if (next.q) qs.set("q", next.q);
  if (next.status) qs.set("status", next.status);
  if (next.sort !== "created_at") qs.set("sort", next.sort);
  if (next.dir !== "desc") qs.set("dir", next.dir);
  if (next.page > 1) qs.set("page", String(next.page));
  return qs.toString();
}
```

Defaults stay out of the URL so a plain `/rolls` is the canonical view.

## 2. `app/rolls/page.tsx` — the query and the zero-row path

```tsx
import { Suspense } from "react";
import { redirect } from "next/navigation";
import type { Prisma, RollStatus } from "@prisma/client";
import { prisma } from "@/lib/prisma";
import { parseTableParams, toQuery, type TableParams } from "@/lib/table-params";
import RollTable from "@/components/rolls/RollTable";
import TableSkeleton from "@/components/rolls/TableSkeleton";
import RollsEmpty from "@/components/rolls/RollsEmpty";
import RollFilters from "@/components/rolls/RollFilters";

export const dynamic = "force-dynamic"; // next build must not touch the DB

export default async function Page(props: PageProps<'/rolls'>) {
  const p = parseTableParams(await props.searchParams); // Next 16: a Promise
  return (
    <main className="flex min-h-screen flex-col gap-4 p-6">
      <h1 className="text-xl font-semibold">ทะเบียนม้วนผ้า</h1>
      <RollFilters params={p} />
      {/* key = the query string, so the skeleton returns when filters change */}
      <Suspense key={toQuery(p)} fallback={<TableSkeleton />}>
        <RollSection params={p} />
      </Suspense>
    </main>
  );
}

async function RollSection({ params: p }: { params: TableParams }) {
  const where: Prisma.RollWhereInput = {
    ...(p.q ? { barcode: { contains: p.q, mode: "insensitive" } } : {}),
    ...(p.status ? { status: p.status as RollStatus } : {}),   // generated enum type
  };

  // One round trip, one snapshot: rows and total can never disagree.
  const [rows, total] = await prisma.$transaction([
    prisma.roll.findMany({
      where,
      include: { fabric: true },
      orderBy: [{ [p.sort]: p.dir }, { id: "asc" }], // stable tiebreaker
      skip: (p.page - 1) * p.perPage,
      take: p.perPage,
    }),
    prisma.roll.count({ where }),
  ]);

  // A page number past the end of a shrinking list is not an empty state.
  if (rows.length === 0 && total > 0) redirect(`/rolls?${toQuery(p, { page: 1 })}`);

  if (rows.length === 0) {
    const filtered = Boolean(p.q || p.status);
    if (!filtered) return <RollsEmpty kind="no-data" />;
    // Filters matched nothing — is the whole table empty, or just this query?
    // LIMIT 1, and only on the path that already returned nothing.
    const anyRow = await prisma.roll.findFirst({ select: { id: true } });
    if (!anyRow) return <RollsEmpty kind="no-data" />;
    return <RollsEmpty kind={p.q ? "no-search-match" : "no-filter-match"} q={p.q} />;
  }

  return <RollTable rows={rows} total={total} params={p} />;
}
```

Anti-pattern to delete on sight — it is what the prototype did, and it breaks
both the count and the memory profile:

```ts
const all = await prisma.roll.findMany();                 // ❌ whole table
const rows = all.filter((r) => r.barcode.includes(q)).slice(0, 20); // ❌
```

## 3. `components/rolls/RollsEmpty.tsx` — three states, three instructions

"ยังไม่มีข้อมูล" and "ไม่พบผลการค้นหา" ask the user to do different things: fill
the table, or fix the query. One shared component, three copies of text.

```tsx
import Link from "next/link";

type Kind = "no-data" | "no-search-match" | "no-filter-match";

const COPY: Record<Kind, { title: string; body: string; action: string; href: string }> = {
  "no-data": {
    title: "ยังไม่มีข้อมูลม้วนผ้า",
    body: "เริ่มต้นด้วยการเพิ่มม้วนผ้าม้วนแรกเข้าระบบ",
    action: "เพิ่มม้วนผ้า",
    href: "/rolls/new",           // this app's REAL write path
  },
  "no-search-match": {
    title: "ไม่พบรายการที่ค้นหา",
    body: "ลองตรวจสอบตัวสะกด หรือค้นหาด้วยคำที่สั้นลง",
    action: "ล้างคำค้นหา",
    href: "/rolls",
  },
  "no-filter-match": {
    title: "ไม่มีรายการที่ตรงกับตัวกรอง",
    body: "มีข้อมูลอยู่ในระบบ แต่ไม่ตรงกับเงื่อนไขที่เลือกไว้",
    action: "ล้างตัวกรองทั้งหมด",
    href: "/rolls",
  },
};

export default function RollsEmpty({ kind, q }: { kind: Kind; q?: string }) {
  const c = COPY[kind];
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed p-12 text-center">
      <p className="text-base font-medium">
        {kind === "no-search-match" && q ? `ไม่พบรายการที่ตรงกับ "${q}"` : c.title}
      </p>
      <p className="text-sm text-neutral-500">{c.body}</p>
      <Link href={c.href} className="mt-2 rounded-md bg-neutral-900 px-4 py-2 text-sm text-white">
        {c.action}
      </Link>
    </div>
  );
}
```

The `no-data` action must point at something that really writes rows — a create
form, or the screen whose flow writes them as a side effect (a check-in that
writes a stock movement). An empty state whose button goes nowhere is worse than
no button.

## 4. `components/rolls/RollTable.tsx` — columns that line up

`COLS` is declared once and reused by the skeleton, which is what stops the
layout jumping when data arrives. `table-fixed` is required for `<col>` widths
to be honoured.

```tsx
export const COLS = [
  { key: "barcode",        label: "บาร์โค้ด",     width: "w-40",  align: "text-left"  },
  { key: "fabric",         label: "ชนิดผ้า",      width: "w-64",  align: "text-left"  },
  { key: "current_length", label: "คงเหลือ (หลา)", width: "w-32", align: "text-right" },
  { key: "status",         label: "สถานะ",        width: "w-32",  align: "text-left"  },
  { key: "actions",        label: "",             width: "w-28",  align: "text-right" },
] as const;
```

```tsx
// min-w-0 on the flex child is what keeps the PAGE from scrolling sideways
<div className="min-w-0">
  <div className="overflow-x-auto rounded-lg border">
    <table className="w-full min-w-[880px] table-fixed border-collapse text-sm">
      <colgroup>{COLS.map((c) => <col key={c.key} className={c.width} />)}</colgroup>
      <thead>
        <tr>
          {COLS.map((c) => (
            <th key={c.key}
                className={`sticky top-0 z-10 bg-white px-3 py-2 font-medium ${c.align}`}>
              <SortHeader col={c} params={p} />
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.id} className="border-t">
            <td className="truncate px-3 py-2 font-mono" title={r.barcode}>{r.barcode}</td>
            {/* optional FK: the related row may have been deleted */}
            <td className="truncate px-3 py-2" title={r.fabric?.name ?? ""}>
              {r.fabric?.name ?? "— (ถูกลบแล้ว)"}
            </td>
            <td className="px-3 py-2 text-right tabular-nums">
              {r.current_length.toFixed(2)}
            </td>
            <td className="px-3 py-2">{r.status}</td>
            <td className="px-3 py-2 text-right"><RowActions id={r.id} /></td>
          </tr>
        ))}
      </tbody>
    </table>
  </div>
  <p className="px-1 py-2 text-sm text-neutral-500">
    ทั้งหมด {total.toLocaleString("th-TH")} รายการ · หน้า {p.page} จาก{" "}
    {Math.max(1, Math.ceil(total / p.perPage))}
  </p>
</div>
```

`sticky top-0` belongs on `<th>`, not `<thead>` — a sticky `<thead>` does nothing
in most browsers. `tabular-nums` is not optional on numeric columns: without it
digits have different widths and the column reads as ragged.

`RollTable` stays a **server** component; only `RowActions` is `"use client"`.
That matters for `Decimal` and `Date` columns: a Prisma `Decimal` is a class
instance, so handing a whole row to a client component fails at runtime with
"Only plain objects can be passed to Client Components". If the ported table
really must be a client component, map the rows first —
`{ ...r, current_length: r.current_length.toFixed(2) }` — and format on the
server, never in the browser.

Sortable headers are plain links — no client component needed:

```tsx
function SortHeader({ col, params: p }: { col: (typeof COLS)[number]; params: TableParams }) {
  if (!(SORTABLE as readonly string[]).includes(col.key)) return <>{col.label}</>;
  const active = p.sort === col.key;
  const dir = active && p.dir === "asc" ? "desc" : "asc";
  return (
    <Link href={`/rolls?${toQuery(p, { sort: col.key as SortKey, dir, page: 1 })}`}>
      {col.label}{active ? (p.dir === "asc" ? " ↑" : " ↓") : ""}
    </Link>
  );
}
```

## 5. Sorting Thai correctly (and when you cannot)

- **Tiebreaker is mandatory.** `orderBy: [{ status: "asc" }, { id: "asc" }]`.
  Ties without a tiebreaker come back in whatever order Postgres feels like, so
  the same row can appear on page 1 and page 2 — or on neither.
- **The container's Postgres has no Thai collation.** `orderBy` on a Thai text
  column compares UTF-8 code points, so words beginning เ แ โ ใ ไ (U+0E40–U+0E44)
  sort after every consonant-initial word. Thai dictionary order files them under
  the following consonant. Accept it for paginated lists; it is consistent and
  the alternative is worse.
- **Never "fix" it in JS on a paginated page.** `rows.sort((a, b) =>
  a.name.localeCompare(b.name, "th"))` reorders the 20 rows you fetched while the
  page boundaries stay in code-point order — the list now looks sorted and is
  wrong. Sorting has to live where the pagination lives.
- **Small unpaginated lists may use `localeCompare`.** If the prototype shows the
  whole lookup table with no pager, `findMany()` then
  `.sort((a, b) => a.name.localeCompare(b.name, "th"))` is correct — Node ships
  full ICU, so the `"th"` locale really works.
- **Nullable keys:** `orderBy: { shipped_at: { sort: "desc", nulls: "last" } }`
  (Postgres) so rows without a date sink instead of filling page 1.

## 6. `components/rolls/RollFilters.tsx` — controls that write the URL

```tsx
"use client";
import { useRef, useTransition } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";

export default function RollFilters({ params }: { params: TableParams }) {
  const router = useRouter();
  const pathname = usePathname();
  const sp = useSearchParams();
  const [pending, startTransition] = useTransition();
  // React 19 requires an initial value for useRef
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  function push(key: "q" | "status", value: string) {
    const next = new URLSearchParams(sp.toString());
    if (value.trim()) next.set(key, value.trim());
    else next.delete(key);
    next.delete("page"); // a new query ALWAYS returns to page 1
    startTransition(() => router.replace(`${pathname}?${next}`, { scroll: false }));
  }

  return (
    <div className="flex items-center gap-2" data-pending={pending}>
      <input
        type="search"
        defaultValue={params.q}          // uncontrolled: the round trip must not fight typing
        placeholder="ค้นหาบาร์โค้ด"
        aria-label="ค้นหาบาร์โค้ด"
        className="rounded-md border px-3 py-2 text-sm"
        onChange={(e) => {
          const v = e.target.value;
          clearTimeout(timer.current);
          timer.current = setTimeout(() => push("q", v), 300);
        }}
      />
      <select
        defaultValue={params.status}
        aria-label="กรองตามสถานะ"
        className="rounded-md border px-3 py-2 text-sm"
        onChange={(e) => push("status", e.target.value)}
      >
        <option value="">ทุกสถานะ</option>
        <option value="FULL">เต็มม้วน</option>
        <option value="REMNANT">เศษผ้า</option>
        <option value="ISSUED">จ่ายออกแล้ว</option>
      </select>
    </div>
  );
}
```

`replace`, not `push`, so typing does not bury the back button under one entry
per keystroke. Pagination links are plain `<Link href={`/rolls?${toQuery(p, {
page: p.page + 1 })}`}>` — no client component.

## 7. `components/rolls/TableSkeleton.tsx`

Same `COLS`, same widths, grey bars. Never sample text, never fake rows.

```tsx
import { COLS } from "./RollTable";

export default function TableSkeleton() {
  return (
    <div className="min-w-0">
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full min-w-[880px] table-fixed text-sm">
          <colgroup>{COLS.map((c) => <col key={c.key} className={c.width} />)}</colgroup>
          <thead>
            <tr>{COLS.map((c) => (
              <th key={c.key} className={`px-3 py-2 font-medium ${c.align}`}>{c.label}</th>
            ))}</tr>
          </thead>
          <tbody>
            {Array.from({ length: 8 }, (_, i) => (
              <tr key={i} className="border-t">
                {COLS.map((c) => (
                  <td key={c.key} className="px-3 py-2">
                    <div className="h-4 animate-pulse rounded bg-neutral-200" />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
```

## 8. Row actions and destructive ones

The server action, with the two failures that are normal rather than 500s:

```ts
"use server";
import { Prisma } from "@prisma/client";
import { revalidatePath } from "next/cache";
import { prisma } from "@/lib/prisma";

export async function deleteRoll(id: string) {
  try {
    await prisma.roll.delete({ where: { id } });
  } catch (e) {
    if (e instanceof Prisma.PrismaClientKnownRequestError) {
      // P2025: someone already deleted it (stale page, double click) — done, not an error.
      if (e.code === "P2025") {
        revalidatePath("/rolls");
        return { ok: true, message: "รายการนี้ถูกลบไปแล้ว" };
      }
      // P2003: still referenced by another table.
      if (e.code === "P2003") {
        return { ok: false, message: "ลบไม่ได้ เพราะมีข้อมูลอื่นอ้างถึงรายการนี้อยู่" };
      }
    }
    throw e;
  }
  revalidatePath("/rolls");
  return { ok: true };
}
```

Confirmation is inline and non-blocking — `window.confirm` freezes the thread,
cannot be styled, and reads as a browser error:

```tsx
"use client";
import { useState, useTransition } from "react";
import { deleteRoll } from "@/app/rolls/actions";

export default function RowActions({ id }: { id: string }) {
  const [armed, setArmed] = useState(false);
  const [msg, setMsg] = useState("");
  const [pending, start] = useTransition();

  if (!armed) {
    return (
      <>
        <button onClick={() => { setArmed(true); setTimeout(() => setArmed(false), 4000); }}
                className="text-sm text-red-600">ลบ</button>
        {msg && <span className="ml-2 text-xs text-neutral-500">{msg}</span>}
      </>
    );
  }
  return (
    <span className="flex justify-end gap-2 text-sm">
      <button disabled={pending}
              onClick={() => start(async () => {
                const r = await deleteRoll(id);
                setArmed(false);
                setMsg(r.message ?? "");
              })}
              className="font-medium text-red-600">
        {pending ? "กำลังลบ…" : "ยืนยันลบ"}
      </button>
      <button onClick={() => setArmed(false)} className="text-neutral-500">ยกเลิก</button>
    </span>
  );
}
```

## 9. Checklist for a list screen

- [ ] `export const dynamic = "force-dynamic"` on the page; `next build` passes with no DB.
- [ ] `await props.searchParams`, typed `PageProps<'/route'>`.
- [ ] `where` / `orderBy` / `skip` / `take` in Prisma — no `.filter()` over a full `findMany()`.
- [ ] `$transaction([findMany, count])`, one snapshot.
- [ ] `orderBy` ends with a stable `{ id: "asc" }`; the sort key is whitelisted.
- [ ] Empty `q` means no filter; `q` is trimmed.
- [ ] All three empty states exist, in Thai, and `no-data`'s button reaches a real write path.
- [ ] Out-of-range `page` redirects to page 1 instead of rendering an empty state.
- [ ] Skeleton shares `COLS` with the table; numeric columns are `tabular-nums`.
- [ ] The table scrolls sideways, the page does not (`min-w-0` on the flex/grid child).
- [ ] Delete confirms inline and survives `P2025` / `P2003`; optional relations render `null` safely.
- [ ] Only the controls the prototype had. Nothing invented, no sample rows anywhere.
