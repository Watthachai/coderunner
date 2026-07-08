// Wire types mirroring github.com/Watthachai/fitt-coderunner/internal/domain.
// These are the JSON shapes the Go backend serializes. Keep them in lockstep
// with internal/domain/{events,types,status}.go — they are the contract.

// --- enums (internal/domain/status.go) ---

export type JobStatus =
  | "queued"
  | "building"
  | "done"
  | "failed"
  | "cancelled";

export type ProjectStatus = "pending" | "active" | "archived";

// --- normalized live WebSocket event (internal/domain/events.go) ---
//
// The Go BuildEventMsg serializes its Kind field under the json key "event".

export type WSEventKind =
  | "assistant_text"
  | "tool_call"
  | "tool_result"
  | "build_phase"
  | "result"
  | "error";

export interface BuildEventMsg {
  event: WSEventKind;
  tool?: string;
  file?: string;
  text?: string;
  // Code a Write/Edit tool call produced, for the diff view: Write sets `after`
  // (new file); Edit sets both (old->new). Absent for non-file tools.
  before?: string;
  after?: string;
  phase?: string;
  cost_usd?: number;
  session_id?: string;
  job_id?: string;
  timestamp: string; // RFC3339
}

// --- read model GET /api/v1/projects/{id}/status (ProjectStatusView) ---

export interface ProjectStatusView {
  status: JobStatus;
  current_build: number;
  queue_depth: number;
  session_id?: string;
  docker_tag?: string;
}

// A locally-tracked event: the wire message plus a stable client id for keys.
export interface FeedEvent extends BuildEventMsg {
  _id: number;
}

// --- operator console read model (GET /internal/dashboard) ---
// Mirrors internal/domain/dashboard.go. Keep json keys in lockstep.

export interface DashboardVitals {
  projects: number;
  queued: number;
  building: number;
  done_today: number;
  failed_today: number;
}

export interface BuildingJob {
  job_id: string;
  project_id: string;
  project_name: string;
  org_name: string;
  build_no: number;
  branch: string;
  started_at: string | null;
}

export interface QueuedJob {
  job_id: string;
  project_id: string;
  project_name: string;
  org_name: string;
  build_no: number;
  queued_at: string;
}

export interface ProjectRow {
  id: string;
  name: string;
  org_name: string;
  status: ProjectStatus | string;
  current_build: number;
  last_status: JobStatus | "";
  last_branch: string;
  last_activity_at: string | null;
  // Number of ENABLED skills — these apply to every build.
  skill_count: number;
  // Browser URL of the project's own private GitHub repo, when the repo-per-
  // project model is enabled (CRN_GITHUB_OWNER set). Empty otherwise. May carry
  // a trailing ".git" — strip it before linking.
  repo_url: string;
}

// An open GitHub issue on a project's repo (GET /internal/projects/{id}/issues).
export interface Issue {
  number: number;
  title: string;
  body: string;
  url: string;
}

export interface ActivityRow {
  type: string; // build_started | build_done | build_failed
  project_id: string;
  project_name: string;
  build_no: number;
  at: string;
}

export interface DashboardSnapshot {
  vitals: DashboardVitals;
  building: BuildingJob[];
  queue: QueuedJob[];
  projects: ProjectRow[];
  activity: ActivityRow[];
  generated_at: string;
}

// --- build traces (durable per-build state history) ---
// Mirrors internal/domain.BuildTrace (job_traces table). The live WS stream is
// discarded when a build ends; a BuildTrace is the persisted snapshot for the
// retroactive "state trace" view. `events` is populated only by the single-trace
// read (GET /internal/jobs/{id}/trace); list reads (recent / per-project builds)
// return it as [].

export interface BuildTrace {
  job_id: string;
  project_id: string;
  build_no: number;
  outcome: JobStatus | string; // "done" | "failed" | "cancelled"
  mode: string; // "build" | "edit"
  commit_sha: string;
  branch: string;
  remote: string;
  session_id: string;
  cost_usd: number;
  tool_count: number;
  file_count: number;
  error_msg: string;
  events: BuildEventMsg[]; // full stream on detail read; [] on list reads
  started_at: string | null;
  finished_at: string | null;
  created_at: string;
}

// GET /internal/traces           -> { traces: BuildTrace[] }  (summary, no events)
// GET /internal/projects/{id}/builds -> { builds: BuildTrace[] } (summary, no events)
// GET /internal/jobs/{id}/trace  -> BuildTrace                 (with events)
export interface RecentTracesResponse {
  traces: BuildTrace[];
}
export interface ProjectBuildsResponse {
  builds: BuildTrace[];
}

// --- skill management (GET/PUT/DELETE /internal/skills) ---
// Mirrors the `skills` table: harness Agent Skills injected into every build.

export interface Skill {
  name: string;
  description: string;
  body: string;
  enabled: boolean;
  is_builtin: boolean;
  // Extra bundled files keyed by relative path (e.g. "scripts/verify.py").
  // SKILL.md itself stays in `body`; this map carries everything else.
  files: Record<string, string>;
  updated_at: string; // RFC3339
}

// A single recorded revision of a skill (GET /internal/skills/{name}/versions).
// Every user-initiated change (PUT + upload) records one; builtin re-seed does
// not. Newest first in the response.
export interface SkillVersion {
  version: number;
  description: string;
  body: string;
  // Extra bundled files at this revision, keyed by relative path.
  files: Record<string, string>;
  note: string;
  created_at: string; // RFC3339
}

// --- in-demo feedback loop (edit-request contract) ---
// The shape a CRN-built demo's feedback widget submits and the CRN Edit Request
// Panel consumes. Mirrors the planned `feedback_requests` table (see
// docs/superpowers/specs/2026-07-07-feedback-loop-design.md). Frontend-first for
// the mock intake slice; a Go domain mirror + Supabase table follow.

export type FeedbackCategory = "bug" | "feature" | "style";
export type FeedbackPriority = "low" | "med" | "high";
export type FeedbackStatus =
  | "new"
  | "reviewing"
  | "approved"
  | "building"
  | "done"
  | "rejected";

// One element the reporter pinned on the live page: a stable selector, the
// bounding box, a captured region screenshot, and what they want changed.
export interface FeedbackPin {
  selector: string;
  label: string;
  note: string;
  box: { x: number; y: number; w: number; h: number };
  region_shot: string; // storage path / URL (data URI in the mock)
}

export interface FeedbackPayload {
  pins: FeedbackPin[];
  full_shot: string; // full-page screenshot
  viewport: { w: number; h: number };
  user_agent: string;
}

export interface FeedbackRequest {
  id: string;
  project_id: string;
  status: FeedbackStatus;
  category: FeedbackCategory;
  priority: FeedbackPriority;
  note: string; // the overall ask
  page_url: string;
  reporter: string; // optional; "" when anonymous
  payload: FeedbackPayload;
  created_at: string; // RFC3339
}
