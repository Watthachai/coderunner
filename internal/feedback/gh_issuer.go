package feedback

import (
	"context"
	"log/slog"

	"github.com/Watthachai/fitt-coderunner/internal/github"
)

// GHIssuer adapts the package-level github helpers to the Issuer interface.
type GHIssuer struct{ logger *slog.Logger }

// NewGHIssuer returns an Issuer backed by the authenticated gh CLI.
func NewGHIssuer(logger *slog.Logger) *GHIssuer { return &GHIssuer{logger: logger} }

func (g *GHIssuer) EnsureLabels(ctx context.Context, repoSlug string) error {
	return github.EnsureLabels(ctx, repoSlug, g.logger)
}

func (g *GHIssuer) CreateIssue(ctx context.Context, repoSlug, title, body string, labels []string) (github.Issue, error) {
	return github.CreateIssue(ctx, repoSlug, title, body, labels, g.logger)
}

func (g *GHIssuer) CloseIssue(ctx context.Context, repoSlug string, number int, reason, comment string) error {
	return github.CloseIssue(ctx, repoSlug, number, reason, comment, g.logger)
}

func (g *GHIssuer) FindByFeedback(ctx context.Context, repoSlug, feedbackID string) (github.Issue, bool, error) {
	return github.FindIssueByFeedback(ctx, repoSlug, feedbackID, g.logger)
}
