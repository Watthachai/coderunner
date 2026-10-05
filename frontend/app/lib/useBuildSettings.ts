"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { buildSettingsUrl } from "./config";
import type { BuildSettings } from "./types";

/**
 * useBuildSettings loads the Claude model/effort builds run with and saves a
 * new choice. Re-loads on mount, every 30s while the tab is visible, and
 * whenever the window regains focus or the tab becomes visible again — so a
 * dashboard left open doesn't go stale. Saving it goes through here too.
 */
export function useBuildSettings(): {
  data: BuildSettings | null;
  error: string | null;
  saving: boolean;
  save: (model: string, effort: string) => Promise<boolean>;
} {
  const [data, setData] = useState<BuildSettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const alive = useRef(true);
  // Tracks whether we've ever loaded data, without the staleness a `data`
  // dependency would put on `load`'s identity.
  const hasData = useRef(false);

  const load = useCallback(async () => {
    try {
      const res = await fetch(buildSettingsUrl(), { cache: "no-store" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const json = (await res.json()) as BuildSettings;
      if (!alive.current) return;
      hasData.current = true;
      setData(json);
      setError(null);
    } catch (e) {
      if (!alive.current) return;
      // A background re-load failing must not wipe an already-loaded value —
      // only surface the error when there is nothing loaded yet.
      if (!hasData.current) {
        setError(e instanceof Error ? e.message : "load failed");
      }
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    void load();

    const id = setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 30_000);
    const onFocus = () => void load();
    const onVisibilityChange = () => {
      if (document.visibilityState === "visible") void load();
    };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisibilityChange);

    return () => {
      alive.current = false;
      clearInterval(id);
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [load]);

  const save = useCallback(async (model: string, effort: string) => {
    setSaving(true);
    setError(null);
    try {
      const res = await fetch(buildSettingsUrl(), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ model, effort }),
      });
      const json = (await res.json().catch(() => null)) as
        | BuildSettings
        | { error?: string }
        | null;
      if (!res.ok) {
        const reason =
          json && "error" in json && json.error ? json.error : `HTTP ${res.status}`;
        throw new Error(reason);
      }
      if (alive.current) {
        hasData.current = true;
        setData(json as BuildSettings);
      }
      return true;
    } catch (e) {
      if (alive.current) setError(e instanceof Error ? e.message : "save failed");
      return false;
    } finally {
      if (alive.current) setSaving(false);
    }
  }, []);

  return { data, error, saving, save };
}
