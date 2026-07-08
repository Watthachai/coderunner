"use client";

import { useMemo } from "react";
import type { BuildingJob, BuildTrace, FeedEvent } from "../lib/types";
import { useJobStream } from "../lib/useJobStream";
import { useTick } from "../lib/useTick";
import { formatElapsed, formatDuration, formatRelative } from "../lib/format";
import { PhasePipeline, phaseRank } from "./PhaseTrack";
import { BuildConsole } from "./BuildConsole";
import { SpanGraph, traceNodeStates } from "./TraceSpanGraph";

const OUTCOME_COLOR: Record<string, string> = {
  done: "var(--done)",
  failed: "var(--error)",
  cancelled: "var(--fg-dim)",
};

// Derived readouts for the telemetry HUD, computed once per event batch.
interface Telemetry {
  rank: number; // furthest phase reached (PhaseTrack index)
  tools: number; // tool_call count
  files: number; // distinct file paths touched
  current: string; // latest tool_call, formatted (mono, one line)
  sessionId: string | null;
}

function oneLine(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

// Format the latest tool call for the "CURRENT" readout — the single action
// the agent is on right now, in shell/file vocabulary.
function currentAction(e: FeedEvent): string {
  const tool = e.tool ?? "tool";
  if (e.tool === "Bash") {
    const cmd = oneLine(e.text ?? "");
    return cmd ? `$ ${cmd}` : "bash";
  }
  if (e.file) return `${tool} ${e.file}`;
  const t = oneLine(e.text ?? "");
  return t ? `${tool} ${t}` : tool;
}

function readTelemetry(events: FeedEvent[]): Telemetry {
  let rank = 0;
  let tools = 0;
  let current = "";
  let sessionId: string | null = null;
  const files = new Set<string>();

  for (const e of events) {
    if (e.event === "build_phase" && e.phase) {
      rank = Math.max(rank, phaseRank(e.phase));
    } else if (e.event === "tool_call") {
      rank = Math.max(rank, 1); // Claude is working
      tools += 1;
      if (e.file) files.add(e.file);
      current = currentAction(e);
    } else if (e.event === "assistant_text") {
      rank = Math.max(rank, 1);
    }
    if (e.session_id && !sessionId) sessionId = e.session_id;
  }

  return { rank, tools, files: files.size, current, sessionId };
}

function BuildingCard({ job, nowMs }: { job: BuildingJob; nowMs: number }) {
  const stream = useJobStream(job.project_id, job.build_no, { enabled: true });

  const tel = useMemo(() => readTelemetry(stream.events), [stream.events]);
  const terminal = stream.result !== null || stream.status === "closed";

  const streamPhase =
    terminal || stream.result !== null
      ? "done"
      : stream.status === "open"
        ? "live"
        : "connecting";

  const live = streamPhase === "live";

  return (
    <article className={`bc ${live ? "bc--live" : ""}`}>
      <div className="bc-scan" aria-hidden="true" />

      <header className="bc-head">
        <div className="bc-id">
          <span className="bc-eyebrow">now building</span>
          <span className="bc-proj">
            {job.project_name || job.project_id.slice(0, 8)}
          </span>
          <span className="bc-org">{job.org_name || "—"}</span>
        </div>
        <span className="bc-build">build #{job.build_no}</span>
      </header>

      {/* TELEMETRY HUD — live mission readouts. */}
      <div className="hud">
        <div className="hud-cell hud-cell--clock">
          <span className="hud-label">elapsed</span>
          <span className="hud-clock">
            {formatElapsed(job.started_at, nowMs)}
          </span>
        </div>
        <div className="hud-cell">
          <span className="hud-label">tools</span>
          <span className="hud-num">{tel.tools}</span>
        </div>
        <div className="hud-cell">
          <span className="hud-label">files</span>
          <span className="hud-num">{tel.files}</span>
        </div>
        <div className="hud-cell hud-cell--now">
          <span className="hud-label">current</span>
          <span className="hud-current" title={tel.current}>
            {tel.current || (live ? "thinking…" : "awaiting link")}
          </span>
        </div>
      </div>

      {/* Signature: the illuminated phase pipeline. */}
      <PhasePipeline current={tel.rank} done={terminal} />

      {/* Hero: the live Claude stream as a real terminal. */}
      <BuildConsole
        events={stream.events}
        sessionId={tel.sessionId}
        phase={streamPhase}
      />
    </article>
  );
}

// The idle console. When nothing is building, the reactor stays lit and the
// console holds the last finished build's trace on screen — "armed" and ready
// for the next build or an incoming edit-request API call, rather than snapping
// to a blank standby.
function IdleConsole({
  queued,
  lastTrace,
  nowMs,
}: {
  queued: number;
  lastTrace: BuildTrace | null;
  nowMs: number;
}) {
  const outcomeColor = lastTrace
    ? (OUTCOME_COLOR[lastTrace.outcome] ?? "var(--fg-dim)")
    : "var(--fg-dim)";

  return (
    <div className={`standby${lastTrace ? " standby--armed" : ""}`}>
      <div className="reactor" aria-hidden="true">
        <span className="reactor-core" />
        <span className="reactor-ring" />
      </div>
      <p className="standby-title">
        {lastTrace ? "ARMED · awaiting request" : "STANDBY · systems nominal"}
      </p>
      <p className="standby-sub">
        {queued > 0
          ? `${queued} ${queued === 1 ? "build" : "builds"} holding in the queue`
          : lastTrace
            ? "Console idle — ready for the next build or an edit request."
            : "No builds in flight. Console is armed and idle."}
      </p>

      {lastTrace ? (
        <div className="idle-last">
          <div className="idle-last-head">
            <span className="bc-eyebrow">last build</span>
            <span className="idle-last-proj">
              {lastTrace.project_id.slice(0, 8)} #{lastTrace.build_no}
            </span>
            <span
              className="pill"
              style={{ color: outcomeColor, borderColor: outcomeColor }}
            >
              <span
                className="pill-dot"
                style={{ background: outcomeColor }}
                aria-hidden
              />
              {lastTrace.outcome}
            </span>
            <span className="idle-last-rel">
              {formatRelative(
                lastTrace.finished_at ?? lastTrace.created_at,
                nowMs,
              )}
            </span>
          </div>
          <SpanGraph states={traceNodeStates(lastTrace)} compact />
          <div className="idle-last-meta">
            {lastTrace.commit_sha ? (
              <code>commit {lastTrace.commit_sha.slice(0, 7)}</code>
            ) : (
              <span>no commit</span>
            )}
            {lastTrace.branch ? <code>{lastTrace.branch}</code> : null}
            <span>
              {formatDuration(lastTrace.started_at, lastTrace.finished_at)}
            </span>
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function NowBuilding({
  building,
  queued,
  lastTrace = null,
}: {
  building: BuildingJob[];
  queued: number;
  lastTrace?: BuildTrace | null;
}) {
  const nowMs = useTick(1000);

  return (
    <section className="panel panel--hero">
      <header className="panel-head">
        <h2>Now building</h2>
        <span className="panel-meta">{building.length} active</span>
      </header>

      {building.length === 0 ? (
        <IdleConsole queued={queued} lastTrace={lastTrace} nowMs={nowMs} />
      ) : (
        <div className="bc-list">
          {building.map((j) => (
            <BuildingCard key={j.job_id} job={j} nowMs={nowMs} />
          ))}
        </div>
      )}
    </section>
  );
}
