# Service-status switch (FITT Billing)

Required on every build and edit. FITT Billing can suspend a delivered app (unpaid subscription, lapsed maintenance), put it in read-only mode, and reactivate it. The app learns its status from FITT Builder through the shipped switch. Never write your own switch and never edit the shipped files.

1. Copy `assets/service-status.mjs` unchanged to `lib/service-status.mjs` (or wherever the app keeps platform code, e.g. `src/platform/`), replacing any earlier copy: this is version 2, which knows read-only. Keep `allowJs` in tsconfig (the Next.js default) so the import type-checks.
2. Call it first in the app's request proxy. Next.js 16 and later use `proxy.ts` at the project root, which runs on the Node.js runtime; read the installed Next.js docs for the installed major's file name and runtime. If the app already has a proxy (authentication, redirects), keep that logic after the switch:

```ts
import { NextResponse, type NextRequest } from "next/server";
import { serviceStatusResponse } from "./lib/service-status.mjs";

// Writes that must still work while the app is read-only: sign-in and sign-out, and any
// search or export the app sends by POST. A path ending in "/" covers everything under it.
const allowWrites = ["/api/auth/"];

export async function proxy(request: NextRequest) {
  const answer = await serviceStatusResponse(request, { allowWrites });
  if (answer) return answer;
  return NextResponse.next(); // or the app's existing proxy logic
}

export const config = { matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"] };
```

   The matcher must cover every page and every API route. Excluding an application path lets a suspended customer keep using it, or a read-only one change data. `allowWrites` lists the app's real sign-in and sign-out paths (a server action posts to its page's path) and nothing that creates, changes or deletes data.
3. Add `app/api/health/route.ts` answering 200 `{ "ok": true }` without secrets or database details. It stays reachable while the app is suspended.
4. Add `app/api/service-status/route.ts` for the read-only banner, importing from wherever step 1 put the switch:

```ts
import { serviceStatusNow } from "../../../lib/service-status.mjs";

export const dynamic = "force-dynamic";

export async function GET() {
  return Response.json(await serviceStatusNow(), { headers: { "cache-control": "no-store" } });
}
```

5. Copy `assets/fitt-service-status.js` unchanged to `public/fitt-service-status.js`. In the root layout, load it in `<head>` and render its empty placeholder as the first child of `<body>`; while the app is read-only it shows FITT Billing's message there, at the top of every page:

```tsx
<html lang="th">
  <head>
    <script src="/fitt-service-status.js" defer></script>
  </head>
  <body>
    <div id="fitt-service-status" hidden suppressHydrationWarning />
    {children}
  </body>
</html>
```

   The script only fills that placeholder. Never let it, or anything else, add nodes to `<body>` before React hydrates: React then throws the page away and renders it again. Where it is simple, also disable the app's save, create and delete buttons while `GET /api/service-status` says `read_only`; the switch refuses those writes at the server either way.
6. The switch reads `FITT_STATUS_URL` and `FITT_LICENSE_KEY` at request time. Never inline them at build time, in `next.config` `env`, or as `NEXT_PUBLIC_` variables. Document both in BUILD_NOTES as optional runtime variables: unset means the switch is off and the app runs normally.
7. Never call the real FITT Builder from tests. The release checker runs the customer image against a fake status server:
   - suspended: `/` must answer 503 with the suspension page, and `/api/health` must not;
   - read-only: pages must still be served, `POST /crn-verify-write` must answer 423 with the message, `GET /api/service-status` must answer `read_only` with the message, and the page at `/` (after any sign-in redirect) must load `/fitt-service-status.js` and hold the placeholder;
   - active: the app must work normally.
