"use client";

import Link from "next/link";
import type { ProjectRow } from "../lib/types";
import { useTick } from "../lib/useTick";
import { formatRelative } from "../lib/format";
import { StatusPill } from "./StatusPill";

export function ProjectsTable({
  projects,
  remote,
}: {
  projects: ProjectRow[];
  remote?: string;
}) {
  const nowMs = useTick(10000);

  return (
    <section className="panel">
      <header className="panel-head">
        <h2>Projects</h2>
        <span className="panel-meta">{projects.length} total</span>
      </header>

      {projects.length === 0 ? (
        <p className="empty">
          No projects yet. Export one from FITT Builder to get started.
        </p>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Project</th>
                <th>Org</th>
                <th>Status</th>
                <th>Build</th>
                <th>Branch</th>
                <th className="ta-right">Activity</th>
                <th className="ta-right" />
              </tr>
            </thead>
            <tbody>
              {projects.map((p) => (
                <tr key={p.id}>
                  <td className="td-name">
                    <Link className="proj-link" href={`/projects/${p.id}`}>
                      {p.name || p.id.slice(0, 8)}
                    </Link>
                  </td>
                  <td className="td-muted">{p.org_name || "—"}</td>
                  <td>
                    {p.last_status ? (
                      <StatusPill
                        status={p.last_status}
                        pulse={p.last_status === "building"}
                      />
                    ) : (
                      <span className="td-dim">—</span>
                    )}
                  </td>
                  <td className="td-mono">
                    {p.current_build > 0 ? `#${p.current_build}` : "—"}
                  </td>
                  <td>
                    <BranchCell branch={p.last_branch} remote={remote} />
                  </td>
                  <td className="td-muted ta-right">
                    {formatRelative(p.last_activity_at, nowMs)}
                  </td>
                  <td className="ta-right">
                    <Link
                      className="row-action"
                      href={`/projects/${p.id}`}
                      title="Open terminal"
                    >
                      terminal
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function BranchCell({ branch, remote }: { branch: string; remote?: string }) {
  if (!branch) return <span className="td-dim">—</span>;
  const href = branchHref(branch, remote);
  if (!href) return <span className="td-mono">{branch}</span>;
  return (
    <a className="branch" href={href} target="_blank" rel="noreferrer">
      {branch}
    </a>
  );
}

// https://github.com/owner/repo(.git) -> .../tree/<branch>
function branchHref(branch: string, remote?: string): string | null {
  if (!remote) return null;
  const m = remote.match(/github\.com[/:]([^/]+)\/(.+?)(?:\.git)?$/);
  if (!m) return null;
  return `https://github.com/${m[1]}/${m[2]}/tree/${branch}`;
}
