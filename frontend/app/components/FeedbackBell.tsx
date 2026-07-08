"use client";

// The dashboard entry point to the Edit Request Panel: a "feedback" button with
// an unread badge that opens the floating modal. Fed by real incoming feedback
// (useFeedback → GET /internal/feedback?status=new). Approve enqueues an edit
// build; reject closes the request. Acted-on rows hide immediately, then the
// poll reconciles (their status leaves "new").

import { useState } from "react";
import { useFeedback } from "../lib/useFeedback";
import { feedbackApproveUrl, feedbackRejectUrl } from "../lib/config";
import { FeedbackModal } from "./FeedbackModal";

export function FeedbackBell() {
  const { feedback, error, loading, refresh } = useFeedback("new", 5000);
  const [open, setOpen] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());

  const visible = feedback.filter((f) => !hidden.has(f.id));
  const count = visible.length;

  async function act(id: string, url: string) {
    setBusyId(id);
    setActionError(null);
    try {
      const res = await fetch(url, { method: "POST" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      setHidden((h) => new Set(h).add(id));
      refresh();
    } catch (e) {
      setActionError(e instanceof Error ? e.message : "action failed");
    } finally {
      setBusyId(null);
    }
  }

  const approve = (id: string) => act(id, feedbackApproveUrl(id));
  const reject = (id: string) => act(id, feedbackRejectUrl(id));

  return (
    <>
      <button
        type="button"
        className="fb-bell"
        onClick={() => setOpen(true)}
        aria-label={`Incoming feedback — ${count} new`}
        title="Incoming feedback"
      >
        <span className="fb-bell-icon" aria-hidden>
          💬
        </span>
        <span className="fb-bell-text">feedback</span>
        {count > 0 ? <span className="fb-bell-badge">{count}</span> : null}
      </button>

      {open ? (
        <FeedbackModal
          requests={visible}
          loading={loading}
          error={actionError ?? error}
          busyId={busyId}
          onApprove={approve}
          onReject={reject}
          onClose={() => setOpen(false)}
        />
      ) : null}
    </>
  );
}
