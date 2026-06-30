"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { skillsUrl } from "./config";
import type { Skill } from "./types";

export interface SkillsState {
  skills: Skill[];
  error: string | null;
  loading: boolean;
  refresh: () => Promise<void>;
}

interface SkillsResponse {
  skills: Skill[];
}

/**
 * useSkills fetches GET /internal/skills once on mount and exposes a refresh()
 * to re-fetch after a mutation (PUT/DELETE/toggle). Unlike the dashboard there
 * is no polling — the list only changes in response to operator actions here.
 */
export function useSkills(): SkillsState {
  const [skills, setSkills] = useState<Skill[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const alive = useRef(true);

  const refresh = useCallback(async () => {
    try {
      const res = await fetch(skillsUrl(), { cache: "no-store" });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const json = (await res.json()) as SkillsResponse;
      if (!alive.current) return;
      setSkills(json.skills);
      setError(null);
    } catch (e) {
      if (!alive.current) return;
      setError(e instanceof Error ? e.message : "fetch failed");
    } finally {
      if (alive.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    void refresh();
    return () => {
      alive.current = false;
    };
  }, [refresh]);

  return { skills, error, loading, refresh };
}
