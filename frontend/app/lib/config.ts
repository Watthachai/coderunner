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
 * The backend mounts this under the API-key-authenticated /api/v1 group:
 *   GET /api/v1/projects/{id}/jobs/{build_no}/logs
 *
 * Browsers cannot set the X-API-Key request header on a WebSocket handshake,
 * so when an API key is supplied we pass it as the `api_key` query parameter.
 * TODO(crn): confirm the api package accepts the key via query string for WS
 * (the REST auth middleware reads the X-API-Key header).
 */
export function jobLogsWsUrl(
  projectId: string,
  buildNo: number,
  apiKey?: string,
): string {
  const path = `/api/v1/projects/${encodeURIComponent(
    projectId,
  )}/jobs/${buildNo}/logs`;
  const url = new URL(WS_BASE + path);
  if (apiKey) url.searchParams.set("api_key", apiKey);
  return url.toString();
}

export function projectStatusUrl(projectId: string): string {
  return `${API_BASE}/api/v1/projects/${encodeURIComponent(projectId)}/status`;
}
