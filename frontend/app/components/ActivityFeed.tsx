import type { ActivityRow } from "../lib/types";
import { formatClock } from "../lib/format";

// Status color is information: started=blue, done=green, failed=red.
const TONE: Record<string, string> = {
  build_started: "var(--building)",
  build_done: "var(--done)",
  build_failed: "var(--error)",
};

const LABEL: Record<string, string> = {
  build_started: "started",
  build_done: "done",
  build_failed: "failed",
};

export function ActivityFeed({ activity }: { activity: ActivityRow[] }) {
  return (
    <section className="panel">
      <header className="panel-head">
        <h2>Activity</h2>
        <span className="panel-meta">recent</span>
      </header>

      {activity.length === 0 ? (
        <p className="empty">No build events yet.</p>
      ) : (
        <ol className="afeed">
          {activity.map((a, i) => (
            <li key={`${a.project_id}-${a.at}-${i}`} className="arow">
              <span className="atime">{formatClock(a.at)}</span>
              <span
                className="akind"
                style={{ color: TONE[a.type] ?? "var(--fg-muted)" }}
              >
                {LABEL[a.type] ?? a.type}
              </span>
              <span className="aproj">
                {a.project_name || a.project_id.slice(0, 8)}
                <span className="abuild"> #{a.build_no}</span>
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
