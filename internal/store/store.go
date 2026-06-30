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
		RETURNING id, org_id, name, status, current_build, COALESCE(stack, ''), created_at`
	row := s.pool.QueryRow(ctx, q,
		p.ID, p.OrgID, p.Name, string(p.Status), p.CurrentBuild, p.Stack,
	)
	return scanProject(row, p)
}

// GetProject fetches a project by id, returning domain.ErrNotFound if absent.
func (s *pgStore) GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	const q = `
		SELECT id, org_id, name, status, current_build, COALESCE(stack, ''), created_at
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
		SELECT id, org_id, name, status, current_build, COALESCE(stack, ''), created_at
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

// QueueDepth counts jobs in JobQueued for a project.
func (s *pgStore) QueueDepth(ctx context.Context, projectID uuid.UUID) (int, error) {
	const q = `SELECT count(*) FROM project_jobs WHERE project_id = $1 AND status = 'queued'`
	var n int
	if err := s.pool.QueryRow(ctx, q, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: queue depth: %w", err)
	}
	return n, nil
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
		&p.ID, &p.OrgID, &p.Name, &status, &p.CurrentBuild, &p.Stack, &p.CreatedAt,
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
