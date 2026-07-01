package domain

import (
	"context"

	"github.com/google/uuid"
)

// ports.go declares the cross-package interfaces (hexagonal "ports"). The
// concrete adapters live in their own packages:
//
//	Store        -> internal/store   (pgx-backed Postgres)
//	ClaudeRunner -> internal/claude  (spawns `claude --output-format stream-json`)
//	JobManager   -> internal/jobs    (queue + lifecycle + per-org advisory lock)
//	Notifier     -> internal/store   (writes build_events to the central DB)
//
// Implementers MUST satisfy these exactly. Method sets are final — do not add,
// remove, or re-type methods without updating every implementer and main.go.

// Store is the persistence port over the CRN Postgres database. All methods
// take a context for cancellation/deadlines. Implementations must be safe for
// concurrent use by multiple goroutines.
type Store interface {
	// --- Orgs ---
	// EnsureOrg upserts an org by id (INSERT ... ON CONFLICT (id) DO UPDATE SET
	// name = EXCLUDED.name). A zero id is replaced with gen_random_uuid(). The
	// persisted row (id, name, created_at) is read back into org.
	EnsureOrg(ctx context.Context, org *Org) error

	// --- Projects ---
	CreateProject(ctx context.Context, p *Project) error
	GetProject(ctx context.Context, id uuid.UUID) (*Project, error)
	ListProjectsByOrg(ctx context.Context, orgID uuid.UUID) ([]*Project, error)
	// BumpBuildNo atomically increments and returns the project's next build number.
	BumpBuildNo(ctx context.Context, projectID uuid.UUID) (int, error)
	// SetProjectRepo records the project's dedicated GitHub repo https clone URL
	// (the "one repo per project" model). Idempotent: called once per project the
	// first time its repo is ensured.
	SetProjectRepo(ctx context.Context, projectID uuid.UUID, repoURL string) error

	// --- Jobs ---
	CreateJob(ctx context.Context, j *Job) error
	GetJob(ctx context.Context, id uuid.UUID) (*Job, error)
	// JobByBuildNo resolves the job for a project's build number (used by the
	// live-logs WS to turn {project_id, build_no} into a job to subscribe to).
	// Returns ErrNotFound when no such build exists.
	JobByBuildNo(ctx context.Context, projectID uuid.UUID, buildNo int) (*Job, error)
	// UpdateJobStatus moves a job to a new status. errMsg is stored only for
	// JobFailed; pass "" otherwise.
	UpdateJobStatus(ctx context.Context, id uuid.UUID, status JobStatus, errMsg string) error
	// SetJobSession persists the Claude Code session id (for --resume).
	SetJobSession(ctx context.Context, id uuid.UUID, sessionID string) error
	// SetJobDockerTag persists the produced image tag.
	SetJobDockerTag(ctx context.Context, id uuid.UUID, dockerTag string) error
	// NextQueuedJob returns the oldest queued job for an org, or (nil, nil) if none.
	NextQueuedJob(ctx context.Context, orgID uuid.UUID) (*Job, error)
	// LastSessionID returns the session_id of the project's most recent job that
	// has a non-empty session_id ("" when none). Used by an edit build to --resume
	// the project's prior Claude session.
	LastSessionID(ctx context.Context, projectID uuid.UUID) (string, error)
	// QueueDepth counts jobs in JobQueued for a project.
	QueueDepth(ctx context.Context, projectID uuid.UUID) (int, error)

	// --- Edit requests ---
	CreateEditRequest(ctx context.Context, r *EditRequest) error
	GetEditRequest(ctx context.Context, id uuid.UUID) (*EditRequest, error)
	UpdateEditRequestStatus(ctx context.Context, id uuid.UUID, status EditRequestStatus, jobID *uuid.UUID) error

	// --- API keys (per-org auth) ---
	// OrgByAPIKeyHash resolves an org from a hashed API key, ignoring revoked
	// keys. Returns ErrNotFound when no active key matches.
	OrgByAPIKeyHash(ctx context.Context, hash string) (*Org, error)
	CreateAPIKey(ctx context.Context, k *APIKey) error

	// --- Per-org advisory lock (enforces "max 1 build per org") ---
	// AcquireOrgLock takes a PostgreSQL transaction-less session advisory lock
	// keyed by orgID. It returns ErrOrgLocked if another session holds it
	// (non-blocking pg_try_advisory_lock). The returned release func MUST be
	// called to unlock; it is safe to call multiple times.
	AcquireOrgLock(ctx context.Context, orgID uuid.UUID) (release func(), err error)

	// --- Skills (Claude Agent Skills registry) ---
	// ListSkills returns every skill, name-ordered (each skill's Files map is
	// always non-nil). SetSkillEnabled flips the enabled flag. DeleteSkill refuses
	// built-in skills (returns an error the api layer maps to 409).
	// EnsureBuiltinSkill (re)seeds a built-in skill from code: ON CONFLICT (name)
	// it UPDATEs body/description/files and forces is_builtin = true, but PRESERVES
	// the operator's enabled flag — so restart re-applies the canonical harness
	// while enable/disable stays an operator decision.
	ListSkills(ctx context.Context) ([]*Skill, error)
	GetSkill(ctx context.Context, name string) (*Skill, error)
	UpsertSkill(ctx context.Context, s *Skill) error
	SetSkillEnabled(ctx context.Context, name string, enabled bool) error
	DeleteSkill(ctx context.Context, name string) error
	EnsureBuiltinSkill(ctx context.Context, s *Skill) error

	// RecordSkillVersion snapshots the skill's CURRENT persisted row into
	// skill_versions at version = COALESCE(max,0)+1, tagged with note (e.g.
	// "edited" or "uploaded"). Called after every user-initiated change; never
	// from EnsureBuiltinSkill.
	RecordSkillVersion(ctx context.Context, name, note string) error
	// ListSkillVersions returns a skill's version history, newest first.
	ListSkillVersions(ctx context.Context, name string) ([]*SkillVersion, error)

	// --- Dashboard (operator console read model) ---
	// DashboardSnapshot returns a single point-in-time read model for the
	// operator console: vitals (counts), the in-flight builds, the FIFO queue,
	// every project with its latest-job summary, and a recent activity feed.
	// It is computed from a handful of parameter-free queries; slices are always
	// non-nil (so JSON renders [] not null).
	DashboardSnapshot(ctx context.Context) (*DashboardSnapshot, error)

	// Ping verifies connectivity (used by /healthz and startup checks).
	Ping(ctx context.Context) error
	// Close releases the connection pool.
	Close()
}

