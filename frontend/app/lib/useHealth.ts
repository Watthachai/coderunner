"use client";

import { useEffect, useRef, useState } from "react";
import { healthUrl } from "./config";
import type { BuildInfo, Health } from "./types";

/**
 * useHealth fetches GET /healthz once on mount and returns the daemon's build
 * stamp, or null while it is loading or unreachable.
 *
 * Deliberately not polled: the commit a server is running cannot change without
 * a restart, which drops every open connection anyway. The dashboard's existing
 * `conn` indicator already covers liveness.
 */
export function useHealth(): BuildInfo | null {
  const [build, setBuild] = useState<BuildInfo | null>(null);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    void (async () => {
      try {
        const res = await fetch(healthUrl(), { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = (await res.json()) as Health;
        if (alive.current) setBuild(json.build ?? null);
      } catch {
        // A build stamp is a convenience, not a function of the console. An
        // unreachable daemon is already reported by the connection indicator.
        if (alive.current) setBuild(null);
      }
    })();
    return () => {
      alive.current = false;
    };
  }, []);

  return build;
}
