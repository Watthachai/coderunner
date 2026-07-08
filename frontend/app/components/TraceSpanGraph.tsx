"use client";

// The BUILD TRACE span-graph — a distributed-tracing style view of one durable
// build: a horizontal materialize -> claude -> git -> push conduit whose nodes
// are lit by how far the build actually got, annotated with the commit it
// produced, where it pushed, and its cost / duration / tool·file counts. Click
// "replay" to stream the stored event log back into a terminal well.
//
// A BuildTrace summary (list read) carries no `events`; the full stream is
// fetched on demand from GET /internal/jobs/{id}/trace when replay is opened.

import { useState } from "react";
import type { BuildTrace, FeedEvent } from "../lib/types";
import { fetchTrace } from "../lib/useTraces";
import { formatDuration, formatRelative } from "../lib/format";
import { useTick } from "../lib/useTick";
import { EventFeed } from "./EventFeed";

// The four lifecycle stages, in order — mirrors the live PhasePipeline and the
// backend's phase vocabulary (materialize -> claude -> git -> push).
const PHASES = ["materialize", "claude", "git", "push"] as const;
const CAPTION: Record<(typeof PHASES)[number], string> = {
  materialize: "write files",
  claude: "agent build",
  git: "commit",
  push: "publish",
};

export type NodeState = "done" | "failed" | "stopped" | "pending";

// Derive per-node states from the trace SUMMARY (no events needed). We infer how
// far the build got from the durable facts: a commit sha means git succeeded; a
// "done" outcome means it also pushed. A failed build with no commit stopped in
// the agent phase (the overwhelmingly common case); one WITH a commit made it
// through git and stumbled at publish. "cancelled" is a neutral stop, not a
// failure.
export function traceNodeStates(t: BuildTrace): NodeState[] {
  if (t.outcome === "done") return ["done", "done", "done", "done"];

  const committed = t.commit_sha.trim() !== "";
  const stop: NodeState = t.outcome === "cancelled" ? "stopped" : "failed";

  if (committed) {
    // materialize + claude + git succeeded; publish is where it ended.
    return ["done", "done", "done", stop];
  }
  // No commit — the agent build is where it ended. Materializing the workspace
  // precedes it and is treated as complete.
  return ["done", stop, "pending", "pending"];
}

// Fraction of the conduit to light: up to the furthest non-pending node.
function fillPct(states: NodeState[]): number {
  let furthest = 0;
  states.forEach((s, i) => {
    if (s !== "pending") furthest = i;
  });
  return (furthest / (states.length - 1)) * 100;
}

const OUTCOME_COLOR: Record<string, string> = {
  done: "var(--done)",
  failed: "var(--error)",
  cancelled: "var(--fg-dim)",
};

function shortSHA(sha: string): string {
  return sha.trim().slice(0, 7);
}

// The bare span-graph: the lit conduit + the four annotated nodes. Shared by the
// full trace card and the idle "armed" console (compact).
export function SpanGraph({
  states,
  compact = false,
}: {
  states: NodeState[];
  compact?: boolean;
}) {
  const pct = fillPct(states);
  return (
    <div
      className={`pipe pipe--trace${compact ? " pipe--compact" : ""}`}
      aria-label="build span graph"
    >
      <div className="pipe-rail">
        <div className="pipe-fill" style={{ width: `${pct}%` }} />
      </div>
      <ol className="pipe-nodes">
        {PHASES.map((p, i) => (
          <li key={p} className={`node node--${states[i]}`}>
            <span className="node-core">
              <span className="node-index">{i + 1}</span>
            </span>
            <span className="node-name">{p}</span>
            {!compact ? (
              <span className="node-caption">{CAPTION[p]}</span>
            ) : null}
          </li>
        ))}
      </ol>
    </div>
  );
}

// Replay state machine for one trace's stored event stream.
type Replay =
  | { phase: "closed" }
  | { phase: "loading" }
  | { phase: "open"; events: FeedEvent[] }
  | { phase: "empty" }
  | { phase: "error"; message: string };

