package domain

import (
	"time"

	"github.com/google/uuid"
)

// trace.go holds the durable per-build trace read model — the retroactive
// "state trace" an operator inspects after a build is over. The live WS stream
// (BuildEventMsg fan-out) is ephemeral and gone once a job finishes; a
// BuildTrace is the persisted snapshot of that stream plus a derived summary,
// written once at the terminal state (see jobs.manager.saveTrace and the
// job_traces table in migration 0006).

// BuildTrace is one build's persisted history: derived summary fields for the
// history list plus the full normalized event stream for replay. Events is
// populated only on the single-trace read (GetBuildTrace); list reads leave it
// as an empty slice so the payload stays small.
type BuildTrace struct {
	JobID     uuid.UUID `json:"job_id"`
	ProjectID uuid.UUID `json:"project_id"`
	BuildNo   int       `json:"build_no"`
	// Outcome is the terminal job status: "done" | "failed" | "cancelled".
	Outcome string `json:"outcome"`
	// Mode is "edit" for an edit build, "build" for a normal build.
	Mode      string  `json:"mode"`
	CommitSHA string  `json:"commit_sha"`
	Branch    string  `json:"branch"`
	Remote    string  `json:"remote"`
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"cost_usd"`
	// ToolCount / FileCount are derived from the event stream: how many tool
	// calls Claude made and how many distinct files it touched.
	ToolCount int    `json:"tool_count"`
	FileCount int    `json:"file_count"`
	ErrorMsg  string `json:"error_msg"`
	// Events is the full normalized stream for replay (empty on list reads).
	Events     []BuildEventMsg `json:"events"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	CreatedAt  time.Time       `json:"created_at"`
}