// ClaudeRunner spawns Claude Code and streams its decoded output. Owned by
// internal/claude.
type ClaudeRunner interface {
	// Run launches `claude --output-format stream-json` for the given job in
	// the project's working directory and streams decoded events to emit until
	// the process exits or ctx is cancelled.
	//
	// emit is called sequentially (never concurrently) for each event in order.
	// If resumeSessionID is non-empty, the runner passes --resume to continue a
	// prior session. The returned RunResult carries the terminal cost/session.
	//
	// Implementations MUST return a non-nil error if the process exits non-zero
	// or fails to start; emit errors should abort the run and propagate.
	Run(ctx context.Context, spec RunSpec, emit func(ClaudeEvent) error) (RunResult, error)
}

// RunSpec is the fully-resolved input for one Claude Code invocation.
type RunSpec struct {
	JobID           uuid.UUID
	ProjectID       uuid.UUID
	WorkDir         string // /projects/{project_id}
	Prompt          string // change description / requirement handed to Claude
	ResumeSessionID string // empty for a fresh session
}

// RunResult is the terminal outcome of a Claude Code run.
type RunResult struct {
	SessionID string
	CostUSD   float64
	Success   bool
}

// JobManager owns the queue, the build lifecycle, and the per-org concurrency
// rule (1 building job per org). Owned by internal/jobs.
type JobManager interface {
	// Enqueue records a new job (status=queued) and, if the org is idle, kicks
	// off processing asynchronously. Returns the created job (with build_no).
	Enqueue(ctx context.Context, projectID, orgID uuid.UUID, payload []byte) (*Job, error)

	// HandleTrigger is invoked when FTC DV signals a queued job exists. It
	// attempts to start the next queued job for the org, respecting the lock.
	HandleTrigger(ctx context.Context, t TriggerRequest) error

	// Cancel requests cancellation of a queued or building job.
	Cancel(ctx context.Context, jobID uuid.UUID) error

	// Subscribe registers a live listener for normalized build events of a job
	// (used by the WebSocket handler). The returned channel is closed when the
	// job finishes or ctx is cancelled; unsubscribe via the returned func.
	Subscribe(ctx context.Context, jobID uuid.UUID) (<-chan BuildEventMsg, func())

	// Status returns the read model for a project's current/last job.
	Status(ctx context.Context, projectID uuid.UUID) (*ProjectStatusView, error)
}

// Notifier writes build_events to the central DB for fan-out to FBD and
// FTC DV (CRN-architecture.md §2.2). Owned by internal/store (it shares the
// pgx pool but targets the central DB DSN).
type Notifier interface {
	// Notify appends a build event. Implementations should also emit a Postgres
	// NOTIFY so subscribers using LISTEN are woken (TODO: channel name convention).
	Notify(ctx context.Context, e *BuildEvent) error
}
