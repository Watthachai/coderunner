// Package jobs implements domain.JobManager: the build queue, the per-org
// concurrency rule (1 building job per org), and the full build lifecycle
// (CRN-architecture.md §2.4 "Build Lifecycle" and §3 state machine).
//
// OWNED BY: the 'jobs' implementer.
//
// Lifecycle of processing one job (run under the org lock, in its own goroutine):
//  1. store.AcquireOrgLock(ctx, orgID) — if domain.ErrOrgLocked, leave it
//     queued (another build owns the org); return without error.
//  2. store.UpdateJobStatus(job, JobBuilding); notifier.Notify(build_started).
//  3. runner.Run(ctx, RunSpec{...}, emit) where emit fans each ClaudeEvent out
//     to (a) live WS subscribers via Subscribe (translated to BuildEventMsg)
//     and (b) store.SetJobSession when a session id appears.
//  4. On success: TODO(crn) git commit + docker build/push + SetJobDockerTag,
//     then UpdateJobStatus(JobDone) + notifier.Notify(build_done).
//     On failure: UpdateJobStatus(JobFailed, err) + notifier.Notify(build_failed).
//  5. Release the org lock, then pull the next queued job for the org
//     (store.NextQueuedJob) and chain into it — never interrupt, only chain.
//
// A simple in-memory map[jobID][]*subscriber guarded by a mutex backs the live
// WebSocket fan-out (Subscribe).
package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
	"github.com/Watthachai/fitt-coderunner/internal/github"
)

// subscriberBuffer bounds each live WebSocket subscriber's channel. A slow
// consumer that fills its buffer drops events rather than stalling the build —
// the authoritative record is build_events in the central DB; the WS stream is
// best-effort live telemetry.
const subscriberBuffer = 64

// historyCap bounds the per-job replay buffer of live events. A WS that connects
// or refreshes mid-build is sent this history first so the console shows what
// already happened instead of a blank "waiting…" state.
const historyCap = 3000

// manager is the concrete domain.JobManager. It is unexported; callers receive
// it through the domain.JobManager interface from NewManager.
type manager struct {
	store    domain.Store
	runner   domain.ClaudeRunner
	notifier domain.Notifier
	logger   *slog.Logger

	// projectsDir is the absolute root under which each project's working dir
	// (projectsDir/<project_id>) is materialized and git-pushed.
	projectsDir string
	// gitRemote is the push target for every build's branch. Empty -> push skipped.
	gitRemote string
	// githubOwner opts into the "one repo per project" model: when non-empty each
	// project gets its own repo "crn-<slug>-<id8>" under this owner and builds/edits
	// push to its "main"; when empty the legacy gitRemote+branch model is used.
	githubOwner string
	// repoPrivate makes per-project repos private (owner-model only).
	repoPrivate bool
	// runClaude toggles invoking Claude Code after files are materialized.
	runClaude bool

	mu   sync.Mutex
	subs map[uuid.UUID][]*subscriber          // jobID -> live listeners
	hist map[uuid.UUID][]domain.BuildEventMsg // jobID -> capped replay buffer
}

// subscriber is one live WebSocket listener for a job's normalized events.
type subscriber struct {
	ch     chan domain.BuildEventMsg
	once   sync.Once // guards close(ch) so unsubscribe is idempotent
	closed bool      // guarded by manager.mu
}

// compile-time guard.
var _ domain.JobManager = (*manager)(nil)

// NewManager returns a domain.JobManager composed of the store, Claude runner,
// and notifier. projectsDir is the absolute root for per-project working dirs;
// gitRemote is the push target for the legacy shared-remote model (empty skips
// the push); githubOwner (when non-empty) opts into the "one repo per project"
// model with repoPrivate controlling repo visibility; runClaude toggles the
// Claude Code step. Signature is kept in lockstep with cmd/server/main.go.
func NewManager(
	store domain.Store,
	runner domain.ClaudeRunner,
	notifier domain.Notifier,
	logger *slog.Logger,
	projectsDir string,
	gitRemote string,
	githubOwner string,
	repoPrivate bool,
	runClaude bool,
) domain.JobManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &manager{
		store:       store,
		runner:      runner,
		notifier:    notifier,
		logger:      logger.With("component", "jobs"),
		projectsDir: projectsDir,
		gitRemote:   gitRemote,
		githubOwner: githubOwner,
		repoPrivate: repoPrivate,
		hist:        make(map[uuid.UUID][]domain.BuildEventMsg),
		runClaude:   runClaude,
		subs:        make(map[uuid.UUID][]*subscriber),
	}
}

