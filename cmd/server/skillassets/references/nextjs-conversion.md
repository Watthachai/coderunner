# Vite + React SPA -> Next.js App Router — conversion guide

You are converting a **FITT Builder export**: a client-only **Vite + React 18** single-page app. Convert it to **Next.js (App Router, latest stable, TypeScript)** while preserving the EXACT UI/UX and behavior. This guide is the map.

## 1. What the export looks like

The extracted zip is a Vite SPA. Expect roughly:

```
index.html            # <head>: Tailwind CDN + Inter/Anuphan fonts; <body><div id="root"></div><script src="/src/main.tsx">
package.json          # deps: react, react-dom, vite, @vitejs/plugin-react; scripts: dev/build/preview
vite.config.js        # defineConfig({ plugins: [react()] })
tsconfig.json         # bundler moduleResolution, jsx: react-jsx, non-strict
src/main.tsx          # createRoot(document.getElementById("root")).render(<App/>)
src/App.tsx           # the root view (often the whole app is one screen)
src/index.css         # global CSS
src/*.tsx             # feature components
src/data.ts | src/types.ts   # in-memory mock data + TS types (may or may not exist)
docs/IDEA.md docs/BRD.md docs/PRD.md   # briefs
public/*              # static assets (may be absent)
```

Some exports use plain CSS, some use Tailwind via the CDN `<script>` in index.html. Detect which and KEEP it.

## 2. Target structure (App Router)

Create a standard App-Router tree at the project root (no `src/` router needed):

```
app/
  layout.tsx      # root layout: <html>/<body>, metadata, global CSS import
  page.tsx        # the root view (was src/App.tsx)
  globals.css     # was src/index.css (+ any Tailwind entry)
components/        # was src/ components (ports 1:1)
lib/              # prisma.ts singleton, helpers
public/           # static assets
prisma/           # schema.prisma, seed.ts
next.config.ts
tsconfig.json
package.json
```

Keep the same component boundaries and file names where practical — this is a port, not a rewrite.

## 3. Entry points: index.html -> layout.tsx + page.tsx

`index.html` becomes `app/layout.tsx`. The `<div id="root">` and the `main.tsx` bootstrap DISAPPEAR — Next owns mounting.

- `<title>` / `<meta>` -> `export const metadata: Metadata = { title, description }`.
- Fonts + global styles: prefer `next/font` for the Google fonts; otherwise keep the `<link>` tags in `layout.tsx`. If the prototype used the Tailwind CDN `<script>`, you MAY keep that `<script>` in `<head>` for a faithful, low-risk port — but the clean path is to install Tailwind properly (see section 6).
- `src/index.css` becomes `app/globals.css`, imported ONCE at the top of `app/layout.tsx`.

Minimal `app/layout.tsx`:

```tsx
import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "…",        // from index.html <title>
  description: "…",  // from BRD/PRD one-liner
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
```

`src/App.tsx` becomes `app/page.tsx`. If `App` uses `useState`/`useEffect`/event handlers (it usually does), the page is a **client component** — put `"use client"` at the very top. Change the default-export name to `Page` if you like; the important part is `export default`.

## 4. Client vs Server components (the core decision)

- App Router files are **Server Components by default**. Add `"use client"` to any file that uses hooks (`useState`, `useEffect`, `useRef`, `useContext`), browser APIs (`window`, `localStorage`), or event handlers.
- A pure interactive prototype screen -> mark it `"use client"` and move on. Faithful UI first.
- Where the prototype reads/writes DATA, prefer the server: fetch in a **Server Component** and pass props down, mutate via **Server Actions** or **Route Handlers**. See prisma-setup.md. Do NOT call Prisma from a client component.
- Common split: `app/page.tsx` is a server component that reads data with Prisma, then renders a `"use client"` child (e.g. `components/AppView.tsx`, the ported `App.tsx`) for interactivity.

## 5. Translate Vite-isms

| Vite / SPA | Next.js App Router |
|---|---|
| `import.meta.env.VITE_FOO` (client) | `process.env.NEXT_PUBLIC_FOO` |
| `import.meta.env.FOO` (server-only secret) | `process.env.FOO` (server components / actions only) |
| `import.meta.env.MODE` / `DEV` / `PROD` | `process.env.NODE_ENV` |
| `import.meta.env.BASE_URL` | usually just `/` (drop it) |
| `<script src="/src/main.tsx">` | delete — Next bootstraps |
| `createRoot(...).render(<App/>)` | delete — Next owns the root |
| `react-router` `<Routes>`/`<Route>` | filesystem routes under `app/<segment>/page.tsx` |
| `useNavigate` / `<Link to>` (react-router) | `useRouter` from `next/navigation` / `<Link href>` from `next/link` |
| `<img src="/logo.png">` from `public/` | keep `public/logo.png`, reference `/logo.png` (or `import` + `next/image`) |
| asset `import logo from "./logo.svg"` | works the same (Next handles static imports) |
| `index.html` `<head>` tags | `metadata` export or `<head>` in `layout.tsx` |

