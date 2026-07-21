# Prisma + PostgreSQL setup for Next.js (App Router)

Goal: replace the prototype's in-memory / mock data with a real Prisma + PostgreSQL layer, derived from the prototype's data shapes and the BRD/PRD — while keeping `next build` green WITHOUT a live database.

## 1. Install

```bash
npm i @prisma/client
npm i -D prisma
npx prisma init --datasource-provider postgresql   # creates prisma/schema.prisma + .env
```

`@prisma/client` and `prisma` are on Next.js's auto-external list, so you do NOT need `serverExternalPackages` in `next.config.ts`.

## 2. schema.prisma

`prisma/schema.prisma` (provider postgresql, url from env). Derive the models from the prototype's `src/data.ts` / `src/types.ts` and the BRD/PRD entities. Example shape — REPLACE with the real model:

```prisma
generator client {
  provider = "prisma-client-js"
}

datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

// Derived from src/types.ts `Task` + BRD "task board" entity.
model Task {
  id         String   @id @default(cuid())
  title      String
  done       Boolean  @default(false)
  created_at DateTime @default(now())
  updated_at DateTime @updatedAt
}
```

Mapping guide from a TS mock to Prisma:
- `id: string` -> `@id @default(cuid())` (or `Int @id @default(autoincrement())` if the mock used numeric ids).
- `string` -> `String`, `number` -> `Int` (or `Float`/`Decimal` for money), `boolean` -> `Boolean`, `Date`/ISO string -> `DateTime`.
- optional `foo?: T` -> `foo T?`.
- union/enum literal (`"todo" | "doing" | "done"`) -> a Prisma `enum`.
- nested arrays/relations -> separate models + a relation (`@relation`) with a foreign-key field.
- add a `created_at DateTime @default(now())` where it helps seeding/ordering.

### Column naming — pick ONE convention and use it EVERYWHERE (critical)

Choose a single style for scalar/column names and apply it to **every field in every
model**. Mixing styles is a real bug: the Prisma field name is exactly what your
queries use, so `prisma.customer.findMany({ orderBy: { createdAt } })` fails at
runtime if the schema declared `created_at` (and vice-versa).

- **Recommended: `snake_case`** for all DB columns (`created_at`, `order_id`,
  `product_name`) — it mirrors the prototype's `src/types.ts` UI shapes 1:1, so the
  `.map()` from Prisma rows to UI props stays trivial.
- Do **NOT** mix — e.g. `Customer.createdAt` in one model and `Order.created_at` in
  another. Grep the finished `schema.prisma` and every `orderBy`/`where`/`select`
  in `app/` to confirm they all use the same names.
- If code truly needs camelCase Prisma fields but snake_case DB columns, use
  `@map`: `createdAt DateTime @default(now()) @map("created_at")` — but consistency
  beats cleverness; prefer plain snake_case throughout.

## 3. Prisma client singleton — `lib/prisma.ts`

Next.js dev hot-reload re-imports modules, which would open a new pool each time. Use the standard global singleton:

```ts
import { PrismaClient } from "@prisma/client";

const globalForPrisma = globalThis as unknown as { prisma?: PrismaClient };

export const prisma =
  globalForPrisma.prisma ?? new PrismaClient();

if (process.env.NODE_ENV !== "production") globalForPrisma.prisma = prisma;
```

Import `{ prisma }` from `@/lib/prisma` everywhere on the server. Never instantiate `new PrismaClient()` elsewhere.

## 4. Use Prisma instead of the mock

Replace mock reads/writes with Prisma via the server:

**Read in a Server Component** (`app/page.tsx`):

```tsx
import { prisma } from "@/lib/prisma";
import AppView from "@/components/AppView"; // the ported "use client" UI

export default async function Page() {
  const tasks = await prisma.task.findMany({ orderBy: { created_at: "asc" } });
  return <AppView initialTasks={tasks} />;
}
```

**Mutate with a Server Action** (co-locate or in `app/actions.ts`):

