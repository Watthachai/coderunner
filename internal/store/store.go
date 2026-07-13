// Package store is the persistence adapter. It implements domain.Store and
// domain.Notifier on top of a github.com/jackc/pgx/v5 connection pool.
//
// OWNED BY: the 'store' implementer.
//
// What you must build here (replace the panics — the signatures below are the
// contract main.go already wires against and MUST NOT change):
//
//   - New(ctx, dsn) (domain.Store, error): open a *pgxpool.Pool, Ping it, and
//     return a struct implementing every domain.Store method. Map pgx.ErrNoRows
//     to domain.ErrNotFound.
//   - NewNotifier(ctx, dsn) (domain.Notifier, error): same pool style but
//     pointed at the central DB; Notify inserts into build_events and issues a
//     Postgres NOTIFY.
//   - AcquireOrgLock: use pg_try_advisory_lock(hashOrg(orgID)) on a dedicated
//     connection checked out from the pool; return domain.ErrOrgLocked when the
//     try fails, and a release func that runs pg_advisory_unlock + returns the
//     conn. This is how "max 1 build per org" is enforced (CRN-architecture §3).
//
// Keep concrete types unexported; callers only ever see the domain interfaces.
package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// notifyChannel is the Postgres LISTEN/NOTIFY channel that subscribers (FBD and
// FTC DV) listen on for live build-event fan-out.
//
// TODO(crn): finalize the channel-name convention with the FBD/FTC DV teams
// (per-org or per-job channels may be desired). For now a single global channel
// carrying the new event id is the minimal viable contract.
const notifyChannel = "build_events"

// unlockTimeout bounds the best-effort advisory-unlock issued by an org lock's
// release func so a stuck connection cannot block the caller indefinitely.
const unlockTimeout = 5 * time.Second

// pgStore is the pgx-backed implementation of domain.Store. It is safe for
// concurrent use: *pgxpool.Pool is goroutine-safe, and AcquireOrgLock checks
// out its own dedicated connection per call.
type pgStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// New opens the CRN Postgres pool and returns a domain.Store.
//
// Signature is fixed by cmd/server/main.go.
func New(ctx context.Context, dsn string) (domain.Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &pgStore{
		pool:   pool,
		logger: slog.Default().With("component", "store"),
	}, nil
}

// Ping verifies connectivity (used by /healthz and startup checks).
func (s *pgStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close releases the connection pool.
func (s *pgStore) Close() {
	s.pool.Close()
}

// --- Orgs --------------------------------------------------------------------

// EnsureOrg upserts an org by id. A zero id falls back to gen_random_uuid();
// on a primary-key conflict the name is refreshed from the incoming value. The
// persisted row is read back into org.
func (s *pgStore) EnsureOrg(ctx context.Context, org *domain.Org) error {
	const q = `
		INSERT INTO orgs (id, name)
		VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2
		)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name
		RETURNING id, name, created_at`
	if err := s.pool.QueryRow(ctx, q, org.ID, org.Name).
		Scan(&org.ID, &org.Name, &org.CreatedAt); err != nil {
		return mapErr(err, "store: ensure org")
	}
	return nil
}

// --- Projects ---------------------------------------------------------------

// CreateProject inserts a project. Zero-valued id/status fall back to DB
// defaults (gen_random_uuid / 'pending'); the row is read back so the caller's
// struct reflects the persisted state.
func (s *pgStore) CreateProject(ctx context.Context, p *domain.Project) error {
	const q = `
		INSERT INTO projects (id, org_id, name, status, current_build, stack)
		VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2, $3,
			COALESCE(NULLIF($4, ''), 'pending'),
			$5, $6
		)
		RETURNING id, org_id, name, status, current_build, COALESCE(stack, ''), repo_url, created_at`
	row := s.pool.QueryRow(ctx, q,
		p.ID, p.OrgID, p.Name, string(p.Status), p.CurrentBuild, p.Stack,
	)
	return scanProject(row, p)
}

// GetProject fetches a project by id, returning domain.ErrNotFound if absent.
func (s *pgStore) GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	const q = `
		SELECT id, org_id, name, status, current_build, COALESCE(stack, ''), repo_url, created_at
		FROM projects WHERE id = $1`
	p := &domain.Project{}
	if err := scanProject(s.pool.QueryRow(ctx, q, id), p); err != nil {
		return nil, err
	}
	return p, nil
}

// ListProjectsByOrg returns all projects for an org, newest first.
func (s *pgStore) ListProjectsByOrg(ctx context.Context, orgID uuid.UUID) ([]*domain.Project, error) {
	const q = `
		SELECT id, org_id, name, status, current_build, COALESCE(stack, ''), repo_url, created_at
		FROM projects WHERE org_id = $1 ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	var out []*domain.Project
	for rows.Next() {
		p := &domain.Project{}
		if err := scanProject(rows, p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	return out, nil
}

// BumpBuildNo atomically increments and returns the project's next build number.
func (s *pgStore) BumpBuildNo(ctx context.Context, projectID uuid.UUID) (int, error) {
	const q = `
		UPDATE projects SET current_build = current_build + 1
		WHERE id = $1
		RETURNING current_build`
	var n int
	if err := s.pool.QueryRow(ctx, q, projectID).Scan(&n); err != nil {
		return 0, mapErr(err, "store: bump build_no")
	}
	return n, nil
}

// SetProjectRepo records the project's dedicated GitHub repo https clone URL
// (the "one repo per project" model). Returns domain.ErrNotFound when no such
// project exists.
func (s *pgStore) SetProjectRepo(ctx context.Context, projectID uuid.UUID, repoURL string) error {
	const q = `UPDATE projects SET repo_url = $2 WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, projectID, repoURL)
	if err != nil {
		return mapErr(err, "store: set project repo")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: set project repo: %w", domain.ErrNotFound)
	}
	return nil
}

// --- Jobs -------------------------------------------------------------------

