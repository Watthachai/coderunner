---
name: barcode-scanning
description: Build the scan-driven screens of a ported FITT demo — a barcode or QR code arriving from a USB/Bluetooth scanner as keystrokes, looked up in Postgres, then acted on (รับเข้า, จ่ายออก, ตรวจนับ, เช็คอิน) — plus the "not found" and duplicate paths that dominate a database starting EMPTY. Use when porting any screen with บาร์โค้ด, คิวอาร์/QR, สแกน, ยิงบาร์โค้ด, เครื่องอ่านบาร์โค้ด, รหัสสินค้า/SKU/serial ที่ยิงเข้ามา, a scan input box that submits on Enter, a scan-to-add line-item flow, stock check-in/check-out by code, or ticket/asset/roll lookup by code. Skip when a code is only ever typed into an ordinary form field with no scanning flow — that is data-tables plus a normal create form.
---

# barcode-scanning — scan input, lookup and the paths that actually fire

A scan screen is one input box and a great deal of failure handling. On the delivered demo the database is EMPTY, so the very first scan any customer performs **misses** — "not found" is not an edge case here, it is the opening screen. Build that path first and it will feel finished; build it last and the demo dies on the tester's first attempt.

**Lane.** `data-tables` owns the list of scanned rows and its filters. `thai-formatting` owns every rendered value — `formatQty`, `formatTHB`, `formatDateTime` from `lib/format.ts`. This skill owns the input, the lookup, the duplicate and not-found paths, and the write.

## The scanner is the input. The camera is a special case, not the default.

Read the briefs before assuming a camera is wanted at all: these documents typically say `สแกน` and `เครื่องสแกน` and never mention กล้อง, มือถือ or camera, and describe the screen as a `ช่องกรอก/สแกนบาร์โค้ด`. A field that is typed *or* scanned is a text input — that is the whole requirement, and it is satisfied by the section below without any device API.

The camera is also usually unavailable. CRN delivers the demo as a plain-HTTP container — `docker-compose.customer.yml` publishes `APP_PORT:3000`, with no TLS, no certificate and no proxy. Customers open it at `http://HOST:PORT` on a LAN address. Browsers expose `navigator.mediaDevices` **only in a secure context** (HTTPS, or `localhost`), so on every machine except the one running the container it is `undefined` — and naive camera code fails as `TypeError: Cannot read properties of undefined (reading 'getUserMedia')`, which reaches the user as a blank panel or a crashed boundary.

This is not a limitation to work around; it matches how Thai warehouses and shops already work. A **USB or Bluetooth barcode scanner is a keyboard**: it types the code and presses Enter. So the primary implementation reads keystrokes, and it needs no library at all.

**`isSecureContext` is also true on `http://localhost`** — and that is the trap, not the escape hatch. The camera therefore works perfectly on the machine running the container, which is exactly where the demo gets rehearsed, and is dead on every phone and laptop that opens it over the LAN, which is exactly where it gets shown to the customer. A feature that fails everywhere is found on day one; this one waits until it has an audience.

So on the rare export that genuinely had a camera scanner, keep it as **progressive enhancement** and **say why it is unavailable** — never hide the button silently, or the operator reports a broken demo:

```tsx
const cameraReady =
  typeof window !== "undefined" && window.isSecureContext && !!navigator.mediaDevices?.getUserMedia;
```

```tsx
{cameraReady
  ? <CameraScanButton onCode={onCode} />
  : <p className="text-sm text-muted-foreground">
      สแกนด้วยกล้องต้องเปิดผ่าน HTTPS — ใช้เครื่องอ่านบาร์โค้ดยิงเข้าช่องนี้ได้เลย
    </p>}
```

Keep the keyboard path mounted either way, note the HTTPS requirement in `BUILD_NOTES.md`, and never `import` a camera library at module scope — load it inside the click handler with `await import(...)` so it cannot break the page for the majority who will never see the button.

## Capture the scan globally, not from one focused input

Binding the scanner to a single `<input>` assumes the cursor is in it. It will not be: the operator clicked a row, closed a dialog, or tabbed away, and the entire barcode goes to the page body and disappears without a trace. Listen on the document instead, and keep a visible input as the affordance for typing by hand.

