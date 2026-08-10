---
name: fitt-build
description: Convert a FITT Builder export (a Vite + React SPA prototype zip plus IDEA/BRD/PRD briefs) in the current directory into a runnable Next.js (App Router, TypeScript) app backed by Prisma + PostgreSQL with a Docker build, faithfully preserving the prototype UI/UX. Use at the start of every Code Runner build.
---

# fitt-build — FITT Builder export -> Next.js + Prisma + Docker

The working directory holds ONE FITT Builder export zip (a **Vite + React 18 single-page app**) and its briefs. Your job: **convert it to a clean, buildable Next.js App Router app with Prisma + PostgreSQL and a Docker build, preserving the prototype's exact UI/UX and behavior.** This is a port, not a redesign.

Read the two guides in this skill before and during the work — they carry the details this summary omits:
- `references/nextjs-conversion.md` — Vite+React SPA -> Next.js App Router (entry points, client/server, import.meta.env, assets, routing, pitfalls).
- `references/prisma-setup.md` — Prisma + Postgres (schema, client singleton, seed, and making `next build` pass with NO live DB).
- `references/test-cases.md` — the `TEST_CASES.md` the demo ships with (format, negative-case catalogue, the honest "not supported yet" section).

Use the ready-made `assets/Dockerfile` and `assets/.dockerignore` as your starting templates.

## Steps

1. **Extract & read.** Find the `*.zip` in this directory, extract it in place, then delete the zip. Read `docs/IDEA.md` / `docs/BRD.md` / `docs/PRD.md` (or root-level IDEA/BRD/PRD) — enough to name the product and derive the data model.

2. **Recognize the source.** It is a **Vite + React SPA**: `index.html` (Tailwind CDN + fonts, `<div id="root">`), `src/main.tsx` (createRoot), `src/App.tsx` (root view), `src/index.css`, optional `src/data.ts`/`src/types.ts` (mock data), `vite.config.*`. Confirm before converting.

3. **Inventory the source BEFORE converting — write `PORT_CHECKLIST.md`.** A large export (dozens of files, many screens) is where a port silently loses work: you cannot hold 100 files in your head, and `next build` passes just fine on a half-ported app. So FIRST walk the whole extracted tree (`src/**` + `index.html`) and write `PORT_CHECKLIST.md` at the root — one **unchecked** line per thing you must port:
   - every screen/view, every component file (nested dirs included), every route in the router, every hook/util/context module, every mock-data module (`data.ts`/`types.ts`), the global CSS, and the assets in `public/`.
   - record the **source file count** at the top (e.g. `source: 97 files, 14 screens`).
   Then port item by item and tick `[x]` only when the Next.js counterpart exists AND compiles. This file is your work list, the section list for `TEST_CASES.md`, *and* the completeness gate in step 10 — it is not optional bookkeeping.