// CreateJob inserts a job and reads back DB-generated columns.
func (s *pgStore) CreateJob(ctx context.Context, j *domain.Job) error {
	const q = `
		INSERT INTO project_jobs (id, project_id, org_id, status, build_no, payload)
		VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2, $3,
			COALESCE(NULLIF($4, ''), 'queued'),
			$5, $6
		)
		RETURNING id, project_id, org_id, status, build_no,
		          payload,
		          COALESCE(session_id, ''), COALESCE(docker_tag, ''), COALESCE(error_msg, ''),
		          queued_at, started_at, finished_at`
	row := s.pool.QueryRow(ctx, q,
		j.ID, j.ProjectID, j.OrgID, string(j.Status), j.BuildNo, []byte(j.Payload),
	)
	return scanJob(row, j)
}

// GetJob fetches a job by id, returning domain.ErrNotFound if absent.
func (s *pgStore) GetJob(ctx context.Context, id uuid.UUID) (*domain.Job, error) {
	const q = `
		SELECT id, project_id, org_id, status, build_no,
		       payload,
		       COALESCE(session_id, ''), COALESCE(docker_tag, ''), COALESCE(error_msg, ''),
		       queued_at, started_at, finished_at
		FROM project_jobs WHERE id = $1`
	j := &domain.Job{}
	if err := scanJob(s.pool.QueryRow(ctx, q, id), j); err != nil {
		return nil, err
	}
	return j, nil
}

// JobByBuildNo resolves the job for a project's build number, returning
// domain.ErrNotFound if absent. build_no is unique per project (BumpBuildNo),
// so at most one row matches.
func (s *pgStore) JobByBuildNo(ctx context.Context, projectID uuid.UUID, buildNo int) (*domain.Job, error) {
	const q = `
		SELECT id, project_id, org_id, status, build_no,
		       payload,
		       COALESCE(session_id, ''), COALESCE(docker_tag, ''), COALESCE(error_msg, ''),
		       queued_at, started_at, finished_at
		FROM project_jobs WHERE project_id = $1 AND build_no = $2
		ORDER BY queued_at DESC LIMIT 1`
	j := &domain.Job{}
	if err := scanJob(s.pool.QueryRow(ctx, q, projectID, buildNo), j); err != nil {
		return nil, err
	}
	return j, nil
}

// UpdateJobStatus moves a job to a new status. errMsg is stored only for
// JobFailed; for terminal statuses finished_at is stamped, and started_at is
// stamped on the first transition into building. The partial unique index
// uq_jobs_one_building_per_org backstops the advisory lock here: a concurrent
// transition into 'building' for the same org violates it and surfaces as a
// constraint error.
func (s *pgStore) UpdateJobStatus(ctx context.Context, id uuid.UUID, status domain.JobStatus, errMsg string) error {
	if !status.Valid() {
		return fmt.Errorf("store: invalid job status %q", status)
	}

	// error_msg is only meaningful for failed jobs; clear it otherwise.
	var errArg any
	if status == domain.JobFailed && errMsg != "" {
		errArg = errMsg
	}

	const q = `
		UPDATE project_jobs SET
			status = $2,
			error_msg = $3,
			started_at = CASE WHEN $2 = 'building' AND started_at IS NULL
			                  THEN now() ELSE started_at END,
			finished_at = CASE WHEN $2 IN ('done','failed','cancelled')
			                   THEN now() ELSE finished_at END
		WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, id, string(status), errArg)
	if err != nil {
		return mapErr(err, "store: update job status")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: update job status: %w", domain.ErrNotFound)
	}
	return nil
}

// SetJobSession persists the Claude Code session id (for --resume).
func (s *pgStore) SetJobSession(ctx context.Context, id uuid.UUID, sessionID string) error {
	const q = `UPDATE project_jobs SET session_id = $2 WHERE id = $1`
	return s.execOneJob(ctx, q, id, sessionID, "store: set job session")
}

// SetJobDockerTag persists the produced image tag.
func (s *pgStore) SetJobDockerTag(ctx context.Context, id uuid.UUID, dockerTag string) error {
	const q = `UPDATE project_jobs SET docker_tag = $2 WHERE id = $1`
	return s.execOneJob(ctx, q, id, dockerTag, "store: set job docker tag")
}

// execOneJob runs a single-parameter UPDATE on one job and maps a zero-row
// result to domain.ErrNotFound.
func (s *pgStore) execOneJob(ctx context.Context, q string, id uuid.UUID, val, op string) error {
	tag, err := s.pool.Exec(ctx, q, id, val)
	if err != nil {
		return mapErr(err, op)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", op, domain.ErrNotFound)
	}
	return nil
}

// NextQueuedJob returns the oldest queued job for an org, or (nil, nil) if none.
func (s *pgStore) NextQueuedJob(ctx context.Context, orgID uuid.UUID) (*domain.Job, error) {
	const q = `
		SELECT id, project_id, org_id, status, build_no,
		       payload,
		       COALESCE(session_id, ''), COALESCE(docker_tag, ''), COALESCE(error_msg, ''),
		       queued_at, started_at, finished_at
		FROM project_jobs
		WHERE org_id = $1 AND status = 'queued'
		ORDER BY queued_at ASC
		LIMIT 1`
	j := &domain.Job{}
	err := scanJob(s.pool.QueryRow(ctx, q, orgID), j)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// LastSessionID returns the Claude session id of the project's most recent job
// that carries a non-empty session_id, or "" when none exists. Used by an edit
// build to --resume the project's prior Claude session.
func (s *pgStore) LastSessionID(ctx context.Context, projectID uuid.UUID) (string, error) {
	const q = `
		SELECT session_id
		FROM project_jobs
		WHERE project_id = $1 AND session_id IS NOT NULL AND session_id <> ''
		ORDER BY queued_at DESC
		LIMIT 1`
	var sid string
	if err := s.pool.QueryRow(ctx, q, projectID).Scan(&sid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("store: last session id: %w", err)
	}
	return sid, nil
}

// QueueDepth counts jobs in JobQueued for a project.
func (s *pgStore) QueueDepth(ctx context.Context, projectID uuid.UUID) (int, error) {
	const q = `SELECT count(*) FROM project_jobs WHERE project_id = $1 AND status = 'queued'`
	var n int
	if err := s.pool.QueryRow(ctx, q, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: queue depth: %w", err)
	}
	return n, nil
}

// --- Dashboard (operator console read model) --------------------------------

// stripBranchPrefix turns a job's docker_tag into a display branch. The
// pipeline stores "branch:crn/<project_id>" in docker_tag; the console wants
// just the branch, and an unset tag renders as empty.
func stripBranchPrefix(dockerTag string) string {
	const prefix = "branch:"
	if len(dockerTag) >= len(prefix) && dockerTag[:len(prefix)] == prefix {
		return dockerTag[len(prefix):]
	}
	return ""
}

// DashboardSnapshot assembles the operator-console read model from a handful of
// parameter-free queries against the pool. All slices are initialized non-nil
// so the JSON renders [] rather than null. generated_at is the server time.
func (s *pgStore) DashboardSnapshot(ctx context.Context) (*domain.DashboardSnapshot, error) {
	snap := &domain.DashboardSnapshot{
		Building:    []domain.BuildingJob{},
		Queue:       []domain.QueuedJob{},
		Projects:    []domain.ProjectRow{},
		Activity:    []domain.ActivityRow{},
		GeneratedAt: time.Now().UTC(),
	}

	if err := s.dashboardVitals(ctx, &snap.Vitals); err != nil {
		return nil, err
	}
	if err := s.dashboardBuilding(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.dashboardQueue(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.dashboardProjects(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.dashboardActivity(ctx, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// dashboardVitals fills the headline counts in one round-trip. done_today /
// failed_today count terminal jobs whose finished_at falls on the current date.
func (s *pgStore) dashboardVitals(ctx context.Context, v *domain.DashboardVitals) error {
	const q = `
		SELECT
			(SELECT count(*) FROM projects),
			(SELECT count(*) FROM project_jobs WHERE status = 'queued'),
			(SELECT count(*) FROM project_jobs WHERE status = 'building'),
			(SELECT count(*) FROM project_jobs
			   WHERE status = 'done'   AND finished_at::date = CURRENT_DATE),
			(SELECT count(*) FROM project_jobs
			   WHERE status = 'failed' AND finished_at::date = CURRENT_DATE)`
	if err := s.pool.QueryRow(ctx, q).Scan(
		&v.Projects, &v.Queued, &v.Building, &v.DoneToday, &v.FailedToday,
	); err != nil {
		return fmt.Errorf("store: dashboard vitals: %w", err)
	}
	return nil
}

// dashboardBuilding lists all in-flight builds (newest first), joined to
// projects + orgs for display names. branch is derived from docker_tag.
func (s *pgStore) dashboardBuilding(ctx context.Context, snap *domain.DashboardSnapshot) error {
	const q = `
		SELECT j.id, j.project_id, p.name, o.name, j.build_no,
		       COALESCE(j.docker_tag, ''), j.started_at
		FROM project_jobs j
		JOIN projects p ON p.id = j.project_id
		JOIN orgs o     ON o.id = j.org_id
		WHERE j.status = 'building'
		ORDER BY j.started_at DESC NULLS LAST, j.queued_at DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("store: dashboard building: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var b domain.BuildingJob
		var dockerTag string
		if err := rows.Scan(
			&b.JobID, &b.ProjectID, &b.ProjectName, &b.OrgName, &b.BuildNo,
			&dockerTag, &b.StartedAt,
		); err != nil {
			return fmt.Errorf("store: dashboard building scan: %w", err)
		}
		b.Branch = stripBranchPrefix(dockerTag)
		snap.Building = append(snap.Building, b)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: dashboard building: %w", err)
	}
	return nil
}