// Enqueue records a new job (status=queued) and, if the org is idle, kicks off
// processing asynchronously. Returns the created job (with build_no).
func (m *manager) Enqueue(ctx context.Context, projectID, orgID uuid.UUID, payload []byte) (*domain.Job, error) {
	buildNo, err := m.store.BumpBuildNo(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("jobs: bump build no: %w", err)
	}

	job := &domain.Job{
		ID:        uuid.New(),
		ProjectID: projectID,
		OrgID:     orgID,
		Status:    domain.JobQueued,
		BuildNo:   buildNo,
		Payload:   json.RawMessage(payload),
		QueuedAt:  time.Now().UTC(),
	}
	if err := m.store.CreateJob(ctx, job); err != nil {
		return nil, fmt.Errorf("jobs: create job: %w", err)
	}

	m.logger.Info("job enqueued",
		"job_id", job.ID, "project_id", projectID, "org_id", orgID, "build_no", buildNo)

	// Kick off processing in the background. We do NOT inherit the request ctx
	// (it is cancelled when the HTTP handler returns); a build outlives the
	// enqueue call.
	go m.processOrg(context.Background(), orgID)

	return job, nil
}

// HandleTrigger is invoked when FTC DV signals a queued job exists. It attempts
// to start the next queued job for the org, respecting the per-org lock. The
// trigger is only a notification; authoritative state is re-read from the store.
func (m *manager) HandleTrigger(ctx context.Context, t domain.TriggerRequest) error {
	m.logger.Info("trigger received",
		"job_id", t.JobID, "project_id", t.ProjectID, "org_id", t.OrgID)
	// Process in the background so the trigger HTTP call returns immediately and
	// the build is not bound to the request ctx.
	go m.processOrg(context.Background(), t.OrgID)
	return nil
}

// Cancel requests cancellation of a queued or building job.
//
// TODO(crn): cancelling a *building* job must also signal the in-flight
// runner.Run goroutine (cancel its ctx) so the spawned `claude`/docker
// processes are killed. For now we only flip the persisted status; a build
// already running will still finish and overwrite this on completion. We
// pre-validate the transition to return domain.ErrInvalidTransition cleanly
// instead of relying on the store's CHECK constraint to surface it.
func (m *manager) Cancel(ctx context.Context, jobID uuid.UUID) error {
	job, err := m.store.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("jobs: cancel: load job: %w", err)
	}
	if job.Status.IsTerminal() {
		return fmt.Errorf("jobs: cancel %s: %w", job.Status, domain.ErrInvalidTransition)
	}
	if err := m.store.UpdateJobStatus(ctx, jobID, domain.JobCancelled, ""); err != nil {
		return fmt.Errorf("jobs: cancel: update status: %w", err)
	}
	m.logger.Info("job cancelled", "job_id", jobID, "from", job.Status)
	m.closeSubscribers(jobID)
	return nil
}

// Subscribe registers a live listener for normalized build events of a job. The
// returned channel is closed when the job finishes (via closeSubscribers) or
// when the returned unsubscribe func is called. Unsubscribe is idempotent.
func (m *manager) Subscribe(ctx context.Context, jobID uuid.UUID) ([]domain.BuildEventMsg, <-chan domain.BuildEventMsg, func()) {
	sub := &subscriber{ch: make(chan domain.BuildEventMsg, subscriberBuffer)}

	m.mu.Lock()
	// Snapshot the replay buffer and register the subscriber atomically so no
	// event is missed or duplicated between the snapshot and going live.
	history := append([]domain.BuildEventMsg(nil), m.hist[jobID]...)
	m.subs[jobID] = append(m.subs[jobID], sub)
	m.mu.Unlock()

	unsubscribe := func() { m.removeSubscriber(jobID, sub) }

	// Auto-unsubscribe when the caller's context is done (e.g. the WS
	// disconnects) so we never leak subscriber slots.
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()

	return history, sub.ch, unsubscribe
}

// Status returns the read model for a project's current/last job.
func (m *manager) Status(ctx context.Context, projectID uuid.UUID) (*domain.ProjectStatusView, error) {
	proj, err := m.store.GetProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("jobs: status: load project: %w", err)
	}

	depth, err := m.store.QueueDepth(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("jobs: status: queue depth: %w", err)
	}

	view := &domain.ProjectStatusView{
		CurrentBuild: proj.CurrentBuild,
		QueueDepth:   depth,
	}

	// Surface the next queued job's status/session/tag for this project when one
	// exists. TODO(crn): the store does not expose a "latest job for project"
	// read, so an idle project (no queued job) reports an empty status. Add
	// store.LatestJobByProject and prefer it here over NextQueuedJob.
	next, err := m.store.NextQueuedJob(ctx, proj.OrgID)
	if err != nil {
		return nil, fmt.Errorf("jobs: status: next queued: %w", err)
	}
	if next != nil && next.ProjectID == projectID {
		view.Status = next.Status
		view.SessionID = next.SessionID
		view.DockerTag = next.DockerTag
	}

	return view, nil
}

