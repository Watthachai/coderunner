"use client";

import type { BuildingJob } from "../lib/types";
import { useJobStream } from "../lib/useJobStream";
import { useTick } from "../lib/useTick";
import { formatElapsed } from "../lib/format";
import { PhaseTrack, phaseRank } from "./PhaseTrack";

function BuildingCard({ job, nowMs }: { job: BuildingJob; nowMs: number }) {
  const stream = useJobStream(job.project_id, job.build_no, { enabled: true });

  // Furthest phase reached from the live feed. Past phases imply prior ones
  // completed, so the track reads correctly even if we connected mid-build.
  let rank = 0;
  for (const e of stream.events) {
    if (e.event === "build_phase" && e.phase) {
      rank = Math.max(rank, phaseRank(e.phase));
    } else if (e.event === "tool_call" || e.event === "assistant_text") {
      rank = Math.max(rank, 1); // Claude is working
    }
  }
  const terminal = stream.result !== null || stream.status === "closed";

  // The most recent meaningful line to surface under the track.
  const lastTool = [...stream.events]
    .reverse()
    .find((e) => e.event === "tool_call");
  const lastText = [...stream.events]
    .reverse()
    .find((e) => e.event === "assistant_text" && e.text);
  const line = lastTool
    ? `▸ ${lastTool.tool ?? "tool"}${lastTool.file ? " " + lastTool.file : ""}`
    : (lastText?.text?.slice(0, 140) ??
      (stream.status === "open" ? "warming up…" : "connecting…"));

  return (
    <article className="now-card">
      <div className="now-card-head">
        <div className="now-id">
          <span className="now-proj">
            {job.project_name || job.project_id.slice(0, 8)}
          </span>
          <span className="now-org">{job.org_name || "—"}</span>
        </div>
        <span className="now-build">build #{job.build_no}</span>
      </div>

      <PhaseTrack current={rank} done={terminal} />

      <div className="now-foot">
        <span className="now-clock">{formatElapsed(job.started_at, nowMs)}</span>
        <span className="now-line" title={line}>
          {line}
        </span>
      </div>
    </article>
  );
}

export function NowBuilding({
  building,
  queued,
}: {
  building: BuildingJob[];
  queued: number;
}) {
  const nowMs = useTick(1000);

  return (
    <section className="panel panel--hero">
      <header className="panel-head">
        <h2>Now building</h2>
        <span className="panel-meta">{building.length} active</span>
      </header>

      {building.length === 0 ? (
        <div className="idle">
          <span className="idle-mark" />
          <p className="idle-title">Standing by</p>
          <p className="idle-sub">
            {queued > 0
              ? `${queued} ${queued === 1 ? "build" : "builds"} in the queue`
              : "Nothing building right now"}
          </p>
        </div>
      ) : (
        <div className="now-list">
          {building.map((j) => (
            <BuildingCard key={j.job_id} job={j} nowMs={nowMs} />
          ))}
        </div>
      )}
    </section>
  );
}