```ts
"use server";
import { prisma } from "@/lib/prisma";
import { revalidatePath } from "next/cache";

export async function addTask(title: string) {
  await prisma.task.create({ data: { title } });
  revalidatePath("/");
}
```

Call the action from the client component (form action or event handler). Alternatively expose a **Route Handler** at `app/api/tasks/route.ts` (`export async function GET/POST(req: Request)`) and `fetch` it. Prefer server actions for simple CRUD; use route handlers when the prototype clearly spoke to an HTTP API.

The old in-memory array (e.g. `let tasks = [...]` in `data.ts`) is DELETED; its contents move to the seed (below).

## 5. Seed — `prisma/seed.ts`

Port the prototype's mock array into a seed so the app has data. **The seed MUST
be idempotent** — the delivered app image self-migrates on start and runs
`prisma db seed` on EVERY start (seed runs by default; `DEMO_SEED=0` disables it),
so re-running must never duplicate rows or fail on a unique constraint. **Always `upsert` keyed by a stable id** —
never bare `create` / `createMany` (those insert duplicates on re-run unless the
seeded field happens to have a unique constraint):

```ts
import { PrismaClient } from "@prisma/client";
const prisma = new PrismaClient();

// Give every seeded row a stable, deterministic id so the upsert key is fixed.
const tasks = [
  { id: "t1", title: "Design the landing page" },
  { id: "t2", title: "Wire up the API" },
];

async function main() {
  for (const t of tasks) {
    await prisma.task.upsert({ where: { id: t.id }, update: t, create: t });
  }
}

main().finally(() => prisma.$disconnect());
```

Wire it in `package.json`:

```json
"prisma": { "seed": "tsx prisma/seed.ts" }
```

Add `tsx` as a dev dep (`npm i -D tsx`). `prisma db seed` needs a live DB — it is
NOT part of `next build`; the delivered app image runs it on start (by default;
`DEMO_SEED=0` disables), and you can run it on demand locally.

### 5a. Auth — standardized env email+password login (CRITICAL)

Every demo with a login uses ONE standardized mechanism, **regardless of what the
prototype did** (Google/SSO, an email allowlist, OAuth, a random mock): an email +
password form validated ONLY against `DEV_EMAIL` / `DEV_PASSWORD` from env. This
gives the operator one known credential to sign into every UAT — no provider to
configure, no allowlist to satisfy. Replace the prototype's login mechanism; keep
its look.

**The login server action checks env, not a hashed DB password** — simplest and
deterministic for a demo:

```ts
"use server";
const DEV_EMAIL = process.env.DEV_EMAIL ?? "dev@fitt.local";
const DEV_PASSWORD = process.env.DEV_PASSWORD ?? "changeme";

export async function loginAction(email: string, password: string) {
  if (email.trim().toLowerCase() !== DEV_EMAIL.toLowerCase() || password !== DEV_PASSWORD) {
    return { error: "Invalid email or password" };
  }
  // If the app has a User model, make sure the Admin account exists, then attach the
  // session to it so role-based UI works. (No User model → just set a session cookie.)
  const user = await prisma.user.upsert({
    where: { email: DEV_EMAIL },
    update: {},
    create: { email: DEV_EMAIL, name: "Dev Admin", role: "Admin" /* + app's required fields */ },
  });
  await setSessionUserId(user.id);
  return { user };
}
```

- **The login accepts ONLY the env credential.** Do NOT keep the prototype's SSO
  button, allowlist, `Math.random()` mock, or a password-hash scheme.
- The seed (§5) **also** upserts this same dev Admin user, so the account is present
  even before anyone logs in and other pages that list/reference users work.
- Adjust `create` to the app's ACTUAL `User` model (required columns, role enum). If
  the model has a `password` column, you may store `DEV_PASSWORD` (plain is fine for a
  demo) — but the login check above is against env, not that column.
- JWT-based apps: mint the token here on a successful env check — never bake a token
  into the image.
- CRN advertises `DEV_EMAIL` / `DEV_PASSWORD` (with these defaults) in the build's
  `env` contract, and the delivered image seeds by default. Apps with no login: skip.

