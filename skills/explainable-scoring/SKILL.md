---
name: explainable-scoring
description: Port the screens of a FITT demo that rank, score or rate something and show WHY — a total built from weighted factors, the per-factor contributions that must add back up to it, the badge or ผ่าน/ไม่ผ่าน threshold, and the honest states when the database is EMPTY or a factor has no data. Use when porting คะแนน, ให้คะแนน, จัดอันดับ, เรตติ้ง, ระดับความเสี่ยง, ความน่าเชื่อถือ, เกรด, ผลการประเมิน, เปอร์เซ็นต์ความเหมาะสม/ความตรงกัน, a score column with a breakdown popover, a progress or gauge meter, a leaderboard, or any number the UI must justify to the user. Skip when a number is merely stored and displayed with no derivation and nothing to explain — that is thai-formatting.
---

# explainable-scoring — a number the screen can defend

The prototype's scores were fiction. Almost every FITT export computes them with `Math.random()`, a hand-written literal, or a formula over mock rows that no longer exist. Port that literally and the demo reranks itself on every refresh — the customer notices within a minute, and the whole screen loses credibility.

**A score in the delivered app is derived from real rows, deterministically, and the breakdown must add up to the total shown beside it.** If those two numbers disagree by even a satang-equivalent, the feature is worse than not shipping it: it is a screen that argues with itself in front of the customer.

**Lane.** `thai-formatting` owns rendering — `formatPercent` takes a **ratio** (`0.07` → `7%`), `formatQty` and `formatTHB` own numbers and money. Import them; never format a score by hand. `data-tables` owns the ranked list, its sorting and pagination. `charts` owns a score rendered as a chart. This skill owns the **derivation and the explanation**.

## Where the formula comes from

