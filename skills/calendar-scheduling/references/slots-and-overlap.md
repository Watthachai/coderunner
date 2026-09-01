# slots-and-overlap — the code the calendar screens paste

Everything here assumes `lib/format.ts` from `thai-formatting` (`formatDate`, `formatDateTime`,
`toDateInput`, `fromDateInput`) already exists. Import from it; do not re-derive a formatter.

## 1. `lib/schedule.ts` — the whole time layer

```ts
// Bangkok day boundaries as real UTC instants. The offset is written here rather than
// imported: lib/format.ts exports formatters, not constants.
const ICT = "+07:00";                       // fixed year-round — Thailand has no DST
export const DAY_MS = 86_400_000;

/** "2026-01-15" (a Bangkok day) -> the instant that day starts, in UTC. */
export const dayStart = (ymd: string) => new Date(`${ymd}T00:00:00${ICT}`);
/** Half-open end: the NEXT day's start. Never 23:59:59.999. */
export const dayEnd = (ymd: string) => new Date(dayStart(ymd).getTime() + DAY_MS);

/** "2026-01-15" + "09:30" -> a UTC instant. Both halves come from <input>. */
export const at = (ymd: string, hhmm: string) => new Date(`${ymd}T${hhmm}:00${ICT}`);

/** Minutes added to an instant. Duration is ALWAYS integer minutes, never float hours. */
export const plusMinutes = (d: Date, m: number) => new Date(d.getTime() + m * 60_000);

/** Do [aStart, aEnd) and [bStart, bEnd) overlap? Strict on both sides:
 *  10:00-11:00 and 11:00-12:00 do NOT overlap. */
export const overlaps = (aS: Date, aE: Date, bS: Date, bE: Date) => aS < bE && bS < aE;
```

## 2. Month grid — six fixed rows, Sunday first

```ts
/** 42 cells covering the whole visible month, including adjacent-month days. */
export function monthCells(year: number, month: number): Date[] {
  const first = new Date(Date.UTC(year, month, 1));
  const lead = first.getUTCDay();                       // 0 = อาทิตย์
  return Array.from({ length: 42 }, (_, i) => new Date(Date.UTC(year, month, 1 - lead + i)));
}

export const DOW_TH = ["อา", "จ", "อ", "พ", "พฤ", "ศ", "ส"];
export const inMonth = (d: Date, month: number) => d.getUTCMonth() === month;
```

Six rows always. Sizing the grid to the month (four, five or six rows) makes the page jump
every time the user pages through months, and the last row disappears in some months.

Cells are built in `Date.UTC` so the *grid* never shifts with a local timezone, while every
visible string still goes through `formatDate` (which renders in `Asia/Bangkok`, in พ.ศ.).
Never write `getFullYear() + 543` anywhere.

## 3. Loading the visible month in one query

```ts
export const dynamic = "force-dynamic";                 // required: next build has no DB

const cells = monthCells(year, month);
const from = cells[0], to = new Date(cells[41].getTime() + DAY_MS);

const rows = await prisma.booking.findMany({
  where: { startAt: { lt: to }, endAt: { gt: from }, status: { not: "CANCELLED" } },
  orderBy: [{ startAt: "asc" }, { id: "asc" }],
  include: { room: true },
});

// Group by the BANGKOK day, not by getDate().
const byDay = new Map<string, typeof rows>();
for (const r of rows) {
  const k = toDateInput(r.startAt);
  const list = byDay.get(k);
  if (list) list.push(r);
  else byDay.set(k, [r]);
}
```

Querying `from`/`to` from the 42 cells (not the 1st to the 31st) is what keeps bookings
visible in the grey leading and trailing days.

A booking spanning midnight appears only on its start day with this grouping. If the
prototype showed it on both days, expand it per day it touches — and only then.

## 4. Slot generator

```ts
/** Fixed slots for one day: 09:00, 09:30, … up to (but not including) closing. */
export function slotsFor(ymd: string, openHHMM: string, closeHHMM: string, minutes: number) {
  const out: { start: Date; end: Date }[] = [];
  const close = at(ymd, closeHHMM);
  for (let s = at(ymd, openHHMM); plusMinutes(s, minutes) <= close; s = plusMinutes(s, minutes)) {
    out.push({ start: s, end: plusMinutes(s, minutes) });
  }
  return out;
}

/** Mark each slot free/taken against the day's bookings — in memory is fine here,
 *  because it is ONE day of rows, already fetched. */
export const markSlots = (slots: { start: Date; end: Date }[], booked: { startAt: Date; endAt: Date }[]) =>
  slots.map((s) => ({ ...s, taken: booked.some((b) => overlaps(s.start, s.end, b.startAt, b.endAt)) }));
```

