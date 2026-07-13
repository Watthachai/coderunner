"use client";

// The CRN Edit Request Panel as a floating modal — the incoming in-demo feedback
// (category/priority, the ask, screenshots, and the pinned elements) and the
// operator actions on it. Fed by real requests (useFeedback); approve enqueues
// an edit build, reject closes the request.

import { useEffect, useState } from "react";
import type {
  FeedbackRequest,
  FeedbackCategory,
  FeedbackPriority,
} from "../lib/types";
import { useTick } from "../lib/useTick";
import { formatRelative } from "../lib/format";

const CAT_GLYPH: Record<FeedbackCategory, string> = {
  bug: "🐞",
  feature: "✨",
  style: "🎨",
};

const PRIO_LABEL: Record<FeedbackPriority, string> = {
  low: "low",
  med: "medium",
  high: "high",
};

function FeedbackCard({
  req,
  nowMs,
  busy,
  onApprove,
  onReject,
  onZoom,
}: {
  req: FeedbackRequest;
  nowMs: number;
  busy: boolean;
  onApprove: (id: string) => void;
  onReject: (id: string) => void;
  onZoom: (src: string) => void;
}) {
  const cat = (CAT_GLYPH[req.category as FeedbackCategory] ?? "•") + " ";
  return (
    <article className="fb-card">
      <header className="fb-card-head">
        <span className={`pill fb-cat fb-cat--${req.category}`}>
          {cat}
          {req.category}
        </span>
        <span className={`pill fb-prio fb-prio--${req.priority}`}>
          {PRIO_LABEL[req.priority as FeedbackPriority] ?? req.priority}
        </span>
        <span className="fb-meta">
          {formatRelative(req.created_at, nowMs)} ·{" "}
          {req.reporter || "anonymous"}
        </span>
        {req.issue_url ? (
          <a
            className="pill fb-issue"
            href={req.issue_url}
            target="_blank"
            rel="noreferrer"
          >
            #{req.issue_number} ↗
          </a>
        ) : null}
      </header>

      <p className="fb-note">{req.note}</p>

      {req.payload.full_shot ? (
        <div className="fb-shots">
          <figure className="fb-shot">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img
              className="fb-zoomable"
              src={req.payload.full_shot}
              alt="full page"
              onClick={() => onZoom(req.payload.full_shot)}
            />
            <figcaption>full page · click to enlarge</figcaption>
          </figure>
        </div>
      ) : null}

      {req.payload.pins.length > 0 ? (
        <ol className="fb-pins">
          {req.payload.pins.map((p, i) => (
            <li className="fb-pin" key={i}>
              {p.region_shot ? (
                // eslint-disable-next-line @next/next/no-img-element
                <img
                  className="fb-pin-shot fb-zoomable"
                  src={p.region_shot}
                  alt={p.label}
                  onClick={() => onZoom(p.region_shot)}
                />
              ) : null}
              <div className="fb-pin-body">
                <span className="fb-pin-label">📍 {p.label || "element"}</span>
                {p.selector ? (
                  <code className="fb-pin-sel">{p.selector}</code>
                ) : null}
                <span className="fb-pin-note">{p.note}</span>
              </div>
            </li>
          ))}
        </ol>
      ) : null}

      <footer className="fb-card-foot">
        <div className="fb-actions">
          <button
            type="button"
            className="btn btn--sm"
            disabled={busy}
            onClick={() => onApprove(req.id)}
          >
            {busy ? "…" : "approve → build"}
          </button>
          <button
            type="button"
            className="btn btn--danger btn--sm"
            disabled={busy}
            onClick={() => onReject(req.id)}
          >
            reject
          </button>
        </div>
        <span className="fb-page" title={req.page_url}>
          {req.page_url.replace(/^https?:\/\//, "")}
        </span>
      </footer>
    </article>
  );
}

export function FeedbackModal({
  requests,
  loading = false,
  error = null,
  busyId = null,
  onApprove,
  onReject,
  onClose,
}: {
  requests: FeedbackRequest[];
  loading?: boolean;
  error?: string | null;
  busyId?: string | null;
  onApprove: (id: string) => void;
  onReject: (id: string) => void;
  onClose: () => void;
}) {
  const nowMs = useTick(30000);
  const [zoom, setZoom] = useState<string | null>(null);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key !== "Escape") return;
      if (zoom) setZoom(null); // close the lightbox first, then the modal
      else onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, zoom]);

  return (
    <>
    <div className="fb-overlay" onClick={onClose}>
      <div
        className="fb-modal"
        role="dialog"
        aria-modal="true"
        aria-label="Incoming feedback"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="fb-modal-head">
          <div className="fb-modal-title">
            <span className="bc-eyebrow">incoming feedback</span>
            <h2>Edit requests</h2>
          </div>
          <span className="fb-modal-count">
            {requests.length} {requests.length === 1 ? "request" : "requests"}
          </span>
          <button
            type="button"
            className="fb-close"
            onClick={onClose}
            aria-label="Close"
          >
            ✕
          </button>
        </header>

        <div className="fb-modal-body">
          {error ? (
            <p className="alert">Can’t reach the daemon — {error}.</p>
          ) : requests.length === 0 ? (
            <p className="empty">
              {loading
                ? "Loading feedback…"
                : "All caught up — no new feedback. 🎉"}
            </p>
          ) : (
            requests.map((r) => (
              <FeedbackCard
                key={r.id}
                req={r}
                nowMs={nowMs}
                busy={busyId === r.id}
                onApprove={onApprove}
                onReject={onReject}
                onZoom={setZoom}
              />
            ))
          )}
        </div>
      </div>
    </div>
    {zoom ? (
      <div
        className="fb-lightbox"
        onClick={() => setZoom(null)}
        role="dialog"
        aria-label="Screenshot"
      >
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src={zoom} alt="feedback screenshot" />
        <button type="button" className="fb-lightbox-close" aria-label="Close">
          ✕
        </button>
      </div>
    ) : null}
    </>
  );
}