## 6. DATABASE_URL + .env

`.env.example` (committed) documents the contract; real `.env` is gitignored:

```
DATABASE_URL="postgresql://USER:PASSWORD@localhost:5432/APP?schema=public"
```

For local dev the operator runs Postgres (docker or native). In the container, `DATABASE_URL` is injected at runtime (compose/orchestrator), NOT baked into the image.

## 7. Schema application: this pipeline uses `db push` (no migration history)

The delivered app image is **self-migrating**: on start it runs `prisma db push`
against the external `DATABASE_URL`, then serves — it does **not** use `prisma
migrate`. The harness regenerates `schema.prisma` from the prototype on every
build, so there is no migration history to maintain; `db push` diffs the live DB
against the schema directly. Match that model:

- **Do NOT create or commit a `prisma/migrations/` directory** and do NOT run
  `prisma migrate dev` / `migrate deploy`. They add a checksummed history that this
  regenerate-each-build pipeline can't keep consistent (a re-squashed init migration
  fails the checksum on the next version). One way only: `schema.prisma` is the
  single source of truth.
- Additive schema changes across versions apply cleanly and keep the data; a
  **destructive** change (dropped/retyped column with data) makes `db push` error
  and the container refuse to start rather than lose data — that's intended and
  data-safe, the operator handles it.
- `db push` / `db seed` need a reachable DB, so they NEVER run inside `next build` —
  they run when the app image starts. The only DB command you may run to
  sanity-check the schema locally is `npx prisma db push` against a dev database.

## 8. Make `next build` pass WITHOUT a live DB (critical)

`next build` must NOT connect to Postgres. Two things make this safe:

1. **Generate the client, don't touch the DB.** `prisma generate` only reads `schema.prisma`; it needs no connection. Put it in `postinstall` (`"postinstall": "prisma generate"`) so `npm install` produces the client, and it runs again fine during build. In the Dockerfile, run `npx prisma generate` before `next build`.

2. **Don't run DB queries at build time.** A query executes at build only if a page is statically prerendered AND calls Prisma at the module/render top level. Keep DB-reading pages dynamic so their code runs at request time, not build time:
   - Add `export const dynamic = "force-dynamic";` to any page/route that calls Prisma, **or**
   - read a request-time API (`cookies()`, `headers()`, `searchParams`) in that route, which opts it out of static prerender, **or**
   - mark the route `export const revalidate = 0;`.

   Without this, `next build` tries to prerender the page, runs the Prisma query, and fails with `Can't reach database server`. `force-dynamic` is the simplest, most reliable guard for a data-backed prototype.

3. If a build step still imports Prisma in a context that connects (rare), guard it so the query only runs at runtime. Do NOT stub or fake data to get a green build — fix the render timing instead.

Result: `npm install` -> `prisma generate` (no DB) -> `next build` (no DB) all pass. The DB is only needed for `dev`, `migrate`, `seed`, and running the container.

## 9. Checklist

- [ ] `@prisma/client` + `prisma` installed; `prisma/schema.prisma` with `provider = "postgresql"`, `url = env("DATABASE_URL")`.
- [ ] Models derived from `src/data.ts`/`src/types.ts` + BRD/PRD.
- [ ] `lib/prisma.ts` singleton; all server code imports it.
- [ ] Mock reads/writes replaced by Prisma in server components / actions / route handlers.
- [ ] `prisma/seed.ts` carries the old mock data; `prisma.seed` script + `tsx` dev dep.
- [ ] Seed is **idempotent** — `upsert` keyed by a stable id (re-runs on every deploy; no bare `create`/`createMany`).
- [ ] `package-lock.json` committed (CRN's image build uses `npm ci`).
- [ ] `postinstall: prisma generate`; DB-reading pages are `force-dynamic`.
- [ ] `.env.example` has `DATABASE_URL`; `.env` gitignored.
- [ ] `npm install` and `npx next build` pass with NO database running.
