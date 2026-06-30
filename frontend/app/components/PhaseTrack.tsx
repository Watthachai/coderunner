// The signature element: the build pipeline rendered as an illuminated track.
// It mirrors exactly what CRN does to a job — materialize the files, run Claude
// Code, commit, push — and fills left-to-right as the build advances.

const PHASES = ["materialize", "claude", "git", "push"] as const;

/**
 * Map a live phase string (from build_phase events) to its track index.
 * The runner emits "materialize", "claude_skipped"/"claude", "git",
 * "push"/"push_skipped"; prefix matching keeps this resilient.
 */
export function phaseRank(phase: string): number {
  const p = phase.toLowerCase();
  if (p.startsWith("materialize")) return 0;
  if (p.startsWith("claude")) return 1;
  if (p.startsWith("git")) return 2;
  if (p.startsWith("push")) return 3;
  return -1;
}

export function PhaseTrack({
  current,
  done = false,
}: {
  current: number;
  done?: boolean;
}) {
  return (
    <ol className="track" aria-label="build phases">
      {PHASES.map((p, i) => {
        const state =
          done || i < current ? "done" : i === current ? "active" : "pending";
        return (
          <li key={p} className={`seg seg--${state}`}>
            <span className="seg-dot" />
            <span className="seg-label">{p}</span>
          </li>
        );
      })}
    </ol>
  );
}