// --- internal: org processing loop -----------------------------------------

// processOrg drains the org's queue under the per-org advisory lock. It is safe
// to call concurrently: only one goroutine can hold the lock, the rest observe
// domain.ErrOrgLocked and return, leaving their jobs queued for the holder (or
// a later trigger) to pick up.
func (m *manager) processOrg(ctx context.Context, orgID uuid.UUID) {
	release, err := m.store.AcquireOrgLock(ctx, orgID)
	if err != nil {
		if errors.Is(err, domain.ErrOrgLocked) {
			// Another goroutine owns this org's build slot; it will chain to the
			// jobs we leave queued. Nothing to do.
			m.logger.Debug("org busy, leaving queued", "org_id", orgID)
			return
		}
		m.logger.Error("acquire org lock failed", "org_id", orgID, "err", err)
		return
	}
	defer release()

	// Chain: process queued jobs one at a time until the queue drains. We never
	// interrupt a running build; each iteration runs a job to a terminal state
	// before pulling the next.
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := m.store.NextQueuedJob(ctx, orgID)
		if err != nil {
			m.logger.Error("next queued job failed", "org_id", orgID, "err", err)
			return
		}
		if job == nil {
			return // queue drained
		}
		m.runJob(ctx, job)
	}
}

// runJob drives a single job from queued -> building -> done/failed, fanning
// Claude events out to live subscribers and notifying the central DB at the
// lifecycle boundaries. It assumes the org lock is held by the caller.
func (m *manager) runJob(ctx context.Context, job *domain.Job) {
	log := m.logger.With("job_id", job.ID, "project_id", job.ProjectID, "org_id", job.OrgID, "build_no", job.BuildNo)

	// --- building ---
	if err := m.store.UpdateJobStatus(ctx, job.ID, domain.JobBuilding, ""); err != nil {
		log.Error("mark building failed", "err", err)
		return
	}
	job.Status = domain.JobBuilding
	m.notify(ctx, job.ID, domain.EventBuildStarted, nil)
	m.publish(job.ID, domain.BuildEventMsg{
		Kind:      domain.WSBuildPhase,
		Phase:     string(domain.JobBuilding),
		JobID:     job.ID.String(),
		Timestamp: time.Now().UTC(),
	})

	// The absolute per-project working dir: every file write, Claude run, and
	// git push targets the same directory.
	workDir := filepath.Join(m.projectsDir, job.ProjectID.String())

	spec := parsePayload(job.Payload)

	// --- resolve the git model for this build ---------------------------------
	// SHARED (githubOwner == ""): push a "crn/<slug>-<id8>" branch to gitRemote —
	//   the legacy behavior, unchanged.
	// PER-PROJECT (githubOwner != ""): ensure the project's own repo
	//   "crn-<slug>-<id8>" exists under the owner and push its "main" branch.
	// pushRemote/pushBranch are the values the git phase (and edit clone/pull) use;
	// repoSlug is set in the owner model so a successful issue-driven edit can
	// comment on the issue.
	slug := slugify(spec.Name)
	id8 := job.ProjectID.String()[:8]
	ownerMode := m.githubOwner != ""

	var pushRemote, pushBranch, repoSlug string
	if ownerMode {
		repoName := "crn-" + slug + "-" + id8
		repoSlug = m.githubOwner + "/" + repoName
		m.publishPhase(job.ID, "repo")
		repoURL, err := github.EnsureRepo(ctx, m.githubOwner, repoName, m.repoPrivate, m.logger)
		if err != nil {
			log.Error("ensure github repo failed", "err", err, "repo", repoSlug)
			m.finishFailed(ctx, job, "ensure github repo failed: "+err.Error(), 0)
			return
		}
		// Record the repo URL on the project once (idempotent SetProjectRepo).
		if err := m.store.SetProjectRepo(ctx, job.ProjectID, repoURL); err != nil {
			log.Warn("persist project repo url failed", "err", err, "repo_url", repoURL)
		}
		pushRemote = repoURL
		pushBranch = "main"
	} else {
		// The build branch name derived from the product name + a short project id,
		// e.g. "crn/expenseflow-70d7409b". Both build modes push to this branch.
		pushRemote = m.gitRemote
		pushBranch = "crn/" + slug + "-" + id8
	}

	// resumeSession is the Claude session to --resume for this run; the Claude
	// prompt is the requirement handed to Claude. Both are set per-mode below.
	var resumeSession, claudePrompt string

	if spec.Mode == "edit" {
		// --- EDIT mode: clone/pull the existing branch, do NOT reset/materialize ---
		if pushRemote == "" {
			log.Error("edit build requires a git remote (set CRN_GIT_REMOTE or CRN_GITHUB_OWNER)")
			m.finishFailed(ctx, job, "edit build requires a git remote (set CRN_GIT_REMOTE or CRN_GITHUB_OWNER)", 0)
			return
		}
		m.publishPhase(job.ID, "pull")
		if err := buildstep.GitCloneOrPull(ctx, workDir, pushRemote, pushBranch, m.logger); err != nil {
			log.Error("edit clone/pull failed", "err", err, "branch", pushBranch)
			m.finishFailed(ctx, job, "edit clone/pull failed: "+err.Error(), 0)
			return
		}
		log.Info("edit workspace ready", "workdir", workDir, "branch", pushBranch)

		claudePrompt = "You are editing an existing project in the current directory. " +
			"Apply this change and keep everything else working:\n\n" + spec.Change

		// Resume the project's last Claude session so the edit has full context.
		sid, err := m.store.LastSessionID(ctx, job.ProjectID)
		if err != nil {
			log.Warn("look up last session id failed", "err", err)
		}
		resumeSession = sid
	} else {
		// --- NORMAL mode: reset the workspace + materialize the export zip ---
		// Reset the workspace so each build starts from a clean slate. Every build
		// ships a full export, so stale artifacts from a prior build (old zips,
		// extracted source, a previous .git) must not leak in — two leftover zips
		// would make the harness ambiguous about which to extract.
		if err := os.RemoveAll(workDir); err != nil {
			log.Error("reset workspace failed", "err", err, "workdir", workDir)
			m.finishFailed(ctx, job, "reset workspace failed: "+err.Error(), 0)
			return
		}

		// --- materialize: write the incoming payload files to disk ---
		m.publishPhase(job.ID, "materialize")
		if err := buildstep.WriteFiles(workDir, spec.Files); err != nil {
			log.Error("materialize files failed", "err", err)
			m.finishFailed(ctx, job, "materialize files failed: "+err.Error(), 0)
			return
		}
		log.Info("materialized files", "count", len(spec.Files), "workdir", workDir)

		claudePrompt = harnessPrompt(spec)
		resumeSession = job.SessionID
	}

	// --- run Claude Code (optional) ---
	var result domain.RunResult
	if m.runClaude {
		// Inject every enabled skill into {workDir}/.claude/skills/{name}/SKILL.md
		// so the spawned `claude` discovers the fitt-build harness. The injected
		// .claude dir is removed before the git phase so it is never committed.
		if err := m.injectSkills(ctx, workDir); err != nil {
			log.Error("inject skills failed", "err", err)
			m.finishFailed(ctx, job, "inject skills failed: "+err.Error(), 0)
			return
		}

		runSpec := domain.RunSpec{
			JobID:           job.ID,
			ProjectID:       job.ProjectID,
			WorkDir:         workDir,
			Prompt:          claudePrompt,
			ResumeSessionID: resumeSession,
		}

		emit := func(ev domain.ClaudeEvent) error {
			// Persist the session id as soon as it appears so a future build can
			// --resume even if this run later fails.
			if ev.SessionID != "" && ev.SessionID != job.SessionID {
				job.SessionID = ev.SessionID
				if err := m.store.SetJobSession(ctx, job.ID, ev.SessionID); err != nil {
					log.Warn("persist session id failed", "err", err)
				}
			}
			msg := translate(job.ID, ev)
			// Skip empty assistant chatter (e.g. an assistant turn that only carried
			// a tool_use, already surfaced above as a tool call) so the live console
			// isn't full of blank "text" rows.
			if msg.Kind == domain.WSAssistantText && strings.TrimSpace(msg.Text) == "" {
				return nil
			}
			m.publish(job.ID, msg)
			return nil
		}

		var runErr error
		result, runErr = m.runner.Run(ctx, runSpec, emit)

		// Remove the injected .claude dir (success OR fail) BEFORE the git phase so
		// the harness skills are never committed/pushed.
		if rmErr := os.RemoveAll(filepath.Join(workDir, ".claude")); rmErr != nil {
			log.Warn("remove injected .claude failed", "err", rmErr)
		}

		// Persist the terminal session id from the result line if not already stored.
		if result.SessionID != "" && result.SessionID != job.SessionID {
			job.SessionID = result.SessionID
			if err := m.store.SetJobSession(ctx, job.ID, result.SessionID); err != nil {
				log.Warn("persist terminal session id failed", "err", err)
			}
		}

		// --- terminal: failed ---
		if runErr != nil || !result.Success {
			errMsg := "claude run reported failure"
			if runErr != nil {
				errMsg = runErr.Error()
			}
			log.Error("build failed", "err", errMsg, "cost_usd", result.CostUSD)
			m.finishFailed(ctx, job, errMsg, result.CostUSD)
			return
		}
	} else {
		log.Info("claude step skipped (CRN_RUN_CLAUDE=false)")
		m.publishPhase(job.ID, "claude_skipped")
	}

	// --- success: git commit + force-push the build branch ---
	// pushRemote/pushBranch were resolved above per git model: the owner model
	// pushes the project's own repo "main"; the shared model pushes a
	// "crn/<slug>-<id8>" branch to CRN_GIT_REMOTE.
	if pushRemote != "" {
		m.publishPhase(job.ID, "git")
		m.publishPhase(job.ID, "push")
		msg := commitMessage(spec, job)
		commit, err := buildstep.GitCommitAndPush(ctx, workDir, pushRemote, pushBranch, msg, m.logger)
		if err != nil {
			log.Error("git push failed", "err", err)
			m.finishFailed(ctx, job, "git push failed: "+err.Error(), result.CostUSD)
			return
		}
		log.Info("git pushed", "branch", pushBranch, "commit", commit, "remote", pushRemote)
		// Reuse docker_tag to record the produced branch (no docker pipeline yet).
		if err := m.store.SetJobDockerTag(ctx, job.ID, "branch:"+pushBranch); err != nil {
			log.Warn("persist branch tag failed", "err", err)
		}
		job.DockerTag = "branch:" + pushBranch

		// Owner model: an edit driven by a GitHub issue comments the commit back on
		// the issue (best-effort — never fails the build).
		if ownerMode && spec.Mode == "edit" && spec.IssueNumber > 0 && repoSlug != "" {
			body := "Fixed by CRN in " + commit
			if err := github.CommentIssue(ctx, repoSlug, spec.IssueNumber, body, m.logger); err != nil {
				log.Warn("comment issue failed", "err", err, "issue", spec.IssueNumber)
			}
		}
	} else {
		log.Info("git push skipped (no CRN_GIT_REMOTE / CRN_GITHUB_OWNER)", "branch", pushBranch)
		m.publishPhase(job.ID, "push_skipped")
	}

	if err := m.store.UpdateJobStatus(ctx, job.ID, domain.JobDone, ""); err != nil {
		log.Error("mark done failed", "err", err)
		// Best-effort failure path so subscribers are not left hanging.
		m.finishFailed(ctx, job, "persist done status failed: "+err.Error(), result.CostUSD)
		return
	}
	job.Status = domain.JobDone
	log.Info("build done", "cost_usd", result.CostUSD, "session_id", result.SessionID, "branch", job.DockerTag)

	m.publish(job.ID, domain.BuildEventMsg{
		Kind:      domain.WSResult,
		Phase:     string(domain.JobDone),
		CostUSD:   result.CostUSD,
		SessionID: result.SessionID,
		File:      job.DockerTag, // carries "branch:crn/<project_id>" for the dashboard
		JobID:     job.ID.String(),
		Timestamp: time.Now().UTC(),
	})
	m.notify(ctx, job.ID, domain.EventBuildDone, donePayload(result))
	m.closeSubscribers(job.ID)
}

