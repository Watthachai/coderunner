---
name: calendar-scheduling
description: Build the calendar, booking and appointment screens of a ported FITT demo — month/week grids, available time slots, overlap (double-booking) checks pushed into Postgres, and reschedule/cancel — with Bangkok-correct times and the Thai empty states a calendar needs on a database that starts EMPTY. Use when porting any screen with ปฏิทิน, ตารางนัด, จอง/การจอง, นัดหมาย, ตารางเวลา, ช่วงเวลาว่าง, เลื่อนนัด, ยกเลิกนัด, เช็คอิน/เช็คเอาท์ by date, a month or week grid, a date/time picker that must not collide with an existing booking, a resource/room/staff timetable, or a "ว่าง / ไม่ว่าง / เต็ม" slot list. Skip when a screen merely displays a date column with no availability, no grid and no booking — that is thai-formatting plus data-tables.
---

# calendar-scheduling — grids, slots and double-booking on an empty database

A booking screen breaks in ways a list screen does not: the grid shows the wrong year, back-to-back appointments report a clash, a slot gets sold twice, and every time is off by seven hours. This skill is **how to port the calendar the prototype already has** — it never tells you to add one. No week view in the prototype → do not build one; no cancel button → do not add one.

**Lane.** `thai-formatting` owns the **values**: `lib/format.ts` has `formatDate`, `formatDateTime`, `toDateInput`, `fromDateInput` and the `Asia/Bangkok` constant. Import them. Never write a second date formatter here — that is how one screen renders 2569 while the panel beside it renders 2026. This skill owns the **grid, the availability query and the write path**. A table of bookings is `data-tables`; a printed schedule is `printable-documents`.

`references/slots-and-overlap.md` carries the full pasteable code: month-grid builder, slot generator, the overlap query, the reschedule action, and the week view.

## Times: store UTC, compute and render in Bangkok

The container clock is UTC. Thailand is **ICT, UTC+7, with no daylight saving at all** — no spring-forward gap, no ambiguous hour — so a fixed `+07:00` offset is always correct here, which is not true of most timezones. Take the simplification, but take it explicitly:

```ts
// lib/schedule.ts — Bangkok day boundaries as real UTC instants.
// The offset is written here, not imported: lib/format.ts keeps its TZ private and
// exports formatters, not constants. One literal, one file, one comment saying why.
const ICT = "+07:00";                      // fixed year-round — Thailand has no DST

/** "2026-01-15" (a Bangkok day) -> the instant that day starts, in UTC. */
export const dayStart = (ymd: string) => new Date(`${ymd}T00:00:00${ICT}`);
/** Half-open: the NEXT day's start. Never 23:59:59.999 — that leaks a millisecond. */
export const dayEnd = (ymd: string) => new Date(dayStart(ymd).getTime() + 86_400_000);
```

Store every `startAt` / `endAt` as Prisma `DateTime` (UTC in the column). **Never** `toISOString().slice(0, 10)` to get "the date" — at 21:00 in Bangkok that returns yesterday. Use `toDateInput` from `lib/format.ts`, which formats in `Asia/Bangkok`.

## The grid is built from ค.ศ. and labelled in พ.ศ.

`Date` arithmetic is Gregorian: `getFullYear()` on January 2026 returns `2026`. `th-TH` formatting is Buddhist: the same month prints `มกราคม 2569`. Both are right. The bug is mixing them — a header built with `${MONTHS[m]} ${d.getFullYear() + 543}` next to a cell formatted by `Intl` drifts the moment anyone touches the arithmetic.

**Do the maths on `Date`, produce every visible string through `lib/format.ts`.** Never add 543 by hand anywhere.

```tsx
// Weeks start SUNDAY on a Thai calendar. Six rows always, so the grid never reflows.
const first = new Date(Date.UTC(year, month, 1));
const lead  = first.getUTCDay();                 // 0 = อาทิตย์
const cells = Array.from({ length: 42 }, (_, i) =>
  new Date(Date.UTC(year, month, 1 - lead + i)));
const DOW = ["อา", "จ", "อ", "พ", "พฤ", "ศ", "ส"];
```

Build cells in `Date.UTC` so no local-timezone shift creeps into the grid, then render each with `formatDate`. Days outside the month keep their cell and get muted styling — dropping them collapses the row.

## Overlap: half-open intervals, checked in Postgres

Two ranges collide when **`startA < endB AND startB < endA`**. The comparison is strict on both sides, and every interval is half-open `[start, end)`. Get this wrong and 10:00–11:00 "conflicts" with 11:00–12:00, so a fully free day reports itself as full — the single most common bug on these screens.

```ts
const clash = await prisma.booking.findFirst({
  where: {
    roomId,
    status: { not: "CANCELLED" },      // a cancelled booking frees its slot
    id: editingId ? { not: editingId } : undefined,   // reschedule: ignore itself
    startAt: { lt: endAt },
    endAt:   { gt: startAt },
  },
  select: { id: true, startAt: true, endAt: true },
});
```

Three details that are all load-bearing: exclude cancelled rows or the calendar silently fills up; exclude the row being edited or rescheduling a booking by five minutes always collides with itself; and filter by the resource (room/staff/table) — a global overlap check makes the whole business single-threaded.

