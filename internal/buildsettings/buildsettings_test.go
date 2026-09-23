package buildsettings

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

type fakeReader struct {
	s   *domain.BuildSettings
	err error
}

func (f fakeReader) GetBuildSettings(context.Context) (*domain.BuildSettings, error) {
	return f.s, f.err
}

var envDefaults = Defaults{Model: "claude-opus-5", Effort: "xhigh"}

func TestValidateAcceptsEveryListedChoice(t *testing.T) {
	for _, m := range Models {
		for _, e := range Efforts {
			if err := Validate(m.ID, e); err != nil {
				t.Errorf("Validate(%q, %q) = %v, want nil", m.ID, e, err)
			}
		}
	}
}

// A typo in either field must be refused rather than saved: every build after
// it would hand the value to the CLI and fail.
func TestValidateRejectsAnythingElse(t *testing.T) {
	for _, c := range []struct{ model, effort string }{
		{"claude-opus-5-6", "xhigh"}, // not a model
		{"Claude-Opus-5", "xhigh"},   // case matters to the API
		{"opus", "xhigh"},            // CLI alias: fine in env, not from the dashboard
		{"", "xhigh"},
		{"claude-opus-5", "extreme"},
		{"claude-opus-5", "XHIGH"},
		{"claude-opus-5", ""},
	} {
		if err := Validate(c.model, c.effort); err == nil {
			t.Errorf("Validate(%q, %q) = nil, want an error", c.model, c.effort)
		}
	}
}

func TestResolveUsesTheSavedRow(t *testing.T) {
	at := time.Date(2026, 9, 23, 14, 32, 0, 0, time.UTC)
	got := Resolve(context.Background(), fakeReader{s: &domain.BuildSettings{
		Model: "claude-opus-5-5", Effort: "high", UpdatedAt: at, UpdatedFrom: "172.168.1.50",
	}}, envDefaults, nil)

	if got.Model != "claude-opus-5-5" || got.Effort != "high" || got.Source != SourceSaved {
		t.Errorf("got %+v, want the saved claude-opus-5-5/high", got)
	}
	if got.UpdatedAt == nil || !got.UpdatedAt.Equal(at) || got.UpdatedFrom != "172.168.1.50" {
		t.Errorf("lost who/when: at=%v from=%q", got.UpdatedAt, got.UpdatedFrom)
	}
}

func TestResolveFallsBackToEnvWhenNothingIsSaved(t *testing.T) {
	notFound := fmt.Errorf("store: get build settings: %w", domain.ErrNotFound)
	got := Resolve(context.Background(), fakeReader{err: notFound}, envDefaults, nil)
	if got.Model != "claude-opus-5" || got.Effort != "xhigh" || got.Source != SourceEnv || got.UpdatedAt != nil {
		t.Errorf("got %+v, want the env defaults", got)
	}
}

// An unreachable database must not stop a build: it runs on the defaults.
func TestResolveFallsBackToEnvWhenTheStoreFails(t *testing.T) {
	got := Resolve(context.Background(), fakeReader{err: errors.New("connection refused")}, envDefaults, nil)
	if got.Model != "claude-opus-5" || got.Effort != "xhigh" || got.Source != SourceEnv {
		t.Errorf("got %+v, want the env defaults", got)
	}
}