// publishPhase fans a lifecycle phase marker out to live subscribers.
func (m *manager) publishPhase(jobID uuid.UUID, phase string) {
	m.publish(jobID, domain.BuildEventMsg{
		Kind:      domain.WSBuildPhase,
		Phase:     phase,
		JobID:     jobID.String(),
		Timestamp: time.Now().UTC(),
	})
}

// finishFailed records the failure, notifies the central DB, pushes a terminal
// error to live subscribers, and closes their channels.
func (m *manager) finishFailed(ctx context.Context, job *domain.Job, errMsg string, costUSD float64) {
	if err := m.store.UpdateJobStatus(ctx, job.ID, domain.JobFailed, errMsg); err != nil {
		m.logger.Error("mark failed failed", "job_id", job.ID, "err", err)
	}
	job.Status = domain.JobFailed

	m.publish(job.ID, domain.BuildEventMsg{
		Kind:      domain.WSError,
		Phase:     string(domain.JobFailed),
		Text:      errMsg,
		CostUSD:   costUSD,
		JobID:     job.ID.String(),
		Timestamp: time.Now().UTC(),
	})
	m.notify(ctx, job.ID, domain.EventBuildFailed, failedPayload(errMsg))
	m.closeSubscribers(job.ID)
}

// notify appends a build_event to the central DB; failures are logged, never
// fatal — the build outcome is already persisted in the jobs table.
func (m *manager) notify(ctx context.Context, jobID uuid.UUID, kind domain.BuildEventType, payload json.RawMessage) {
	ev := &domain.BuildEvent{
		ID:        uuid.New(),
		JobID:     jobID,
		EventType: kind,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
	if err := m.notifier.Notify(ctx, ev); err != nil {
		m.logger.Error("notify failed", "job_id", jobID, "event_type", kind, "err", err)
	}
}

// --- internal: live subscriber fan-out -------------------------------------

// publish delivers msg to every live subscriber of jobID. A subscriber whose
// buffer is full is skipped (best-effort live telemetry; the build is never
// blocked by a slow WebSocket client).
func (m *manager) publish(jobID uuid.UUID, msg domain.BuildEventMsg) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Buffer for replay so a WS connecting/refreshing mid-build sees history.
	h := append(m.hist[jobID], msg)
	if len(h) > historyCap {
		h = append([]domain.BuildEventMsg(nil), h[len(h)-historyCap:]...)
	}
	m.hist[jobID] = h
	for _, sub := range m.subs[jobID] {
		if sub.closed {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
			m.logger.Warn("dropping event for slow subscriber", "job_id", jobID, "kind", msg.Kind)
		}
	}
}