```tsx
"use client";
// A scanner types a whole code in well under a second; a human cannot.
const MAX_GAP_MS = 120;      // between keys within one scan
// Set from the SHORTEST code format the PRD's Data Model documents (e.g. "R-00001"
// -> 7). Only fall back to 4 when the documents state no format at all.
const MIN_LENGTH = 4;        // shorter bursts are someone using the keyboard

export function useScanner(onCode: (code: string) => void) {
  const buf = useRef<string[]>([]);
  const last = useRef(0);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      // Let real typing into a field be real typing.
      const el = e.target as HTMLElement | null;
      const typing = el?.tagName === "INPUT" || el?.tagName === "TEXTAREA" || el?.isContentEditable;

      const now = e.timeStamp;
      if (now - last.current > MAX_GAP_MS) buf.current = [];
      last.current = now;

      if (e.key === "Enter") {
        const code = buf.current.join("");
        buf.current = [];
        if (code.length >= MIN_LENGTH) { e.preventDefault(); onCode(code); }
        return;
      }
      const ch = charFromEvent(e);
      if (ch) { buf.current.push(ch); if (!typing) e.preventDefault(); }
    }
    document.addEventListener("keydown", onKeyDown, true);   // capture phase
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [onCode]);
}
```

The timing gate is what separates a scan from typing — not a heuristic you can skip. Keep the buffer flush (`now - last > MAX_GAP_MS`) or a stray keystroke from an hour ago prefixes the next scan.

### A Thai keyboard layout turns `R-00001` into `ก-00001`

This one bites in Thailand specifically. When the OS input source is Thai, the scanner's keystrokes go through the same layout a human's would, so `KeyR` produces `พ` and the code arrives as Thai text. The operator sees garbage and blames the scanner.

**`event.key` does not save you — it is the layout-dependent character, so it is already Thai.** `event.code` is the *physical* key and is layout-independent. Recover the intended character from it:

```ts
/** Physical key -> the character a US layout would have produced. */
function charFromEvent(e: KeyboardEvent): string | null {
  const c = e.code;
  if (/^Key[A-Z]$/.test(c)) return e.shiftKey ? c.slice(3) : c.slice(3).toLowerCase();
  if (/^Digit[0-9]$/.test(c)) return e.shiftKey ? null : c.slice(5);
  if (/^Numpad[0-9]$/.test(c)) return c.slice(6);
  if (c === "Minus") return "-";
  if (c === "NumpadSubtract") return "-";
  // Fall back to key for anything else, but only single printable characters.
  return e.key.length === 1 ? e.key : null;
}
```

Barcode symbologies in use here (Code 39, Code 128, EAN) are ASCII, so mapping the physical key is always safe — there is no legitimate Thai character in a barcode. Uppercase via `normalizeCode` below rather than trusting Shift, since scanners vary in how they send capitals.

`event.code` earns its place a second time even on a US layout: many scanners ship configured to emit digits as **numpad** keys, and with NumLock off `event.key` for those arrives as `ArrowLeft`, `Home`, `PageUp` — navigation, not digits — while `event.code` is still `Numpad4`. The mapper above accepts `Digit0-9` and `Numpad0-9` both, so it does not matter which mode the device is in. Do not narrow it to one: the briefs do not name the scanner model, and you cannot know.

## Codes are strings, and they are not clean

**Store every code as `String`, never `Int`.** `0410123456789` is a real barcode and an `Int` eats the leading zero and overflows past 2³¹ — the same rule `thai-formatting` states for 13-digit tax IDs, for the same reason.

Normalize once, in one exported function, and use it on **both** the write and the lookup — normalizing only one side is why a code that is visibly present refuses to be found:

```ts
// lib/barcode.ts
export const normalizeCode = (raw: string) =>
  raw.trim().replace(/[​-‍﻿\s]/g, "").toUpperCase();
```

Scanners append a suffix (usually `\n`, sometimes `\t` or `\r\n`), configurable per device, and some emit zero-width characters. Trim them all. Uppercase only if the codes are alphanumeric SKUs; leave pure digits alone either way.

## Lookup, and the three answers