**Never** `findMany()` a month and overlap-check in JavaScript. The check belongs in the same query as the write.

## The slot can be sold twice

`findFirst` then `create` has a gap between them: two requests both see the slot free. For a demo the honest fix is cheap — put the constraint in the database and let it lose the race:

```prisma
model Booking {
  // fixed slots (09:00, 09:30, …): the pair IS the identity of the slot
  @@unique([roomId, startAt], name: "uq_booking_slot")
}
```

```ts
try { await prisma.booking.create({ data: { ... } }); }
catch (e) {
  if (e.code === "P2002") return { error: "ช่วงเวลานี้ถูกจองไปแล้ว กรุณาเลือกเวลาอื่น" };
  throw e;
}
```

If the prototype books **arbitrary** ranges rather than fixed slots, no single unique index expresses that. Do the check and the insert inside one `prisma.$transaction`, keep the `P2002` handler for whatever unique keys do exist, and write the remaining race window into `BUILD_NOTES.md` rather than pretending it is closed. Do not invent a locking scheme the prototype never had.

## An empty calendar is a full screen, not an empty state

The delivered app starts on an EMPTY database, and this is where a calendar differs from a list: **the grid itself is the content.** A month with zero bookings renders all 42 cells normally — never a "ยังไม่มีข้อมูล" panel where the calendar should be, and never a spinner.

Empty states belong to the *lists beside* the grid:

- day/agenda pane with nothing on it — "ไม่มีนัดหมายในวันนี้" + the button that creates one (`เพิ่มนัดหมาย`), wired to this app's real write path.
- slot picker for a day that is fully booked — "ช่วงเวลาของวันนี้ถูกจองหมดแล้ว" / "ลองเลือกวันอื่น".
- a resource filter matching nothing — "ไม่พบห้อง/พนักงานที่ตรงกับตัวกรอง".

Never render example appointments to "show what it looks like". A demo whose calendar is pre-filled with invented meetings is worse than an empty one.

## Fetch the month in one query

```ts
export const dynamic = "force-dynamic";          // required: next build has no DB

const from = dayStart(firstVisibleYmd), to = dayEnd(lastVisibleYmd);   // 42 cells, not 28
const rows = await prisma.booking.findMany({
  where: { startAt: { lt: to }, endAt: { gt: from }, status: { not: "CANCELLED" } },
  orderBy: [{ startAt: "asc" }, { id: "asc" }],   // stable tiebreaker
  include: { room: true },                        // FKs are optional here — may be null
});
const byDay = new Map<string, typeof rows>();     // group by toDateInput(r.startAt)
```

Query the **visible** range including the leading and trailing days from adjacent months, or bookings vanish from the grey cells. Group by the Bangkok day (`toDateInput`), not by `getDate()`. An `include`d relation can be `null`, so render `{b.room?.name ?? "— (ถูกลบแล้ว)"}` and never drop the booking.

## Rules

- **Port, don't design.** Build only the views the prototype has. No "while I'm here" week view, no recurring appointments the PRD never mentions, no drag-to-reschedule.
- **Every visible date and time string comes from `lib/format.ts`.** No local `Intl.DateTimeFormat`, no `+ 543`, no `toLocaleDateString` scattered through components.
- **Half-open `[start, end)` everywhere** — in the overlap check, the day query and the slot generator. Mixing an inclusive end in one place is invisible until two bookings touch.
- **`<input type="date">` and `type="time"` always** — a hand-built picker cannot be typed into and fails on mobile. Convert with `fromDateInput`, never `new Date(inputValue)`.
- **Duration is minutes stored as `Int`, never a float of hours.** `0.1 * 3` is not `0.3`, and half-hour slots turn into 29-minute ones.
- **Past dates: disable, do not hide.** The customer needs to see last week's appointments; they just cannot book into it. Copy the boundary rule from the PRD — never guess whether today counts.
- **Copy validation messages verbatim from the PRD's `การควบคุมความถูกต้องของข้อมูล (Validation & Edge Cases)`** — the real Thai error text for a past date, an end before a start, a clash, or an over-long booking is already written there. Do not invent it.
- **Never ask.** Builds run unattended. Slot length, week start, working hours: take them from the prototype and the PRD, and if neither says, pick the prototype's visible behaviour and record the choice in `BUILD_NOTES.md`.
- **No fake bookings** — not in the grid, not in a skeleton, not as a "sample" appointment.

## Verify before you finish

Against an **empty** database, then with bookings you created through the app's own form:

- the month grid renders with zero rows, six week-rows, no crash and no invented entries;
- the header year matches the year inside the cells (both พ.ศ.);
- 10:00–11:00 and 11:00–12:00 both save — back-to-back is not a clash;
- 10:00–11:00 and 10:30–11:30 refuse, with the PRD's message;
- rescheduling a booking to a time overlapping only itself succeeds;
- cancelling a booking frees its slot for a new one;
- a booking created at 21:00 Bangkok time appears on **today**, not tomorrow;
- no `Invalid Date`, no `NaN`, no off-by-one day anywhere.