export function TraceSpanGraph({ trace }: { trace: BuildTrace }) {
  const nowMs = useTick(30000); // relative timestamp, refreshed lazily
  const [replay, setReplay] = useState<Replay>({ phase: "closed" });

  const states = traceNodeStates(trace);
  const outcomeColor = OUTCOME_COLOR[trace.outcome] ?? "var(--fg-dim)";
  const sha = shortSHA(trace.commit_sha);
  const duration = formatDuration(trace.started_at, trace.finished_at);

  async function toggleReplay() {
    if (replay.phase !== "closed") {
      setReplay({ phase: "closed" });
      return;
    }
    setReplay({ phase: "loading" });
    try {
      const full = await fetchTrace(trace.job_id);
      const events: FeedEvent[] = (full.events ?? []).map((e, i) => ({
        ...e,
        _id: i,
      }));
      setReplay(
        events.length === 0
          ? { phase: "empty" }
          : { phase: "open", events },
      );
    } catch (e) {
      setReplay({
        phase: "error",
        message: e instanceof Error ? e.message : "fetch failed",
      });
    }
  }

  return (
    <article className="bc trace-card">
      <header className="bc-head">
        <div className="bc-id">
          <span className="bc-eyebrow">build trace</span>
          <span className="bc-proj">
            {trace.project_id.slice(0, 8)}
            <span className="trace-buildno"> #{trace.build_no}</span>
          </span>
          <span className="bc-org">
            {trace.mode === "edit" ? "edit build" : "fresh build"} ·{" "}
            {formatRelative(trace.finished_at ?? trace.created_at, nowMs)}
          </span>
        </div>
        <span
          className="pill trace-outcome"
          style={{ color: outcomeColor, borderColor: outcomeColor }}
        >
          <span
            className="pill-dot"
            style={{ background: outcomeColor }}
            aria-hidden
          />
          {trace.outcome}
        </span>
      </header>

      {/* The span-graph: the lit materialize->claude->git->push conduit. */}
      <SpanGraph states={states} />

      {/* Per-build telemetry, hung off the graph like trace metadata. */}
      <div className="hud trace-hud">
        <div className="hud-cell">
          <span className="hud-label">cost</span>
          <span className="hud-num">
            {trace.cost_usd > 0 ? `$${trace.cost_usd.toFixed(2)}` : "—"}
          </span>
        </div>
        <div className="hud-cell">
          <span className="hud-label">duration</span>
          <span className="hud-num">{duration}</span>
        </div>
        <div className="hud-cell">
          <span className="hud-label">tools</span>
          <span className="hud-num">{trace.tool_count}</span>
        </div>
        <div className="hud-cell">
          <span className="hud-label">files</span>
          <span className="hud-num">{trace.file_count}</span>
        </div>
      </div>

      {/* What it committed and where it pushed. */}
      <div className="trace-meta">
        {sha ? (
          <span className="trace-chip trace-chip--sha" title={trace.commit_sha}>
            <span className="trace-chip-key">commit</span>
            <code>{sha}</code>
          </span>
        ) : (
          <span className="trace-chip trace-chip--muted">no commit</span>
        )}
        {trace.branch ? (
          <span className="trace-chip" title={trace.branch}>
            <span className="trace-chip-key">branch</span>
            <code>{trace.branch}</code>
          </span>
        ) : null}
        {trace.remote ? (
          <span className="trace-chip trace-chip--remote" title={trace.remote}>
            <span className="trace-chip-key">→</span>
            <code>{trace.remote.replace(/^https?:\/\//, "")}</code>
          </span>
        ) : null}
        <button
          type="button"
          className="btn btn--ghost btn--sm trace-replay-btn"
          onClick={toggleReplay}
        >
          {replay.phase === "closed"
            ? "replay ▸"
            : replay.phase === "loading"
              ? "loading…"
              : "hide ▾"}
        </button>
      </div>

      {trace.error_msg ? (
        <p className="trace-error">{trace.error_msg}</p>
      ) : null}

      {/* The replayed event stream, rendered in a dark terminal well. */}
      {replay.phase === "open" ? (
        <div className="trace-replay">
          <div className="trace-replay-bar">
            <span className="trace-replay-glyph">◆</span>
            replayed stream · {replay.events.length} events
            {trace.session_id ? (
              <span className="trace-replay-session">
                {trace.session_id.slice(0, 8)}
              </span>
            ) : null}
          </div>
          <EventFeed events={replay.events} />
        </div>
      ) : null}

      {replay.phase === "empty" ? (
        <p className="trace-replay-note">
          Step-by-step replay wasn’t captured for this build (it predates the
          state-trace feature).
        </p>
      ) : null}

      {replay.phase === "error" ? (
        <p className="trace-error">Couldn’t load the stream — {replay.message}</p>
      ) : null}
    </article>
  );
}
