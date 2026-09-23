# Build Model & Effort Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator see and change, from the CRN dashboard, which Claude model and effort builds run with — snapshotted per build and always passed to the CLI as `--model` / `--effort`.

**Architecture:** A single-row `build_settings` table holds the operator's choice. `internal/buildsettings` owns the allowed values, validation, and the fallback to env (`CRN_CLAUDE_MODEL`, new `CRN_CLAUDE_EFFORT`). The job manager resolves the setting once, right before the build's single Claude call, stamps it on the job row (live view) and the trace (history), and passes it through `RunSpec`. `GET`/`PUT /internal/settings/build` back a dashboard panel; badges show the model on running and past builds.

**Tech Stack:** Go 1.23, pgx v5, chi v5, Postgres 16; Next.js 16, React 19, TypeScript 7; Node 26 (`node --test` runs `.ts` files natively).

**Spec:** `docs/superpowers/specs/2026-09-23-build-model-effort-design.md`

## Global Constraints

- Every Claude invocation passes both `--model` and `--effort`. The build box's `~/.claude/settings.json` sets `"model": "sonnet"`; anything left to the CLI inherits it.
- Selectable models, exactly (USD per 1M input / output tokens, display only): `claude-opus-5-5` Opus 5.5 4/20 · `claude-opus-5` Opus 5 5/25 · `claude-sonnet-5` Sonnet 5 2/10 · `claude-fable-5-1` Fable 5.1 10/50.
- Efforts, exactly: `low`, `medium`, `high`, `xhigh`, `max`. `CRN_CLAUDE_EFFORT` defaults to `xhigh`; an invalid value stops startup.
- `CRN_CLAUDE_MODEL` is not validated against the list — the existing config allows CLI aliases such as `opus`.
- No auth on the new endpoints (operator decision; matches the rest of `/internal/*`).
- A change affects only builds that reach their Claude run afterwards; a running build keeps its snapshot.
- A settings read or stamp failure never fails a build: fall back to env, log a warning.
- Builds that never invoke Claude (cached-image reuse, `CRN_RUN_CLAUDE=false`) record no model: `""` in JSON, `—` in the UI.
- API changes are additive only — FITT Builder reads `/internal/projects/{id}/builds`.
- Migrations are applied with `make migrate`, never by the Go server. 0012 is additive so it can run before the restart.
- Never `make restart` over ssh on the build machine (`macagents@172.168.1.171`): CRN has no supervisor there.
- `npm run lint` is broken (Next 16 removed `next lint`). Frontend checks: `npx tsc --noEmit`, `npm run build`, `node --test`.
- Do not stage `frontend/package.json`, `frontend/package-lock.json`, or the root `package-lock.json` — they carry unrelated pre-existing changes. Stage files by path.
- Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
  ```

## File Structure

| File | Responsibility |
|---|---|
| `internal/domain/settings.go` (new) | `BuildSettings` — the persisted row |
| `internal/buildsettings/buildsettings.go` (new) | allowed values, `Validate`, `ValidEffort`, `Resolve` (saved → env) |
| `internal/config/config.go` | `ClaudeEffort` from `CRN_CLAUDE_EFFORT`, validated |
| `migrations/0012_build_settings.sql` (new) | table + four nullable columns |
| `internal/domain/ports.go` | three Store methods; `RunSpec.Model` / `RunSpec.Effort` |
| `internal/domain/trace.go`, `internal/domain/dashboard.go` | `ClaudeModel` / `ClaudeEffort` fields |
| `internal/store/store.go` | settings get/save, job stamp, trace + dashboard columns |
| `internal/claude/claude.go` | `runArgs` / `improveArgs`; `Improve` takes effort |
| `internal/jobs/jobs.go` | `claudeConfigFor`; snapshot into meta, `RunSpec`, trace |
| `internal/api/buildsettings.go` (new) | GET/PUT handlers and response view |
| `internal/api/api.go` | routes; server holds defaults; improve uses the resolved config |
| `cmd/server/main.go` | wiring |
| `frontend/app/lib/modelLabel.ts` (new) | model id → label, pure, no imports |
| `frontend/app/lib/useBuildSettings.ts` (new) | load / save hook |
| `frontend/app/components/BuildSettingsPanel.tsx` (new) | the panel |
| `frontend/app/lib/config.ts`, `types.ts`, `components/NowBuilding.tsx`, `components/TraceSpanGraph.tsx`, `page.tsx`, `globals.css`, `tsconfig.json` | wiring, badges, styles |

---

### Task 1: `buildsettings` package

**Files:**
- Create: `internal/domain/settings.go`
- Create: `internal/buildsettings/buildsettings.go`
- Test: `internal/buildsettings/buildsettings_test.go`

**Interfaces:**
- Produces:
  - `domain.BuildSettings{Model, Effort string; UpdatedAt time.Time; UpdatedFrom string}`
  - `buildsettings.Model{ID, Label string; InputUSDPerMTok, OutputUSDPerMTok float64}` (JSON: `id`, `label`, `input_usd_per_mtok`, `output_usd_per_mtok`)
  - `buildsettings.Models []Model`, `buildsettings.Efforts []string`, `buildsettings.DefaultEffort = "xhigh"`
  - `buildsettings.ValidEffort(effort string) bool`
  - `buildsettings.Validate(model, effort string) error`
  - `buildsettings.Source` with `SourceEnv = "env"`, `SourceSaved = "saved"`
  - `buildsettings.Defaults{Model, Effort string}`
  - `buildsettings.Resolved{Model, Effort string; Source Source; UpdatedAt *time.Time; UpdatedFrom string}`
  - `buildsettings.Reader` interface `{ GetBuildSettings(ctx context.Context) (*domain.BuildSettings, error) }`
  - `buildsettings.Resolve(ctx context.Context, r Reader, d Defaults, log *slog.Logger) Resolved`

- [ ] **Step 1: Add the domain type**

Create `internal/domain/settings.go`:

```go
package domain

import "time"

// BuildSettings is the operator's saved choice of Claude model and effort for
// builds — the single row of build_settings. It is absent until someone saves
// it from the dashboard; until then the environment's defaults apply.
type BuildSettings struct {
	Model  string
	Effort string
	// UpdatedAt is set by the store on save.
	UpdatedAt time.Time
	// UpdatedFrom is the client address that saved it. /internal has no auth,
	// so this is the only record of who changed a setting that drives spend.
	UpdatedFrom string
}
```

- [ ] **Step 2: Write the failing test**

Create `internal/buildsettings/buildsettings_test.go`:

```go
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
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/buildsettings/`
Expected: FAIL — `undefined: Models`, `undefined: Validate`, `undefined: Resolve`, …

- [ ] **Step 4: Write the implementation**

Create `internal/buildsettings/buildsettings.go`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/buildsettings/ && go vet ./internal/buildsettings/ ./internal/domain/`
Expected: `ok  github.com/Watthachai/fitt-coderunner/internal/buildsettings`, no vet output.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/settings.go internal/buildsettings/
git commit -F - <<'MSG'
feat(buildsettings): the choices a build's model and effort come from

One place owns which models and efforts the dashboard may offer, refuses
anything off that list, and falls back to the environment when nothing is
saved — or when the settings row can't be read, since a build must not
fail over a display preference.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 2: `CRN_CLAUDE_EFFORT` in config

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (new)

**Interfaces:**
- Consumes: `buildsettings.DefaultEffort`, `buildsettings.Efforts`, `buildsettings.ValidEffort` (Task 1)
- Produces: `config.Config.ClaudeEffort string`

- [ ] **Step 1: Write the failing test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

// requiredEnv sets the three values Load refuses to start without.
func requiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CRN_DATABASE_URL", "postgres://unused")
	t.Setenv("CRN_CLAUDE_BIN", "/usr/bin/true")
	t.Setenv("CRN_DOCKER_USER", "crn")
}

func TestClaudeEffortDefaultsToXhigh(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_CLAUDE_EFFORT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeEffort != "xhigh" {
		t.Errorf("ClaudeEffort = %q, want xhigh", cfg.ClaudeEffort)
	}
}

func TestClaudeEffortComesFromEnv(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_CLAUDE_EFFORT", "high")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClaudeEffort != "high" {
		t.Errorf("ClaudeEffort = %q, want high", cfg.ClaudeEffort)
	}
}

