# Suspension switch (FITT Billing)

Required on every build and edit. FITT Billing can suspend a delivered app (unpaid subscription, lapsed maintenance) and reactivate it. The app learns its status from FITT Builder through the shipped switch. Never write your own switch and never edit the shipped file.

1. Copy `assets/service-status.mjs` unchanged to `lib/service-status.mjs`. Keep `allowJs` in tsconfig (the Next.js default) so the import type-checks.
2. Call it first in the app's request proxy. Next.js 16 and later use `proxy.ts` at the project root, which runs on the Node.js runtime; read the installed Next.js docs for the installed major's file name and runtime. If the app already has a proxy (authentication, redirects), keep that logic after the switch:

```ts
import { NextResponse, type NextRequest } from "next/server";
import { serviceStatusResponse } from "./lib/service-status.mjs";

export async function proxy(request: NextRequest) {
  const suspended = await serviceStatusResponse(request);
  if (suspended) return suspended;
  return NextResponse.next(); // or the app's existing proxy logic
}

export const config = { matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"] };
```

   The matcher must cover every page and every API route. Excluding an application path lets a suspended customer keep using it.
3. Add `app/api/health/route.ts` answering 200 `{ "ok": true }` without secrets or database details. It stays reachable while the app is suspended.
4. The switch reads `FITT_STATUS_URL` and `FITT_LICENSE_KEY` at request time. Never inline them at build time, in `next.config` `env`, or as `NEXT_PUBLIC_` variables. Document both in BUILD_NOTES as optional runtime variables: unset means the switch is off and the app runs normally.
5. Never call the real FITT Builder from tests. The release checker runs the customer image against a fake status server: when it answers suspended, `/` must answer 503 with the suspension page and `/api/health` must not; when it answers active, the app must work normally.
