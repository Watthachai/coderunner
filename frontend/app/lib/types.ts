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