```ts
"use server";
export async function scanAction(raw: string) {
  const code = normalizeCode(raw);
  if (!code) return { kind: "empty" as const };

  const item = await prisma.product.findUnique({ where: { barcode: code } });
  if (!item) return { kind: "not-found" as const, code };
  if (item.status === "DISPOSED") {
    return { kind: "rejected" as const, code, message: "สินค้านี้ถูกตัดออกจากระบบแล้ว" };
  }
  return { kind: "ok" as const, item };
}
```

- **`ok`** — show the item and the action the prototype offers. Keep the scan box focused.
- **`not-found`** — **"ไม่พบบาร์โค้ด "{code}" ในระบบ"** plus the route that fixes it: the create form, pre-filled with the scanned code. On an empty database this is the common answer, and a dead end here is a dead demo.
- **`rejected`** — a real row that this flow refuses, with the PRD's own message. Distinct from not-found; saying "ไม่พบ" for a disposed item sends the operator hunting for a data-entry mistake that does not exist.

`barcode` needs `@unique` in the schema for `findUnique`, and it is what makes duplicates catchable:

```ts
catch (e) {
  if (e.code === "P2002") return { error: `บาร์โค้ด ${code} นี้มีอยู่ในระบบแล้ว` };
  throw e;
}
```

Handle `P2002` even when the form already checked — two operators can scan the same new code at once, and the constraint is the only thing that actually holds.

## Feedback has to be non-visual

The operator is looking at the shelf, not the screen. Whatever the prototype does on a successful scan — a colour flash, a row sliding in, a counter incrementing — keep it, and keep it **big and instant**. The result line renders in the same place every time, so it can be read peripherally. A short `AudioContext` beep on success and a lower one on failure is worth porting if the prototype had one; do not add sound it never had, and never make sound the only signal.

Scanned rows accumulate newest-first, and the running total lives beside them. Optimistic UI is right here: append the row immediately, reconcile when the action resolves, and remove it with the error message if it failed.

## Empty database

- The scan screen itself renders normally with zero rows — the input is the content, exactly as a calendar grid is.
- The list under it says **"ยังไม่มีรายการที่สแกนในรอบนี้"** with a line telling the operator to scan to begin.
- A product catalogue with nothing in it gets the `data-tables` `no-data` state and a `เพิ่มสินค้า` button — because on a fresh demo, adding the first product is a prerequisite for any scan to succeed at all.
- Never seed sample barcodes so a scan "works". A demo whose codes exist only in a seed teaches the tester nothing and hides the create flow.

## Rules

- **Port, don't design.** Build the scan flow the prototype has. No camera the prototype lacked, no bulk-import, no history screen nobody asked for.
- **Keyboard input is the primary path, always.** Camera only when `isSecureContext`, always alongside the input, never replacing it.
- **`String` codes, `@unique`, normalized on both write and read** — through the single `normalizeCode`.
- **Capture on `document` with a timing gate, and read `event.code`.** Focus-bound input loses scans; `event.key` loses them to a Thai layout. Clear the buffer before awaiting, or a fast second scan appends to the first.
- **Distinguish not-found from rejected from empty.** Three messages, three next steps.
- **Copy every message verbatim from the PRD's `การควบคุมความถูกต้องของข้อมูล (Validation & Edge Cases)`** — duplicate code, unknown code, wrong status are written there with real Thai text.
- **Never `window.alert`/`confirm`** — it blocks the thread and swallows the next scan.
- **Never ask.** Builds run unattended. If the PRD does not say whether a rescan increments or replaces, follow the prototype and record it in `BUILD_NOTES.md`.

## Verify before you finish

On an **empty** database, then after adding one product through the app's own form:

- scanning anything on the empty database shows "ไม่พบบาร์โค้ด" **with a working link to create it**, not a crash;
- typing a code and pressing Enter does not reload the page;
- a scan registers while focus is on a table row or a button, not only inside the input;
- switching the OS input source to Thai still produces the Latin code, not Thai characters;
- after each scan the input is empty and still focused, so three scans in a row all register;
- a code with leading zeros survives the round trip intact;
- scanning the same new code twice gives the duplicate message, not a 500;
- pasting a code with a trailing newline or space finds the same row as a clean one;
- opened over the LAN (not `localhost`), the camera button is replaced by the Thai explanation — nothing throws `Cannot read properties of undefined`, and the scanner still works.