// dashboardQueue lists waiting builds FIFO (queued_at ASC), joined for names.
func (s *pgStore) dashboardQueue(ctx context.Context, snap *domain.DashboardSnapshot) error {
	const q = `
		SELECT j.id, j.project_id, p.name, o.name, j.build_no, j.queued_at
		FROM project_jobs j
		JOIN projects p ON p.id = j.project_id
		JOIN orgs o     ON o.id = j.org_id
		WHERE j.status = 'queued'
		ORDER BY j.queued_at ASC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("store: dashboard queue: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var qj domain.QueuedJob
		if err := rows.Scan(
			&qj.JobID, &qj.ProjectID, &qj.ProjectName, &qj.OrgName, &qj.BuildNo, &qj.QueuedAt,
		); err != nil {
			return fmt.Errorf("store: dashboard queue scan: %w", err)
		}
		snap.Queue = append(snap.Queue, qj)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: dashboard queue: %w", err)
	}
	return nil
}

// dashboardProjects lists every project (newest created first) with its org
// name and a summary of its latest job. A LATERAL join pulls each project's
// most recent job (by queued_at); last_* are empty/null when there is no job.
// last_activity_at prefers finished_at, falling back to queued_at.
func (s *pgStore) dashboardProjects(ctx context.Context, snap *domain.DashboardSnapshot) error {
	// skill_count is a single scalar (count of enabled skills, which apply to
	// every build) computed once via a subquery and broadcast to each row.
	const q = `
		SELECT p.id, p.name, o.name, p.status, p.current_build,
		       (SELECT count(*) FROM skills WHERE enabled) AS skill_count,
		       COALESCE(lj.status, ''),
		       COALESCE(lj.docker_tag, ''),
		       COALESCE(lj.finished_at, lj.queued_at),
		       p.repo_url
		FROM projects p
		JOIN orgs o ON o.id = p.org_id
		LEFT JOIN LATERAL (
			SELECT j.status, j.docker_tag, j.finished_at, j.queued_at
			FROM project_jobs j
			WHERE j.project_id = p.id
			ORDER BY j.queued_at DESC
			LIMIT 1
		) lj ON true
		ORDER BY p.created_at DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("store: dashboard projects: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var pr domain.ProjectRow
		var lastDockerTag string
		if err := rows.Scan(
			&pr.ID, &pr.Name, &pr.OrgName, &pr.Status, &pr.CurrentBuild,
			&pr.SkillCount,
			&pr.LastStatus, &lastDockerTag, &pr.LastActivityAt,
			&pr.RepoURL,
		); err != nil {
			return fmt.Errorf("store: dashboard projects scan: %w", err)
		}
		pr.LastBranch = stripBranchPrefix(lastDockerTag)
		snap.Projects = append(snap.Projects, pr)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: dashboard projects: %w", err)
	}
	return nil
}