Opening hours and slot length come from the prototype and the PRD. If neither states them,
use what the prototype visibly rendered and record the choice in `BUILD_NOTES.md`.

## 5. Create and reschedule — one action, one shape

```ts
"use server";
export async function saveBooking(input: {
  id?: string; roomId: string; ymd: string; startHHMM: string; minutes: number;
}) {
  const startAt = at(input.ymd, input.startHHMM);
  const endAt = plusMinutes(startAt, input.minutes);

  if (endAt <= startAt) return { error: "เวลาสิ้นสุดต้องมาหลังเวลาเริ่มต้น" };
  if (startAt < new Date()) return { error: "ไม่สามารถจองย้อนหลังได้" };

  const clash = await prisma.booking.findFirst({
    where: {
      roomId: input.roomId,
      status: { not: "CANCELLED" },
      ...(input.id ? { id: { not: input.id } } : {}),     // reschedule ignores itself
      startAt: { lt: endAt },
      endAt: { gt: startAt },
    },
    select: { id: true, startAt: true, endAt: true },
  });
  if (clash) {
    return { error: `ช่วงเวลานี้ทับกับการจองเดิม (${formatDateTime(clash.startAt)})` };
  }

  try {
    const data = { roomId: input.roomId, startAt, endAt };
    input.id
      ? await prisma.booking.update({ where: { id: input.id }, data })
      : await prisma.booking.create({ data: { ...data, status: "CONFIRMED" } });
  } catch (e: any) {
    if (e.code === "P2002") return { error: "ช่วงเวลานี้ถูกจองไปแล้ว กรุณาเลือกเวลาอื่น" };
    if (e.code === "P2025") return { error: "ไม่พบการจองนี้ อาจถูกยกเลิกไปแล้ว" };
    throw e;
  }
  revalidatePath("/calendar");
  return { ok: true };
}
```

The error strings above are placeholders for the PRD's own text — copy the real Thai
messages from `การควบคุมความถูกต้องของข้อมูล (Validation & Edge Cases)` verbatim.

`P2002` stays even with the pre-check: it is the only thing that actually stops two people
booking the same slot at the same moment.

## 6. Cancel, don't delete

```ts
await prisma.booking.update({ where: { id }, data: { status: "CANCELLED" } });
```

Cancelling frees the slot (every availability query filters `status: { not: "CANCELLED" }`)
while the row survives for the history screen. Hard-delete only if the prototype had no
cancelled state at all. Confirm inline — the button becomes "ยืนยันยกเลิก" for a few
seconds — never `window.confirm`, which blocks the thread and cannot be styled.

## 7. Week view

```ts
/** Sunday of the week containing `ymd`, as a Bangkok day string. */
export function weekStart(ymd: string) {
  // Build a PURE calendar date. Do NOT use dayStart() here: it returns 17:00 UTC on the
  // PREVIOUS day, so getUTCDay() would be one day early and every week start slips back.
  const [y, m, d] = ymd.split("-").map(Number);
  const cal = new Date(Date.UTC(y, m - 1, d));
  const back = new Date(cal.getTime() - cal.getUTCDay() * DAY_MS);
  return toDateInput(back);        // UTC midnight -> 07:00 Bangkok, same calendar day
}
```

Lay the week out as a CSS grid of 7 columns × N rows of slots, positioning each booking by
`(startAt - dayStart) / slotMinutes`. Absolute pixel offsets computed from `getHours()` are
what break the layout at 00:00 and across the month edge — position from the same instants
the query used.

Wrap a horizontally scrolling week in `<div class="overflow-x-auto">` whose parent flex/grid
child carries `min-w-0`, or the page scrolls sideways instead of the calendar.

## 8. Empty states — and the one that is NOT empty

The grid renders normally with zero bookings; it is the content. Write empty states only for
the lists beside it:

| situation | message | action |
|---|---|---|
| day pane, no bookings | `ไม่มีนัดหมายในวันนี้` | `เพิ่มนัดหมาย` → the real create form |
| every slot taken | `ช่วงเวลาของวันนี้ถูกจองหมดแล้ว` | `ลองเลือกวันอื่น` |
| resource filter matches nothing | `ไม่พบห้อง/พนักงานที่ตรงกับตัวกรอง` | `ล้างตัวกรอง` |

Never sample appointments, never a skeleton that renders fake blocks — grey bars only.
