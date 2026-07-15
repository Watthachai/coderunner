import type { NextConfig } from "next";
import os from "os";

// Next 16 blocks cross-origin requests to dev-only assets (e.g. /_next/webpack-hmr)
// by default. Auto-allow the dev server's own LAN IPv4 addresses so HMR works
// when the dashboard is opened over the LAN (e.g. http://172.168.x:3000) instead
// of only localhost. Dev-only; ignored by `next build`/`next start`.
function lanHosts(): string[] {
  const hosts: string[] = [];
  for (const ifaces of Object.values(os.networkInterfaces())) {
    for (const iface of ifaces ?? []) {
      if (iface.family === "IPv4" && !iface.internal) hosts.push(iface.address);
    }
  }
  return hosts;
}

// NOTE: the backend URL is resolved in app/lib/config.ts — NEXT_PUBLIC_CRN_API
// (or the _BASE alias) when set, otherwise derived from the page host at runtime
// (http://<page-host>:8080). We deliberately do NOT force a NEXT_PUBLIC_CRN_API
// default here: the old `env` block pinned it to localhost:8080 and broke LAN
// access, since the browser then hit localhost on the VIEWER's machine.
const nextConfig: NextConfig = {
  allowedDevOrigins: lanHosts(),
};

export default nextConfig;
