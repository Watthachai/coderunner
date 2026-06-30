// A status-colored pill. Status color IS information here (it encodes the job
// lifecycle), so the palette is fixed and shared across the console.

const COLOR: Record<string, string> = {
  queued: "var(--queued)",
  building: "var(--accent)",
  done: "var(--done)",
  failed: "var(--error)",
  cancelled: "var(--fg-dim)",
  active: "var(--done)",
  pending: "var(--queued)",
  archived: "var(--fg-dim)",
};

export function StatusPill({
  status,
  pulse = false,
}: {
  status: string;
  pulse?: boolean;
}) {
  const color = COLOR[status] ?? "var(--fg-dim)";
  return (
    <span className="pill" style={{ color, borderColor: color }}>
      <span
        className={`pill-dot${pulse ? " pill-dot--pulse" : ""}`}
        style={{ background: color }}
      />
      {status || "—"}
    </span>
  );
}
