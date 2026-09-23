package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// settingsStore answers only what claudeConfigFor calls; the nil embedded
// interface makes anything else panic.
type settingsStore struct {
	domain.Store
	saved    *domain.BuildSettings
	getErr   error
	stampErr error

	stampedJob    uuid.UUID
	stampedModel  string
	stampedEffort string
}

func (s *settingsStore) GetBuildSettings(context.Context) (*domain.BuildSettings, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.saved == nil {
		return nil, fmt.Errorf("store: get build settings: %w", domain.ErrNotFound)
	}
	return s.saved, nil
}

func (s *settingsStore) SetJobClaudeConfig(_ context.Context, id uuid.UUID, model, effort string) error {
	s.stampedJob, s.stampedModel, s.stampedEffort = id, model, effort
	return s.stampErr
}

func configManager(st *settingsStore) *manager {
	return &manager{
		store:          st,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		claudeDefaults: buildsettings.Defaults{Model: "claude-opus-5", Effort: "xhigh"},
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestClaudeConfigUsesTheSavedChoiceAndRecordsIt(t *testing.T) {
	st := &settingsStore{saved: &domain.BuildSettings{
		Model: "claude-opus-5-5", Effort: "high", UpdatedAt: time.Now(),
	}}
	job := uuid.New()

	got := configManager(st).claudeConfigFor(context.Background(), job, quiet)

	if got.Model != "claude-opus-5-5" || got.Effort != "high" {
		t.Errorf("resolved %s/%s, want claude-opus-5-5/high", got.Model, got.Effort)
	}
	if st.stampedJob != job || st.stampedModel != "claude-opus-5-5" || st.stampedEffort != "high" {
		t.Errorf("job stamped %v %s/%s, want %v claude-opus-5-5/high", st.stampedJob, st.stampedModel, st.stampedEffort, job)
	}
}

func TestClaudeConfigFallsBackToEnvWhenNothingIsSaved(t *testing.T) {
	st := &settingsStore{}
	got := configManager(st).claudeConfigFor(context.Background(), uuid.New(), quiet)
	if got.Model != "claude-opus-5" || got.Effort != "xhigh" || got.Source != buildsettings.SourceEnv {
		t.Errorf("got %+v, want env defaults", got)
	}
	if st.stampedModel != "claude-opus-5" || st.stampedEffort != "xhigh" {
		t.Errorf("job stamped %s/%s, want the env defaults", st.stampedModel, st.stampedEffort)
	}
}

// The stamp only feeds the dashboard. Failing it must not change what the
// build runs with, let alone stop it.
func TestClaudeConfigSurvivesAFailedStamp(t *testing.T) {
	st := &settingsStore{
		saved:    &domain.BuildSettings{Model: "claude-opus-5-5", Effort: "high"},
		stampErr: errors.New("connection reset"),
	}
	got := configManager(st).claudeConfigFor(context.Background(), uuid.New(), quiet)
	if got.Model != "claude-opus-5-5" || got.Effort != "high" {
		t.Errorf("got %s/%s, want claude-opus-5-5/high despite the stamp error", got.Model, got.Effort)
	}
}
