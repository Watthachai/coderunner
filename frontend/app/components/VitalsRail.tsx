import type { DashboardVitals } from "../lib/types";

const ITEMS: { key: keyof DashboardVitals; label: string; tone?: string }[] = [
  { key: "projects", label: "projects" },
  { key: "queued", label: "queued", tone: "var(--queued)" },
  { key: "building", label: "building", tone: "var(--building)" },
  { key: "done_today", label: "done today", tone: "var(--done)" },
  { key: "failed_today", label: "failed today", tone: "var(--error)" },
];

// The load-bar meter: proportions of the live pipeline (queued/building) + the
// day's outcomes (done/failed), btop-style — a glanceable state of the fleet.
const SEGMENTS: { key: keyof DashboardVitals; color: string }[] = [
  { key: "queued", color: "var(--queued)" },
  { key: "building", color: "var(--building)" },
  { key: "done_today", color: "var(--done)" },
  { key: "failed_today", color: "var(--error)" },
];

export function VitalsRail({ vitals }: { vitals: DashboardVitals | null }) {
  const total = SEGMENTS.reduce((sum, s) => sum + (vitals ? vitals[s.key] : 0), 0);

  return (
    <div className="vitals-rail">
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

      <div className="vitals-meter" role="img" aria-label="pipeline load">
        {total > 0 ? (
          SEGMENTS.map((s) => {
            const v = vitals ? vitals[s.key] : 0;
            return v > 0 ? (
              <span
                key={s.key}
                className="vm-seg"
                style={{ flexGrow: v, background: s.color }}
              />
            ) : null;
          })
        ) : (
          <span className="vm-empty" />
        )}
      </div>
    </div>
  );
}
