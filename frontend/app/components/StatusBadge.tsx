import type { JobStatus } from "../lib/types";

// The mission asks for an "idle" badge in addition to the JobStatus values;
// "idle" is the UI's name for "no active job" (the project status view reports
// a JobStatus, but a project with no jobs reads as idle).
export type BadgeStatus = JobStatus | "idle";

const LABEL: Record<BadgeStatus, string> = {
  idle: "idle",
  queued: "queued",
  building: "building",
  done: "done",
  failed: "error",
  cancelled: "cancelled",
};

// Duolingo palette — status color is information (queued=amber, building=blue,
// done=green, failed=red, idle/cancelled=nav-gray).
const COLOR: Record<BadgeStatus, string> = {
  idle: "#afafaf",
  queued: "#ff9600",
  building: "#1cb0f6",
  done: "#58cc02",
  failed: "#ff4b4b",
  cancelled: "#afafaf",
};

export function StatusBadge({ status }: { status: BadgeStatus }) {
  const color = COLOR[status] ?? COLOR.idle;
  const label = LABEL[status] ?? status;
  const pulsing = status === "building";

  return (
    <span
      className="badge"
      style={{
        // border + faint fill tinted by the status color
        borderColor: color,
        color,
        boxShadow: pulsing ? `0 0 0 1px ${color}` : undefined,
      }}
    >
      <span
        className={pulsing ? "badge-dot badge-dot--pulse" : "badge-dot"}
        style={{ background: color }}
      />
      {label}
    </span>
  );
}