// dashboardActivity lists the ~30 most recent build_events (newest first),
// joined to the originating job for build_no and to its project for the name.
// build_no defaults to 0 if the job cannot be resolved.
func (s *pgStore) dashboardActivity(ctx context.Context, snap *domain.DashboardSnapshot) error {
	const q = `
		SELECT e.event_type, p.id, p.name,
		       COALESCE(j.build_no, 0), e.created_at
		FROM build_events e
		JOIN project_jobs j ON j.id = e.job_id
		JOIN projects p     ON p.id = j.project_id
		ORDER BY e.created_at DESC
		LIMIT 30`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("store: dashboard activity: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var a domain.ActivityRow
		if err := rows.Scan(
			&a.Type, &a.ProjectID, &a.ProjectName, &a.BuildNo, &a.At,
		); err != nil {
			return fmt.Errorf("store: dashboard activity scan: %w", err)
		}
		snap.Activity = append(snap.Activity, a)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: dashboard activity: %w", err)
	}
	return nil
}

// --- Build traces (durable per-build state history) -------------------------

// traceListCols is the summary projection shared by the list reads (no events).
const traceListCols = `
	job_id, project_id, build_no, outcome, mode, commit_sha, branch, remote,
	session_id, cost_usd, tool_count, file_count, error_msg,
	started_at, finished_at, created_at`

// SaveJobTrace upserts one build's trace (summary + full event stream) by
// job_id. The events slice is stored as JSONB; a nil slice persists as '[]'.
func (s *pgStore) SaveJobTrace(ctx context.Context, t *domain.BuildTrace) error {
	events := t.Events
	if events == nil {
		events = []domain.BuildEventMsg{}
	}
	eventsJSON, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("store: save job trace: marshal events: %w", err)
	}
	const q = `
		INSERT INTO job_traces (
			job_id, project_id, build_no, outcome, mode, commit_sha, branch, remote,
			session_id, cost_usd, tool_count, file_count, error_msg, events,
			started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (job_id) DO UPDATE SET
			outcome     = EXCLUDED.outcome,
			mode        = EXCLUDED.mode,
			commit_sha  = EXCLUDED.commit_sha,
			branch      = EXCLUDED.branch,
			remote      = EXCLUDED.remote,
			session_id  = EXCLUDED.session_id,
			cost_usd    = EXCLUDED.cost_usd,
			tool_count  = EXCLUDED.tool_count,
			file_count  = EXCLUDED.file_count,
			error_msg   = EXCLUDED.error_msg,
			events      = EXCLUDED.events,
			started_at  = EXCLUDED.started_at,
			finished_at = EXCLUDED.finished_at`
	if _, err := s.pool.Exec(ctx, q,
		t.JobID, t.ProjectID, t.BuildNo, t.Outcome, t.Mode, t.CommitSHA, t.Branch, t.Remote,
		t.SessionID, t.CostUSD, t.ToolCount, t.FileCount, t.ErrorMsg, eventsJSON,
		t.StartedAt, t.FinishedAt,
	); err != nil {
		return fmt.Errorf("store: save job trace: %w", err)
	}
	return nil
}

// ListBuildTraces returns a project's traces newest build first (summary only —
// Events left empty). Always returns a non-nil slice.
func (s *pgStore) ListBuildTraces(ctx context.Context, projectID uuid.UUID) ([]*domain.BuildTrace, error) {
	q := `SELECT` + traceListCols + `
		FROM job_traces WHERE project_id = $1 ORDER BY build_no DESC`
	rows, err := s.pool.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list build traces: %w", err)
	}
	defer rows.Close()
	return scanTraceSummaries(rows)
}

// RecentBuildTraces returns the most recent traces across all projects, newest
// first (summary only). A non-positive limit falls back to 30.
func (s *pgStore) RecentBuildTraces(ctx context.Context, limit int) ([]*domain.BuildTrace, error) {
	if limit <= 0 {
		limit = 30
	}
	q := `SELECT` + traceListCols + `
		FROM job_traces ORDER BY created_at DESC LIMIT $1`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent build traces: %w", err)
	}
	defer rows.Close()
	return scanTraceSummaries(rows)
}

// GetBuildTrace returns one trace by job id WITH its full event stream.
func (s *pgStore) GetBuildTrace(ctx context.Context, jobID uuid.UUID) (*domain.BuildTrace, error) {
	q := `SELECT` + traceListCols + `, events
		FROM job_traces WHERE job_id = $1`
	t := &domain.BuildTrace{}
	var eventsJSON []byte
	if err := scanTraceSummaryRow(s.pool.QueryRow(ctx, q, jobID), t, &eventsJSON); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(eventsJSON, &t.Events); err != nil {
		return nil, fmt.Errorf("store: get build trace: unmarshal events: %w", err)
	}
	if t.Events == nil {
		t.Events = []domain.BuildEventMsg{}
	}
	return t, nil
}

// scanTraceSummaries collects rows projecting traceListCols (no events).
func scanTraceSummaries(rows pgx.Rows) ([]*domain.BuildTrace, error) {
	out := []*domain.BuildTrace{}
	for rows.Next() {
		t := &domain.BuildTrace{}
		if err := scanTraceSummaryRow(rows, t, nil); err != nil {
			return nil, err
		}
		t.Events = []domain.BuildEventMsg{}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan build traces: %w", err)
	}
	return out, nil
}

