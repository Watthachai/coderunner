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
  id        String   @id @default(cuid())
  title     String
  done      Boolean  @default(false)
  createdAt DateTime @default(now())
  updatedAt DateTime @updatedAt
}
```

Mapping guide from a TS mock to Prisma:
- `id: string` -> `@id @default(cuid())` (or `Int @id @default(autoincrement())` if the mock used numeric ids).
- `string` -> `String`, `number` -> `Int` (or `Float`/`Decimal` for money), `boolean` -> `Boolean`, `Date`/ISO string -> `DateTime`.
- optional `foo?: T` -> `foo T?`.
- union/enum literal (`"todo" | "doing" | "done"`) -> a Prisma `enum`.
- nested arrays/relations -> separate models + a relation (`@relation`) with a foreign-key field.
- always add `createdAt DateTime @default(now())` where it helps seeding/ordering.

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
  const tasks = await prisma.task.findMany({ orderBy: { createdAt: "asc" } });
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

Port the prototype's mock array into a seed so the app has data:

```ts
import { PrismaClient } from "@prisma/client";
const prisma = new PrismaClient();

async function main() {
  await prisma.task.createMany({
    data: [
      { title: "Design the landing page" },
      { title: "Wire up the API" },
    ],
    skipDuplicates: true,
  });
}

main().finally(() => prisma.$disconnect());
```

Wire it in `package.json`:

```json
"prisma": { "seed": "tsx prisma/seed.ts" }
```

Add `tsx` as a dev dep (`npm i -D tsx`). Seeding runs at the operator's discretion with `npx prisma db seed` (needs a live DB) — it is NOT part of `next build`.

## 6. DATABASE_URL + .env

`.env.example` (committed) documents the contract; real `.env` is gitignored:

```
DATABASE_URL="postgresql://USER:PASSWORD@localhost:5432/APP?schema=public"
```

For local dev the operator runs Postgres (docker or native). In the container, `DATABASE_URL` is injected at runtime (compose/orchestrator), NOT baked into the image.

## 7. Migrations vs db push

- **`prisma migrate dev --name init`**: creates a versioned SQL migration in `prisma/migrations/` — the right choice for a real app; commit the migrations.
- **`prisma db push`**: syncs the schema to the DB with no migration history — fine for the earliest prototype iteration.
- Both require a reachable database, so run them locally/against a real DB — never inside `next build`.
- Production applies migrations at deploy time: `npx prisma migrate deploy` (run it as a release step or container entrypoint, before `node server.js`).

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
- [ ] `postinstall: prisma generate`; DB-reading pages are `force-dynamic`.
- [ ] `.env.example` has `DATABASE_URL`; `.env` gitignored.
- [ ] `npm install` and `npx next build` pass with NO database running.