// A mistyped effort would reach every build's command line and fail it there,
// one at a time. Refusing to start says so once, where the mistake is.
func TestClaudeEffortTypoStopsStartup(t *testing.T) {
	requiredEnv(t)
	t.Setenv("CRN_CLAUDE_EFFORT", "extreme")
	_, err := Load()
	if err == nil {
		t.Fatal("Load accepted CRN_CLAUDE_EFFORT=extreme")
	}
	if !strings.Contains(err.Error(), "CRN_CLAUDE_EFFORT") || !strings.Contains(err.Error(), "xhigh") {
		t.Errorf("error should name the variable and the valid values: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL — `cfg.ClaudeEffort undefined`

- [ ] **Step 3: Add the field, the default, and the check**

In `internal/config/config.go`:

Add the import `"github.com/Watthachai/fitt-coderunner/internal/buildsettings"` to the import block (after the standard-library imports, separated by a blank line).

Directly after the `ClaudeModel string` field and its comment, add:

```go
	// ClaudeEffort is the default passed as --effort (CRN_CLAUDE_EFFORT,
	// default buildsettings.DefaultEffort). Checked at load: a bad value would
	// fail every build, so it stops the server instead. The dashboard can
	// override ClaudeModel and ClaudeEffort; these are the fallback.
	ClaudeEffort string
```

In the `Load` doc comment's optional list, after the `CRN_CLAUDE_MODEL` line, add:

```go
//	CRN_CLAUDE_EFFORT      (buildsettings.DefaultEffort — "xhigh"; must be low|medium|high|xhigh|max)
```

In the `cfg := &Config{…}` literal, after `ClaudeModel: getEnv("CRN_CLAUDE_MODEL", defaultClaudeModel),` add:

```go
		ClaudeEffort:       getEnv("CRN_CLAUDE_EFFORT", buildsettings.DefaultEffort),
```

After the `cfg.MaxConcurrentBuilds = maxBuilds` line and before `if err := cfg.validate(); err != nil {`, add:

```go
	if !buildsettings.ValidEffort(cfg.ClaudeEffort) {
		return nil, fmt.Errorf("config: CRN_CLAUDE_EFFORT must be one of %s, got %q",
			strings.Join(buildsettings.Efforts, ", "), cfg.ClaudeEffort)
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `gofmt -l internal/config/ && go test ./internal/config/ && go build ./...`
Expected: no gofmt output, `ok …/internal/config`, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -F - <<'MSG'
feat(config): CRN_CLAUDE_EFFORT, refused at startup if it isn't real

Effort was never passed, so builds ran on whatever the CLI defaulted to.
Default xhigh (Claude Code's own), and a typo stops the server with the
valid values listed rather than failing every build one by one.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 3: Migration, store, and domain fields

**Files:**
- Create: `migrations/0012_build_settings.sql`
- Modify: `internal/domain/ports.go` (Store interface)
- Modify: `internal/domain/trace.go` (`BuildTrace`)
- Modify: `internal/domain/dashboard.go` (`BuildingJob`)
- Modify: `internal/store/store.go`
- Test: `internal/store/buildsettings_integration_test.go` (new)

**Interfaces:**
- Consumes: `domain.BuildSettings` (Task 1)
- Produces (on `domain.Store`, implemented by the pg store):
  - `SetJobClaudeConfig(ctx context.Context, id uuid.UUID, model, effort string) error`
  - `GetBuildSettings(ctx context.Context) (*BuildSettings, error)` — `ErrNotFound` (wrapped) until the first save
  - `SaveBuildSettings(ctx context.Context, s *BuildSettings) error` — upsert; sets `s.UpdatedAt`
  - `domain.BuildTrace.ClaudeModel`, `.ClaudeEffort` (JSON `claude_model`, `claude_effort`)
  - `domain.BuildingJob.ClaudeModel`, `.ClaudeEffort` (JSON `claude_model`, `claude_effort`)

- [ ] **Step 1: Write the migration**

Create `migrations/0012_build_settings.sql`:

```sql
-- 0012: pick the build's Claude model and effort from the dashboard.
--
-- build_settings holds the operator's saved choice — one row, enforced by the
-- CHECK. It is absent until the first save; until then CRN uses
-- CRN_CLAUDE_MODEL / CRN_CLAUDE_EFFORT. The allowed values live in Go
-- (internal/buildsettings), not in a CHECK here, so a new model does not need
-- a migration.
--
-- project_jobs / job_traces record what each build actually ran with: the job
-- row for the dashboard while it runs, the trace for history. Nullable —
-- builds from before this migration never recorded it, and builds that reuse
-- a cached image never invoke a model at all.
--
-- Additive only: the running binary ignores the new table and columns, so this
-- is safe to apply before restarting onto the code that reads them.

CREATE TABLE IF NOT EXISTS build_settings (
  id            SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  claude_model  TEXT NOT NULL,
  claude_effort TEXT NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_from  TEXT NOT NULL DEFAULT ''
);

ALTER TABLE project_jobs ADD COLUMN IF NOT EXISTS claude_model  TEXT;
ALTER TABLE project_jobs ADD COLUMN IF NOT EXISTS claude_effort TEXT;
ALTER TABLE job_traces   ADD COLUMN IF NOT EXISTS claude_model  TEXT;
ALTER TABLE job_traces   ADD COLUMN IF NOT EXISTS claude_effort TEXT;
```

- [ ] **Step 2: Write the failing integration test**

This test needs a real Postgres. It creates a fresh schema, applies every migration into it, and drops it afterwards — it never touches a table that already existed in the target database. It skips when `CRN_TEST_DATABASE_URL` is unset, so `go test ./...` stays green without a database.

Create `internal/store/buildsettings_integration_test.go`:

```go
package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// openScratchStore applies every migration into a new schema of the database
// at CRN_TEST_DATABASE_URL and returns a store scoped to it. The schema is
// dropped when the test ends.
func openScratchStore(t *testing.T) domain.Store {
	t.Helper()
	dsn := os.Getenv("CRN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set CRN_TEST_DATABASE_URL to a disposable Postgres to run store integration tests")
	}
	ctx := context.Background()

	schema := fmt.Sprintf("crn_it_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("CRN_TEST_DATABASE_URL must be a postgres:// URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped := u.String()

	conn, err := pgx.Connect(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}

	st, err := New(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestBuildSettingsRoundTrip(t *testing.T) {
	st := openScratchStore(t)
	ctx := context.Background()

	if _, err := st.GetBuildSettings(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("before any save: err = %v, want ErrNotFound", err)
	}

	first := &domain.BuildSettings{Model: "claude-opus-5", Effort: "high", UpdatedFrom: "10.0.0.1"}
	if err := st.SaveBuildSettings(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.UpdatedAt.IsZero() {
		t.Error("SaveBuildSettings did not read back updated_at")
	}

	// A second save replaces the row — there is only ever one.
	second := &domain.BuildSettings{Model: "claude-opus-5-5", Effort: "xhigh", UpdatedFrom: "10.0.0.2"}
	if err := st.SaveBuildSettings(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetBuildSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-opus-5-5" || got.Effort != "xhigh" || got.UpdatedFrom != "10.0.0.2" {
		t.Errorf("GetBuildSettings = %+v, want the second save", got)
	}
}

func TestClaudeConfigReachesTheDashboardAndTheTrace(t *testing.T) {
	st := openScratchStore(t)
	ctx := context.Background()

	org := &domain.Org{Name: "it"}
	if err := st.EnsureOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	proj := &domain.Project{ID: uuid.New(), OrgID: org.ID, Name: "it-project", Status: domain.ProjectActive}
	if err := st.CreateProject(ctx, proj); err != nil {
		t.Fatal(err)
	}
	newJob := func(buildNo int) *domain.Job {
		t.Helper()
		j := &domain.Job{ID: uuid.New(), ProjectID: proj.ID, OrgID: org.ID, Status: domain.JobQueued,
			BuildNo: buildNo, Payload: []byte(`{}`)}
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
		return j
	}

	// Build 1 runs Claude: stamped while building, shown on the dashboard.
	ran := newJob(1)
	if err := st.UpdateJobStatus(ctx, ran.ID, domain.JobBuilding, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetJobClaudeConfig(ctx, ran.ID, "claude-opus-5-5", "xhigh"); err != nil {
		t.Fatal(err)
	}
	snap, err := st.DashboardSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Building) != 1 || snap.Building[0].ClaudeModel != "claude-opus-5-5" || snap.Building[0].ClaudeEffort != "xhigh" {
		t.Errorf("dashboard building = %+v, want one job on claude-opus-5-5/xhigh", snap.Building)
	}

	// Its trace keeps them. Build 2 reused a cached image: no model ran, and
	// its trace must read back empty rather than fail on NULL.
	if err := st.SaveJobTrace(ctx, &domain.BuildTrace{JobID: ran.ID, ProjectID: proj.ID, BuildNo: 1,
		Outcome: "done", Mode: "build", ClaudeModel: "claude-opus-5-5", ClaudeEffort: "xhigh"}); err != nil {
		t.Fatal(err)
	}
	reused := newJob(2)
	if err := st.SaveJobTrace(ctx, &domain.BuildTrace{JobID: reused.ID, ProjectID: proj.ID, BuildNo: 2,
		Outcome: "done", Mode: "build"}); err != nil {
		t.Fatal(err)
	}

	traces, err := st.ListBuildTraces(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 {
		t.Fatalf("ListBuildTraces returned %d traces, want 2", len(traces))
	}
	if traces[0].BuildNo != 2 || traces[0].ClaudeModel != "" || traces[0].ClaudeEffort != "" {
		t.Errorf("reused build trace = %+v, want no model recorded", traces[0])
	}
	if traces[1].ClaudeModel != "claude-opus-5-5" || traces[1].ClaudeEffort != "xhigh" {
		t.Errorf("build 1 trace = %+v, want claude-opus-5-5/xhigh", traces[1])
	}

	// The single-trace read appends the events column after the summary — the
	// new columns must not shift it.
	full, err := st.GetBuildTrace(ctx, ran.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.ClaudeModel != "claude-opus-5-5" || full.Events == nil {
		t.Errorf("GetBuildTrace = model %q events %v", full.ClaudeModel, full.Events)
	}
}
```

- [ ] **Step 3: Start a throwaway Postgres**

On this machine (Docker Desktop must be running — `open -a Docker`, then poll `docker info` until it succeeds):

```bash
docker run --rm -d --name crn-pgtest -p 5544:5432 -e POSTGRES_PASSWORD=test postgres:16-alpine
# poll until ready (repeat this command until it prints "accepting connections"):
docker exec crn-pgtest pg_isready -U postgres
export CRN_TEST_DATABASE_URL='postgres://postgres:test@localhost:5544/postgres?sslmode=disable'
```

Never point `CRN_TEST_DATABASE_URL` at the build machine's `crn` database.

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/store/ -run 'TestBuildSettingsRoundTrip|TestClaudeConfigReaches' -v`
Expected: FAIL to compile — `st.GetBuildSettings undefined`, `unknown field ClaudeModel in struct literal of type domain.BuildTrace`, …

- [ ] **Step 5: Add the domain fields and Store methods**

In `internal/domain/trace.go`, inside `BuildTrace`, directly after the `ErrorMsg string` field, add:

```go
	// ClaudeModel / ClaudeEffort are what the build's Claude run used. Empty
	// for builds from before they were recorded and for builds that reused a
	// cached image without invoking a model.
	ClaudeModel  string `json:"claude_model"`
	ClaudeEffort string `json:"claude_effort"`
```

In `internal/domain/dashboard.go`, inside `BuildingJob`, after `StartedAt`, add:

```go
	// ClaudeModel / ClaudeEffort: empty until the build reaches its Claude run.
	ClaudeModel  string `json:"claude_model"`
	ClaudeEffort string `json:"claude_effort"`
```

In `internal/domain/ports.go`, inside `type Store interface`, directly after the `SetJobDockerTag` method, add:

```go
	// SetJobClaudeConfig records the model and effort a build's Claude run
	// uses, so the dashboard can show them while it runs.
	SetJobClaudeConfig(ctx context.Context, id uuid.UUID, model, effort string) error
```

and directly before `// --- Dashboard (operator console read model) ---`, add:

```go
	// --- Build settings (dashboard-chosen Claude model/effort) ---
	// GetBuildSettings returns the saved choice; ErrNotFound until the first save.
	GetBuildSettings(ctx context.Context) (*BuildSettings, error)
	// SaveBuildSettings upserts the single settings row and sets s.UpdatedAt.
	SaveBuildSettings(ctx context.Context, s *BuildSettings) error

```

- [ ] **Step 6: Implement them in the store**

In `internal/store/store.go`, directly after `SetJobDockerTag`, add:

```go
// SetJobClaudeConfig records the model and effort the build's Claude run uses.
func (s *pgStore) SetJobClaudeConfig(ctx context.Context, id uuid.UUID, model, effort string) error {
	const q = `UPDATE project_jobs SET claude_model = $2, claude_effort = $3 WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, id, model, effort)
	if err != nil {
		return mapErr(err, "store: set job claude config")
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: set job claude config: %w", domain.ErrNotFound)
	}
	return nil
}
```

Directly before the `// --- Build traces (durable per-build state history) ---` section, add:

```go
// --- Build settings -------------------------------------------------------------

// GetBuildSettings returns the saved model/effort; ErrNotFound until the first save.
func (s *pgStore) GetBuildSettings(ctx context.Context) (*domain.BuildSettings, error) {
	const q = `SELECT claude_model, claude_effort, updated_at, updated_from
		FROM build_settings WHERE id = 1`
	bs := &domain.BuildSettings{}
	if err := s.pool.QueryRow(ctx, q).Scan(&bs.Model, &bs.Effort, &bs.UpdatedAt, &bs.UpdatedFrom); err != nil {
		return nil, mapErr(err, "store: get build settings")
	}
	return bs, nil
}

// SaveBuildSettings upserts the single settings row and reads back updated_at.
func (s *pgStore) SaveBuildSettings(ctx context.Context, bs *domain.BuildSettings) error {
	const q = `
		INSERT INTO build_settings (id, claude_model, claude_effort, updated_at, updated_from)
		VALUES (1, $1, $2, now(), $3)
		ON CONFLICT (id) DO UPDATE SET
			claude_model  = EXCLUDED.claude_model,
			claude_effort = EXCLUDED.claude_effort,
			updated_at    = EXCLUDED.updated_at,
			updated_from  = EXCLUDED.updated_from
		RETURNING updated_at`
	if err := s.pool.QueryRow(ctx, q, bs.Model, bs.Effort, bs.UpdatedFrom).Scan(&bs.UpdatedAt); err != nil {
		return fmt.Errorf("store: save build settings: %w", err)
	}
	return nil
}

```

Replace `traceListCols` with (the two new columns go LAST so `GetBuildTrace`'s appended `, events` stays last):

```go
const traceListCols = `
	job_id, project_id, build_no, outcome, mode, commit_sha, branch, remote,
	session_id, cost_usd, tool_count, file_count, error_msg,
	started_at, finished_at, created_at,
	COALESCE(claude_model, ''), COALESCE(claude_effort, '')`
```

In `scanTraceSummaryRow`, replace the `dest := []any{…}` literal with:

```go
	dest := []any{
		&t.JobID, &t.ProjectID, &t.BuildNo, &t.Outcome, &t.Mode, &t.CommitSHA, &t.Branch, &t.Remote,
		&t.SessionID, &t.CostUSD, &t.ToolCount, &t.FileCount, &t.ErrorMsg,
		&t.StartedAt, &t.FinishedAt, &t.CreatedAt,
		&t.ClaudeModel, &t.ClaudeEffort,
	}
```

In `SaveJobTrace`, replace the `const q = …` and the `s.pool.Exec` call with:

```go
	const q = `
		INSERT INTO job_traces (
			job_id, project_id, build_no, outcome, mode, commit_sha, branch, remote,
			session_id, cost_usd, tool_count, file_count, error_msg, events,
			started_at, finished_at, claude_model, claude_effort)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
			NULLIF($17, ''), NULLIF($18, ''))
		ON CONFLICT (job_id) DO UPDATE SET
			outcome       = EXCLUDED.outcome,
			mode          = EXCLUDED.mode,
			commit_sha    = EXCLUDED.commit_sha,
			branch        = EXCLUDED.branch,
			remote        = EXCLUDED.remote,
			session_id    = EXCLUDED.session_id,
			cost_usd      = EXCLUDED.cost_usd,
			tool_count    = EXCLUDED.tool_count,
			file_count    = EXCLUDED.file_count,
			error_msg     = EXCLUDED.error_msg,
			events        = EXCLUDED.events,
			started_at    = EXCLUDED.started_at,
			finished_at   = EXCLUDED.finished_at,
			claude_model  = EXCLUDED.claude_model,
			claude_effort = EXCLUDED.claude_effort`
	if _, err := s.pool.Exec(ctx, q,
		t.JobID, t.ProjectID, t.BuildNo, t.Outcome, t.Mode, t.CommitSHA, t.Branch, t.Remote,
		t.SessionID, t.CostUSD, t.ToolCount, t.FileCount, t.ErrorMsg, eventsJSON,
		t.StartedAt, t.FinishedAt, t.ClaudeModel, t.ClaudeEffort,
	); err != nil {
		return fmt.Errorf("store: save job trace: %w", err)
	}
	return nil
```

In `dashboardBuilding`, replace the query and the scan with:

```go
	const q = `
		SELECT j.id, j.project_id, p.name, o.name, j.build_no,
		       COALESCE(j.docker_tag, ''), j.started_at,
		       COALESCE(j.claude_model, ''), COALESCE(j.claude_effort, '')
		FROM project_jobs j
		JOIN projects p ON p.id = j.project_id
		JOIN orgs o     ON o.id = j.org_id
		WHERE j.status = 'building'
		ORDER BY j.started_at DESC NULLS LAST, j.queued_at DESC`
```

```go
		if err := rows.Scan(
			&b.JobID, &b.ProjectID, &b.ProjectName, &b.OrgName, &b.BuildNo,
			&dockerTag, &b.StartedAt, &b.ClaudeModel, &b.ClaudeEffort,
		); err != nil {
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `gofmt -l internal/ && go build ./... && go test ./internal/store/ -run 'TestBuildSettingsRoundTrip|TestClaudeConfigReaches' -v`
Expected: no gofmt output; both tests `PASS`.

Then: `unset CRN_TEST_DATABASE_URL; go test ./...`
Expected: every package `ok` (the store tests report as skipped, not failed).

Stop the database: `docker stop crn-pgtest`

- [ ] **Step 8: Commit**

```bash
git add migrations/0012_build_settings.sql internal/domain/ports.go internal/domain/trace.go internal/domain/dashboard.go internal/store/store.go internal/store/buildsettings_integration_test.go
git commit -F - <<'MSG'
feat(store): persist the build's model/effort choice and what each build ran

build_settings holds the dashboard's choice (one row). project_jobs and
job_traces now say which model and effort a build actually used: the job
row while it runs, the trace afterwards. Additive, so the migration can
land before the restart. The integration test runs every migration in a
throwaway schema and never touches existing tables.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 4: The runner passes the build's model and effort

**Files:**
- Modify: `internal/domain/ports.go` (`RunSpec`)
- Modify: `internal/claude/claude.go`
- Test: `internal/claude/claude_test.go` (new)

**Interfaces:**
- Produces: `domain.RunSpec.Model string`, `domain.RunSpec.Effort string`; unexported `runArgs(spec domain.RunSpec, fallbackModel string) []string`

- [ ] **Step 1: Write the failing test**

Create `internal/claude/claude_test.go`:

```go
package claude

import (
	"slices"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

var baseArgs = []string{"-p", "build it", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}

func TestRunArgsPassTheBuildsModelAndEffort(t *testing.T) {
	got := runArgs(domain.RunSpec{Prompt: "build it", Model: "claude-opus-5-5", Effort: "xhigh"}, "claude-opus-5")
	want := append(slices.Clone(baseArgs), "--model", "claude-opus-5-5", "--effort", "xhigh")
	if !slices.Equal(got, want) {
		t.Errorf("runArgs =\n  %q\nwant\n  %q", got, want)
	}
}

// A spec without a model still names one: leaving --model off would hand the
// choice to the build box's settings.json, which says "sonnet".
func TestRunArgsFallBackToTheRunnersModel(t *testing.T) {
	got := runArgs(domain.RunSpec{Prompt: "build it"}, "claude-opus-5")
	want := append(slices.Clone(baseArgs), "--model", "claude-opus-5")
	if !slices.Equal(got, want) {
		t.Errorf("runArgs =\n  %q\nwant\n  %q", got, want)
	}
}

func TestRunArgsKeepResume(t *testing.T) {
	got := runArgs(domain.RunSpec{Prompt: "build it", ResumeSessionID: "sess-1", Model: "claude-opus-5", Effort: "high"}, "")
	want := append(slices.Clone(baseArgs), "--resume", "sess-1", "--model", "claude-opus-5", "--effort", "high")
	if !slices.Equal(got, want) {
		t.Errorf("runArgs =\n  %q\nwant\n  %q", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/claude/`
Expected: FAIL — `undefined: runArgs`, `unknown field Model in struct literal of type domain.RunSpec`

- [ ] **Step 3: Add the fields and extract `runArgs`**

In `internal/domain/ports.go`, inside `type RunSpec struct`, after `ResumeSessionID`, add:

```go
	// Model / Effort are this build's snapshot of the model and effort
	// (internal/buildsettings), passed as --model / --effort.
	Model  string
	Effort string
```

In `internal/claude/claude.go`, add this function directly above `func (r *runner) Run(`:

```go
// runArgs builds the `claude` command line for one build. The model is the
// build's snapshot, else the runner's own; --effort is added whenever the
// snapshot has one. The job manager always fills both, so neither is left to
// the CLI's defaults.
func runArgs(spec domain.RunSpec, fallbackModel string) []string {
	args := []string{
		"-p", spec.Prompt,
		"--output-format", "stream-json",
		"--verbose", // required for stream-json to emit intermediate events
		"--dangerously-skip-permissions",
	}
	if spec.ResumeSessionID != "" {
		args = append(args, "--resume", spec.ResumeSessionID)
	}
	model := spec.Model
	if model == "" {
		model = fallbackModel
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	return args
}
```

In `Run`, replace everything from `args := []string{` through the closing `}` of `if r.model != "" { … }` with:

```go
	args := runArgs(spec, r.model)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `gofmt -l internal/ && go test ./internal/claude/ && go build ./...`
Expected: no gofmt output, `ok …/internal/claude`, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/ports.go internal/claude/claude.go internal/claude/claude_test.go
git commit -F - <<'MSG'
feat(claude): a build's run carries its own model and effort

RunSpec gains Model and Effort, and the command line is built by one
function the tests can see. With no model on the spec the runner still
names its own — never the CLI default, which on the build box is sonnet.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 5: The job manager snapshots model and effort per build

**Files:**
- Modify: `internal/jobs/jobs.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/jobs/trace_test.go`
- Test: `internal/jobs/claude_config_test.go` (new)

**Interfaces:**
- Consumes: `buildsettings.Defaults`, `buildsettings.Resolve`, `buildsettings.Resolved` (Task 1); `Store.SetJobClaudeConfig`, `Store.GetBuildSettings` (Task 3); `RunSpec.Model/Effort` (Task 4); `config.Config.ClaudeEffort` (Task 2)
- Produces: `jobs.NewManager(…, maxConcurrent int, claudeDefaults buildsettings.Defaults) domain.JobManager` (new last parameter); unexported `(m *manager) claudeConfigFor(ctx context.Context, jobID uuid.UUID, log *slog.Logger) buildsettings.Resolved`; `traceMeta.model`, `traceMeta.effort`

- [ ] **Step 1: Write the failing tests**

Create `internal/jobs/claude_config_test.go`:

```go
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
```

In `internal/jobs/trace_test.go`, in `TestFailedBuildKeepsWhatItAlreadyAchieved`, add to the `traceMeta{…}` literal:

```go
		model:  "claude-opus-5-5",
		effort: "xhigh",
```

and add to its table of `{field, got, want}` cases:

```go
		{"claude_model", tr.ClaudeModel, "claude-opus-5-5"},
		{"claude_effort", tr.ClaudeEffort, "xhigh"},
```

In `TestCancelledBuildKeepsWhatItAlreadyAchieved`, add to its `traceMeta{…}` literal:

```go
		model:  "claude-sonnet-5",
		effort: "high",
```

and after the existing context check add:

```go
	if tr.ClaudeModel != "claude-sonnet-5" || tr.ClaudeEffort != "high" {
		t.Errorf("trace lost the model: %q/%q", tr.ClaudeModel, tr.ClaudeEffort)
	}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/jobs/`
Expected: FAIL — `unknown field claudeDefaults in struct literal of type manager`, `m.claudeConfigFor undefined`, `unknown field model in struct literal of type traceMeta`

- [ ] **Step 3: Implement**

In `internal/jobs/jobs.go`:

Add the import `"github.com/Watthachai/fitt-coderunner/internal/buildsettings"` to the internal-imports group.

In `type manager struct`, after `artifactDir string`, add:

```go

	// claudeDefaults is the environment's model/effort, used until an operator
	// saves a choice on the dashboard (internal/buildsettings).
	claudeDefaults buildsettings.Defaults
```

Change `NewManager`'s parameter list so its last two parameters are:

```go
	maxConcurrent int,
	claudeDefaults buildsettings.Defaults,
) domain.JobManager {
```

and add `claudeDefaults: claudeDefaults,` to the `&manager{…}` literal it returns.

In `type traceMeta struct`, after `errMsg string`, add:

```go
	// model/effort are what the Claude run used. Left empty on paths that
	// never invoke Claude, so the trace never names a model that did not run.
	model  string
	effort string
```

Add this method directly above `func (m *manager) saveTrace(`:

```go
// claudeConfigFor settles the model and effort this build's Claude run uses and
// records them on the job, so the dashboard can show them while it runs. It is
// called once, right before the run: a change on the dashboard reaches the
// next build to get here, never one already running. Recording is best-effort
// — a failure costs the dashboard its label, not the build.
func (m *manager) claudeConfigFor(ctx context.Context, jobID uuid.UUID, log *slog.Logger) buildsettings.Resolved {
	res := buildsettings.Resolve(ctx, m.store, m.claudeDefaults, log)
	if err := m.store.SetJobClaudeConfig(ctx, jobID, res.Model, res.Effort); err != nil {
		log.Warn("record claude config on job failed", "err", err, "model", res.Model, "effort", res.Effort)
	}
	return res
}
```

In `saveTrace`, add to the `&domain.BuildTrace{…}` literal:

```go
		ClaudeModel:  meta.model,
		ClaudeEffort: meta.effort,
```

In `runJob`, inside `if m.runClaude {`, replace the `runSpec := domain.RunSpec{ … }` literal with:

```go
		claudeCfg := m.claudeConfigFor(ctx, job.ID, log)
		meta.model, meta.effort = claudeCfg.Model, claudeCfg.Effort
		log.Info("claude config", "model", claudeCfg.Model, "effort", claudeCfg.Effort, "source", claudeCfg.Source)

		runSpec := domain.RunSpec{
			JobID:           job.ID,
			ProjectID:       job.ProjectID,
			WorkDir:         workDir,
			Prompt:          claudePrompt,
			ResumeSessionID: resumeSession,
			Model:           claudeCfg.Model,
			Effort:          claudeCfg.Effort,
		}
```

In `cmd/server/main.go`:

Add the import `"github.com/Watthachai/fitt-coderunner/internal/buildsettings"` to the internal-imports group.

In the boot log, change `"run_claude", cfg.RunClaude, "claude_model", cfg.ClaudeModel,` to:

```go
		"run_claude", cfg.RunClaude, "claude_model", cfg.ClaudeModel, "claude_effort", cfg.ClaudeEffort,
```

Directly above the `jobManager := jobs.NewManager(` line, add:

```go
	// The environment's model/effort. The dashboard can save a different
	// choice (build_settings); these apply until it does.
	claudeDefaults := buildsettings.Defaults{Model: cfg.ClaudeModel, Effort: cfg.ClaudeEffort}
```

and append `, claudeDefaults` as the last argument of the `jobs.NewManager(…)` call.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `gofmt -l internal/ cmd/ && go vet ./... && go test ./...`
Expected: no gofmt/vet output; every package `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/jobs/jobs.go internal/jobs/claude_config_test.go internal/jobs/trace_test.go cmd/server/main.go
git commit -F - <<'MSG'
feat(jobs): each build snapshots its model and effort before Claude runs

Resolved once, right before the build's single Claude call: stamped on
the job for the live dashboard, kept in the trace for history, and
passed to the runner. A change mid-build reaches the next build only.
Builds that reuse a cached image never get here and record no model —
which is the truth.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 6: Settings endpoints, and improve-skill uses the same choice

**Files:**
- Create: `internal/api/buildsettings.go`
- Modify: `internal/api/api.go`
- Modify: `internal/claude/claude.go`, `internal/claude/claude_test.go`
- Modify: `cmd/server/main.go`
- Test: `internal/api/buildsettings_test.go` (new)

**Interfaces:**
- Consumes: `buildsettings.*` (Task 1); `Store.GetBuildSettings`, `Store.SaveBuildSettings` (Task 3); `claudeDefaults` in `main.go` (Task 5)
- Produces:
  - `GET /internal/settings/build` → `200 {model, effort, source, updated_at, updated_from, options: {models: [{id, label, input_usd_per_mtok, output_usd_per_mtok}], efforts: [...]}}`
  - `PUT /internal/settings/build` body `{model, effort}` → `200` same shape · `400 {"error": "..."}` off-list / malformed / unknown field · `500` store failure
  - `api.NewServer(logger, store, jm, gitRemote, githubOwner, projectsDir, terminalShell, claudeBin string, claudeDefaults buildsettings.Defaults) http.Handler` (last parameter changes type and name)
  - `claude.Improve(ctx context.Context, binPath, model, effort, skillBody string, logger *slog.Logger) (string, error)` (gains `effort`)

- [ ] **Step 1: Write the failing tests**

Create `internal/api/buildsettings_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// settingsStore answers only the settings calls; the nil embedded interface
// makes anything else panic, so a handler that starts touching more of the
// store fails loudly.
type settingsStore struct {
	domain.Store
	saved *domain.BuildSettings
	saves int
}

func (s *settingsStore) GetBuildSettings(context.Context) (*domain.BuildSettings, error) {
	if s.saved == nil {
		return nil, fmt.Errorf("store: get build settings: %w", domain.ErrNotFound)
	}
	cp := *s.saved
	return &cp, nil
}

func (s *settingsStore) SaveBuildSettings(_ context.Context, bs *domain.BuildSettings) error {
	s.saves++
	bs.UpdatedAt = time.Date(2026, 9, 23, 14, 32, 0, 0, time.UTC)
	cp := *bs
	s.saved = &cp
	return nil
}

func settingsServer(st *settingsStore) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	defaults := buildsettings.Defaults{Model: "claude-opus-5", Effort: "xhigh"}
	return NewServer(logger, st, nil, "", "", "", "", "", defaults)
}

func callSettings(t *testing.T, h http.Handler, method, body string) (*httptest.ResponseRecorder, buildSettingsView) {
	t.Helper()
	req := httptest.NewRequest(method, "/internal/settings/build", strings.NewReader(body))
	req.RemoteAddr = "172.168.1.50:51234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var v buildSettingsView
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("decode %s response: %v\n%s", method, err, rec.Body)
		}
	}
	return rec, v
}

func TestGetBuildSettingsBeforeAnySaveReportsEnv(t *testing.T) {
	rec, v := callSettings(t, settingsServer(&settingsStore{}), http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if v.Model != "claude-opus-5" || v.Effort != "xhigh" || v.Source != buildsettings.SourceEnv || v.UpdatedAt != nil {
		t.Errorf("got %+v, want the env defaults", v)
	}
	if len(v.Options.Models) != len(buildsettings.Models) || len(v.Options.Efforts) != len(buildsettings.Efforts) {
		t.Errorf("options = %+v, want every model and effort", v.Options)
	}
}

func TestPutBuildSettingsSavesAndRecordsWho(t *testing.T) {
	st := &settingsStore{}
	h := settingsServer(st)

	rec, v := callSettings(t, h, http.MethodPut, `{"model":"claude-opus-5-5","effort":"high"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if v.Model != "claude-opus-5-5" || v.Effort != "high" || v.Source != buildsettings.SourceSaved || v.UpdatedAt == nil {
		t.Errorf("PUT response = %+v, want the saved claude-opus-5-5/high", v)
	}
	if st.saved.UpdatedFrom != "172.168.1.50" {
		t.Errorf("updated_from = %q, want the caller's host without its port", st.saved.UpdatedFrom)
	}

	_, got := callSettings(t, h, http.MethodGet, "")
	if got.Model != "claude-opus-5-5" || got.Source != buildsettings.SourceSaved || got.UpdatedFrom != "172.168.1.50" {
		t.Errorf("GET after PUT = %+v", got)
	}
}

// Anything off the list is refused before it reaches the store: saved, it
// would be handed to the CLI by every build that follows.
func TestPutBuildSettingsRefusesAnythingElse(t *testing.T) {
	for _, body := range []string{
		`{"model":"claude-opus-5-6","effort":"high"}`,
		`{"model":"opus","effort":"high"}`,
		`{"model":"claude-opus-5","effort":"extreme"}`,
		`{"model":"claude-opus-5","effort":"high","budget":10}`,
		`not json`,
	} {
		st := &settingsStore{}
		rec, _ := callSettings(t, settingsServer(st), http.MethodPut, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: status %d, want 400", body, rec.Code)
		}
		if st.saves != 0 {
			t.Errorf("PUT %s reached the store", body)
		}
	}
}
```

Append to `internal/claude/claude_test.go`:

```go
func TestImproveArgsPassModelAndEffort(t *testing.T) {
	got := improveArgs("improve this", "claude-opus-5-5", "xhigh")
	want := []string{"-p", "improve this", "--dangerously-skip-permissions", "--model", "claude-opus-5-5", "--effort", "xhigh"}
	if !slices.Equal(got, want) {
		t.Errorf("improveArgs =\n  %q\nwant\n  %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/api/ ./internal/claude/`
Expected: FAIL — `undefined: buildSettingsView`, `too many arguments in call to NewServer` / type mismatch, `undefined: improveArgs`

- [ ] **Step 3: Implement the handlers**

Create `internal/api/buildsettings.go`:

```go
package api

import (
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// buildSettingsView is the GET/PUT /internal/settings/build response: the
// model and effort the next build will use, where that came from, and every
// choice the dashboard may offer — with prices, because nothing else stands
// between an operator and an expensive pick.
type buildSettingsView struct {
	Model       string               `json:"model"`
	Effort      string               `json:"effort"`
	Source      buildsettings.Source `json:"source"`
	UpdatedAt   *time.Time           `json:"updated_at"`
	UpdatedFrom string               `json:"updated_from"`
	Options     buildSettingsOptions `json:"options"`
}

type buildSettingsOptions struct {
	Models  []buildsettings.Model `json:"models"`
	Efforts []string              `json:"efforts"`
}

func settingsViewOf(res buildsettings.Resolved) buildSettingsView {
	return buildSettingsView{
		Model:       res.Model,
		Effort:      res.Effort,
		Source:      res.Source,
		UpdatedAt:   res.UpdatedAt,
		UpdatedFrom: res.UpdatedFrom,
		Options:     buildSettingsOptions{Models: buildsettings.Models, Efforts: buildsettings.Efforts},
	}
}

// handleGetBuildSettings returns the model/effort the next build will use.
func (s *server) handleGetBuildSettings(w http.ResponseWriter, r *http.Request) {
	res := buildsettings.Resolve(r.Context(), s.store, s.claudeDefaults, s.logger)
	s.writeJSON(w, r, http.StatusOK, settingsViewOf(res))
}

// putBuildSettingsBody is the PUT /internal/settings/build payload.
type putBuildSettingsBody struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// handlePutBuildSettings saves a new model/effort. Only listed values pass.
// Every change is logged with the caller's address: with no auth on
// /internal, that log line and updated_from are the only record of who moved
// a setting that drives spend.
func (s *server) handlePutBuildSettings(w http.ResponseWriter, r *http.Request) {
	var body putBuildSettingsBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := buildsettings.Validate(body.Model, body.Effort); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	prev := buildsettings.Resolve(r.Context(), s.store, s.claudeDefaults, s.logger)
	bs := &domain.BuildSettings{Model: body.Model, Effort: body.Effort, UpdatedFrom: clientHost(r)}
	if err := s.store.SaveBuildSettings(r.Context(), bs); err != nil {
		s.logger.Error("save build settings failed", "err", err,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "save build settings failed")
		return
	}
	s.logger.Info("build settings changed",
		"from_model", prev.Model, "from_effort", prev.Effort,
		"model", bs.Model, "effort", bs.Effort, "by", bs.UpdatedFrom)

	at := bs.UpdatedAt
	s.writeJSON(w, r, http.StatusOK, settingsViewOf(buildsettings.Resolved{
		Model: bs.Model, Effort: bs.Effort, Source: buildsettings.SourceSaved,
		UpdatedAt: &at, UpdatedFrom: bs.UpdatedFrom,
	}))
}

// clientHost is the caller's address without its port. RemoteAddr is
// "ip:port" directly, or a bare IP once middleware.RealIP has applied a
// forwarding header — which the client supplies, so this is an audit hint,
// not proof.
func clientHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
```

- [ ] **Step 4: Wire the server**

In `internal/api/api.go`:

Add the import `"github.com/Watthachai/fitt-coderunner/internal/buildsettings"` to the internal-imports group.

In `type server struct`, replace

```go
	// claudeModel optionally pins the model for the improve-skill run (empty = CLI
	// default).
	claudeModel string
```

with

```go
	// claudeDefaults is the environment's model/effort. Both the settings
	// endpoints and the improve-skill run resolve against it
	// (internal/buildsettings), so every Claude call made here names a model
	// and an effort explicitly.
	claudeDefaults buildsettings.Defaults
```

Change `NewServer`'s signature to end with `claudeBin string, claudeDefaults buildsettings.Defaults) http.Handler {`, and in its `&server{…}` literal replace `claudeModel:   claudeModel,` with `claudeDefaults: claudeDefaults,`.

After the `r.Post("/internal/skills/{name}/improve", s.handleImproveSkill)` line, add:

```go

	// The Claude model/effort builds run with (no auth; operator console). A
	// change applies to the next build that reaches its Claude run.
	r.Get("/internal/settings/build", s.handleGetBuildSettings)
	r.Put("/internal/settings/build", s.handlePutBuildSettings)
```

In `handleImproveSkill`, replace

```go
	improved, err := claude.Improve(r.Context(), s.claudeBin, s.claudeModel, body.Body, s.logger)
```

with

```go
	cfg := buildsettings.Resolve(r.Context(), s.store, s.claudeDefaults, s.logger)
	improved, err := claude.Improve(r.Context(), s.claudeBin, cfg.Model, cfg.Effort, body.Body, s.logger)
```

In `internal/claude/claude.go`:

Add directly above `func Improve(`:

```go
// improveArgs builds the one-shot `claude` command line for Improve.
func improveArgs(prompt, model, effort string) []string {
	args := []string{"-p", prompt, "--dangerously-skip-permissions"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	return args
}
```

Change `Improve`'s signature to `func Improve(ctx context.Context, binPath, model, effort, skillBody string, logger *slog.Logger) (string, error) {`, update its doc comment's command to `claude -p <prompt> --dangerously-skip-permissions [--model model] [--effort effort]`, and replace

```go
	args := []string{"-p", prompt, "--dangerously-skip-permissions"}
	if model != "" {
		args = append(args, "--model", model)
	}
```

with

```go
	args := improveArgs(prompt, model, effort)
```

In `cmd/server/main.go`, replace the last argument `cfg.ClaudeModel` of the `api.NewServer(…)` call with `claudeDefaults`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `gofmt -l internal/ cmd/ && go vet ./... && go test ./...`
Expected: no gofmt/vet output; every package `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal/api/buildsettings.go internal/api/buildsettings_test.go internal/api/api.go internal/claude/claude.go internal/claude/claude_test.go cmd/server/main.go
git commit -F - <<'MSG'
feat(api): read and change the build's model and effort

GET/PUT /internal/settings/build. Only listed values are accepted, and
each change is logged with who made it — no auth on /internal, so that
is the only record. Improve-skill resolves the same setting, so no
Claude call in CRN is left to the CLI's defaults anymore.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 7: Frontend data layer

**Files:**
- Create: `frontend/app/lib/modelLabel.ts`
- Test: `frontend/app/lib/modelLabel.test.ts` (new)
- Modify: `frontend/tsconfig.json`
- Modify: `frontend/app/lib/types.ts`
- Modify: `frontend/app/lib/config.ts`
- Create: `frontend/app/lib/useBuildSettings.ts`

**Interfaces:**
- Consumes: the Task 6 JSON shapes
- Produces:
  - `modelLabel(id: string): string`, `claudeLabel(model?: string, effort?: string): string`
  - `interface ModelOption {id; label; input_usd_per_mtok; output_usd_per_mtok}`, `interface BuildSettings {model; effort; source: "env" | "saved"; updated_at: string | null; updated_from; options: {models: ModelOption[]; efforts: string[]}}`
  - `BuildingJob.claude_model?`, `.claude_effort?`; `BuildTrace.claude_model?`, `.claude_effort?`
  - `buildSettingsUrl(): string`
  - `useBuildSettings(): {data: BuildSettings | null; error: string | null; saving: boolean; save: (model: string, effort: string) => Promise<boolean>}`

All commands in this task run from `frontend/`.

- [ ] **Step 1: Write the failing test**

Create `frontend/app/lib/modelLabel.test.ts`:

```ts
// Runs under Node's own test runner, which executes .ts natively:
//   node --test app/lib/modelLabel.test.ts
// Excluded from tsconfig: Node needs the ".ts" import extension below, which
// tsc refuses without allowImportingTsExtensions.
import { test } from "node:test";
import assert from "node:assert/strict";
import { claudeLabel, modelLabel } from "./modelLabel.ts";

test("modelLabel turns model ids into names", () => {
  assert.equal(modelLabel("claude-opus-5-5"), "Opus 5.5");
  assert.equal(modelLabel("claude-opus-5"), "Opus 5");
  assert.equal(modelLabel("claude-sonnet-5"), "Sonnet 5");
  assert.equal(modelLabel("claude-fable-5-1"), "Fable 5.1");
});

test("modelLabel leaves anything else as it is", () => {
  assert.equal(modelLabel("opus"), "opus"); // an env alias
  assert.equal(modelLabel("claude-opus-4-5-20251101"), "claude-opus-4-5-20251101");
  assert.equal(modelLabel(""), "");
});

test("claudeLabel joins model and effort, and is empty when nothing ran", () => {
  assert.equal(claudeLabel("claude-opus-5-5", "xhigh"), "Opus 5.5 · xhigh");
  assert.equal(claudeLabel("claude-opus-5", ""), "Opus 5");
  assert.equal(claudeLabel(undefined, undefined), "");
  assert.equal(claudeLabel("", "xhigh"), "");
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `node --test app/lib/modelLabel.test.ts`
Expected: FAIL — `Cannot find module '…/app/lib/modelLabel.ts'`

- [ ] **Step 3: Implement the labels**

Create `frontend/app/lib/modelLabel.ts`:

```ts
/**
 * Human label for a Claude model id: "claude-opus-5-5" -> "Opus 5.5",
 * "claude-fable-5-1" -> "Fable 5.1", "claude-sonnet-5" -> "Sonnet 5".
 * Anything not shaped "claude-<family>-<n>[-<n>]" comes back unchanged, so an
 * env alias like "opus" still shows as itself.
 *
 * No imports, so it runs under `node --test` without a bundler.
 */
export function modelLabel(id: string): string {
  const m = /^claude-([a-z]+)-(\d+)(?:-(\d+))?$/.exec(id);
  if (!m) return id;
  const family = m[1].charAt(0).toUpperCase() + m[1].slice(1);
  return m[3] === undefined ? `${family} ${m[2]}` : `${family} ${m[2]}.${m[3]}`;
}

/** "Opus 5.5 · xhigh"; "" when the build recorded no model. */
export function claudeLabel(model?: string, effort?: string): string {
  if (!model) return "";
  return effort ? `${modelLabel(model)} · ${effort}` : modelLabel(model);
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `node --test app/lib/modelLabel.test.ts`
Expected: `ℹ pass 3`, `ℹ fail 0`

- [ ] **Step 5: Keep the test out of tsc**

In `frontend/tsconfig.json`, change

```json
  "exclude": [
    "node_modules"
  ]
```

to

```json
  "exclude": [
    "node_modules",
    "**/*.test.ts"
  ]
```

- [ ] **Step 6: Types, URL, and hook**

In `frontend/app/lib/types.ts`, inside `interface BuildingJob`, after `started_at: string | null;`, add:

```ts
  // What this build's Claude run uses; "" (or absent) until it gets there.
  claude_model?: string;
  claude_effort?: string;
```

Inside `interface BuildTrace`, after `error_msg: string;`, add:

```ts
  // What the build's Claude run used; "" for builds before this was recorded
  // and for builds that reused a cached image.
  claude_model?: string;
  claude_effort?: string;
```

At the end of `types.ts`, add:

```ts
// GET/PUT /internal/settings/build — the Claude model/effort builds run with.
export interface ModelOption {
  id: string;
  label: string;
  input_usd_per_mtok: number;
  output_usd_per_mtok: number;
}

export interface BuildSettings {
  model: string;
  effort: string;
  source: "env" | "saved";
  updated_at: string | null;
  updated_from: string;
  options: {
    models: ModelOption[];
    efforts: string[];
  };
}
```

In `frontend/app/lib/config.ts`, directly above the `healthUrl` doc comment, add:

```ts
// The Claude model and effort builds run with (no auth).
//   GET /internal/settings/build             -> BuildSettings
//   PUT /internal/settings/build {model, effort} -> BuildSettings · 400 off-list
// A change applies to the next build that reaches its Claude run; a running
// build keeps what it started with.
export function buildSettingsUrl(): string {
  return `${API_BASE}/internal/settings/build`;
}

```

Create `frontend/app/lib/useBuildSettings.ts`:

```ts
"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { buildSettingsUrl } from "./config";
import type { BuildSettings } from "./types";

/**
 * useBuildSettings loads the Claude model/effort builds run with and saves a
 * new choice. Loaded once, not polled: the value only changes when someone
 * saves it, and saving it goes through here.
 */
export function useBuildSettings(): {
  data: BuildSettings | null;
  error: string | null;
  saving: boolean;
  save: (model: string, effort: string) => Promise<boolean>;
} {
  const [data, setData] = useState<BuildSettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    void (async () => {
      try {
        const res = await fetch(buildSettingsUrl(), { cache: "no-store" });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = (await res.json()) as BuildSettings;
        if (alive.current) setData(json);
      } catch (e) {
        if (alive.current) setError(e instanceof Error ? e.message : "load failed");
      }
    })();
    return () => {
      alive.current = false;
    };
  }, []);

  const save = useCallback(async (model: string, effort: string) => {
    setSaving(true);
    setError(null);
    try {
      const res = await fetch(buildSettingsUrl(), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ model, effort }),
      });
      const json = (await res.json().catch(() => null)) as
        | BuildSettings
        | { error?: string }
        | null;
      if (!res.ok) {
        const reason =
          json && "error" in json && json.error ? json.error : `HTTP ${res.status}`;
        throw new Error(reason);
      }
      if (alive.current) setData(json as BuildSettings);
      return true;
    } catch (e) {
      if (alive.current) setError(e instanceof Error ? e.message : "save failed");
      return false;
    } finally {
      if (alive.current) setSaving(false);
    }
  }, []);

  return { data, error, saving, save };
}
```

- [ ] **Step 7: Typecheck**

Run: `npx tsc --noEmit && node --test app/lib/modelLabel.test.ts`
Expected: tsc prints nothing and exits 0; the tests still pass.

- [ ] **Step 8: Commit**

```bash
git add app/lib/modelLabel.ts app/lib/modelLabel.test.ts app/lib/types.ts app/lib/config.ts app/lib/useBuildSettings.ts tsconfig.json
git commit -F - <<'MSG'
feat(console): load and save the build's model and effort

Types and URL for /internal/settings/build, a hook that loads and saves
it, and one function that turns a model id into the name people use —
tested with Node's own runner, which runs .ts without a bundler.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 8: The build model panel

**Files:**
- Create: `frontend/app/components/BuildSettingsPanel.tsx`
- Modify: `frontend/app/page.tsx`
- Modify: `frontend/app/globals.css`

**Interfaces:**
- Consumes: `useBuildSettings`, `modelLabel` (Task 7); `formatRelative` (`app/lib/format.ts`); `useTick` (`app/lib/useTick.ts`)
- Produces: `<BuildSettingsPanel />` (no props)

All commands in this task run from `frontend/`.

- [ ] **Step 1: Write the panel**

Create `frontend/app/components/BuildSettingsPanel.tsx`:

```tsx
"use client";

import { useEffect, useState } from "react";
import { useBuildSettings } from "../lib/useBuildSettings";
import { modelLabel } from "../lib/modelLabel";
import { formatRelative } from "../lib/format";
import { useTick } from "../lib/useTick";

// The Claude model and effort builds run with, and the control that changes
// them. /internal has no auth, so every choice shows its price and the header
// says who changed it last — visibility is the only guardrail.
export function BuildSettingsPanel() {
  const { data, error, saving, save } = useBuildSettings();
  const nowMs = useTick(30_000);
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [justSaved, setJustSaved] = useState(false);

  // Follow the server's value when it loads and after each save.
  useEffect(() => {
    if (!data) return;
    setModel(data.model);
    setEffort(data.effort);
  }, [data]);

  if (!data) {
    return (
      <section className="panel">
        <header className="panel-head">
          <h2>Build model</h2>
        </header>
        <p className={error ? "alert" : "empty"}>
          {error ? `Couldn’t load build settings — ${error}.` : "Loading…"}
        </p>
      </section>
    );
  }

  const isListed = (id: string) => data.options.models.some((m) => m.id === id);
  const dirty = model !== data.model || effort !== data.effort;
  // An env alias ("opus") is shown but can't be saved — the server would refuse it.
  const canSave = dirty && isListed(model) && !saving;
  const origin =
    data.source === "saved"
      ? `saved ${formatRelative(data.updated_at, nowMs)}${
          data.updated_from ? ` from ${data.updated_from}` : ""
        }`
      : "default from env";

  async function onSave() {
    setJustSaved(false);
    setJustSaved(await save(model, effort));
  }

  return (
    <section className="panel">
      <header className="panel-head">
        <h2>Build model</h2>
        <span className="panel-meta">
          {modelLabel(data.model)} · {data.effort} — {origin}
        </span>
      </header>

      <div className="settings-row">
        <label className="field">
          <span className="field-label">model</span>
          <select
            className="input"
            value={model}
            disabled={saving}
            onChange={(e) => {
              setModel(e.target.value);
              setJustSaved(false);
            }}
          >
            {isListed(data.model) ? null : (
              <option value={data.model}>{data.model} (from env)</option>
            )}
            {data.options.models.map((m) => (
              <option key={m.id} value={m.id}>
                {`${m.label} · $${m.input_usd_per_mtok} in / $${m.output_usd_per_mtok} out per 1M tokens`}
              </option>
            ))}
          </select>
        </label>

        <label className="field field--narrow">
          <span className="field-label">effort</span>
          <select
            className="input"
            value={effort}
            disabled={saving}
            onChange={(e) => {
              setEffort(e.target.value);
              setJustSaved(false);
            }}
          >
            {data.options.efforts.map((e) => (
              <option key={e} value={e}>
                {e}
              </option>
            ))}
          </select>
        </label>

        <button type="button" className="btn" disabled={!canSave} onClick={onSave}>
          {saving ? "saving…" : "save"}
        </button>
      </div>

      <p className="settings-note">
        Applies to the next build that starts its Claude run — a running build
        keeps what it started with.{justSaved ? " Saved." : ""}
      </p>
      {error ? <p className="alert">Couldn’t save — {error}.</p> : null}
    </section>
  );
}
```

- [ ] **Step 2: Put it on the dashboard**

In `frontend/app/page.tsx`, add the import after the `BuildStamp` import:

```tsx
import { BuildSettingsPanel } from "./components/BuildSettingsPanel";
```

and directly after `<VitalsRail vitals={data?.vitals ?? null} />` add:

```tsx

      <div className="row">
        <BuildSettingsPanel />
      </div>
```

- [ ] **Step 3: Styles**

Append to `frontend/app/globals.css`:

```css
/* --- build model panel: the Claude model/effort builds run with --- */
.settings-row {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 0.85rem;
}

.settings-row .field {
  flex: 1 1 320px;
  min-width: 0;
}

.settings-row .field--narrow {
  flex: 0 1 150px;
}

.settings-note {
  margin: 0.65rem 0 0;
  font-size: 0.72rem;
  color: var(--fg-dim);
}
```

- [ ] **Step 4: Verify**

Run: `npx tsc --noEmit && npm run build`
Expected: tsc exits 0; `next build` completes (`✓ Compiled successfully`, route `/` listed) with no type errors.

- [ ] **Step 5: Commit**

```bash
git add app/components/BuildSettingsPanel.tsx app/page.tsx app/globals.css
git commit -F - <<'MSG'
feat(console): a panel to see and change the build's model and effort

Current model and effort, where they came from and who changed them
last, two dropdowns with the price on every model, and a note that a
running build keeps what it started with.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 9: Model badges on running and past builds

**Files:**
- Modify: `frontend/app/components/NowBuilding.tsx`
- Modify: `frontend/app/components/TraceSpanGraph.tsx`
- Modify: `frontend/app/globals.css`

**Interfaces:**
- Consumes: `claudeLabel` (Task 7); `BuildingJob.claude_model/effort`, `BuildTrace.claude_model/effort` (Task 7)

All commands in this task run from `frontend/`.

- [ ] **Step 1: Running build**

In `frontend/app/components/NowBuilding.tsx`, add the import after the `format` import:

```tsx
import { claudeLabel } from "../lib/modelLabel";
```

In `BuildingCard`, directly before `<span className="bc-build">build #{job.build_no}</span>`, add:

```tsx
          {job.claude_model ? (
            <span className="bc-build" title="Claude model · effort this build runs with">
              {claudeLabel(job.claude_model, job.claude_effort)}
            </span>
          ) : null}
```

- [ ] **Step 2: Build history**

In `frontend/app/components/TraceSpanGraph.tsx`, add the import after the `format` import:

```tsx
import { claudeLabel } from "../lib/modelLabel";
```

In the `trace-hud`, directly after the `cost` cell's closing `</div>`, add:

```tsx
        <div className="hud-cell">
          <span className="hud-label">model</span>
          <span className="hud-num hud-num--text">
            {claudeLabel(trace.claude_model, trace.claude_effort) || "—"}
          </span>
        </div>
```

- [ ] **Step 3: Styles**

In `frontend/app/globals.css`, change the `.trace-hud` rule's comment and template to five cells:

```css
/* --- per-build telemetry, five equal cells (overrides .hud's template) --- */
.trace-hud {
  grid-template-columns: repeat(5, minmax(0, 1fr));
  margin-bottom: 0.9rem;
}
```

and append:

```css
/* A word-valued hud cell (the model): numbers stay big, words must fit. */
.hud-num--text {
  font-size: 0.95rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
```

- [ ] **Step 4: Verify**

Run: `npx tsc --noEmit && npm run build`
Expected: tsc exits 0; `next build` completes with no type errors.

- [ ] **Step 5: Commit**

```bash
git add app/components/NowBuilding.tsx app/components/TraceSpanGraph.tsx app/globals.css
git commit -F - <<'MSG'
feat(console): show which model a build ran on

A badge beside "build #N" while it runs, and a model cell next to cost
in the build history — so a switch to a cheaper model can be judged by
what builds actually cost afterwards. Builds from before, and builds
that reused a cached image, show a dash.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_012dsVHNHQLdEc7pXHuUY74k
MSG
```

---

### Task 10: Deploy to the build machine and verify

**Files:** none changed. Build machine: `macagents@172.168.1.171`, repo `~/fitt-coderunner`, branch `dev`, backend `:8080`, dashboard `next dev` on `:3000`.

- [ ] **Step 1: Everything green locally**

Run (repo root): `gofmt -l . && go vet ./... && go test ./...`
Run (`frontend/`): `npx tsc --noEmit && node --test app/lib/modelLabel.test.ts && npm run build`
Expected: no gofmt/vet output; all Go packages `ok`; tsc exit 0; `ℹ fail 0`; build succeeds.

- [ ] **Step 2: Get the user's go-ahead**

Restarting CRN kills any build in flight, and the new code changes what every later build runs with. Stop and ask the user before continuing.

- [ ] **Step 3: Push**

Run: `git push origin dev`

- [ ] **Step 4: Pull, build, migrate**

```bash
ssh macagents@172.168.1.171 'export PATH=/opt/homebrew/bin:/usr/local/bin:$PATH; cd ~/fitt-coderunner && git pull --ff-only && make build && make migrate'
```

Expected: pull fast-forwards to the new HEAD; `go build` succeeds; `make migrate` prints `skip   0001_init` … `skip   0011_concurrent_builds` and applies `0012_build_settings`.

Confirm the schema: `ssh macagents@172.168.1.171 'export PATH=/usr/local/bin:/opt/homebrew/bin:$PATH; docker exec crn-postgres psql -U crn -d crn -c "\d build_settings"'`
Expected: the table with `claude_model`, `claude_effort`, `updated_at`, `updated_from`.

- [ ] **Step 5: Wait until nothing is building**

Check right before restarting — a build can start while Step 4 runs, and the restart would kill it.

Run: `curl -s http://172.168.1.171:8080/internal/dashboard | python3 -c "import json,sys; print(json.load(sys.stdin)['vitals'])"`
Expected: `'building': 0` and `'queued': 0`. If not, wait and re-check — never restart over a running build. (The migration from Step 4 is already safe under the old binary.)

- [ ] **Step 6: Restart safely (never `make restart` over ssh)**

```bash
ssh macagents@172.168.1.171 'cd ~/fitt-coderunner && make stop && ( set -a; . ./.env; set +a; nohup ./bin/crn-server > ~/crn.log 2>&1 & )'
```

The ssh session has now ended. Verify from this machine:

```bash
curl -s http://172.168.1.171:8080/healthz
curl -s http://172.168.1.171:8080/internal/settings/build
ssh macagents@172.168.1.171 'tail -5 ~/crn.log'
```

Expected: `/healthz` reports the new commit with `modified: false`; settings returns `"model":"claude-opus-5","effort":"xhigh","source":"env"` and four models; `~/crn.log` shows `starting CRN … claude_model=claude-opus-5 claude_effort=xhigh` (logs are back in a file).

- [ ] **Step 7: Check the dashboard**

Open `http://172.168.1.171:3000` (the `next dev` server picks the pulled code up by itself). Expected: a **Build model** panel under the vitals showing `Opus 5 · xhigh — default from env`, a model dropdown listing the four models with prices, and the effort dropdown; the save button disabled until something changes. Open a project page: build history shows a `model` cell with `—` for old builds.

- [ ] **Step 8: Hand the choice to the user**

Which model and effort to run is a spend decision: ask the user what to set (e.g. Opus 5.5 · xhigh). Save it on the dashboard, then confirm:

```bash
curl -s http://172.168.1.171:8080/internal/settings/build
ssh macagents@172.168.1.171 'grep "build settings changed" ~/crn.log | tail -1'
```

Expected: `"source":"saved"`, the chosen values, `updated_from` = the browser's IP; the log line shows old → new and `by=`.

- [ ] **Step 9: First build on the new setting**

A build costs real money — wait for the next real build, or ask the user before triggering one. While it runs, the dashboard shows the badge beside `build #N`. When it finishes:

```bash
curl -s 'http://172.168.1.171:8080/internal/traces?limit=1' | python3 -c "import json,sys; t=json.load(sys.stdin)['traces'][0]; print(t['build_no'], t['outcome'], t['claude_model'], t['claude_effort'], t['cost_usd'])"
```

Expected: the chosen model and effort next to the build's cost.

If that first build is an **edit** build, it resumes a Claude session the previous model created; the new model drops the old model's thinking blocks. Claude Code supports switching model on resume, but check this build's outcome before trusting the switch on edit builds — its trace names the model, so a failure here is traceable to the switch.
