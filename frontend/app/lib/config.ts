// Backend location. NEXT_PUBLIC_CRN_API is the canonical env var (the skeleton
// also defines NEXT_PUBLIC_CRN_API_BASE in next.config.ts; we accept either,
// preferring CRN_API). Both default to localhost:8080.

const RAW_BASE =
  process.env.NEXT_PUBLIC_CRN_API ??
  process.env.NEXT_PUBLIC_CRN_API_BASE ??
  "http://localhost:8080";

// Normalize: strip any trailing slash so path joins are predictable.
export const API_BASE = RAW_BASE.replace(/\/+$/, "");

// http(s):// -> ws(s):// for the live-log WebSocket.
export const WS_BASE = API_BASE.replace(/^http/, "ws");

/**
 * Build the live-log WebSocket URL for a given project + build number.
 *
 * The dashboard connects to the NO-AUTH internal endpoint so it works without
 * an org API key (browsers cannot set the X-API-Key header on a WS handshake):
 *   GET /internal/projects/{id}/jobs/{build_no}/logs
 *
 * The apiKey arg is kept for call-site compatibility but is unused here; the
 * tenant-facing /api/v1 variant (which does require the key) is not used by the
 * internal dashboard.
 */
export function jobLogsWsUrl(
  projectId: string,
  buildNo: number,
  apiKey?: string,
): string {
  void apiKey; // unused: internal endpoint is no-auth
  const path = `/internal/projects/${encodeURIComponent(
    projectId,
  )}/jobs/${buildNo}/logs`;
  const url = new URL(WS_BASE + path);
  return url.toString();
}

export function projectStatusUrl(projectId: string): string {
  return `${API_BASE}/api/v1/projects/${encodeURIComponent(projectId)}/status`;
}
