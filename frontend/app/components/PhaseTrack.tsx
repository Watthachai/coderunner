// The signature element: the build pipeline rendered as an illuminated track.
// It mirrors exactly what CRN does to a job — materialize the files, run Claude
// Code, commit, push — and fills left-to-right as the build advances.

const PHASES = ["repo", "materialize", "claude", "git", "push", "docker"] as const;

/**
 * Map a live phase string (from build_phase events) to its track index.
 * The runner emits "repo" (owner mode), "materialize"/"pull",
 * "claude_skipped"/"claude", "git", "push"/"push_skipped", and "docker" (image
 * mode). Prefix matching keeps this resilient; unknown phases return -1.
 */
export function phaseRank(phase: string): number {
  const p = phase.toLowerCase();
  if (p.startsWith("repo")) return 0;
  if (p.startsWith("materialize") || p.startsWith("pull")) return 1;
  if (p.startsWith("claude")) return 2;
  if (p.startsWith("git")) return 3;
  if (p.startsWith("push")) return 4;
  if (p.startsWith("docker")) return 5;
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

// Short flight-plan captions for the pipeline stages — what CRN is actually
// doing at each node, in the operator's vocabulary.
const PHASE_CAPTION: Record<(typeof PHASES)[number], string> = {
  repo: "github repo",
  materialize: "write files",
  claude: "agent build",
  git: "commit",
  push: "publish",
  docker: "build image",
};

/**
 * PhasePipeline — the signature element. The same four stages as PhaseTrack,
 * rendered as an illuminated flight conduit: a rail that fills as stages
 * complete, big reactor nodes, and the active node pulsing under an accent
 * halo. `progress` (0..1) drives the fill; derived here from `current`/`done`
 * so a mid-build reconnect still reads correctly.
 */
export function PhasePipeline({
  current,
  done = false,
}: {
  current: number;
  done?: boolean;
}) {
  const last = PHASES.length - 1;
  // Fill reaches the center of the active node, or the far end when done.
  const reached = done ? last : Math.max(0, Math.min(current, last));
  const progress = done ? 1 : last === 0 ? 0 : reached / last;

  return (
    <div className="pipe" aria-label="build pipeline">
      <div className="pipe-rail">
        <div
          className="pipe-fill"
          style={{ width: `${(progress * 100).toFixed(2)}%` }}
        />
      </div>
      <ol className="pipe-nodes">
        {PHASES.map((p, i) => {
          const state =
            done || i < current
              ? "done"
              : i === current
                ? "active"
                : "pending";
          return (
            <li key={p} className={`node node--${state}`}>
              <span className="node-core">
                <span className="node-index">{i + 1}</span>
              </span>
              <span className="node-name">{p}</span>
              <span className="node-caption">{PHASE_CAPTION[p]}</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
