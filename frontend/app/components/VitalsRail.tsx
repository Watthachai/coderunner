import type { DashboardVitals } from "../lib/types";

const ITEMS: { key: keyof DashboardVitals; label: string; tone?: string }[] = [
  { key: "projects", label: "projects" },
  { key: "queued", label: "queued", tone: "var(--queued)" },
  { key: "building", label: "building", tone: "var(--accent)" },
  { key: "done_today", label: "done today", tone: "var(--done)" },
  { key: "failed_today", label: "failed today", tone: "var(--error)" },
];

export function VitalsRail({ vitals }: { vitals: DashboardVitals | null }) {
  return (
    <div className="vitals">
      {ITEMS.map((it) => {
        const v = vitals ? vitals[it.key] : 0;
        const live = it.key === "building" && v > 0;
        return (
          <div key={it.key} className={`vital${live ? " vital--live" : ""}`}>
            <span
              className="vital-num"
              style={it.tone ? { color: it.tone } : undefined}
            >
              {v}
            </span>
            <span className="vital-label">
              {live ? <span className="vital-dot" /> : null}
              {it.label}
            </span>
          </div>
        );
      })}
    </div>
  );
}
