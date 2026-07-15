// Backend location. NEXT_PUBLIC_CRN_API is the canonical env var (the skeleton
// also defines NEXT_PUBLIC_CRN_API_BASE in next.config.ts; we accept either,
// preferring CRN_API).
//
// With no env override, derive the backend host from the page so the dashboard
// "just works" when opened over the LAN: the backend runs on the SAME box on
// port 8080, so `<page-protocol>//<page-host>:8080` is correct whether that host
// is localhost or a LAN IP. Falls back to localhost during SSR (no window).
// Set NEXT_PUBLIC_CRN_API only when the backend lives on a different host.

const RAW_BASE =
  process.env.NEXT_PUBLIC_CRN_API ??
  process.env.NEXT_PUBLIC_CRN_API_BASE ??
  (typeof window !== "undefined"
    ? `${window.location.protocol}//${window.location.hostname}:8080`
    : "http://localhost:8080");

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

/**
 * Build the interactive PTY WebSocket URL for a project's terminal.
 *
 * Connects to the NO-AUTH internal endpoint (browsers cannot set the
 * X-API-Key header on a WS handshake):
 *   GET /internal/projects/{id}/terminal
 *
 * Protocol:
 *   server -> client: raw PTY output as BINARY frames
 *   client -> server: TEXT frames, JSON:
 *     { "type": "input",  "data": "<keystrokes>" }
 *     { "type": "resize", "cols": <int>, "rows": <int> }
 */
export function terminalWsUrl(projectId: string): string {
  return `${WS_BASE}/internal/projects/${encodeURIComponent(
    projectId,
  )}/terminal`;
}

export function projectStatusUrl(projectId: string): string {
  return `${API_BASE}/api/v1/projects/${encodeURIComponent(projectId)}/status`;
}

// Request an EDIT build for an existing project (no auth). Body:
//   { "change": "<what to change>" }
// -> 202 { "job_id", "build_no", "git_branch", "status" }. An edit build pulls
// the project's existing branch and --resumes the last session (no reset/zip).
//   POST /internal/projects/{id}/edit
export function projectEditUrl(projectId: string): string {
  return `${API_BASE}/internal/projects/${encodeURIComponent(projectId)}/edit`;
}

// GitHub issues on a project's own repo (no auth; repo-per-project model).
//   GET /internal/projects/{id}/issues
//   -> { "issues": [ { number, title, body, url } ] }  ([] if no repo/owner)
export function projectIssuesUrl(projectId: string): string {
  return `${API_BASE}/internal/projects/${encodeURIComponent(
    projectId,
  )}/issues`;
}

// Enqueue an EDIT build that fixes a specific issue (no auth). The build's
// change = the issue title+body; when it finishes the backend comments on the
// issue. -> 202 { job_id, build_no, status }.
//   POST /internal/projects/{id}/issues/{number}/fix
export function projectIssueFixUrl(projectId: string, number: number): string {
  return `${API_BASE}/internal/projects/${encodeURIComponent(
    projectId,
  )}/issues/${number}/fix`;
}

// Operator-console snapshot (no auth): vitals, in-flight builds, queue,
// projects, recent activity — polled by the dashboard.
export function dashboardUrl(): string {
  return `${API_BASE}/internal/dashboard`;
}

// Durable build traces (no auth): the persisted per-build state history that
// survives after the live WS stream is discarded. Summary reads omit `events`;
// the single-trace read includes the full replayable event stream.
//   GET /internal/traces?limit=N            -> { traces: BuildTrace[] }
//   GET /internal/jobs/{id}/trace           -> BuildTrace (with events)
//   GET /internal/projects/{id}/builds      -> { builds: BuildTrace[] }
export function tracesUrl(limit?: number): string {
  return limit === undefined
    ? `${API_BASE}/internal/traces`
    : `${API_BASE}/internal/traces?limit=${limit}`;
}

export function traceUrl(jobId: string): string {
  return `${API_BASE}/internal/jobs/${encodeURIComponent(jobId)}/trace`;
}

export function projectBuildsUrl(projectId: string): string {
  return `${API_BASE}/internal/projects/${encodeURIComponent(
    projectId,
  )}/builds`;
}

// In-demo feedback (Edit Request Panel). Demo widgets write via PostgREST; these
// are the CRN read/act endpoints the dashboard uses.
//   GET  /internal/feedback?status=new        -> { feedback: FeedbackRequest[] }
//   POST /internal/feedback/{id}/approve      -> enqueues an edit build
//   POST /internal/feedback/{id}/reject
export function feedbackUrl(status?: string): string {
  return status
    ? `${API_BASE}/internal/feedback?status=${encodeURIComponent(status)}`
    : `${API_BASE}/internal/feedback`;
}
export function feedbackApproveUrl(id: string): string {
  return `${API_BASE}/internal/feedback/${encodeURIComponent(id)}/approve`;
}
export function feedbackRejectUrl(id: string): string {
  return `${API_BASE}/internal/feedback/${encodeURIComponent(id)}/reject`;
}

// Skill management (no auth): the harness Agent Skills injected into builds.
//   GET  /internal/skills          -> list
//   PUT  /internal/skills/{name}   -> upsert
//   DELETE /internal/skills/{name} -> remove (non-builtin only)
export function skillsUrl(): string {
  return `${API_BASE}/internal/skills`;
}

export function skillUrl(name: string): string {
  return `${API_BASE}/internal/skills/${encodeURIComponent(name)}`;
}

// Run Claude to improve/expand a skill's SKILL.md (no auth). Body:
//   { "body": "<current SKILL.md>" } -> { "body": "<improved SKILL.md>" }
// Does NOT save — the UI drops the result into the editor for review.
//   POST /internal/skills/{name}/improve
export function skillImproveUrl(name: string): string {
  return `${API_BASE}/internal/skills/${encodeURIComponent(name)}/improve`;
}

// Upload a .zip of a skill folder (multipart/form-data, field "file"); the
// server unzips, parses SKILL.md, upserts, and records a version.
//   POST /internal/skills/upload
export function skillUploadUrl(): string {
  return `${API_BASE}/internal/skills/upload`;
}

// Version history for a skill, newest first.
//   GET /internal/skills/{name}/versions
export function skillVersionsUrl(name: string): string {
  return `${API_BASE}/internal/skills/${encodeURIComponent(name)}/versions`;
}

// Optional: the git remote builds are pushed to. When set, project branches
// in the table link to GitHub. Empty -> branches render as plain text.
export const GIT_REMOTE = process.env.NEXT_PUBLIC_CRN_GIT_REMOTE ?? "";