// closeSubscribers closes every subscriber channel for jobID and forgets them.
// Called once a job reaches a terminal state.
func (m *manager) closeSubscribers(jobID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.subs[jobID] {
		sub.closeLocked()
	}
	delete(m.subs, jobID)
	delete(m.hist, jobID)
}

// removeSubscriber drops a single subscriber (explicit unsubscribe / ctx done)
// and closes its channel. Idempotent.
func (m *manager) removeSubscriber(jobID uuid.UUID, target *subscriber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.subs[jobID]
	for i, sub := range list {
		if sub == target {
			sub.closeLocked()
			m.subs[jobID] = append(list[:i], list[i+1:]...)
			if len(m.subs[jobID]) == 0 {
				delete(m.subs, jobID)
			}
			return
		}
	}
}

// closeLocked closes the subscriber's channel exactly once. Caller holds
// manager.mu.
func (s *subscriber) closeLocked() {
	s.once.Do(func() {
		s.closed = true
		close(s.ch)
	})
}

// --- internal: skill injection + harness prompt ----------------------------

// injectSkills writes every enabled skill into {workDir}/.claude/skills so the
// spawned `claude` discovers the fitt-build harness. Each skill's SKILL.md (its
// Body) plus every entry in its Files map (scripts/, references/, ...) is
// written under {workDir}/.claude/skills/{name}/. It is a no-op (no error) when
// no skills are enabled. The caller removes {workDir}/.claude before the git
// phase so the harness is never committed.
func (m *manager) injectSkills(ctx context.Context, workDir string) error {
	skills, err := m.store.ListSkills(ctx)
	if err != nil {
		return fmt.Errorf("list skills: %w", err)
	}
	var files []buildstep.SkillFile
	for _, sk := range skills {
		if !sk.Enabled {
			continue
		}
		files = append(files, buildstep.SkillFile{Name: sk.Name, Body: sk.Body, Files: sk.Files})
	}
	if len(files) == 0 {
		return nil
	}
	if err := buildstep.InjectSkills(workDir, files); err != nil {
		return err
	}
	m.logger.Info("injected skills", "count", len(files), "workdir", workDir)
	return nil
}

