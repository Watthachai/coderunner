package feedback

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
	"github.com/Watthachai/fitt-coderunner/internal/github"
)

// FeedbackStore is the subset of domain.Store the watcher needs.
type FeedbackStore interface {
	ListFeedbackNeedingIssue(ctx context.Context, limit int) ([]*domain.FeedbackRequest, error)
	GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	SetFeedbackIssue(ctx context.Context, id uuid.UUID, number int, url string) error
}

// Issuer creates GitHub issues and ensures the label set exists.
type Issuer interface {
	EnsureLabels(ctx context.Context, repoSlug string) error
	CreateIssue(ctx context.Context, repoSlug, title, body string, labels []string) (github.Issue, error)
}

// Watcher periodically mirrors un-mirrored feedback rows into GitHub issues.
type Watcher struct {
	store    FeedbackStore
	issuer   Issuer
	owner    string
	interval time.Duration
	logger   *slog.Logger
	skip     map[uuid.UUID]bool // feedback whose repo can't be resolved (this process)
	ensured  map[string]bool    // repos whose label set we've upserted (this process)
}

// NewWatcher builds a watcher. interval <= 0 defaults to 20s.
func NewWatcher(store FeedbackStore, issuer Issuer, owner string, interval time.Duration, logger *slog.Logger) *Watcher {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 20 * time.Second
	}
	return &Watcher{
		store:    store,
		issuer:   issuer,
		owner:    owner,
		interval: interval,
		logger:   logger.With("component", "feedback-watcher"),
		skip:     map[uuid.UUID]bool{},
		ensured:  map[string]bool{},
	}
}

// Run reconciles every interval until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.reconcileOnce(ctx)
		}
	}
}

func (w *Watcher) reconcileOnce(ctx context.Context) {
	rows, err := w.store.ListFeedbackNeedingIssue(ctx, 20)
	if err != nil {
		w.logger.Warn("list feedback needing issue failed", "err", err)
		return
	}
	for _, f := range rows {
		if w.skip[f.ID] {
			continue
		}
		project, err := w.store.GetProject(ctx, f.ProjectID)
		if err != nil {
			// Unknown/deleted project (the widget tolerates unknown ids). Skip for
			// this process lifetime so we don't re-attempt every tick.
			w.skip[f.ID] = true
			w.logger.Warn("feedback project unresolved; skipping",
				"feedback_id", f.ID, "project_id", f.ProjectID, "err", err)
			continue
		}
		repoSlug := github.RepoSlug(w.owner, project.RepoURL, project.Name, project.ID.String())
		if repoSlug == "" {
			w.skip[f.ID] = true
			continue
		}
		if !w.ensured[repoSlug] {
			if err := w.issuer.EnsureLabels(ctx, repoSlug); err != nil {
				w.logger.Warn("ensure labels failed", "err", err, "repo", repoSlug)
			}
			w.ensured[repoSlug] = true
		}
		title, body, labels := IssueContent(f)
		iss, err := w.issuer.CreateIssue(ctx, repoSlug, title, body, labels)
		if err != nil {
			// Transient (gh/network): leave issue_number NULL, retry next tick.
			w.logger.Warn("create issue failed", "err", err, "feedback_id", f.ID)
			continue
		}
		if err := w.store.SetFeedbackIssue(ctx, f.ID, iss.Number, iss.URL); err != nil {
			w.logger.Warn("persist feedback issue failed", "err", err, "feedback_id", f.ID)
			continue
		}
		w.logger.Info("mirrored feedback to issue", "feedback_id", f.ID, "issue", iss.Number, "repo", repoSlug)
	}
}
