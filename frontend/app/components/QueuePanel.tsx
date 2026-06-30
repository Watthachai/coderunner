"use client";

import type { QueuedJob } from "../lib/types";
import { useTick } from "../lib/useTick";
import { formatRelative } from "../lib/format";

export function QueuePanel({ queue }: { queue: QueuedJob[] }) {
  const nowMs = useTick(5000);

  return (
    <section className="panel">
      <header className="panel-head">
        <h2>Incoming</h2>
        <span className="panel-meta">{queue.length} queued</span>
      </header>

      {queue.length === 0 ? (
        <p className="empty">Queue clear.</p>
      ) : (
        <ol className="qlist">
          {queue.map((j, i) => (
            <li key={j.job_id} className="qrow">
              <span className="qidx">{String(i + 1).padStart(2, "0")}</span>
              <div className="qmain">
                <span className="qproj">
                  {j.project_name || j.project_id.slice(0, 8)}
                </span>
                <span className="qsub">
                  {j.org_name || "—"} · build #{j.build_no}
                </span>
              </div>
              <span className="qtime">{formatRelative(j.queued_at, nowMs)}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
