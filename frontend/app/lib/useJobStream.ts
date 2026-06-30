"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { jobLogsWsUrl } from "./config";
import type { BuildEventMsg, FeedEvent } from "./types";

export type StreamStatus = "idle" | "connecting" | "open" | "closed" | "error";

export interface JobStream {
  events: FeedEvent[];
  status: StreamStatus;
  /** Last `result` event seen, if any (carries final cost + session id). */
  result: BuildEventMsg | null;
  /** Last error text reported by the stream (transport or `error` event). */
  error: string | null;
  clear: () => void;
}

interface Options {
  /** API key forwarded as the api_key query param (browsers can't set headers on WS). */
  apiKey?: string;
  /** When false, the hook does not open a connection. Defaults to true. */
  enabled?: boolean;
}

function isBuildEventMsg(v: unknown): v is BuildEventMsg {
  return (
    typeof v === "object" &&
    v !== null &&
    typeof (v as { event?: unknown }).event === "string"
  );
}

/**
 * useJobStream opens a WebSocket to the CRN live-log endpoint for one build and
 * accumulates the streamed BuildEventMsg items. The socket is torn down on
 * unmount or when the (projectId, buildNo, enabled) inputs change.
 *
 * It does not auto-reconnect: a build is a finite stream that the server closes
 * when the job reaches a terminal state. The caller can remount to retry.
 */
export function useJobStream(
  projectId: string | null,
  buildNo: number | null,
  opts: Options = {},
): JobStream {
  const { apiKey, enabled = true } = opts;

  const [events, setEvents] = useState<FeedEvent[]>([]);
  const [status, setStatus] = useState<StreamStatus>("idle");
  const [result, setResult] = useState<BuildEventMsg | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Monotonic id source for stable React keys (events have no server id).
  const seq = useRef(0);

  const clear = useCallback(() => {
    setEvents([]);
    setResult(null);
    setError(null);
    seq.current = 0;
  }, []);

  useEffect(() => {
    if (!enabled || !projectId || buildNo === null) {
      setStatus("idle");
      return;
    }

    // Reset accumulated state for the new (project, build) target.
    setEvents([]);
    setResult(null);
    setError(null);
    seq.current = 0;
    setStatus("connecting");

    let closedByUs = false;
    const url = jobLogsWsUrl(projectId, buildNo, apiKey);
    const ws = new WebSocket(url);

    ws.onopen = () => setStatus("open");

    ws.onmessage = (ev) => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(ev.data as string);
      } catch {
        return; // ignore non-JSON frames
      }
      if (!isBuildEventMsg(parsed)) return;

      const feedEvent: FeedEvent = { ...parsed, _id: seq.current++ };
      setEvents((prev) => [...prev, feedEvent]);

      if (parsed.event === "result") setResult(parsed);
      if (parsed.event === "error" && parsed.text) setError(parsed.text);
    };

    ws.onerror = () => {
      if (closedByUs) return;
      setStatus("error");
      setError((prev) => prev ?? "WebSocket connection error");
    };

    ws.onclose = () => {
      if (closedByUs) return;
      setStatus((prev) => (prev === "error" ? "error" : "closed"));
    };

    return () => {
      closedByUs = true;
      // Only close if still connecting/open; closing a CLOSED socket is a no-op.
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
        ws.close();
      }
    };
  }, [projectId, buildNo, apiKey, enabled]);

  return { events, status, result, error, clear };
}
