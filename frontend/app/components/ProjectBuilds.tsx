"use client";

// Per-project build history — every durable trace for one project, newest
// first, each as a replayable span-graph. Mounted on the project terminal page
// so an operator can see the full lineage of what the daemon built, committed,
// and pushed for this project.

import { useEffect, useRef, useState } from "react";
import type { BuildTrace } from "../lib/types";
import { fetchProjectBuilds } from "../lib/useTraces";
import { TraceSpanGraph } from "./TraceSpanGraph";

export function ProjectBuilds({ projectId }: { projectId: string }) {
  const [builds, setBuilds] = useState<BuildTrace[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    fetchProjectBuilds(projectId)
      .then((b) => {
        if (alive.current) setBuilds(b);
      })
      .catch((e) => {
        if (alive.current)
          setError(e instanceof Error ? e.message : "fetch failed");
      })
      .finally(() => {
        if (alive.current) setLoading(false);
      });
    return () => {
      alive.current = false;
    };
  }, [projectId]);

  return (
    <section id="build-history" className="panel">
      <header className="panel-head">
        <h2>Build history</h2>
        <span className="panel-meta">
          {builds.length} {builds.length === 1 ? "build" : "builds"}
        </span>
      </header>

      {error ? (
        <p className="alert">Couldn’t load build history — {error}.</p>
      ) : builds.length === 0 ? (
        <p className="empty">
          {loading
            ? "Loading build history…"
            : "No builds recorded for this project yet."}
        </p>
      ) : (
        <div className="trace-list">
          {builds.map((t) => (
            <TraceSpanGraph key={t.job_id} trace={t} />
          ))}
        </div>
      )}
    </section>
  );
}
