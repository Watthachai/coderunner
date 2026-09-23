// Package buildsettings owns which Claude model and effort a build runs with:
// the choices an operator may pick on the dashboard, their validation, and how
// a saved choice falls back to the environment.
//
// Every Claude invocation passes --model and --effort explicitly. The build
// box's ~/.claude/settings.json pins "sonnet"; an invocation that left either
// flag to the CLI would silently inherit whatever that file says.
package buildsettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// Model is one selectable Claude model. Prices are USD per million tokens and
// are for display only — they drive nothing, and go stale when pricing changes.
type Model struct {
	ID               string  `json:"id"`
	Label            string  `json:"label"`
	InputUSDPerMTok  float64 `json:"input_usd_per_mtok"`
	OutputUSDPerMTok float64 `json:"output_usd_per_mtok"`
}

// Models are the dashboard's choices, in display order. Anthropic list prices
// as of 2026-09.
var Models = []Model{
	{ID: "claude-opus-5-5", Label: "Opus 5.5", InputUSDPerMTok: 4, OutputUSDPerMTok: 20},
	{ID: "claude-opus-5", Label: "Opus 5", InputUSDPerMTok: 5, OutputUSDPerMTok: 25},
	{ID: "claude-sonnet-5", Label: "Sonnet 5", InputUSDPerMTok: 2, OutputUSDPerMTok: 10},
	{ID: "claude-fable-5-1", Label: "Fable 5.1", InputUSDPerMTok: 10, OutputUSDPerMTok: 50},
}

// Efforts are the values `claude --effort` accepts, lowest to highest.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// DefaultEffort applies when CRN_CLAUDE_EFFORT is unset. It is Claude Code's
// own default, so pinning it changes nothing today — it stops a CLI or model
// default from lowering it later.
const DefaultEffort = "xhigh"

// ValidEffort reports whether effort is one of Efforts, exactly.
func ValidEffort(effort string) bool {
	return slices.Contains(Efforts, effort)
}

// Validate checks an operator's choice. Only listed values pass: a free-typed
// model id with one wrong character would fail every build until noticed.
func Validate(model, effort string) error {
	if !slices.ContainsFunc(Models, func(m Model) bool { return m.ID == model }) {
		return fmt.Errorf("unknown model %q", model)
	}
	if !ValidEffort(effort) {
		return fmt.Errorf("unknown effort %q", effort)
	}
	return nil
}

// Source says where a resolved setting came from.
type Source string

const (
	// SourceEnv: nothing has been saved from the dashboard; the environment applies.
	SourceEnv Source = "env"
	// SourceSaved: an operator saved it from the dashboard.
	SourceSaved Source = "saved"
)

// Defaults are the environment's values (CRN_CLAUDE_MODEL, CRN_CLAUDE_EFFORT).
type Defaults struct {
	Model  string
	Effort string
}

// Resolved is the model and effort a Claude run will use, and where they came from.
type Resolved struct {
	Model       string
	Effort      string
	Source      Source
	UpdatedAt   *time.Time
	UpdatedFrom string
}

// Reader is the part of the store Resolve needs.
type Reader interface {
	GetBuildSettings(ctx context.Context) (*domain.BuildSettings, error)
}

// Resolve returns the saved setting, or the defaults when none is saved. A
// store error also yields the defaults, logged: a build must never fail
// because the settings row could not be read.
func Resolve(ctx context.Context, r Reader, d Defaults, log *slog.Logger) Resolved {
	s, err := r.GetBuildSettings(ctx)
	switch {
	case err == nil:
		at := s.UpdatedAt
		return Resolved{Model: s.Model, Effort: s.Effort, Source: SourceSaved, UpdatedAt: &at, UpdatedFrom: s.UpdatedFrom}
	case errors.Is(err, domain.ErrNotFound):
		// Expected until the first save from the dashboard.
	default:
		if log != nil {
			log.Warn("read build settings failed; using env defaults", "err", err)
		}
	}
	return Resolved{Model: d.Model, Effort: d.Effort, Source: SourceEnv}
}