// harnessPrompt composes the prompt that invokes the fitt-build skill on the
// FITT Builder export, embedding the briefs. When no brief is present it falls
// back to the parsed legacy/edit-request prompt so the run is never empty.
func harnessPrompt(spec payloadSpec) string {
	if spec.Idea == "" && spec.BRD == "" && spec.PRD == "" {
		return spec.Prompt
	}
	return fmt.Sprintf(
		"You are in a fresh project directory containing a FITT Builder export "+
			"(a zip). Use the fitt-build skill to extract it and set it up as a "+
			"clean, runnable project that reproduces the prototype. Product briefs "+
			"follow:\n\nIDEA:\n%s\n\nBRD:\n%s\n\nPRD:\n%s",
		spec.Idea, spec.BRD, spec.PRD,
	)
}

// commitMessage builds the git commit message for a build. An edit build reads
// "crn: edit build {n} — {first line of change}"; a normal build reads
// "crn: build {n} — {name}" (falling back to the project id when unnamed).
func commitMessage(spec payloadSpec, job *domain.Job) string {
	if spec.Mode == "edit" {
		return fmt.Sprintf("crn: edit build %d — %s", job.BuildNo, firstLine(spec.Change))
	}
	if spec.Name == "" {
		return fmt.Sprintf("crn: build %d — %s", job.BuildNo, job.ProjectID)
	}
	return fmt.Sprintf("crn: build %d — %s", job.BuildNo, spec.Name)
}

