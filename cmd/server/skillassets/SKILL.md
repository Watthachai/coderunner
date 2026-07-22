---
name: fitt-build
description: Convert a FITT Builder export (a Vite + React SPA prototype zip plus IDEA/BRD/PRD briefs) in the current directory into a runnable Next.js (App Router, TypeScript) app backed by Prisma + PostgreSQL with a Docker build, faithfully preserving the prototype UI/UX. Use at the start of every Code Runner build.
---

# fitt-build — FITT Builder export -> Next.js + Prisma + Docker

The working directory holds ONE FITT Builder export zip (a **Vite + React 18 single-page app**) and its briefs. Your job: **convert it to a clean, buildable Next.js App Router app with Prisma + PostgreSQL and a Docker build, preserving the prototype's exact UI/UX and behavior.** This is a port, not a redesign.

Read the two guides in this skill before and during the work — they carry the details this summary omits:
- `references/nextjs-conversion.md` — Vite+React SPA -> Next.js App Router (entry points, client/server, import.meta.env, assets, routing, pitfalls).
- `references/prisma-setup.md` — Prisma + Postgres (schema, client singleton, seed, and making `next build` pass with NO live DB).

Use the ready-made `assets/Dockerfile` and `assets/.dockerignore` as your starting templates.

## Steps

1. **Extract & read.** Find the `*.zip` in this directory, extract it in place, then delete the zip. Read `docs/IDEA.md` / `docs/BRD.md` / `docs/PRD.md` (or root-level IDEA/BRD/PRD) — enough to name the product and derive the data model.

2. **Recognize the source.** It is a **Vite + React SPA**: `index.html` (Tailwind CDN + fonts, `<div id="root">`), `src/main.tsx` (createRoot), `src/App.tsx` (root view), `src/index.css`, optional `src/data.ts`/`src/types.ts` (mock data), `vite.config.*`. Confirm before converting.

