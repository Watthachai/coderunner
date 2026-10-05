"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { buildSettingsUrl } from "./config";
import type { BuildSettings } from "./types";

/**
 * useBuildSettings loads the Claude model/effort builds run with and saves a
 * new choice. Loaded once, not polled: the value only changes when someone
 * saves it, and saving it goes through here.
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

  useEffect(() => {
    alive.current = true;
    void (async () => {
      try {
        const res = await fetch(buildSettingsUrl(), { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = (await res.json()) as BuildSettings;
        if (alive.current) setData(json);
      } catch (e) {
        if (alive.current) setError(e instanceof Error ? e.message : "load failed");
      }
    })();
    return () => {
      alive.current = false;
    };
  }, []);

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
      if (alive.current) setData(json as BuildSettings);
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