4. **Convert to Next.js (App Router, TypeScript) — install the LATEST Next.js** — preserve UI/UX exactly (see nextjs-conversion.md):
   - Scaffold an `app/` tree (NOT `pages/`). Port components/styles across — **mirror the prototype's own file layout** (`src/components/x/Y.tsx` → `components/x/Y.tsx`), same names, so the checklist maps 1:1.
   - **Port every screen — NEVER consolidate or simplify.** One prototype component → one ported component. Do NOT merge several screens into one page, do NOT ship a cut-down "simplified" version of a component, do NOT skip a screen for being secondary. Only the conversion rules delete files (`main.tsx` bootstrap, `vite.config.js`, `index.html`); anything else you drop goes in `BUILD_NOTES.md` under **"Not ported"** with a reason.
   - `index.html` -> `app/layout.tsx` (`metadata`, fonts, global CSS import); `src/App.tsx` -> `app/page.tsx`. Delete `main.tsx`/`#root` bootstrap.
   - Client vs server: pages that just render the prototype UI can be client components (`"use client"` on the first line); prefer server components + server actions / route handlers for data.
   - Translate Vite-isms: `import.meta.env.VITE_X` -> `process.env.NEXT_PUBLIC_X` (client) or `process.env.X` (server-only); static assets -> `public/` or static imports; react-router routes -> `app/` segments (or keep a single-page client app if the prototype is one screen).
   - Keep the same styling approach (plain CSS or Tailwind — do not change the visual result).
   - **Authentication — STANDARDIZE to an env email+password login (this OVERRIDES the prototype's auth and the "faithful UI" rule for the login screen only).** Whatever the prototype does to sign in — Google/SSO, an email allowlist, OAuth, or a random mock — REPLACE it with ONE simple email + password form that accepts ONLY the credentials from `DEV_EMAIL` / `DEV_PASSWORD` env (fallbacks `dev@fitt.local` / `changeme`). Keep the screen's look, but the mechanism is a plain credential check — NEVER real OAuth/SSO, an allowlist, or a random/mock success. This gives the operator one known credential to sign into every UAT. Exact server action + seed in prisma-setup.md §5a. (Apps with no login: skip.)
   - **Error boundary → report to the 🐞 widget:** add `app/error.tsx` AND `app/global-error.tsx` (both `"use client"`). In a `useEffect`, call `window.__fittReportError?.(error.message, error.stack, "boundary")` then render a minimal fallback UI. CRN injects a feedback widget that already catches uncaught errors / promise rejections / 5xx fetch responses on its own — the boundary adds React *render* errors (which React swallows and would otherwise blank the page) so they surface as auto-detected errors the operator can send to fix.
   - **Install the latest Next.js explicitly: `npm install next@latest react@latest react-dom@latest`** — currently **Next 16**. Do NOT pin or install Next 14/15. Update `package.json` scripts (`next dev`/`build`/`start`, remove vite), add `next.config.ts` (`.ts` config is supported in current Next) with `output: "standalone"`.

5. **Add Prisma + PostgreSQL** (see prisma-setup.md):
   - Install `prisma` + `@prisma/client`; create `prisma/schema.prisma` (`provider = "postgresql"`, `url = env("DATABASE_URL")`).
   - Derive the data model from the prototype's data (`src/data.ts` / `src/types.ts`) + BRD/PRD.
   - **Every REQUIRED field needs a type-appropriate `@default`** (`String→""`, `Int→0`, `Boolean→false`, `DateTime→now()`, `Enum→`a real variant, list→`[]`; foreign keys → make optional `?`). The image self-migrates with `db push` against a DB that may already hold rows, so a later version can only add a required column if it has a default to backfill old rows — otherwise the demo crashes on start. Never blanket-`@default("")`; pick by type (see prisma-setup.md §2).
   - Add `lib/prisma.ts` (client singleton). Replace the prototype's in-memory/mock data with Prisma-backed reads/writes via server components / route handlers / server actions.
   - Add `prisma/seed.ts` from the mock data — **idempotent: `upsert` keyed by a stable id, never bare `create`/`createMany`** (it re-runs on every deploy). Wire `package.json` `"prisma": { "seed": "tsx prisma/seed.ts" }` and add `tsx`. Put `DATABASE_URL` in `.env.example`.
   - **If the app has authentication**, the seed MUST also upsert the **dev Admin user** whose email is `DEV_EMAIL` (fallback `dev@fitt.local`) so the standardized login (step 4) has a matching account with full access. The login itself checks the env credentials, not a hashed DB password — see prisma-setup.md §5a.

6. **Docker.** Set `output: "standalone"` in `next.config.ts` — this is REQUIRED (CRN ships only `.next/standalone`). You do NOT need to hand-craft the Dockerfile/compose: **CRN writes its own deterministic production `Dockerfile` and `docker-compose.customer.yml` at build time** and overwrites any it finds. The app image CRN builds is **self-migrating**: on start it runs `prisma db push` (data-safe) against an external `DATABASE_URL`, seeds by default (`DEMO_SEED=0` disables), then serves — there is no separate migrate image and no bundled database. Your job is only to make `output: "standalone"` + a committed lockfile + a correct `prisma/schema.prisma` + the idempotent seed. (`assets/Dockerfile` is a reference of the shape CRN produces.)

7. **Make it build.** Run `npm install` (this generates `package-lock.json` — **commit it**; CRN's image build uses `npm ci`, which requires the lockfile), then `npx prisma generate`, then `npx next build`, and make the build PASS. The build MUST NOT require a live database — keep every Prisma-reading page/route `export const dynamic = "force-dynamic"` and run `prisma generate` (no connection). Fix ONLY what blocks the build. Never stub or fake data to force a green build.

8. **Write `BUILD_NOTES.md`** at the root: the product in 1-2 lines; the stack (Next.js App Router + Prisma + Postgres + Docker); the exact commands (install / `prisma generate` / dev / build / `docker build`); the DB schema summary; a bullet list of everything you changed; a **coverage line** (`ported N/N screens, M source files`); and a **"Not ported"** section listing anything from `PORT_CHECKLIST.md` you deliberately left out, with the reason (empty section = nothing dropped).

9. **Write `TEST_CASES.md`** at the root — the test script the customer's own testers run against this demo (see test-cases.md). In the briefs' language (normally Thai), one section per screen from `PORT_CHECKLIST.md`, numbered `TC-01`… across the whole file, with the six columns `ID | ฟังก์ชันที่ทดสอบ | ขั้นตอน / สิ่งที่กรอก | ผลลัพธ์ที่คาดหวัง | ผลการทดสอบจริง | สถานะ`. Every screen gets both the happy path and the **negative** cases that fit the fields you actually built (empty required field, letters in a number field, negative/zero price, over-long text, `<script>alert('x')</script>` in a text field, opening an inner page while logged out …). **Leave `ผลการทดสอบจริง` and `สถานะ` EMPTY** — you did not run the app; the tester fills them in. Close the file with **"สิ่งที่ demo นี้ยังไม่รองรับ (ทราบล่วงหน้า)"** listing the rules this port genuinely does not enforce — do NOT add features to make cases pass, and do NOT hide them.

10. **Honest outcome — a green build is NOT the finish line.** Finish only when ALL hold:
   - (a) `npx next build` passes,
   - (b) **every line in `PORT_CHECKLIST.md` is ticked**, AND
   - (c) `BUILD_NOTES.md` + `TEST_CASES.md` exist and describe what you really did.

   `next build` succeeds happily on an app missing half its screens, so re-read the checklist before you declare done and port whatever is still open. A green build over an unticked checklist is a **FAILED** build, not a finished one. If the build genuinely cannot pass, STOP and write the exact blocking error in `BUILD_NOTES.md` — never fake success.

## Hard rules
- Faithful UI/UX — port the prototype, do not redesign or add features. **(One exception: the login — always standardize it to an env email+password check; see step 4.)**
- **Port EVERYTHING — never consolidate, simplify, or skip.** Every prototype screen/component gets a counterpart, whatever the file count. `PORT_CHECKLIST.md` must exist, cover the whole source tree, and be fully ticked before you finish — see steps 3 and 10.
- **Auth is ALWAYS an env email+password login** (`DEV_EMAIL`/`DEV_PASSWORD`), never the prototype's SSO/allowlist/mock. The operator must be able to sign into every UAT with one known credential.
- One clean path: App Router (no `pages/`), Prisma singleton, standalone Docker.
- `next build` must succeed WITHOUT a database. Never fake a passing build.
- **Always install `next@latest` (the current major — Next 16). Never install or pin Next 14/15** — current App Router APIs and `next.config.ts` require the latest.
- Explain every change in `BUILD_NOTES.md`, and ship a `TEST_CASES.md` whose result columns are blank — never write down results for tests you did not run.