// firstLine returns the first non-empty line of s, trimmed. Empty input yields
// "edit" so the commit subject is never blank.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return "edit"
}

// slugify turns a product name into a git-branch-safe slug: lowercase, keeping
// only [a-z0-9]; every other run of characters collapses to a single '-';
// leading/trailing '-' are trimmed and the result is capped at ~40 chars. A
// name that yields nothing (e.g. Thai-only text) falls back to "project" so the
// branch is never empty.
func slugify(s string) string {
	const maxLen = 40
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxLen {
		slug = strings.Trim(slug[:maxLen], "-")
	}
	if slug == "" {
		return "project"
	}
	return slug
}

// --- internal: payload + event translation helpers -------------------------

// payloadSpec is the parsed view of a job payload the build needs: the files to
// materialize, the briefs (idea/brd/prd) the harness prompt embeds, and the
// fallback prompt used when Claude is run without the skill harness (legacy /
// edit-request shapes).
type payloadSpec struct {
	Files  []buildstep.FileEntry
	Name   string
	Idea   string
	BRD    string
	PRD    string
	Prompt string

	// Mode selects the build path: "edit" edits the project's existing repo in
	// place (clone/pull the branch, run Claude with Change, push the same branch);
	// "" (default) is the normal reset -> materialize -> harness path.
	Mode string
	// Change is the edit instruction for an edit build (Mode == "edit").
	Change string
	// IssueNumber, when > 0, is the GitHub issue that drove this edit build. After
	// a successful edit push (owner model) the build comments the commit on it.
	IssueNumber int
}

// ingestPayload mirrors the FBD -> CRN ingest body (api.ingestBody). Only the
// fields the build consumes are decoded; unknown fields are ignored here (the
// strict decode happens at the API boundary). The prototype arrives as one
// base64 zip, not a file array.
type ingestPayload struct {
	Name      string   `json:"name"`
	Idea      string   `json:"idea"`
	BRD       string   `json:"brd"`
	PRD       string   `json:"prd"`
	Prompts   []string `json:"prompts"`
	ZipName   string   `json:"zip_name"`
	ZipBase64 string   `json:"zip_base64"`

	// Edit build fields. When Mode == "edit" the payload carries a Change
	// instruction instead of a prototype zip; the existing repo is edited in place.
	Mode   string `json:"mode"`
	Change string `json:"change"`
	// IssueNumber, when > 0, is the GitHub issue that drove this edit build (the
	// /issues/{number}/fix flow). The build comments on it after a successful push.
	IssueNumber int `json:"issue_number"`
}

// parsePayload materializes the prototype zip and composes the Claude prompt
// from idea/brd/prd/prompts. The zip is written into the workdir as a single
// file; the build agent extracts it itself (see the "# Source" prompt section).
// When no brief/prompt is present it falls back to promptFromPayload (raw JSON /
// legacy "prompt"/"change" shapes).
func parsePayload(payload json.RawMessage) payloadSpec {
	var p ingestPayload
	_ = json.Unmarshal(payload, &p) // best-effort; an unknown shape yields zero values

	var spec payloadSpec
	spec.Name = p.Name
	spec.Idea = p.Idea
	spec.BRD = p.BRD
	spec.PRD = p.PRD

	// Edit build: no zip to materialize and no harness prompt — the repo already
	// exists on disk (cloned/pulled by runJob) and Change is the edit instruction.
	if p.Mode == "edit" {
		spec.Mode = "edit"
		spec.Change = p.Change
		spec.Prompt = p.Change
		spec.IssueNumber = p.IssueNumber
		return spec
	}

	// Drop the prototype zip into the workdir as one file. Decoded zip bytes go
	// through FileEntry.Content (a Go string) byte-for-byte — WriteFiles does
	// []byte(content), so binary survives intact.
	zipName := p.ZipName
	if zipName == "" {
		zipName = "_fitt_export.zip"
	}
	if p.ZipBase64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(p.ZipBase64); err == nil {
			spec.Files = []buildstep.FileEntry{{Path: zipName, Content: string(raw)}}
		} else {
			zipName = "" // bad base64 → nothing materialized; don't ask the agent to extract
		}
	} else {
		zipName = ""
	}

	var b strings.Builder
	if p.Idea != "" {
		b.WriteString("# IDEA\n")
		b.WriteString(p.Idea)
		b.WriteString("\n\n")
	}
	if p.BRD != "" {
		b.WriteString("# BRD\n")
		b.WriteString(p.BRD)
		b.WriteString("\n\n")
	}
	if p.PRD != "" {
		b.WriteString("# PRD\n")
		b.WriteString(p.PRD)
		b.WriteString("\n\n")
	}
	for _, m := range p.Prompts {
		if m == "" {
			continue
		}
		b.WriteString("# Requirement\n")
		b.WriteString(m)
		b.WriteString("\n\n")
	}
	if zipName != "" {
		b.WriteString("# Source\n")
		b.WriteString(fmt.Sprintf(
			"ไฟล์ `%s` ใน working directory คือ zip ของ prototype เดิม (Vite SPA + เอกสารใน docs/). "+
				"ขั้นแรกให้แตกไฟล์ออกมาก่อน (เช่น `unzip -o %s` หรือเครื่องมือที่มีในระบบ) แล้วลบ zip ทิ้ง "+
				"จากนั้นต่อยอดเป็น production app จาก source ที่แตกออกมา\n\n",
			zipName, zipName,
		))
	}

	spec.Prompt = strings.TrimSpace(b.String())
	if spec.Prompt == "" {
		spec.Prompt = promptFromPayload(payload)
	}
	return spec
}