// scanTraceSummaryRow scans one traceListCols row into t. When eventsJSON is
// non-nil the caller has appended ", events" to the projection and the raw JSONB
// is scanned into it (for GetBuildTrace); otherwise only the summary is read.
func scanTraceSummaryRow(r scannable, t *domain.BuildTrace, eventsJSON *[]byte) error {
	dest := []any{
		&t.JobID, &t.ProjectID, &t.BuildNo, &t.Outcome, &t.Mode, &t.CommitSHA, &t.Branch, &t.Remote,
		&t.SessionID, &t.CostUSD, &t.ToolCount, &t.FileCount, &t.ErrorMsg,
		&t.StartedAt, &t.FinishedAt, &t.CreatedAt,
	}
	if eventsJSON != nil {
		dest = append(dest, eventsJSON)
	}
	if err := r.Scan(dest...); err != nil {
		return mapErr(err, "store: scan build trace")
	}
	return nil
}

// --- In-demo feedback (Edit Request Panel) ----------------------------------

// feedbackCols is the full projection for a feedback_requests row.
const feedbackCols = `
	id, project_id, status, category, priority, note, page_url, reporter,
	payload, job_id, created_at, issue_number, issue_url`

// ListFeedback returns feedback requests newest first. A non-empty status
// filters by lifecycle state (e.g. "new"); "" returns all. Non-nil slice.
func (s *pgStore) ListFeedback(ctx context.Context, status string) ([]*domain.FeedbackRequest, error) {
	var rows pgx.Rows
	var err error
	if status == "" {
		rows, err = s.pool.Query(ctx, `SELECT`+feedbackCols+`
			FROM feedback_requests ORDER BY created_at DESC`)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT`+feedbackCols+`
			FROM feedback_requests WHERE status = $1 ORDER BY created_at DESC`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list feedback: %w", err)
	}
	defer rows.Close()

	out := []*domain.FeedbackRequest{}
	for rows.Next() {
		f := &domain.FeedbackRequest{}
		var payloadJSON []byte
		if err := scanFeedbackRow(rows, f, &payloadJSON); err != nil {
			return nil, err
		}
		if err := decodeFeedbackPayload(payloadJSON, f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan feedback: %w", err)
	}
	return out, nil
}

// GetFeedback returns one feedback request by id, ErrNotFound when absent.
func (s *pgStore) GetFeedback(ctx context.Context, id uuid.UUID) (*domain.FeedbackRequest, error) {
	q := `SELECT` + feedbackCols + ` FROM feedback_requests WHERE id = $1`
	f := &domain.FeedbackRequest{}
	var payloadJSON []byte
	if err := scanFeedbackRow(s.pool.QueryRow(ctx, q, id), f, &payloadJSON); err != nil {
		return nil, err
	}
	if err := decodeFeedbackPayload(payloadJSON, f); err != nil {
		return nil, err
	}
	return f, nil
}

// SetFeedbackStatus updates status and, when jobID is non-nil, links the edit
// build the request was merged into (existing job_id preserved when nil).
func (s *pgStore) SetFeedbackStatus(ctx context.Context, id uuid.UUID, status string, jobID *uuid.UUID) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE feedback_requests SET status = $2, job_id = COALESCE($3, job_id)
		WHERE id = $1`, id, status, jobID)
	if err != nil {
		return fmt.Errorf("store: set feedback status: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store: set feedback status: %w", domain.ErrNotFound)
	}
	return nil
}

// ListFeedbackNeedingIssue returns un-mirrored feedback (issue_number IS NULL),
// oldest first, capped at limit. Non-nil slice.
func (s *pgStore) ListFeedbackNeedingIssue(ctx context.Context, limit int, exclude []uuid.UUID) ([]*domain.FeedbackRequest, error) {
	rows, err := s.pool.Query(ctx, `SELECT`+feedbackCols+`
		FROM feedback_requests WHERE issue_number IS NULL AND id <> ALL($2::uuid[])
		ORDER BY created_at ASC LIMIT $1`, limit, exclude)
	if err != nil {
		return nil, fmt.Errorf("store: list feedback needing issue: %w", err)
	}
	defer rows.Close()

	out := []*domain.FeedbackRequest{}
	for rows.Next() {
		f := &domain.FeedbackRequest{}
		var payloadJSON []byte
		if err := scanFeedbackRow(rows, f, &payloadJSON); err != nil {
			return nil, err
		}
		if err := decodeFeedbackPayload(payloadJSON, f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan feedback needing issue: %w", err)
	}
	return out, nil
}

// SetFeedbackIssue records the GitHub issue a feedback row was mirrored into,
// but only if it has none yet (compare-and-set on issue_number IS NULL). It
// returns whether this call won — a false result means another creator mirrored
// the row first, so the caller's just-created issue is a duplicate.
func (s *pgStore) SetFeedbackIssue(ctx context.Context, id uuid.UUID, number int, url string) (bool, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE feedback_requests SET issue_number = $2, issue_url = $3
		WHERE id = $1 AND issue_number IS NULL`, id, number, url)
	if err != nil {
		return false, fmt.Errorf("store: set feedback issue: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

func scanFeedbackRow(r scannable, f *domain.FeedbackRequest, payloadJSON *[]byte) error {
	if err := r.Scan(
		&f.ID, &f.ProjectID, &f.Status, &f.Category, &f.Priority, &f.Note,
		&f.PageURL, &f.Reporter, payloadJSON, &f.JobID, &f.CreatedAt,
		&f.IssueNumber, &f.IssueURL,
	); err != nil {
		return mapErr(err, "store: scan feedback")
	}
	return nil
}

// decodeFeedbackPayload unmarshals the JSONB payload and guarantees Pins is a
// non-nil slice (so JSON renders [] not null for the panel).
func decodeFeedbackPayload(raw []byte, f *domain.FeedbackRequest) error {
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &f.Payload); err != nil {
			return fmt.Errorf("store: decode feedback payload: %w", err)
		}
	}
	if f.Payload.Pins == nil {
		f.Payload.Pins = []domain.FeedbackPin{}
	}
	return nil
}

// --- Edit requests ----------------------------------------------------------

// CreateEditRequest inserts an edit request and reads back generated columns.
func (s *pgStore) CreateEditRequest(ctx context.Context, r *domain.EditRequest) error {
	const q = `
		INSERT INTO edit_requests (id, project_id, job_id, requester, diff_request, priority, status)
		VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2, $3, $4, $5,
			COALESCE(NULLIF($6, ''), 'normal'),
			COALESCE(NULLIF($7, ''), 'pending')
		)
		RETURNING id, project_id, job_id, COALESCE(requester, ''),
		          diff_request, priority, status, created_at`
	row := s.pool.QueryRow(ctx, q,
		r.ID, r.ProjectID, r.JobID, r.Requester, []byte(r.DiffRequest),
		string(r.Priority), string(r.Status),
	)
	return scanEditRequest(row, r)
}

// GetEditRequest fetches an edit request by id.
func (s *pgStore) GetEditRequest(ctx context.Context, id uuid.UUID) (*domain.EditRequest, error) {
	const q = `
		SELECT id, project_id, job_id, COALESCE(requester, ''),
		       diff_request, priority, status, created_at
		FROM edit_requests WHERE id = $1`
	r := &domain.EditRequest{}
	if err := scanEditRequest(s.pool.QueryRow(ctx, q, id), r); err != nil {
		return nil, err
	}
	return r, nil
}

// UpdateEditRequestStatus moves an edit request to a new status and optionally
// links it to the job it was merged into (job_id is left unchanged when nil).
func (s *pgStore) UpdateEditRequestStatus(ctx context.Context, id uuid.UUID, status domain.EditRequestStatus, jobID *uuid.UUID) error {
	const q = `UPDATE edit_requests SET status = $2, job_id = COALESCE($3, job_id) WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, id, string(status), jobID)
	if err != nil {
		return mapErr(err, "store: update edit request status")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: update edit request status: %w", domain.ErrNotFound)
	}
	return nil
}

