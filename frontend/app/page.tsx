"use client";

// CRN operator console (CRN-architecture.md §2.4) — "mission control" for the
// build daemon. Polls GET /internal/dashboard for the snapshot (vitals, queue,
// projects, activity) and opens a live WS per in-flight build so the phase
// track and streamed Claude events update in real time.

import Link from "next/link";
import { API_BASE, GIT_REMOTE } from "./lib/config";
import { useDashboard } from "./lib/useDashboard";
import { useTraces } from "./lib/useTraces";
import { VitalsRail } from "./components/VitalsRail";
import { NowBuilding } from "./components/NowBuilding";
import { QueuePanel } from "./components/QueuePanel";
import { ProjectsTable } from "./components/ProjectsTable";
import { ActivityFeed } from "./components/ActivityFeed";
import { ThemeToggle } from "./components/ThemeToggle";
import { FeedbackBell } from "./components/FeedbackBell";

export default function Home() {
  const { data, error, loading } = useDashboard(2500);
  // Most recent finished build, to keep the idle console "armed" after done.
  const { traces } = useTraces(1);
  const lastTrace = traces[0] ?? null;

  const conn = error ? "down" : loading ? "wait" : "live";
  const connLabel = error ? "disconnected" : loading ? "connecting" : "live";

  return (
    <main className="console">
      <header className="page-head">
        <div className="brand">
          <span className="brand-mark" />
          <div>
            <h1>FITT Code Runner</h1>
            <p className="brand-sub">mission control · build orchestrator</p>
          </div>
        </div>
        <div className="head-right">
          <FeedbackBell />
          <ThemeToggle />
          <Link className="navlink" href="/skills">
            skills
          </Link>
          <span className={`conn conn--${conn}`}>
            <span className="conn-dot" />
            {connLabel}
          </span>
          <code className="api-base">{API_BASE}</code>
        </div>
      </header>

      {error ? (
        <p className="alert">
          Can’t reach the daemon at {API_BASE} — {error}. Is{" "}
          <code>make run</code> up?
        </p>
      ) : null}

      <VitalsRail vitals={data?.vitals ?? null} />

      <div className="row row--two">
        <NowBuilding
          building={data?.building ?? []}
          queued={data?.vitals?.queued ?? 0}
          lastTrace={lastTrace}
        />
        <QueuePanel queue={data?.queue ?? []} />
      </div>

      <div className="row row--two">
        <ProjectsTable projects={data?.projects ?? []} remote={GIT_REMOTE} />
        <ActivityFeed activity={data?.activity ?? []} />
      </div>
    </main>
  );
}
