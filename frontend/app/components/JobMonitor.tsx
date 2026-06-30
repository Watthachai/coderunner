"use client";

import { useState, type FormEvent } from "react";
import { useJobStream, type StreamStatus } from "../lib/useJobStream";
import { EventFeed } from "./EventFeed";

const STREAM_LABEL: Record<StreamStatus, string> = {
  idle: "idle",
  connecting: "connecting…",
  open: "live",
  closed: "closed",
  error: "error",
};

const STREAM_COLOR: Record<StreamStatus, string> = {
  idle: "#6b7280",
  connecting: "#a78bfa",
  open: "#64cefb",
  closed: "#9ca3af",
  error: "#f87171",
};

/**
 * JobMonitor is the live build viewer: enter a project id + build number, hit
 * Connect, and watch the streamed Claude Code events. The API key (if the
 * deployment requires one) is forwarded to the WS as a query param.
 */
export function JobMonitor() {
  const [projectId, setProjectId] = useState("");
  const [buildNoText, setBuildNoText] = useState("");
  const [apiKey, setApiKey] = useState("");

  // Committed connection target (only set when the user submits the form).
  const [target, setTarget] = useState<{
    projectId: string;
    buildNo: number;
    apiKey?: string;
  } | null>(null);

  const stream = useJobStream(
    target?.projectId ?? null,
    target?.buildNo ?? null,
    { apiKey: target?.apiKey, enabled: target !== null },
  );

  function connect(ev: FormEvent) {
    ev.preventDefault();
    const buildNo = Number.parseInt(buildNoText, 10);
    if (!projectId.trim() || Number.isNaN(buildNo)) return;
    setTarget({
      projectId: projectId.trim(),
      buildNo,
      apiKey: apiKey.trim() || undefined,
    });
  }

  function disconnect() {
    setTarget(null);
  }

  const last = stream.result;

  return (
    <section className="card">
      <header className="card-head">
        <h2>Live Job Monitor</h2>
        {target ? (
          <span
            className="stream-status"
            style={{ color: STREAM_COLOR[stream.status] }}
          >
            ● {STREAM_LABEL[stream.status]}
          </span>
        ) : null}
      </header>

      <form className="form-row" onSubmit={connect}>
        <input
          className="input"
          placeholder="project id (uuid)"
          value={projectId}
          onChange={(e) => setProjectId(e.target.value)}
          spellCheck={false}
        />
        <input
          className="input input--narrow"
          placeholder="build no"
          inputMode="numeric"
          value={buildNoText}
          onChange={(e) => setBuildNoText(e.target.value)}
        />
        <input
          className="input"
          placeholder="api key (optional)"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          spellCheck={false}
        />
        {target ? (
          <button type="button" className="btn btn--ghost" onClick={disconnect}>
            Disconnect
          </button>
        ) : (
          <button type="submit" className="btn">
            Connect
          </button>
        )}
      </form>

      {stream.error ? <p className="alert">{stream.error}</p> : null}

      {last ? (
        <p className="summary">
          finished
          {typeof last.cost_usd === "number"
            ? ` · cost $${last.cost_usd.toFixed(4)}`
            : ""}
          {last.session_id ? ` · session ${last.session_id}` : ""}
        </p>
      ) : null}

      <EventFeed events={stream.events} />
    </section>
  );
}
