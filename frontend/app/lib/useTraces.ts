"use client";

import { useEffect, useRef, useState } from "react";
import { tracesUrl, traceUrl, projectBuildsUrl } from "./config";
import type {
  BuildTrace,
  RecentTracesResponse,
  ProjectBuildsResponse,
} from "./types";

export interface TracesState {
  traces: BuildTrace[];
  error: string | null;
  loading: boolean;
}

/**
 * useTraces polls GET /internal/traces on an interval and exposes the most
 * recent durable build traces (summary only — no `events`). It mirrors
 * useDashboard: it keeps the previous list on a transient error so one dropped
 * poll doesn't blank the history view.
 */
export function useTraces(limit = 30, intervalMs = 4000): TracesState {
  const [traces, setTraces] = useState<BuildTrace[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    let timer: ReturnType<typeof setTimeout> | undefined;

    async function tick() {
      try {
        const res = await fetch(tracesUrl(limit), { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = (await res.json()) as RecentTracesResponse;
        if (!alive.current) return;
        setTraces(json.traces ?? []);
        setError(null);
      } catch (e) {
        if (!alive.current) return;
        setError(e instanceof Error ? e.message : "fetch failed");
      } finally {
        if (alive.current) {
          setLoading(false);
          timer = setTimeout(tick, intervalMs);
        }
      }
    }

    void tick();
    return () => {
      alive.current = false;
      if (timer) clearTimeout(timer);
    };
  }, [limit, intervalMs]);

  return { traces, error, loading };
}

/** One-shot fetch of a single trace WITH its full replayable event stream. */
export async function fetchTrace(jobId: string): Promise<BuildTrace> {
  const res = await fetch(traceUrl(jobId), { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return (await res.json()) as BuildTrace;
}

/** One-shot fetch of a project's build history (summaries, no events). */
export async function fetchProjectBuilds(
  projectId: string,
): Promise<BuildTrace[]> {
  const res = await fetch(projectBuildsUrl(projectId), { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const json = (await res.json()) as ProjectBuildsResponse;
  return json.builds ?? [];
}
