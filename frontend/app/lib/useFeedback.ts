"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { feedbackUrl } from "./config";
import type { FeedbackRequest } from "./types";

interface FeedbackResponse {
  feedback: FeedbackRequest[];
}

export interface FeedbackState {
  feedback: FeedbackRequest[];
  error: string | null;
  loading: boolean;
  refresh: () => void;
}

/**
 * useFeedback polls GET /internal/feedback (optionally filtered by status) for
 * incoming in-demo feedback. Mirrors useDashboard/useTraces: keeps the previous
 * list on a transient error. `refresh()` forces an immediate re-fetch (used
 * right after approve/reject so the list reconciles without waiting a poll).
 */
export function useFeedback(status = "new", intervalMs = 5000): FeedbackState {
  const [feedback, setFeedback] = useState<FeedbackRequest[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);
  const alive = useRef(true);

  const refresh = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    alive.current = true;
    let timer: ReturnType<typeof setTimeout> | undefined;

    async function tick() {
      try {
        const res = await fetch(feedbackUrl(status), { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = (await res.json()) as FeedbackResponse;
        if (!alive.current) return;
        setFeedback(json.feedback ?? []);
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
  }, [status, intervalMs, nonce]);

  return { feedback, error, loading, refresh };
}