**Env rule:** anything read in a client component MUST be prefixed `NEXT_PUBLIC_`; it is inlined at build time. Server-only values (like `DATABASE_URL`) must NOT be `NEXT_PUBLIC_` and must only be read on the server.

## 6. Styling — keep the prototype's approach

- **Plain CSS**: move `src/index.css` (and any component CSS) into `app/globals.css` / colocated CSS. Import order preserved.
- **Tailwind via CDN `<script>`** (common in FITT exports): the faithful, low-effort option is to keep that `<script src="https://cdn…/@tailwindcss/browser@4">` in `layout.tsx`'s `<head>`. The clean, production option is to install Tailwind v4 locally:
  - `npm i -D tailwindcss @tailwindcss/postcss postcss`
  - `postcss.config.mjs`: `export default { plugins: { "@tailwindcss/postcss": {} } };`
  - `app/globals.css` starts with `@import "tailwindcss";` (+ any `@theme` tokens the prototype used inline).
  Pick ONE approach and be consistent. Do not change the visual result.
- Fonts: replicate the exact families from `index.html` (e.g. Inter + Anuphan). `next/font/google` is cleanest; keeping the `<link>` is acceptable.

## 7. Routing

- **Single-screen prototype (the common case):** one `app/page.tsx` is enough. Keep it a client app if it is purely interactive.
- **react-router present:** map each route to a folder: `/` -> `app/page.tsx`, `/about` -> `app/about/page.tsx`, `/items/:id` -> `app/items/[id]/page.tsx`. Replace `useNavigate()` with `useRouter()` (`next/navigation`) and `<Link to>` with `<Link href>` (`next/link`). Remove `<BrowserRouter>`/`<Routes>` — the filesystem is the router.

## 8. package.json + config

Replace the Vite `package.json` scripts/deps:

```json
{
  "name": "<kebab-product-name>",
  "private": true,
  "scripts": {
    "dev": "next dev",
    "build": "next build",
    "start": "next start",
    "postinstall": "prisma generate"
  }
}
```

Install: `next react react-dom` (runtime), `typescript @types/react @types/node` (dev), plus Prisma (see prisma-setup.md). Remove `vite`, `@vitejs/plugin-react`, `vite.config.js`, `index.html`.

`next.config.ts` — enable standalone output for Docker:

```ts
import type { NextConfig } from "next";
const nextConfig: NextConfig = {
  output: "standalone",
};
export default nextConfig;
```

`tsconfig.json` — use the standard Next.js one (Next writes/patches it on first `next dev`/`build`; ensure `"paths": { "@/*": ["./*"] }` if the prototype used an alias, and `"jsx": "preserve"`, `"moduleResolution": "bundler"`).

## 9. Common pitfalls

- **`"use client"` must be the FIRST line** of the file (before imports). Forgetting it on a hook-using component is the #1 error.
- **`document`/`window` at module top-level** crashes SSR. Access them inside `useEffect` or guard with `typeof window !== "undefined"`.
- **`localStorage` on the server**: only touch it in client components inside effects. If the prototype persisted to localStorage, either keep that (client-only) or move state to Prisma (preferred for real data).
- **Importing a server-only module (Prisma) into a client component** — build error. Keep DB access in server components / actions / route handlers.
- **Hydration mismatch** from `Math.random()`/`Date.now()` during render, or from browser-only branches — compute in `useEffect` or seed deterministically.
- **Assets**: files served at `/foo` in Vite come from `public/foo` in Next. Don't reference `/src/...` at runtime.
- **Do not use `pages/`** — this is App Router. No `_app`, no `_document`, no `getServerSideProps`; use layouts, server components, and route handlers.
- **Metadata**: don't hand-write `<title>` in a client component; use the `metadata` export in a server layout/page.

## 10. Definition of done

`npm install` succeeds and `npx next build` passes. The rendered UI is visually identical to the prototype, the same interactions work, and data now flows through Prisma (see prisma-setup.md) instead of in-memory mocks. `next build` must succeed WITHOUT a live database (see prisma-setup.md, section "Make next build pass WITHOUT a live DB").