// --- API keys ---------------------------------------------------------------

// OrgByAPIKeyHash resolves an org from a hashed API key, ignoring revoked keys.
// Returns domain.ErrNotFound when no active key matches.
func (s *pgStore) OrgByAPIKeyHash(ctx context.Context, hash string) (*domain.Org, error) {
	const q = `
		SELECT o.id, o.name, o.created_at
		FROM api_keys k
		JOIN orgs o ON o.id = k.org_id
		WHERE k.key_hash = $1 AND k.revoked_at IS NULL`
	o := &domain.Org{}
	if err := s.pool.QueryRow(ctx, q, hash).Scan(&o.ID, &o.Name, &o.CreatedAt); err != nil {
		return nil, mapErr(err, "store: org by api key")
	}
	return o, nil
}

// CreateAPIKey inserts a per-org API key (only the hash is persisted).
func (s *pgStore) CreateAPIKey(ctx context.Context, k *domain.APIKey) error {
	const q = `
		INSERT INTO api_keys (id, org_id, key_hash, revoked_at)
		VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2, $3, $4
		)
		RETURNING id, org_id, created_at, revoked_at`
	if err := s.pool.QueryRow(ctx, q, k.ID, k.OrgID, k.Hash, k.RevokedAt).
		Scan(&k.ID, &k.OrgID, &k.CreatedAt, &k.RevokedAt); err != nil {
		return mapErr(err, "store: create api key")
	}
	return nil
}

// --- Skills (Claude Agent Skills registry) ----------------------------------

