"use client";

import { useEffect, useRef, useState } from "react";
import type { FeedEvent, WSEventKind } from "../lib/types";
import { lineDiff } from "../lib/linediff";

const KIND_LABEL: Record<WSEventKind, string> = {
  assistant_text: "text",
  tool_call: "tool",
  tool_result: "result",
  build_phase: "phase",
  result: "done",
  error: "error",
};

const KIND_COLOR: Record<WSEventKind, string> = {
  assistant_text: "#ededed",
  tool_call: "#1cb0f6",
  tool_result: "#34d399",
  build_phase: "#a78bfa",
  result: "#34d399",
  error: "#f87171",
};

function formatTime(ts: string): string {
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString();
}

// Render the human-meaningful payload for one event, by kind.
function EventBody({ e }: { e: FeedEvent }) {
  switch (e.event) {
    case "tool_call":
      return (
        <span>
          <span className="feed-strong">{e.tool ?? "tool"}</span>
          {e.file ? <span className="feed-file"> {e.file}</span> : null}
        </span>
      );
    case "tool_result":
      return (
        <span className="feed-muted">
          {e.tool ? `${e.tool} → ` : ""}
          {e.file ?? "ok"}
        </span>
      );
    case "build_phase":
      return <span className="feed-strong">{e.phase ?? "phase"}</span>;
    case "result":
      return (
        <span>
          run finished
          {typeof e.cost_usd === "number"
            ? ` · $${e.cost_usd.toFixed(4)}`
            : ""}
          {e.session_id ? ` · session ${e.session_id.slice(0, 8)}` : ""}
        </span>
      );
    case "error":
      return <span className="feed-error">{e.text ?? "error"}</span>;
    case "assistant_text":
    default:
      return <span>{e.text ?? ""}</span>;
  }
}

// The green/red diff a Write/Edit produced (Write => all additions).
function DiffBlock({ before, after }: { before: string; after: string }) {
  const lines = lineDiff(before, after);
  return (
    <pre className="diff mono feed-diff">
      {lines.map((ln, i) => (
        <span
          key={i}
          className={
            ln.kind === "add"
              ? "diff-add"
              : ln.kind === "del"
                ? "diff-del"
                : "diff-ctx"
          }
        >
          {ln.kind === "add" ? "+ " : ln.kind === "del" ? "- " : "  "}
          {ln.text}
          {"\n"}
        </span>
      ))}
    </pre>
  );
}

function FeedRow({ e }: { e: FeedEvent }) {
  const [open, setOpen] = useState(false);
  const hasDiff =
    e.event === "tool_call" && (Boolean(e.before) || Boolean(e.after));

  return (
    <div className="feed-item">
      <div className="feed-row">
        <span className="feed-time">{formatTime(e.timestamp)}</span>
        <span className="feed-kind" style={{ color: KIND_COLOR[e.event] ?? "#ededed" }}>
          {KIND_LABEL[e.event] ?? e.event}
        </span>
        <span className="feed-content">
          <EventBody e={e} />
          {hasDiff ? (
            <button
              type="button"
              className="feed-difftoggle"
              onClick={() => setOpen((o) => !o)}
            >
              {open ? "hide ▾" : "diff ▸"}
            </button>
          ) : null}
        </span>
      </div>
      {hasDiff && open ? (
        <DiffBlock before={e.before ?? ""} after={e.after ?? ""} />
      ) : null}
    </div>
  );
}

export function EventFeed({ events }: { events: FeedEvent[] }) {
  const endRef = useRef<HTMLDivElement>(null);

  // Auto-scroll to the latest event as the stream grows.
  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [events.length]);

  if (events.length === 0) {
    return (
      <div className="feed feed--empty">Waiting for the Claude Code stream…</div>
    );
  }

  return (
    <div className="feed">
      {events.map((e) => (
        <FeedRow key={e._id} e={e} />
      ))}
      <div ref={endRef} />
    </div>
  );
}
