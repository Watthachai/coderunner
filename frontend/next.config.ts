import type { NextConfig } from "next";

// Where the Go backend (CRN daemon) lives, for client fetches + the live-log
// WebSocket. NEXT_PUBLIC_CRN_API is the canonical var the dashboard reads
// (see app/lib/config.ts); NEXT_PUBLIC_CRN_API_BASE is kept as an accepted
// alias from the original skeleton. Both default to http://localhost:8080.
const apiBase =
  process.env.NEXT_PUBLIC_CRN_API ??
  process.env.NEXT_PUBLIC_CRN_API_BASE ??
  "http://localhost:8080";

const nextConfig: NextConfig = {
  env: {
    NEXT_PUBLIC_CRN_API: apiBase,
    NEXT_PUBLIC_CRN_API_BASE: apiBase,
  },
};

export default nextConfig;
