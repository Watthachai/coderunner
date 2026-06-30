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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// subscriberBuffer bounds each live WebSocket subscriber's channel. A slow
// consumer that fills its buffer drops events rather than stalling the build —
// the authoritative record is build_events in the central DB; the WS stream is
// best-effort live telemetry.
const subscriberBuffer = 64

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
	// runClaude toggles invoking Claude Code after files are materialized.
	runClaude bool

	mu   sync.Mutex
	subs map[uuid.UUID][]*subscriber // jobID -> live listeners
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
// gitRemote is the push target (empty skips the push); runClaude toggles the
// Claude Code step. Signature is kept in lockstep with cmd/server/main.go.
func NewManager(
	store domain.Store,
	runner domain.ClaudeRunner,
	notifier domain.Notifier,
	logger *slog.Logger,
	projectsDir string,
	gitRemote string,
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
func (m *manager) Subscribe(ctx context.Context, jobID uuid.UUID) (<-chan domain.BuildEventMsg, func()) {
	sub := &subscriber{ch: make(chan domain.BuildEventMsg, subscriberBuffer)}

	m.mu.Lock()
	m.subs[jobID] = append(m.subs[jobID], sub)
	m.mu.Unlock()

	unsubscribe := func() { m.removeSubscriber(jobID, sub) }

	// Auto-unsubscribe when the caller's context is done (e.g. the WS
	// disconnects) so we never leak subscriber slots.
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()

	return sub.ch, unsubscribe
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

	// --- materialize: write the incoming payload files to disk ---
	spec := parsePayload(job.Payload)
	m.publishPhase(job.ID, "materialize")
	if err := buildstep.WriteFiles(workDir, spec.Files); err != nil {
		log.Error("materialize files failed", "err", err)
		m.finishFailed(ctx, job, "materialize files failed: "+err.Error(), 0)
		return
	}
	log.Info("materialized files", "count", len(spec.Files), "workdir", workDir)

	// --- run Claude Code (optional) ---
	var result domain.RunResult
	if m.runClaude {
		runSpec := domain.RunSpec{
			JobID:           job.ID,
			ProjectID:       job.ProjectID,
			WorkDir:         workDir,
			Prompt:          spec.Prompt,
			ResumeSessionID: job.SessionID,
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
			m.publish(job.ID, translate(job.ID, ev))
			return nil
		}

		var runErr error
		result, runErr = m.runner.Run(ctx, runSpec, emit)

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
	branch := "crn/" + job.ProjectID.String()
	if m.gitRemote != "" {
		m.publishPhase(job.ID, "git")
		m.publishPhase(job.ID, "push")
		msg := fmt.Sprintf("crn: build %d for project %s", job.BuildNo, job.ProjectID)
		commit, err := buildstep.GitCommitAndPush(ctx, workDir, m.gitRemote, branch, msg, m.logger)
		if err != nil {
			log.Error("git push failed", "err", err)
			m.finishFailed(ctx, job, "git push failed: "+err.Error(), result.CostUSD)
			return
		}
		log.Info("git pushed", "branch", branch, "commit", commit, "remote", m.gitRemote)
		// Reuse docker_tag to record the produced branch (no docker pipeline yet).
		if err := m.store.SetJobDockerTag(ctx, job.ID, "branch:"+branch); err != nil {
			log.Warn("persist branch tag failed", "err", err)
		}
		job.DockerTag = "branch:" + branch
	} else {
		log.Info("git push skipped (CRN_GIT_REMOTE empty)", "branch", branch)
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

// --- internal: payload + event translation helpers -------------------------

// payloadSpec is the parsed view of a job payload the build needs: the files to
// materialize and the prompt to (optionally) hand to Claude Code.
type payloadSpec struct {
	Files  []buildstep.FileEntry
	Prompt string
}

// ingestPayload mirrors the FBD -> CRN ingest body (api.ingestBody). Only the
// fields the build consumes are decoded; unknown fields are ignored here (the
// strict decode happens at the API boundary).
type ingestPayload struct {
	BRD     string                `json:"brd"`
	PRD     string                `json:"prd"`
	Prompts []string              `json:"prompts"`
	Files   []buildstep.FileEntry `json:"files"`
}

// parsePayload extracts the files and a composed Claude prompt from a job's JSON
// payload. The prompt is built from brd/prd/prompts; when none are present it
// falls back to promptFromPayload (raw JSON / legacy "prompt"/"change" shapes).
func parsePayload(payload json.RawMessage) payloadSpec {
	var p ingestPayload
	_ = json.Unmarshal(payload, &p) // best-effort; an unknown shape yields zero values

	spec := payloadSpec{Files: p.Files}

	var b strings.Builder
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
		msg.Kind = domain.WSAssistantText
		msg.Text = ev.Text
	case domain.ClaudeToolUse:
		msg.Kind = domain.WSToolCall
		msg.Tool = ev.ToolName
		msg.File = fileFromToolInput(ev.ToolInput)
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

// fileFromToolInput best-effort extracts a file path from a tool_use input so
// the dashboard can show which file a tool touched. Returns "" when absent.
func fileFromToolInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.Path
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