// promptFromPayload extracts the change description handed to Claude Code from a
// job's JSON payload. The payload shape is { "prompt": "...", ... } (or the
// edit-request "change" field); unknown shapes fall back to the raw JSON so the
// run is never empty.
func promptFromPayload(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var p struct {
		Prompt string `json:"prompt"`
		Change string `json:"change"`
	}
	if err := json.Unmarshal(payload, &p); err == nil {
		if p.Prompt != "" {
			return p.Prompt
		}
		if p.Change != "" {
			return p.Change
		}
	}
	// TODO(crn): formalize the payload contract with the api / edit-request
	// layer; for now pass the raw JSON through so Claude has the full requirement.
	return string(payload)
}

// translate normalizes one raw Claude stream-json event into the GUI-facing
// BuildEventMsg the dashboard consumes over the WebSocket.
func translate(jobID uuid.UUID, ev domain.ClaudeEvent) domain.BuildEventMsg {
	msg := domain.BuildEventMsg{
		JobID:     jobID.String(),
		Timestamp: time.Now().UTC(),
	}
	switch ev.Type {
	case domain.ClaudeAssistant:
		// stream-json nests tool_use blocks inside assistant messages; decodeLine
		// surfaces the tool on the same event. Show it as a tool call so the live
		// console reads as Claude's actions, not empty "text" rows.
		if ev.ToolName != "" {
			msg.Kind = domain.WSToolCall
			msg.Tool = ev.ToolName
			msg.File = detailFromToolInput(ev.ToolInput)
		} else {
			msg.Kind = domain.WSAssistantText
			msg.Text = ev.Text
		}
	case domain.ClaudeToolUse:
		msg.Kind = domain.WSToolCall
		msg.Tool = ev.ToolName
		msg.File = detailFromToolInput(ev.ToolInput)
	case domain.ClaudeToolResult:
		msg.Kind = domain.WSToolResult
		msg.Tool = ev.ToolName
	case domain.ClaudeResult:
		msg.Kind = domain.WSResult
		msg.CostUSD = ev.CostUSD
		msg.SessionID = ev.SessionID
	default: // ClaudeSystem and any unknown type
		msg.Kind = domain.WSAssistantText
		msg.Text = ev.Text
	}
	return msg
}

// detailFromToolInput best-effort extracts a human-meaningful detail from a
// tool_use input so the live console reads like Claude's actions: the file path
// (Read/Edit/Write), the command (Bash), the pattern (Grep/Glob), or a URL.
func detailFromToolInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Command  string `json:"command"`
		Pattern  string `json:"pattern"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	switch {
	case in.FilePath != "":
		return in.FilePath
	case in.Path != "":
		return in.Path
	case in.Command != "":
		return truncateOneLine(in.Command, 100)
	case in.Pattern != "":
		return in.Pattern
	case in.URL != "":
		return in.URL
	}
	return ""
}

// truncateOneLine collapses a value to a single line and caps its length so a
// long command/pattern stays a compact one-row entry in the live console.
func truncateOneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// donePayload builds the build_done event payload (cost + session) for the
// central DB.
func donePayload(r domain.RunResult) json.RawMessage {
	b, err := json.Marshal(struct {
		CostUSD   float64 `json:"cost_usd"`
		SessionID string  `json:"session_id,omitempty"`
	}{CostUSD: r.CostUSD, SessionID: r.SessionID})
	if err != nil {
		return nil
	}
	return b
}

// failedPayload builds the build_failed event payload (error message) for the
// central DB.
func failedPayload(errMsg string) json.RawMessage {
	b, err := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: errMsg})
	if err != nil {
		return nil
	}
	return b
}
