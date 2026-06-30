"use client";

import { useCallback, useState, type FormEvent } from "react";
import { projectStatusUrl } from "../lib/config";
import type { ProjectStatusView } from "../lib/types";
import { StatusBadge, type BadgeStatus } from "./StatusBadge";

// Map the server's JobStatus read model to a UI badge: a project with an empty
// queue and no in-flight build reads as "idle".
function badgeFor(view: ProjectStatusView): BadgeStatus {
  if (view.status === "done" && view.queue_depth === 0) return "idle";
  return view.status;
}

/**
 * ProjectStatusPanel fetches GET /api/v1/projects/{id}/status on demand and
 * renders the read model with a status badge. There is no list-projects
 * endpoint in the contract yet (TODO(crn) backend), so the operator supplies a
 * project id; this panel is the per-project status card.
 */
export function ProjectStatusPanel() {
  const [projectId, setProjectId] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [view, setView] = useState<ProjectStatusView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const fetchStatus = useCallback(
    async (ev: FormEvent) => {
      ev.preventDefault();
      const id = projectId.trim();
      if (!id) return;
      setLoading(true);
      setError(null);
      try {
        const res = await fetch(projectStatusUrl(id), {
          headers: apiKey.trim() ? { "X-API-Key": apiKey.trim() } : undefined,
        });
        if (!res.ok) {
          throw new Error(`status ${res.status} ${res.statusText}`);
        }
        const data = (await res.json()) as ProjectStatusView;
        setView(data);
      } catch (err) {
        setView(null);
        setError(err instanceof Error ? err.message : "request failed");
      } finally {
        setLoading(false);
      }
    },
    [projectId, apiKey],
  );

  return (
    <section className="card">
      <header className="card-head">
        <h2>Project Status</h2>
      </header>

      <form className="form-row" onSubmit={fetchStatus}>
        <input
          className="input"
          placeholder="project id (uuid)"
          value={projectId}
          onChange={(e) => setProjectId(e.target.value)}
          spellCheck={false}
        />
        <input
          className="input"
          placeholder="api key (optional)"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          spellCheck={false}
        />
        <button type="submit" className="btn" disabled={loading}>
          {loading ? "…" : "Fetch"}
        </button>
      </form>

      {error ? <p className="alert">{error}</p> : null}

      {view ? (
        <div className="status-grid">
          <div className="status-cell">
            <span className="status-label">state</span>
            <StatusBadge status={badgeFor(view)} />
          </div>
          <div className="status-cell">
            <span className="status-label">build</span>
            <span className="status-value">v{view.current_build}</span>
          </div>
          <div className="status-cell">
            <span className="status-label">queue depth</span>
            <span className="status-value">{view.queue_depth}</span>
          </div>
          {view.docker_tag ? (
            <div className="status-cell status-cell--wide">
              <span className="status-label">docker tag</span>
              <span className="status-value mono">{view.docker_tag}</span>
            </div>
          ) : null}
          {view.session_id ? (
            <div className="status-cell status-cell--wide">
              <span className="status-label">session</span>
              <span className="status-value mono">{view.session_id}</span>
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
