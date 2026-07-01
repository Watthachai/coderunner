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
