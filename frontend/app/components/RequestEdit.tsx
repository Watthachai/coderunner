"use client";

// "REQUEST AN EDIT" panel shown above a project's terminal. The operator types
// a plain-language change and submits it; the backend creates an EDIT build for
// the existing project (pulls its branch + --resumes the last session — no
// reset/zip) and returns the queued job.
//
//   POST /internal/projects/{id}/edit  { "change": "<what to change>" }
//   -> 202 { "job_id", "build_no", "git_branch", "status" }
//
// On success we surface the queued build number + branch and point back at the
// console, then clear the textarea. Errors render inline via .alert.

import { useState } from "react";
import Link from "next/link";
import { projectEditUrl } from "../lib/config";

interface EditJob {
  job_id: string;
  build_no: number;
  git_branch: string;
  status: string;
}

export function RequestEdit({ projectId }: { projectId: string }) {
  const [change, setChange] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [queued, setQueued] = useState<EditJob | null>(null);

  async function submit() {
    const trimmed = change.trim();
    if (trimmed === "") {
      setError("Describe the change first.");
      return;
    }
    setSubmitting(true);
    setError(null);
    setQueued(null);
    try {
      const res = await fetch(projectEditUrl(projectId), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ change: trimmed }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const job = (await res.json()) as EditJob;
      setQueued(job);
      setChange("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "request failed");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="edit-panel">
      <div className="edit-bar">
        <span className="edit-bar-title">request an edit</span>
      </div>
      <div className="edit-body">
        <textarea
          className="input mono edit-input scroll-thin"
          value={change}
          placeholder="Describe the change…"
          spellCheck={false}
          disabled={submitting}
          onChange={(e) => setChange(e.target.value)}
        />

        {error !== null ? <p className="alert">{error}</p> : null}
        {queued !== null ? (
          <p className="notice">
            queued build #{queued.build_no} on {queued.git_branch} —{" "}
            <Link className="edit-link" href="/">
              watch it on the console
            </Link>
          </p>
        ) : null}

        <div className="edit-actions">
          <button
            type="button"
            className="btn"
            disabled={submitting}
            onClick={() => void submit()}
          >
            {submitting ? "requesting…" : "Request edit"}
          </button>
        </div>
      </div>
    </div>
  );
}