// ListSkills returns every skill ordered by name.
func (s *pgStore) ListSkills(ctx context.Context) ([]*domain.Skill, error) {
	const q = `
		SELECT name, description, body, files, enabled, is_builtin, updated_at
		FROM skills ORDER BY name ASC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list skills: %w", err)
	}
	defer rows.Close()

	var out []*domain.Skill
	for rows.Next() {
		sk := &domain.Skill{}
		if err := scanSkill(rows, sk); err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list skills: %w", err)
	}
	return out, nil
}

// GetSkill fetches a skill by name, returning domain.ErrNotFound if absent.
func (s *pgStore) GetSkill(ctx context.Context, name string) (*domain.Skill, error) {
	const q = `
		SELECT name, description, body, files, enabled, is_builtin, updated_at
		FROM skills WHERE name = $1`
	sk := &domain.Skill{}
	if err := scanSkill(s.pool.QueryRow(ctx, q, name), sk); err != nil {
		return nil, err
	}
	return sk, nil
}

// UpsertSkill creates or updates a skill by name (an operator edit). is_builtin
// is never changed here (a new row defaults to false; an existing row keeps its
// flag). The extra Files map is persisted as JSONB. The persisted row is read
// back into s, stamping updated_at.
func (s *pgStore) UpsertSkill(ctx context.Context, sk *domain.Skill) error {
	files, err := marshalSkillFiles(sk.Files)
	if err != nil {
		return fmt.Errorf("store: upsert skill: %w", err)
	}
	const q = `
		INSERT INTO skills (name, description, body, files, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET
			description = EXCLUDED.description,
			body        = EXCLUDED.body,
			files       = EXCLUDED.files,
			enabled     = EXCLUDED.enabled,
			updated_at  = now()
		RETURNING name, description, body, files, enabled, is_builtin, updated_at`
	row := s.pool.QueryRow(ctx, q, sk.Name, sk.Description, sk.Body, files, sk.Enabled)
	if err := scanSkill(row, sk); err != nil {
		return mapErr(err, "store: upsert skill")
	}
	return nil
}

// SetSkillEnabled flips the enabled flag for a skill by name.
func (s *pgStore) SetSkillEnabled(ctx context.Context, name string, enabled bool) error {
	const q = `UPDATE skills SET enabled = $2, updated_at = now() WHERE name = $1`
	tag, err := s.pool.Exec(ctx, q, name, enabled)
	if err != nil {
		return mapErr(err, "store: set skill enabled")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: set skill enabled: %w", domain.ErrNotFound)
	}
	return nil
}

// DeleteSkill removes a non-built-in skill by name. It returns
// domain.ErrSkillBuiltin when the skill is built-in and domain.ErrNotFound when
// no skill matches. The DELETE is guarded by is_builtin = false, so a built-in
// row is never removed; we then disambiguate the zero-row result.
func (s *pgStore) DeleteSkill(ctx context.Context, name string) error {
	const q = `DELETE FROM skills WHERE name = $1 AND is_builtin = false`
	tag, err := s.pool.Exec(ctx, q, name)
	if err != nil {
		return mapErr(err, "store: delete skill")
	}
	if tag.RowsAffected() == 0 {
		// Either the skill is absent or it is built-in; check which to return the
		// right sentinel.
		var isBuiltin bool
		if err := s.pool.QueryRow(ctx, `SELECT is_builtin FROM skills WHERE name = $1`, name).
			Scan(&isBuiltin); err != nil {
			return mapErr(err, "store: delete skill")
		}
		if isBuiltin {
			return fmt.Errorf("store: delete skill %q: %w", name, domain.ErrSkillBuiltin)
		}
		return fmt.Errorf("store: delete skill %q: %w", name, domain.ErrNotFound)
	}
	return nil
}

// EnsureBuiltinSkill (re)seeds a built-in skill so the code is the source of
// truth for the canonical harness. On a fresh row it inserts with the supplied
// enabled flag and is_builtin = true. ON CONFLICT (name) it UPDATEs body,
// description, files, is_builtin = true, and updated_at, but PRESERVES the
// existing row's `enabled` flag — so restarting CRN re-applies the canonical
// SKILL.md/files while leaving enable/disable entirely to the operator.
func (s *pgStore) EnsureBuiltinSkill(ctx context.Context, sk *domain.Skill) error {
	files, err := marshalSkillFiles(sk.Files)
	if err != nil {
		return fmt.Errorf("store: ensure builtin skill: %w", err)
	}
	const q = `
		INSERT INTO skills (name, description, body, files, enabled, is_builtin)
		VALUES ($1, $2, $3, $4, $5, true)
		ON CONFLICT (name) DO UPDATE SET
			description = EXCLUDED.description,
			body        = EXCLUDED.body,
			files       = EXCLUDED.files,
			is_builtin  = true,
			updated_at  = now()`
	if _, err := s.pool.Exec(ctx, q, sk.Name, sk.Description, sk.Body, files, sk.Enabled); err != nil {
		return fmt.Errorf("store: ensure builtin skill: %w", err)
	}
	return nil
}

// RecordSkillVersion snapshots the skill's CURRENT persisted row into
// skill_versions at the next version number (COALESCE(max,0)+1), tagged with
// note. A single INSERT ... SELECT reads the live skills row and the running
// max version together so the recorded snapshot is exactly the current state.
// Returns domain.ErrNotFound when the skill does not exist.
func (s *pgStore) RecordSkillVersion(ctx context.Context, name, note string) error {
	const q = `
		INSERT INTO skill_versions (skill_name, version, description, body, files, note)
		SELECT
			s.name,
			COALESCE((SELECT max(v.version) FROM skill_versions v WHERE v.skill_name = s.name), 0) + 1,
			s.description, s.body, s.files, $2
		FROM skills s
		WHERE s.name = $1`
	tag, err := s.pool.Exec(ctx, q, name, note)
	if err != nil {
		return mapErr(err, "store: record skill version")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: record skill version %q: %w", name, domain.ErrNotFound)
	}
	return nil
}

// ListSkillVersions returns a skill's version history, newest first. An unknown
// skill simply yields an empty slice (no error) — the caller distinguishes a
// missing skill via GetSkill if needed.
func (s *pgStore) ListSkillVersions(ctx context.Context, name string) ([]*domain.SkillVersion, error) {
	const q = `
		SELECT version, description, body, files, note, created_at
		FROM skill_versions
		WHERE skill_name = $1
		ORDER BY version DESC`
	rows, err := s.pool.Query(ctx, q, name)
	if err != nil {
		return nil, fmt.Errorf("store: list skill versions: %w", err)
	}
	defer rows.Close()

	var out []*domain.SkillVersion
	for rows.Next() {
		v := &domain.SkillVersion{}
		var files map[string]string
		if err := rows.Scan(
			&v.Version, &v.Description, &v.Body, &files, &v.Note, &v.CreatedAt,
		); err != nil {
			return nil, mapErr(err, "store: scan skill version")
		}
		if files == nil {
			files = map[string]string{}
		}
		v.Files = files
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list skill versions: %w", err)
	}
	return out, nil
}

// --- Per-org advisory lock --------------------------------------------------

// AcquireOrgLock takes a PostgreSQL session-level advisory lock keyed by orgID.
// It returns domain.ErrOrgLocked if another session already holds it
// (non-blocking pg_try_advisory_lock).
//
// The lock is taken on a single dedicated connection checked out from the pool;
// that connection is HELD for the lifetime of the lock (session advisory locks
// are bound to the holding connection, and only the holder can unlock it). The
// returned release func unlocks and returns the connection to the pool; it is
// idempotent (safe to call multiple times) via sync.Once.
//
// NOTE — session vs. transaction lock: the domain.Store contract requires the
// lock to outlive the acquiring call and be released explicitly later via the
// returned func. A transaction-level lock (pg_advisory_xact_lock) cannot do
// this without holding a transaction open for the entire build, so the
// session-level lock on a pinned connection is the correct primitive. The
// DB-level partial unique index uq_jobs_one_building_per_org remains the
// independent backstop against two jobs reaching 'building' for one org.
func (s *pgStore) AcquireOrgLock(ctx context.Context, orgID uuid.UUID) (release func(), err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: acquire conn for org lock: %w", err)
	}

	key := hashOrg(orgID)
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		conn.Release()
		return nil, fmt.Errorf("store: pg_try_advisory_lock: %w", err)
	}
	if !got {
		conn.Release()
		return nil, fmt.Errorf("store: org %s: %w", orgID, domain.ErrOrgLocked)
	}

	var once sync.Once
	release = func() {
		once.Do(func() {
			// Best-effort unlock on a fresh, short-lived context so an already
			// cancelled caller ctx cannot leave the advisory lock held. The
			// connection is always returned to the pool afterwards.
			unlockCtx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
			defer cancel()
			if _, uerr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, key); uerr != nil {
				s.logger.Error("org lock unlock failed", "org_id", orgID, "err", uerr)
			}
			conn.Release()
		})
	}
	return release, nil
}

// --- Notifier ---------------------------------------------------------------

// pgNotifier writes build_events to the central DB and issues a Postgres NOTIFY
// so LISTEN subscribers (FBD + FTC DV fan-out) are woken.
type pgNotifier struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewNotifier opens a pool against the central DB and returns a domain.Notifier.
//
// Signature is fixed by cmd/server/main.go. It uses its own pool so it targets
// CentralDatabaseURL independently of the CRN store pool.
func NewNotifier(ctx context.Context, dsn string) (domain.Notifier, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("notifier: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("notifier: ping: %w", err)
	}
	return &pgNotifier{
		pool:   pool,
		logger: slog.Default().With("component", "notifier"),
	}, nil
}

// Notify appends a build_events row on the central DB and issues a Postgres
// NOTIFY carrying the new event id so LISTEN subscribers can fetch it. The
// INSERT and NOTIFY share one transaction so a subscriber never sees a NOTIFY
// for a row that is not yet committed.
func (n *pgNotifier) Notify(ctx context.Context, e *domain.BuildEvent) error {
	const insertQ = `
		INSERT INTO build_events (id, job_id, event_type, payload, notified_fbd, notified_ftcdv)
		VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2, $3, $4, $5, $6
		)
		RETURNING id, job_id, event_type, payload, created_at, notified_fbd, notified_ftcdv`

	var payload []byte
	if len(e.Payload) > 0 {
		payload = []byte(e.Payload)
	}

	tx, err := n.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("notifier: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var eventType string
	var rawPayload []byte
	if err := tx.QueryRow(ctx, insertQ,
		e.ID, e.JobID, string(e.EventType), payload, e.NotifiedFBD, e.NotifiedFTCDV,
	).Scan(&e.ID, &e.JobID, &eventType, &rawPayload, &e.CreatedAt, &e.NotifiedFBD, &e.NotifiedFTCDV); err != nil {
		return mapErr(err, "notifier: insert build_event")
	}
	e.EventType = domain.BuildEventType(eventType)
	e.Payload = rawPayload

	// pg_notify with the event id as payload. The channel name cannot be a bound
	// parameter for the NOTIFY statement, but notifyChannel is a package
	// constant (never user input), so there is no injection surface; the payload
	// is bound.
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, notifyChannel, e.ID.String()); err != nil {
		return fmt.Errorf("notifier: pg_notify: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("notifier: commit: %w", err)
	}
	return nil
}

// --- scanning helpers -------------------------------------------------------

// scannable abstracts pgx.Row and pgx.Rows for the single-row scan helpers.
type scannable interface {
	Scan(dest ...any) error
}

func scanProject(r scannable, p *domain.Project) error {
	var status string
	if err := r.Scan(
		&p.ID, &p.OrgID, &p.Name, &status, &p.CurrentBuild, &p.Stack, &p.RepoURL, &p.CreatedAt,
	); err != nil {
		return mapErr(err, "store: scan project")
	}
	p.Status = domain.ProjectStatus(status)
	return nil
}

func scanJob(r scannable, j *domain.Job) error {
	var status string
	var payload []byte
	if err := r.Scan(
		&j.ID, &j.ProjectID, &j.OrgID, &status, &j.BuildNo,
		&payload,
		&j.SessionID, &j.DockerTag, &j.ErrorMsg,
		&j.QueuedAt, &j.StartedAt, &j.FinishedAt,
	); err != nil {
		return mapErr(err, "store: scan job")
	}
	j.Status = domain.JobStatus(status)
	j.Payload = payload
	return nil
}

func scanEditRequest(r scannable, e *domain.EditRequest) error {
	var priority, status string
	var diff []byte
	if err := r.Scan(
		&e.ID, &e.ProjectID, &e.JobID, &e.Requester,
		&diff, &priority, &status, &e.CreatedAt,
	); err != nil {
		return mapErr(err, "store: scan edit request")
	}
	e.DiffRequest = diff
	e.Priority = domain.EditPriority(priority)
	e.Status = domain.EditRequestStatus(status)
	return nil
}

func scanSkill(r scannable, sk *domain.Skill) error {
	// pgx decodes a jsonb column into map[string]string directly. A SQL NULL is
	// not possible (the column is NOT NULL DEFAULT '{}'), but normalize a nil map
	// to an empty non-nil map so callers never see nil.
	var files map[string]string
	if err := r.Scan(
		&sk.Name, &sk.Description, &sk.Body, &files, &sk.Enabled, &sk.IsBuiltin, &sk.UpdatedAt,
	); err != nil {
		return mapErr(err, "store: scan skill")
	}
	if files == nil {
		files = map[string]string{}
	}
	sk.Files = files
	return nil
}

// marshalSkillFiles serializes a skill's extra-files map to a jsonb-ready []byte.
// A nil map is persisted as an empty JSON object so the column never holds null.
func marshalSkillFiles(files map[string]string) ([]byte, error) {
	if files == nil {
		files = map[string]string{}
	}
	b, err := json.Marshal(files)
	if err != nil {
		return nil, fmt.Errorf("marshal skill files: %w", err)
	}
	return b, nil
}

// mapErr converts pgx.ErrNoRows to domain.ErrNotFound and wraps everything else
// with the supplied operation prefix.
func mapErr(err error, op string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, domain.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// hashOrg derives a stable int64 advisory-lock key from an org UUID. Postgres
// advisory locks key on a bigint, so the 16-byte UUID is folded into 8 bytes by
// XORing its two halves. Distinct orgs get distinct keys with overwhelming
// probability; a false collision only momentarily serializes two unrelated
// orgs' builds, and the DB unique-index backstop still guarantees correctness.
func hashOrg(orgID uuid.UUID) int64 {
	hi := binary.BigEndian.Uint64(orgID[0:8])
	lo := binary.BigEndian.Uint64(orgID[8:16])
	return int64(hi ^ lo)
}
