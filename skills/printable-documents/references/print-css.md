# Print CSS, page mechanics and fonts

Everything here goes in `app/globals.css` (Tailwind v4 accepts plain `@media print` blocks) plus a few `data-` attributes on the document markup. The document is a normal page; the browser is the print engine.

## 1. The markup contract

Four attributes carry the whole stylesheet — use these names, nothing else:

| attribute | on what |
|---|---|
| `data-print-root` | the outer element of the document itself (the "sheet of paper") |
| `data-print-page` | one printed copy (ต้นฉบับ, สำเนา) inside the root |
| `data-print-keep` | a block that must never be split across pages (totals, signature, the address box) |
| `data-print-hide` | anything that exists only on screen (buttons, back links, filters) |

```tsx
<main data-print-root className="mx-auto bg-white text-black">
  <section data-print-page>
    <header>…ผู้ออกเอกสาร / เลขที่ / วันที่ / ต้นฉบับ…</header>
    <table>…รายการ…</table>
    <section data-print-keep>…รวมเป็นเงิน / ส่วนลด / VAT 7% / ยอดรวมทั้งสิ้น…</section>
    <section data-print-keep>…ลายเซ็น…</section>
    <footer>เอกสารตัวอย่างจากระบบสาธิต — ไม่ใช่เอกสารทางภาษี</footer>
  </section>
</main>
```

## 2. The stylesheet

```css
/* ---- screen: draw a sheet of A4 so the layout is honest before printing ---- */
[data-print-root] {
  width: 210mm;
  margin-inline: auto;
  background: #fff;
  color: #000;
}
[data-print-page] {
  min-height: 297mm;
  padding: 14mm 12mm;
  box-shadow: 0 2px 16px rgb(0 0 0 / 0.15);
}

/* ---- print ---- */
@page { size: A4; margin: 14mm 12mm 18mm; }   /* wider bottom: the browser prints its own footer there */

@media print {
  html, body { background: #fff !important; }

  /* the app around the document */
  #fitt-feedback-host,               /* CRN's injected 🐞 feedback widget */
  nav, aside, header[data-app-chrome], [data-print-hide] { display: none !important; }

  /* @page margins already reserve the paper edge — a second set of margins overflows */
  [data-print-root] { width: auto; margin: 0; }
  [data-print-page] { min-height: 0; padding: 0; box-shadow: none; }
  [data-print-page] + [data-print-page] { break-before: page; }

  /* a horizontal-scroll wrapper (Tailwind's overflow-x-auto) clips the table and
     kills header repetition — unwrap it for print */
  .overflow-x-auto, .overflow-auto, .overflow-y-auto { overflow: visible !important; }

  /* pagination */
  thead { display: table-header-group; }      /* repeat column headers on every page */
  tr, img, [data-print-keep] { break-inside: avoid; }
  h1, h2, h3 { break-after: avoid; }          /* never leave a heading alone at the bottom */

  /* backgrounds/borders are usually dropped unless asked for */
  * { print-color-adjust: exact; -webkit-print-color-adjust: exact; }

  a[href]::after { content: ""; }             /* Tailwind resets don't add these; UA styles in some browsers do */
}
```

Notes that matter:

- **Totals belong AFTER the table, not in `<tfoot>`.** A `tfoot` is a table-footer-group and repeats at the bottom of every printed page, so a multi-page quotation prints its grand total three times.
- **You cannot print your own page numbers.** `@page { @bottom-center { content: counter(page) " / " counter(pages) } }` is CSS Paged Media; Chrome and Firefox ignore margin boxes. The browser's own header/footer (which the user can toggle in the print dialog) is the only page numbering available. Do not put "หน้า 1 จาก 3" on the page — it will be wrong on page 2.
- **`position: fixed` does not repeat on every page in Chrome** (it prints on page 1 only). That is why the watermark below is a repeating background, not a fixed element.
- **The signature block.** `break-inside: avoid` guarantees it is never sliced in half; it cannot force it to share a page with the last table rows. If a document routinely strands the signature, shorten the header block rather than fighting the pagination.

## 3. The DEMO watermark

A repeating SVG background on the document page, so it appears on every printed page:

```css
[data-print-page] {
  background-image: url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='320' height='220'><text x='160' y='120' fill='%23000' fill-opacity='0.055' font-size='44' font-family='sans-serif' font-weight='700' text-anchor='middle' transform='rotate(-30 160 120)'>DEMO</text></svg>");
  background-repeat: repeat;
  print-color-adjust: exact;
  -webkit-print-color-adjust: exact;
}
```

Keep the watermark word in Latin (`DEMO`). Text inside an SVG data-URI background is rendered with whatever system font resolves for `sans-serif`, which is not a safe place to put Thai.

The watermark is **not** the honesty statement — the print dialog has a "Background graphics" checkbox that removes it. The line `เอกสารตัวอย่างจากระบบสาธิต — ไม่ใช่เอกสารทางภาษี` must be real text in the document footer, where nothing can turn it off.

## 4. Fonts

```ts
// app/layout.tsx
import { Noto_Sans_Thai } from "next/font/google";

const thai = Noto_Sans_Thai({
  subsets: ["thai", "latin"],   // "latin" alone ships a font file with NO Thai glyphs
  weight: ["400", "600", "700"],
  display: "swap",
  variable: "--font-thai",
});
```

```css
@theme { --font-sans: var(--font-thai), "Leelawadee UI", Tahoma, Thonburi, "Noto Sans Thai", sans-serif; }
```

- `next/font/google` self-hosts the file at build time, so printing does not depend on the UAT machine reaching fonts.googleapis.com. Keep the family the prototype used (Sarabun, Prompt, IBM Plex Sans Thai and Noto Sans Thai all have a `thai` subset).
- Always keep OS Thai faces in the fallback chain — `Leelawadee UI` (Windows), `Thonburi` (macOS), `Tahoma` (both). Glyph fallback is per-character, so a Latin-only first family gives mismatched baselines rather than tofu; the fallback chain is what stops actual `□□□`.
- Thai lines need more leading than Latin: `line-height: 1.6` on the document body, or tone marks collide with the line above.
- **Print before the webfont loads and the preview comes out in the fallback face**, with different metrics and a different page count. Always gate the call:

```tsx
"use client";

export function PrintButton() {
  return (
    <button
      data-print-hide
      className="rounded bg-black px-4 py-2 text-white print:hidden"
      onClick={() => document.fonts.ready.then(() => window.print())}
    >
      พิมพ์เอกสาร / บันทึกเป็น PDF
    </button>
  );
}
```

## 5. Checklist before calling a document done

- [ ] Printed from the real route — no `window.open()` + `document.write()` anywhere.
- [ ] Print preview: no nav, no sidebar, no buttons, no 🐞 widget.
- [ ] A document long enough to need 2+ pages repeats its `thead` and splits no row.
- [ ] Totals and signature blocks are whole, and totals are not in a `<tfoot>`.
- [ ] Thai renders in the preview with correct tone-mark placement, not a fallback face.
- [ ] The `DEMO` watermark prints, and the `ไม่ใช่เอกสารทางภาษี` line is present as text with backgrounds off.
- [ ] With an empty database the route shows a Thai message, not an empty document.