The PRD wrote it down. Find it by **keyword, never by section number** (`เกณฑ์การให้คะแนน`, `การคำนวณ`, `น้ำหนัก`, `เกณฑ์การประเมิน`, plus the `ข้อมูลและฟิลด์` Data Model for each input field's type, unit and bounds). The BRD's Acceptance Criteria usually pin the thresholds — "ลูกค้าที่ได้คะแนนตั้งแต่ 80 ขึ้นไปถือว่า ผ่าน".

If the documents define the factors but not the weights, take the weights the prototype visibly used and **write the choice into `BUILD_NOTES.md`**. Never invent a factor the documents do not name, and never quietly drop one they do — a missing factor changes every score without any screen saying so.

## One module, integers, weights that sum to 1

```ts
// lib/scoring.ts — no @prisma/client import: client components read these types too.
export type FactorKey = "payment" | "volume" | "tenure";

export const FACTORS: { key: FactorKey; label: string; weight: number; max: number }[] = [
  { key: "payment", label: "ประวัติการชำระเงิน", weight: 0.5, max: 100 },
  { key: "volume",  label: "ยอดสั่งซื้อสะสม",    weight: 0.3, max: 100 },
  { key: "tenure",  label: "อายุความสัมพันธ์",    weight: 0.2, max: 100 },
];

export type Contribution = { key: FactorKey; label: string; raw: number | null; points: number };

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n));

/** Returns the total AND the parts. The total IS the sum of the parts, so the
 *  breakdown on screen can never disagree with the number beside it. */
export function score(raw: Record<FactorKey, number | null>): {
  total: number; parts: Contribution[]; missing: FactorKey[];
} {
  const parts: Contribution[] = FACTORS.map((f) => {
    const v = raw[f.key];
    // Missing input contributes nothing AND is reported as missing — not scored as 0.
    const points = v === null ? 0 : Math.round((clamp(v, 0, f.max) / f.max) * f.weight * 100);
    return { key: f.key, label: f.label, raw: v, points };
  });

  return {
    total: parts.reduce((s, p) => s + p.points, 0),   // by construction, parts sum to this
    parts,
    missing: FACTORS.filter((f) => raw[f.key] === null).map((f) => f.key),
  };
}
```

**Score in integer points out of 100, not floats.** `0.1 + 0.2 !== 0.3`, and a breakdown of `33.33 + 33.33 + 33.33` renders as `99.99` beside a total of `100`. Integers make "the parts sum to the total" checkable rather than approximately true.

**Never round the total independently of its parts.** Round each part, then let the total *be* their sum — that is why the function above returns both from one pass. Computing the total as `Math.round(weighted sum)` and the parts separately is exactly how a 78 gets explained by factors adding to 79. The cost is that the total can sit a point away from the unrounded ideal; that is the correct trade, because only one of the two numbers is on screen being checked.

This requires the weights to sum to **1.0**. If they do not, the score silently stops being out of 100 — assert it once at module load rather than discovering it in a demo.

## A Prisma `Decimal` is not a number

Every scoring input that comes from money or a quantity arrives as a `Decimal`, and `reduce((s, x) => s + x.amount, 0)` concatenates it into a string — the failure `thai-formatting` documents as `฿NaN`. Convert at the boundary, once:

```ts
const num = (v: unknown): number | null => {
  if (v === null || v === undefined) return null;
  const n = Number(v.toString());
  return Number.isFinite(n) ? n : null;
};
```

Also: `prisma.order.aggregate({ _sum: { total: true } })` returns `_sum.total === null` on an empty table, not `0`. Feed that `null` into arithmetic and every score becomes `NaN`.

## Missing data is not zero

A customer with no orders yet has **no** payment history. Scoring that as `0` says "this customer pays badly" — a factual claim the data does not support, about a real business, on a screen the customer is reading.

Carry `null` through to the UI and render it as unknown:

- the factor row shows `—` (the `thai-formatting` placeholder) and `ยังไม่มีข้อมูล`, not `0 คะแนน`;
- the total is labelled as partial — **"คะแนน 45 (จากข้อมูลที่มี 2 จาก 3 ด้าน)"** — never presented as if complete;
- if **every** factor is missing, there is no score at all: show `ยังไม่สามารถคำนวณคะแนนได้` and what the user must add first.

That last case is the *default* on a freshly delivered demo, so build it first.

## Show the arithmetic

An "explainable" score whose explanation is a tooltip saying "based on multiple factors" explains nothing. The breakdown names each factor, its raw input, its weight and the points it contributed — and the points column visibly adds to the total:

```tsx
{parts.map((p) => (
  <tr key={p.key}>
    <td>{p.label}</td>
    <td className="text-right tabular-nums">{p.raw === null ? "—" : formatQty(p.raw)}</td>
    <td className="text-right tabular-nums">{formatPercent(weightOf(p.key))}</td>
    <td className="text-right tabular-nums">{p.raw === null ? "—" : `${p.points}`}</td>
  </tr>
))}
<tr><td colSpan={3}>รวม</td><td className="text-right tabular-nums font-medium">{total}</td></tr>
```

`formatPercent` takes a **ratio**: pass `0.5`, not `50`, or the weight column reads `5,000%`. Numbers get `text-right tabular-nums` so the column of digits lines up under the total.

## Store or recompute — decide once, deliberately

**Recompute on read** while the inputs are cheap to read. The score can never go stale, and there is no migration when a weight changes. This is the right default for a demo.

**Persist** only when the PRD asks for history ("คะแนนย้อนหลัง") or the score must be stable at a point in time (an approval decision). Then store the total **and the factor values it came from**, so an old score can still be explained. Storing a bare number produces a screen that shows 72 and cannot say why, which fails the one requirement this feature has.

Never persist a score in a required column without a `@default` — the delivered image self-migrates with `db push` against a database that may already hold rows.

## Thresholds and bands

Bands come from the documents, and their boundaries are inclusive-lower/exclusive-upper so no score falls in a gap or matches twice:

```ts
export const BANDS = [
  { min: 80, label: "ดีมาก",  tone: "success" },
  { min: 60, label: "ดี",      tone: "info"    },
  { min: 40, label: "พอใช้",   tone: "warning" },
  { min: 0,  label: "ต้องปรับปรุง", tone: "danger" },
];
export const bandOf = (total: number) => BANDS.find((b) => total >= b.min)!;
```

**Never colour alone.** A red badge and a green badge are the same badge to a colourblind tester and in a printed report — always ship the Thai label beside the colour. And when the total is unknown, the band is unknown too: no badge, not the bottom band.

## Ranking

Sorting by score belongs to `data-tables` — in Postgres, with `skip`/`take`, never `findMany()` then `.sort()`. Two things are specific to scores:

- **Always add a stable tiebreaker** (`orderBy: [{ score: "desc" }, { id: "asc" }]`). Scores tie constantly — far more than dates or names — and without it a row moves between pages.
- **Rank is not a stored column.** Recompute it from the ordered page (`(page - 1) * perPage + i + 1`), or it silently rots the moment any input changes.

If the score is recomputed rather than stored, it cannot be an `orderBy` key at all. Either rank in Postgres over the stored inputs, or — only for a list the prototype shows in full with no pagination — compute and sort in memory, and say so in `BUILD_NOTES.md`.

## Rules

- **Delete every `Math.random()`.** A score that changes on refresh is the single most damaging bug on this screen. Grep for it before you finish.
- **Parts sum to the total, always** — by summing the rounded parts, never by rounding twice.
- **Integer points out of 100.** No floats in the model; format for display only.
- **`null` means unknown and stays `null`.** Never `?? 0` an absent input.
- **Percent helpers take ratios.** `formatPercent(0.5)` → `50%`.
- **No invented factors, weights or thresholds.** They are in the PRD and BRD; go and read them. Anything you had to choose goes in `BUILD_NOTES.md`.
- **Copy validation text verbatim from the PRD's `การควบคุมความถูกต้องของข้อมูล (Validation & Edge Cases)`** — out-of-range inputs and the insufficient-data message are written there.
- **Never ask.** Builds run unattended. Pick from the documents and the prototype, and record the choice.
- **No fake scores** — not a sample leaderboard, not a placeholder gauge, not a "ตัวอย่าง" row.

## Verify before you finish

On an **empty** database, then with rows you entered through the app's own form:

- the empty database shows `ยังไม่สามารถคำนวณคะแนนได้` on every scoring screen — no `NaN`, no `0` presented as a real score, no invented leaderboard;
- an entity missing one factor shows `—` for it, a partial-score label, and a total made only of the factors present;
- the breakdown's points column **adds up to the displayed total**, checked by hand on at least two entities;
- reloading the page five times gives the identical score and identical order;
- an entity at exactly a band boundary (80) lands in the upper band, once;
- every band shows its Thai label, not colour alone;
- `grep -rn "Math.random" app components lib` returns nothing.
