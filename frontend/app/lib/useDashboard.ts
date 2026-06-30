"use client";

import { useEffect, useRef, useState } from "react";
import { dashboardUrl } from "./config";
import type { DashboardSnapshot } from "./types";

export interface DashboardState {
  data: DashboardSnapshot | null;
  error: string | null;
  loading: boolean;
}

/**
 * useDashboard polls GET /internal/dashboard on an interval and exposes the
 * latest snapshot. It keeps the previous data on a transient error (so a single
 * dropped poll doesn't blank the console) and reports the error alongside.
 */
export function useDashboard(intervalMs = 2500): DashboardState {
  const [data, setData] = useState<DashboardSnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    let timer: ReturnType<typeof setTimeout> | undefined;

    async function tick() {
      try {
        const res = await fetch(dashboardUrl(), { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = (await res.json()) as DashboardSnapshot;
        if (!alive.current) return;
        setData(json);
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
  }, [intervalMs]);

  return { data, error, loading };
}
