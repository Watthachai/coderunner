import type { ActivityRow } from "../lib/types";
import { formatClock } from "../lib/format";

// Status color is information: started=blue, done=green, failed=red,
// cancelled=gray (a deliberate stop, not an error).
const TONE: Record<string, string> = {
  build_started: "var(--building)",
  build_done: "var(--done)",
  build_failed: "var(--error)",
  build_cancelled: "var(--fg-dim)",
};

const LABEL: Record<string, string> = {
  build_started: "started",
  build_done: "done",
  build_failed: "failed",
  build_cancelled: "cancelled",
};

// A live vertical timeline of recent build events: a connected rail of
// status-colored nodes (newest on top, gently pulsing), each labelled with the
// build it belongs to and the wall-clock time.
export function ActivityFeed({ activity }: { activity: ActivityRow[] }) {
  return (
    <section className="panel">
      <header className="panel-head">
        <h2>Activity</h2>
        <span className="panel-meta">live · recent</span>
      </header>

      {activity.length === 0 ? (
        <p className="empty">No build events yet.</p>
      ) : (
        <ol className="timeline">
          {activity.map((a, i) => {
            const tone = TONE[a.type] ?? "var(--fg-muted)";
            const live = i === 0; // newest event
            return (
              <li className="tl-row" key={`${a.project_id}-${a.at}-${i}`}>
                <span className="tl-rail">
                  <span
                    className={`tl-node${live ? " tl-node--live" : ""}`}
                    style={{ background: tone }}
                  />
                </span>
                <span className="tl-body">
                  <span className="tl-line">
                    <span className="tl-kind" style={{ color: tone }}>
                      {LABEL[a.type] ?? a.type}
                    </span>
                    <span className="tl-proj">
                      {a.project_name || a.project_id.slice(0, 8)}
                      <span className="tl-build"> #{a.build_no}</span>
                    </span>
                  </span>
                  <span className="tl-time">{formatClock(a.at)}</span>
                </span>
              </li>
            );
          })}
        </ol>
      )}
    </section>
  );
}
