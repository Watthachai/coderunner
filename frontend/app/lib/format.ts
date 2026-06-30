// Time formatting helpers for the operator console. All take an explicit
// `nowMs` (from useTick) so relative values update on the render cadence.

const pad = (n: number) => String(n).padStart(2, "0");

/** Absolute wall-clock time, 24h, e.g. "15:02:25". */
export function formatClock(iso: string | null): string {
  if (!iso) return "--:--:--";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "--:--:--";
  return d.toLocaleTimeString("en-GB", { hour12: false });
}

/** Elapsed since `startIso`, as mm:ss (or h:mm:ss past an hour). */
export function formatElapsed(startIso: string | null, nowMs: number): string {
  if (!startIso) return "00:00";
  const start = new Date(startIso).getTime();
  if (Number.isNaN(start)) return "00:00";
  let s = Math.max(0, Math.floor((nowMs - start) / 1000));
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
}

/** Compact relative time, e.g. "12s ago", "4m ago", "2h ago", "3d ago". */
export function formatRelative(iso: string | null, nowMs: number): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "—";
  const diff = Math.max(0, Math.floor((nowMs - t) / 1000));
  if (diff < 5) return "just now";
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}