3. **Convert to Next.js (App Router, TypeScript) — install the LATEST Next.js** — preserve UI/UX exactly (see nextjs-conversion.md):
   - Scaffold an `app/` tree (NOT `pages/`). Port components/styles across.
   - `index.html` -> `app/layout.tsx` (`metadata`, fonts, global CSS import); `src/App.tsx` -> `app/page.tsx`. Delete `main.tsx`/`#root` bootstrap.
   - Client vs server: pages that just render the prototype UI can be client components (`"use client"` on the first line); prefer server components + server actions / route handlers for data.
   - Translate Vite-isms: `import.meta.env.VITE_X` -> `process.env.NEXT_PUBLIC_X` (client) or `process.env.X` (server-only); static assets -> `public/` or static imports; react-router routes -> `app/` segments (or keep a single-page client app if the prototype is one screen).
   - Keep the same styling approach (plain CSS or Tailwind — do not change the visual result).
   - **Authentication — STANDARDIZE to an env email+password login (this OVERRIDES the prototype's auth and the "faithful UI" rule for the login screen only).** Whatever the prototype does to sign in — Google/SSO, an email allowlist, OAuth, or a random mock — REPLACE it with ONE simple email + password form that accepts ONLY the credentials from `DEV_EMAIL` / `DEV_PASSWORD` env (fallbacks `dev@fitt.local` / `changeme`). Keep the screen's look, but the mechanism is a plain credential check — NEVER real OAuth/SSO, an allowlist, or a random/mock success. This gives the operator one known credential to sign into every UAT. Exact server action + seed in prisma-setup.md §5a. (Apps with no login: skip.)
   - **Install the latest Next.js explicitly: `npm install next@latest react@latest react-dom@latest`** — currently **Next 16**. Do NOT pin or install Next 14/15. Update `package.json` scripts (`next dev`/`build`/`start`, remove vite), add `next.config.ts` (`.ts` config is supported in current Next) with `output: "standalone"`.

4. **Add Prisma + PostgreSQL** (see prisma-setup.md):
   - Install `prisma` + `@prisma/client`; create `prisma/schema.prisma` (`provider = "postgresql"`, `url = env("DATABASE_URL")`).
   - Derive the data model from the prototype's data (`src/data.ts` / `src/types.ts`) + BRD/PRD.
   - **Every REQUIRED field needs a type-appropriate `@default`** (`String→""`, `Int→0`, `Boolean→false`, `DateTime→now()`, `Enum→`a real variant, list→`[]`; foreign keys → make optional `?`). The image self-migrates with `db push` against a DB that may already hold rows, so a later version can only add a required column if it has a default to backfill old rows — otherwise the demo crashes on start. Never blanket-`@default("")`; pick by type (see prisma-setup.md §2).
   - Add `lib/prisma.ts` (client singleton). Replace the prototype's in-memory/mock data with Prisma-backed reads/writes via server components / route handlers / server actions.
   - Add `prisma/seed.ts` from the mock data — **idempotent: `upsert` keyed by a stable id, never bare `create`/`createMany`** (it re-runs on every deploy). Wire `package.json` `"prisma": { "seed": "tsx prisma/seed.ts" }` and add `tsx`. Put `DATABASE_URL` in `.env.example`.
   - **If the app has authentication**, the seed MUST also upsert the **dev Admin user** whose email is `DEV_EMAIL` (fallback `dev@fitt.local`) so the standardized login (step 3) has a matching account with full access. The login itself checks the env credentials, not a hashed DB password — see prisma-setup.md §5a.

5. **Docker.** Set `output: "standalone"` in `next.config.ts` — this is REQUIRED (CRN ships only `.next/standalone`). You do NOT need to hand-craft the Dockerfile/compose: **CRN writes its own deterministic production `Dockerfile` and `docker-compose.customer.yml` at build time** and overwrites any it finds. The app image CRN builds is **self-migrating**: on start it runs `prisma db push` (data-safe) against an external `DATABASE_URL`, seeds by default (`DEMO_SEED=0` disables), then serves — there is no separate migrate image and no bundled database. Your job is only to make `output: "standalone"` + a committed lockfile + a correct `prisma/schema.prisma` + the idempotent seed. (`assets/Dockerfile` is a reference of the shape CRN produces.)

6. **Make it build.** Run `npm install` (this generates `package-lock.json` — **commit it**; CRN's image build uses `npm ci`, which requires the lockfile), then `npx prisma generate`, then `npx next build`, and make the build PASS. The build MUST NOT require a live database — keep every Prisma-reading page/route `export const dynamic = "force-dynamic"` and run `prisma generate` (no connection). Fix ONLY what blocks the build. Never stub or fake data to force a green build.

7. **Write `BUILD_NOTES.md`** at the root: the product in 1-2 lines; the stack (Next.js App Router + Prisma + Postgres + Docker); the exact commands (install / `prisma generate` / dev / build / `docker build`); the DB schema summary; and a bullet list of everything you changed.

8. **Honest outcome.** Finish when `npx next build` passes. If it genuinely cannot pass, STOP and write the exact blocking error in `BUILD_NOTES.md` — never fake success.

## Hard rules
- Faithful UI/UX — port the prototype, do not redesign or add features. **(One exception: the login — always standardize it to an env email+password check; see step 3.)**
- **Auth is ALWAYS an env email+password login** (`DEV_EMAIL`/`DEV_PASSWORD`), never the prototype's SSO/allowlist/mock. The operator must be able to sign into every UAT with one known credential.
- One clean path: App Router (no `pages/`), Prisma singleton, standalone Docker.
- `next build` must succeed WITHOUT a database. Never fake a passing build.
- **Always install `next@latest` (the current major — Next 16). Never install or pin Next 14/15** — current App Router APIs and `next.config.ts` require the latest.
- Explain every change in `BUILD_NOTES.md`.
