package jobs

import (
	"context"
	"io"
	"log/slog"
	"math"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// traceStore is a domain.Store that only answers what the terminal paths call.
// The embedded nil interface makes any other method panic, so the test fails
// loudly if those paths start touching the store in new ways.
type traceStore struct {
	domain.Store
	saved *domain.BuildTrace
}

func (s *traceStore) UpdateJobStatus(context.Context, uuid.UUID, domain.JobStatus, string) error {
	return nil
}

func (s *traceStore) SaveJobTrace(_ context.Context, tr *domain.BuildTrace) error {
	s.saved = tr
	return nil
}

// dropNotifier swallows the terminal build event; the trace, not the fan-out, is
// what these tests are about.
type dropNotifier struct{}

func (dropNotifier) Notify(context.Context, *domain.BuildEvent) error { return nil }

func traceManager(store *traceStore) *manager {
	return &manager{
		store:    store,
		notifier: dropNotifier{},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		hist:     map[uuid.UUID][]domain.BuildEventMsg{},
		subs:     map[uuid.UUID][]*subscriber{},
	}
}

// The regression this exists for: a build that pushed its commit and then died
// at the image step recorded an empty commit/branch/remote, so the console said
// "no commit" about code that was already on the remote. An operator read that
// as "the code never shipped" and nearly rebuilt from scratch.
func TestFailedBuildKeepsWhatItAlreadyAchieved(t *testing.T) {
	store := &traceStore{}
	m := traceManager(store)
	job := &domain.Job{ID: uuid.New(), ProjectID: uuid.New(), BuildNo: 7}

	m.finishFailed(context.Background(), job, "docker push (app) failed", traceMeta{
		mode:   "build",
		commit: "3f67520",
		branch: "main",
		remote: "https://github.com/acme/crn-demo.git",
		cost:   13.36,
		model:  "claude-opus-5-5",
		effort: "xhigh",
	})

	tr := store.saved
	if tr == nil {
		t.Fatal("a failed build saved no trace at all")
	}
	for _, c := range []struct{ field, got, want string }{
		{"commit", tr.CommitSHA, "3f67520"},
		{"branch", tr.Branch, "main"},
		{"remote", tr.Remote, "https://github.com/acme/crn-demo.git"},
		{"mode", tr.Mode, "build"},
		{"error", tr.ErrorMsg, "docker push (app) failed"},
		{"claude_model", tr.ClaudeModel, "claude-opus-5-5"},
		{"claude_effort", tr.ClaudeEffort, "xhigh"},
	} {
		if c.got != c.want {
			t.Errorf("trace lost %s: got %q, want %q", c.field, c.got, c.want)
		}
	}
	if tr.Outcome != string(domain.JobFailed) {
		t.Errorf("outcome = %q, want %q", tr.Outcome, domain.JobFailed)
	}
	if tr.CostUSD != 13.36 {
		t.Errorf("cost = %v, want 13.36", tr.CostUSD)
	}
}

// Cancelling is not failing, but it loses just as much context if the trace
// forgets where the build had got to.
func TestCancelledBuildKeepsWhatItAlreadyAchieved(t *testing.T) {
	store := &traceStore{}
	m := traceManager(store)
	job := &domain.Job{ID: uuid.New(), ProjectID: uuid.New(), BuildNo: 8}

	m.finishCancelled(context.Background(), job, traceMeta{
		mode:   "edit",
		commit: "abc1234",
		branch: "main",
		remote: "https://github.com/acme/crn-demo.git",
		cost:   4.20,
		model:  "claude-sonnet-5",
		effort: "high",
	})

	tr := store.saved
	if tr == nil {
		t.Fatal("a cancelled build saved no trace at all")
	}
	if tr.CommitSHA != "abc1234" || tr.Branch != "main" || tr.Mode != "edit" {
		t.Errorf("trace lost context: commit=%q branch=%q mode=%q", tr.CommitSHA, tr.Branch, tr.Mode)
	}
	if tr.Outcome != string(domain.JobCancelled) {
		t.Errorf("outcome = %q, want %q", tr.Outcome, domain.JobCancelled)
	}
	if tr.ErrorMsg == "" {
		t.Error("a cancelled trace should say it was cancelled")
	}
	if tr.ClaudeModel != "claude-sonnet-5" || tr.ClaudeEffort != "high" {
		t.Errorf("trace lost the model: %q/%q", tr.ClaudeModel, tr.ClaudeEffort)
	}
}

// The error message belongs to the failure, not to whatever the caller happened
// to put in meta — finishFailed must stamp it rather than trust the struct.
func TestFinishFailedStampsItsOwnError(t *testing.T) {
	store := &traceStore{}
	m := traceManager(store)
	job := &domain.Job{ID: uuid.New(), ProjectID: uuid.New(), BuildNo: 9}

	m.finishFailed(context.Background(), job, "the real reason", traceMeta{errMsg: "stale"})

	if store.saved.ErrorMsg != "the real reason" {
		t.Errorf("error = %q, want %q", store.saved.ErrorMsg, "the real reason")
	}
}

// Claude's total_cost_usd is the session's running total, and --resume carries
// it over, so a resumed run cost what the session spent beyond its earlier runs.
func TestRunCostTakesOffWhatTheSessionSpentBefore(t *testing.T) {
	this, earlier := uuid.New(), uuid.New()
	traces := []*domain.BuildTrace{
		{JobID: earlier, SessionID: "a786", CostUSD: 11.14},
		{JobID: uuid.New(), SessionID: "older-session", CostUSD: 7},
		{JobID: this, SessionID: "a786", CostUSD: 99}, // this run's own row, if one was saved early
	}
	if got := runCost(16.96, "a786", this, traces); math.Abs(got-5.82) > 1e-9 {
		t.Errorf("resumed run cost = %v, want 5.82", got)
	}
	if got := runCost(11.14, "", this, traces); got != 11.14 {
		t.Errorf("fresh run cost = %v, want its whole total 11.14", got)
	}
	// A total below what the session spent before means Claude started this run's
	// total from zero; it is then the run's own cost.
	if got := runCost(4, "a786", this, traces); got != 4 {
		t.Errorf("run whose total did not carry over = %v, want 4", got)
	}
}
