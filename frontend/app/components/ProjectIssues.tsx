"use client";

// "ISSUES" panel shown between the edit panel and the terminal on a project
// page. On mount it fetches the open GitHub issues of the project's own repo
// (repo-per-project model — empty when CRN_GITHUB_OWNER is unset):
//
//   GET /internal/projects/{id}/issues -> { issues: [ { number, title, body, url } ] }
//
// Each issue row has a "fix" button that enqueues an EDIT build whose change is
// the issue's title+body; the backend comments on the issue when the build
// finishes:
//
//   POST /internal/projects/{id}/issues/{number}/fix -> 202 { job_id, build_no, status }
//
// On success we mark that row queued (showing the build number, linking to the
// console) and disable its button. Errors render inline via .alert.

import { useEffect, useState } from "react";
import Link from "next/link";
import { projectIssuesUrl, projectIssueFixUrl } from "../lib/config";
import type { Issue } from "../lib/types";

interface IssuesResponse {
  issues: Issue[];
}

interface FixJob {
  job_id: string;
  build_no: number;
  status: string;
}

// A short single-line preview of an issue body for the collapsed row.
function bodyPreview(body: string, max = 160): string {
  const flat = body.replace(/\s+/g, " ").trim();
  if (flat.length <= max) return flat;
  return `${flat.slice(0, max).trimEnd()}…`;
}

// https://github.com/owner/repo.git -> https://github.com/owner/repo
function repoBrowserUrl(repoUrl: string): string {
  return repoUrl.replace(/\.git$/, "");
}

export function ProjectIssues({
  projectId,
  repoUrl,
}: {
  projectId: string;
  repoUrl?: string;
}) {
  const [issues, setIssues] = useState<Issue[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  // Per-issue fix state, keyed by issue number.
  const [fixing, setFixing] = useState<Record<number, boolean>>({});
  const [queued, setQueued] = useState<Record<number, FixJob>>({});
  const [fixError, setFixError] = useState<Record<number, string>>({});

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await fetch(projectIssuesUrl(projectId), {
          cache: "no-store",
        });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = (await res.json()) as IssuesResponse;
        if (!cancelled) setIssues(data.issues);
      } catch (e) {
        if (!cancelled) {
          setLoadError(e instanceof Error ? e.message : "request failed");
          setIssues([]);
        }
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  async function fix(number: number) {
    setFixing((m) => ({ ...m, [number]: true }));
    setFixError((m) => {
      const next = { ...m };
      delete next[number];
      return next;
    });
    try {
      const res = await fetch(projectIssueFixUrl(projectId, number), {
        method: "POST",
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const job = (await res.json()) as FixJob;
      setQueued((m) => ({ ...m, [number]: job }));
    } catch (e) {
      setFixError((m) => ({
        ...m,
        [number]: e instanceof Error ? e.message : "request failed",
      }));
    } finally {
      setFixing((m) => ({ ...m, [number]: false }));
    }
  }

  const browserRepo = repoUrl ? repoBrowserUrl(repoUrl) : null;

  return (
    <div className="issues-panel">
      <div className="issues-bar">
        <span className="issues-bar-title">issues</span>
        {browserRepo ? (
          <a
            className="issues-repo"
            href={browserRepo}
            target="_blank"
            rel="noreferrer"
          >
            repo ↗
          </a>
        ) : null}
      </div>

      <div className="issues-body">
        {loadError !== null ? <p className="alert">{loadError}</p> : null}

        {issues === null ? (
          <p className="issues-empty">Loading issues…</p>
        ) : issues.length === 0 ? (
          <p className="issues-empty">No open issues (or no repo yet).</p>
        ) : (
          <ul className="issue-list">
            {issues.map((issue) => {
              const job = queued[issue.number];
              const isFixing = fixing[issue.number] === true;
              const err = fixError[issue.number];
              const done = job !== undefined;
              return (
                <li className="issue-row" key={issue.number}>
                  <div className="issue-main">
                    <a
                      className="issue-title"
                      href={issue.url}
                      target="_blank"
                      rel="noreferrer"
                    >
                      <span className="issue-num">#{issue.number}</span>
                      {issue.title}
                    </a>
                    {issue.body.trim() !== "" ? (
                      <p className="issue-preview">{bodyPreview(issue.body)}</p>
                    ) : null}
                    {job !== undefined ? (
                      <p className="issue-queued">
                        queued build #{job.build_no} —{" "}
                        <Link className="edit-link" href="/">
                          watch on console
                        </Link>
                      </p>
                    ) : null}
                    {err !== undefined ? (
                      <p className="issue-err">{err}</p>
                    ) : null}
                  </div>
                  <button
                    type="button"
                    className="btn btn--sm"
                    disabled={isFixing || done}
                    onClick={() => void fix(issue.number)}
                  >
                    {done ? "queued" : isFixing ? "fixing…" : "fix"}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
