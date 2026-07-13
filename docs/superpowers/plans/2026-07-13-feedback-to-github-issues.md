# Feedback → GitHub Issues Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mirror every in-demo feedback request to a GitHub issue in the project's repo, and drive its lifecycle (approve → build → summary + close; reject → close "not planned") so GitHub holds the full feedback history.

**Architecture:** A background **watcher** goroutine (started only when the owner model is on) creates an open issue for each new `feedback_requests` row it finds with `issue_number IS NULL`, writing the number back. State changes originate inside CRN handlers, so **approve/reject** update the issue inline, and the existing edit-build success path (which already carries `issue_number`) is extended to post a summary and close. `feedback_requests` stays the source of truth; GitHub is a one-way mirror.

**Tech Stack:** Go (std-lib `os/exec` shelling to the authenticated `gh` CLI — no SDK, matching `internal/github`), Postgres via pgx, PostgREST (unchanged), Next.js/React dashboard.

## Global Constraints

- Module path: `github.com/Watthachai/fitt-coderunner`. Internal imports use this prefix.
- GitHub work shells to `gh` only, via the existing `runGH` in `internal/github/github.go`. No new dependencies.
- **Every `gh` call is best-effort**: failures are logged and swallowed — they must never block feedback intake (PostgREST) or a build.
- The feature is gated on the owner model: active only when `CRN_GITHUB_OWNER != ""`. When empty, the watcher is not started and the handler paths no-op.
- **Idempotent creation:** a feedback row's `issue_number` is written only after a successful `gh issue create`; the watcher's query excludes rows that already have one.
- `feedback_requests` remains the source of truth. GitHub is one-way (issues edited on GitHub do not write back).
- `frontend/app/lib/types.ts` mirrors `internal/domain` — keep the TS `FeedbackRequest` in sync with the Go struct (it is the stated contract).
- Run `go build ./...` after every Go task; it must stay green.

---

### Task 1: Migration 0008 + apply-all migration wiring

**Files:**
- Create: `migrations/0008_feedback_issue_link.sql`
- Modify: `docker-compose.yml` (postgres `volumes:` initdb list, after the `0007` line ~L36)
- Modify: `Makefile` (`migrate:` target ~L53-56)

**Interfaces:**
- Produces: columns `feedback_requests.issue_number INT NULL` and `feedback_requests.issue_url TEXT NOT NULL DEFAULT ''`.

- [ ] **Step 1: Write the migration**

Create `migrations/0008_feedback_issue_link.sql`:

```sql
-- 0008_feedback_issue_link.sql
-- Link each feedback request to the GitHub issue CRN mirrors it into
-- (docs/superpowers/specs/2026-07-13-feedback-to-github-issues-design.md).
-- web_anon still only INSERTs the widget-supplied columns; these default on
-- insert (NULL / '') and are filled later by CRN, so no new grant is needed.
ALTER TABLE feedback_requests ADD COLUMN IF NOT EXISTS issue_number INT;
ALTER TABLE feedback_requests ADD COLUMN IF NOT EXISTS issue_url    TEXT NOT NULL DEFAULT '';

-- The watcher scans for un-mirrored rows; index just those.
CREATE INDEX IF NOT EXISTS idx_feedback_unmirrored
  ON feedback_requests (created_at) WHERE issue_number IS NULL;
```

- [ ] **Step 2: Mount 0008 for fresh volumes**

In `docker-compose.yml`, in the `postgres` service `volumes:` list, after the `0007` line add:

```yaml
      - ./migrations/0008_feedback_issue_link.sql:/docker-entrypoint-initdb.d/0008_feedback_issue_link.sql:ro
```

- [ ] **Step 3: Make `migrate` apply ALL migrations (existing-volume path)**

The initdb mount only runs on a *fresh* volume, and the current `migrate` target applies only `0001`. Replace the `migrate:` target in `Makefile` with one that pipes every migration in over stdin (works on the already-running volume, no recreate needed):

```make
migrate: ## Apply all migrations/*.sql to Postgres in lexical order (idempotent).
	@for f in $$(ls $(MIGRATIONS_DIR)/*.sql | sort); do \
		echo "applying $$f"; \
		docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U crn -d crn < $$f || exit 1; \
	done
```

- [ ] **Step 4: Apply and verify against the running DB**

Run: `make migrate`
Then run: `docker exec crn-postgres psql -U crn -d crn -c "\d feedback_requests" | grep -E "issue_number|issue_url"`
Expected: two rows — `issue_number | integer |` and `issue_url | text | not null`.

- [ ] **Step 5: Commit**

```bash
git add migrations/0008_feedback_issue_link.sql docker-compose.yml Makefile
git commit -m "feat(crn): migration 0008 — feedback→issue link columns + apply-all migrate"
```

---

### Task 2: Domain fields + store projection + new store methods

**Files:**
- Modify: `internal/domain/feedback.go` (`FeedbackRequest` struct ~L36-48)
- Modify: `internal/domain/ports.go` (`Store` interface, near the feedback methods ~L129-136)
- Modify: `internal/store/store.go` (`feedbackCols` ~L708, `scanFeedbackRow` ~L776, add two methods after `SetFeedbackStatus` ~L774)

**Interfaces:**
- Consumes: columns from Task 1.
- Produces:
  - `domain.FeedbackRequest.IssueNumber *int`, `.IssueURL string`.
  - `Store.ListFeedbackNeedingIssue(ctx, limit int) ([]*domain.FeedbackRequest, error)`
  - `Store.SetFeedbackIssue(ctx, id uuid.UUID, number int, url string) error`

- [ ] **Step 1: Add the domain fields**

In `internal/domain/feedback.go`, add to `FeedbackRequest` (after `CreatedAt`):

```go
	CreatedAt   time.Time       `json:"created_at"`
	IssueNumber *int            `json:"issue_number,omitempty"` // nil until mirrored
	IssueURL    string          `json:"issue_url,omitempty"`
```

- [ ] **Step 2: Add the interface methods**

In `internal/domain/ports.go`, in the `Store` interface right after `SetFeedbackStatus(...)`:

```go
	// ListFeedbackNeedingIssue returns feedback rows not yet mirrored to a GitHub
	// issue (issue_number IS NULL), oldest first, capped at limit.
	ListFeedbackNeedingIssue(ctx context.Context, limit int) ([]*FeedbackRequest, error)
	// SetFeedbackIssue records the GitHub issue a feedback row was mirrored into.
	SetFeedbackIssue(ctx context.Context, id uuid.UUID, number int, url string) error
```

- [ ] **Step 3: Extend the projection + scan**

In `internal/store/store.go`, change `feedbackCols`:

```go
const feedbackCols = `
	id, project_id, status, category, priority, note, page_url, reporter,
	payload, job_id, created_at, issue_number, issue_url`
```

And extend `scanFeedbackRow` to scan the two new columns (append to the `r.Scan(...)` list, keeping order aligned with `feedbackCols`):

```go
func scanFeedbackRow(r scannable, f *domain.FeedbackRequest, payloadJSON *[]byte) error {
	if err := r.Scan(
		&f.ID, &f.ProjectID, &f.Status, &f.Category, &f.Priority, &f.Note,
		&f.PageURL, &f.Reporter, payloadJSON, &f.JobID, &f.CreatedAt,
		&f.IssueNumber, &f.IssueURL,
	); err != nil {
		return mapErr(err, "store: scan feedback")
	}
	return nil
}
```

- [ ] **Step 4: Add the two store methods**

In `internal/store/store.go`, after `SetFeedbackStatus`:

```go
// ListFeedbackNeedingIssue returns un-mirrored feedback (issue_number IS NULL),
// oldest first, capped at limit. Non-nil slice.
func (s *pgStore) ListFeedbackNeedingIssue(ctx context.Context, limit int) ([]*domain.FeedbackRequest, error) {
	rows, err := s.pool.Query(ctx, `SELECT`+feedbackCols+`
		FROM feedback_requests WHERE issue_number IS NULL
		ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list feedback needing issue: %w", err)
	}
	defer rows.Close()

	out := []*domain.FeedbackRequest{}
	for rows.Next() {
		f := &domain.FeedbackRequest{}
		var payloadJSON []byte
		if err := scanFeedbackRow(rows, f, &payloadJSON); err != nil {
			return nil, err
		}
		if err := decodeFeedbackPayload(payloadJSON, f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan feedback needing issue: %w", err)
	}
	return out, nil
}

// SetFeedbackIssue records the GitHub issue a feedback row was mirrored into.
func (s *pgStore) SetFeedbackIssue(ctx context.Context, id uuid.UUID, number int, url string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE feedback_requests SET issue_number = $2, issue_url = $3
		WHERE id = $1`, id, number, url)
	if err != nil {
		return fmt.Errorf("store: set feedback issue: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("store: set feedback issue: %w", domain.ErrNotFound)
	}
	return nil
}
```

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: success (the store now satisfies the extended `domain.Store`).

- [ ] **Step 6: Manual DB sanity check**

Run: `docker exec crn-postgres psql -U crn -d crn -c "SELECT id, issue_number, issue_url FROM feedback_requests LIMIT 3;"`
Expected: existing rows show `issue_number` empty (NULL) and `issue_url` blank.

- [ ] **Step 7: Commit**

```bash
git add internal/domain/feedback.go internal/domain/ports.go internal/store/store.go
git commit -m "feat(crn): feedback issue-link fields + ListFeedbackNeedingIssue/SetFeedbackIssue"
```

---

### Task 3: Shared repo-slug helpers in `internal/github`

**Files:**
- Create: `internal/github/slug.go`
- Create: `internal/github/slug_test.go`
- Modify: `internal/api/api.go` (replace `branchSlug(` call sites ~L639, L993, L1059, L1208; delete local `branchSlug` ~L471-492 and `repoSlugFromURL` ~L1214-1224; make `repoSlugForProject` ~L1201 delegate)

**Interfaces:**
- Produces: `github.Slugify(s string) string`, `github.SlugFromURL(repoURL string) string`, `github.RepoSlug(owner, repoURL, name, id string) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/github/slug_test.go`:

```go
package github

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Cafe Pre-Order":  "cafe-pre-order",
		"  Hello!!World ": "hello-world",
		"":                "project",
		"日本 shop":         "shop",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSlugFromURL(t *testing.T) {
	if got := SlugFromURL("https://github.com/acme/crn-shop-1234abcd.git"); got != "acme/crn-shop-1234abcd" {
		t.Errorf("got %q", got)
	}
	if got := SlugFromURL("not-a-url"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestRepoSlug(t *testing.T) {
	const id = "abcdef12-0000-0000-0000-000000000000"
	if got := RepoSlug("", "", "X", id); got != "" {
		t.Errorf("empty owner must yield empty, got %q", got)
	}
	if got := RepoSlug("acme", "", "Cafe Shop", id); got != "acme/crn-cafe-shop-abcdef12" {
		t.Errorf("got %q", got)
	}
	if got := RepoSlug("acme", "https://github.com/acme/existing.git", "Whatever", id); got != "acme/existing" {
		t.Errorf("stored URL should win, got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/github/ -run 'Slug|RepoSlug' -v`
Expected: FAIL — `undefined: Slugify` / `SlugFromURL` / `RepoSlug`.

- [ ] **Step 3: Implement the helpers**

Create `internal/github/slug.go` (bodies moved verbatim from api.go's `branchSlug`/`repoSlugFromURL`):

```go
package github

import "strings"

// Slugify lowercases s and collapses non-alphanumerics to single dashes,
// trimming to 40 chars. Empty input yields "project".
func Slugify(s string) string {
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

// SlugFromURL parses "owner/name" out of https://github.com/<owner>/<name>.git.
// Returns "" when the URL is empty or not in that shape.
func SlugFromURL(repoURL string) string {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(repoURL, prefix) {
		return ""
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(repoURL, prefix), ".git")
	if strings.Count(slug, "/") != 1 || strings.HasPrefix(slug, "/") || strings.HasSuffix(slug, "/") {
		return ""
	}
	return slug
}

// RepoSlug derives "owner/name" for a project under the "one repo per project"
// model: it prefers the project's stored repo URL and falls back to
// "owner/crn-<slug>-<id8>". Returns "" when owner is empty (model disabled).
func RepoSlug(owner, repoURL, name, id string) string {
	if owner == "" {
		return ""
	}
	if slug := SlugFromURL(repoURL); slug != "" {
		return slug
	}
	idShort := id
	if len(idShort) > 8 {
		idShort = idShort[:8]
	}
	return owner + "/crn-" + Slugify(name) + "-" + idShort
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/github/ -run 'Slug|RepoSlug' -v`
Expected: PASS.

- [ ] **Step 5: Refactor api.go to use the shared helpers**

In `internal/api/api.go`:
1. Delete the local `func branchSlug(...)` (~L471-492) and `func repoSlugFromURL(...)` (~L1214-1224).
2. Replace each `branchSlug(` call with `github.Slugify(` — sites at ~L639 (`name = branchSlug(name)`), ~L993 and ~L1059 (`"crn/" + branchSlug(...) + "-" + ...`).
3. Replace the body of `repoSlugForProject`:

```go
func (s *server) repoSlugForProject(p *domain.Project) string {
	return github.RepoSlug(s.githubOwner, p.RepoURL, p.Name, p.ID.String())
}
```

- [ ] **Step 6: Build**

Run: `go build ./...`
Expected: success (no remaining `branchSlug`/`repoSlugFromURL` references; `github` is already imported in api.go).

- [ ] **Step 7: Commit**

```bash
git add internal/github/slug.go internal/github/slug_test.go internal/api/api.go
git commit -m "refactor(crn): share repo-slug logic in internal/github (Slugify/SlugFromURL/RepoSlug)"
```

---

### Task 4: `CreateIssue` / `EnsureLabels` / `CloseIssue` in `internal/github`

**Files:**
- Modify: `internal/github/github.go` (add after `CommentIssue` ~L100)
- Create: `internal/github/github_test.go`

**Interfaces:**
- Produces:
  - `github.CreateIssue(ctx, repoSlug, title, body string, labels []string, logger) (Issue, error)`
  - `github.EnsureLabels(ctx, repoSlug string, logger) error`
  - `github.CloseIssue(ctx, repoSlug string, number int, reason, comment string, logger) error`

- [ ] **Step 1: Write the failing test (pure URL parse)**

Create `internal/github/github_test.go`:

```go
package github

import "testing"

func TestIssueNumberFromURL(t *testing.T) {
	n, err := issueNumberFromURL("https://github.com/acme/repo/issues/42")
	if err != nil || n != 42 {
		t.Fatalf("got n=%d err=%v", n, err)
	}
	if _, err := issueNumberFromURL("https://github.com/acme/repo/issues/"); err == nil {
		t.Fatal("expected error on trailing slash")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/github/ -run IssueNumberFromURL -v`
Expected: FAIL — `undefined: issueNumberFromURL`.

- [ ] **Step 3: Implement the three funcs + helpers**

Add to `internal/github/github.go` (after `CommentIssue`). Note the added `strconv` import is already present.

```go
// crnFeedbackLabels is the fixed label set feedback issues use. EnsureLabels
// upserts all of them so any category/priority combination is valid.
var crnFeedbackLabels = []struct{ name, color, desc string }{
	{"fitt:feedback", "1f6feb", "In-demo feedback mirrored from CRN"},
	{"type:bug", "d73a4a", "Bug report"},
	{"type:feature", "0e8a16", "Feature request"},
	{"type:style", "a2eeef", "Style / polish"},
	{"prio:low", "c5def5", "Low priority"},
	{"prio:med", "fbca04", "Medium priority"},
	{"prio:high", "b60205", "High priority"},
	{"fitt:building", "fef2c0", "Edit build in progress"},
}

// EnsureLabels idempotently upserts the CRN feedback label set on repoSlug
// (`gh label create --force`). Best-effort: per-label failures are logged, not
// returned, so a missing-label race never blocks issue creation.
func EnsureLabels(ctx context.Context, repoSlug string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	log := logger.With("component", "github", "repo", repoSlug)
	for _, l := range crnFeedbackLabels {
		if _, err := runGH(ctx, log, "label", "create", l.name,
			"--repo", repoSlug, "--color", l.color, "--description", l.desc, "--force"); err != nil {
			log.Warn("ensure label failed", "label", l.name, "err", err)
		}
	}
	return nil
}

// CreateIssue opens an issue on repoSlug and returns its number + URL. gh prints
// the issue URL on stdout; the number is parsed from its trailing path segment.
func CreateIssue(ctx context.Context, repoSlug, title, body string, labels []string, logger *slog.Logger) (Issue, error) {
	if logger == nil {
		logger = slog.Default()
	}
	log := logger.With("component", "github", "repo", repoSlug)
	args := []string{"issue", "create", "--repo", repoSlug, "--title", title, "--body", body}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out, err := runGH(ctx, log, args...)
	if err != nil {
		return Issue{}, fmt.Errorf("github: create issue %s: %w", repoSlug, err)
	}
	url := lastLine(out)
	num, err := issueNumberFromURL(url)
	if err != nil {
		return Issue{}, fmt.Errorf("github: parse created issue url %q: %w", url, err)
	}
	log.Info("github issue created", "number", num, "url", url)
	return Issue{Number: num, Title: title, URL: url}, nil
}

// CloseIssue closes issue #number with reason ("completed" | "not planned"),
// optionally leaving a closing comment. Best-effort.
func CloseIssue(ctx context.Context, repoSlug string, number int, reason, comment string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	log := logger.With("component", "github", "repo", repoSlug, "issue", number)
	args := []string{"issue", "close", strconv.Itoa(number), "--repo", repoSlug, "--reason", reason}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	if _, err := runGH(ctx, log, args...); err != nil {
		log.Warn("github close issue failed", "err", err)
		return fmt.Errorf("github: close issue %s#%d: %w", repoSlug, number, err)
	}
	log.Info("github issue closed", "reason", reason)
	return nil
}

// issueNumberFromURL extracts the trailing issue number from a gh issue URL.
func issueNumberFromURL(url string) (int, error) {
	i := strings.LastIndex(url, "/")
	if i < 0 || i == len(url)-1 {
		return 0, fmt.Errorf("no trailing number in %q", url)
	}
	return strconv.Atoi(url[i+1:])
}

// lastLine returns the last non-empty line (gh may print warnings before the URL).
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/github/ -v`
Expected: PASS (all github tests).

- [ ] **Step 5: Commit**

```bash
git add internal/github/github.go internal/github/github_test.go
git commit -m "feat(crn): github CreateIssue/EnsureLabels/CloseIssue via gh CLI"
```

---

### Task 5: Issue content builder (`internal/feedback`)

**Files:**
- Create: `internal/feedback/issue_content.go`
- Create: `internal/feedback/issue_content_test.go`

**Interfaces:**
- Produces: `feedback.IssueContent(f *domain.FeedbackRequest) (title, body string, labels []string)`.

- [ ] **Step 1: Write the failing test**

Create `internal/feedback/issue_content_test.go`:

```go
package feedback

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

func TestIssueContent(t *testing.T) {
	f := &domain.FeedbackRequest{
		ID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Category:  "bug",
		Priority:  "high",
		Note:      "Button color is wrong\nsecond line",
		PageURL:   "http://localhost:3002/",
		CreatedAt: time.Unix(0, 0).UTC(),
		Payload: domain.FeedbackPayload{
			Pins: []domain.FeedbackPin{{Label: "btn", Selector: "button.buy", Note: "make it green"}},
		},
	}
	title, body, labels := IssueContent(f)

	if title != "[feedback] bug: Button color is wrong" {
		t.Errorf("title=%q", title)
	}
	for _, want := range []string{"fitt:feedback", "type:bug", "prio:high"} {
		found := false
		for _, l := range labels {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("labels missing %q: %v", want, labels)
		}
	}
	for _, frag := range []string{"**Priority:** high", "button.buy", "make it green", "Mirrored from CRN feedback 11111111"} {
		if !strings.Contains(body, frag) {
			t.Errorf("body missing %q\n---\n%s", frag, body)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/feedback/ -run IssueContent -v`
Expected: FAIL — package/`IssueContent` undefined.

- [ ] **Step 3: Implement the builder**

Create `internal/feedback/issue_content.go`:

```go
// Package feedback mirrors in-demo feedback requests to GitHub issues: it turns
// a feedback row into issue content and runs the background watcher that creates
// the issues (see docs/superpowers/specs/2026-07-13-feedback-to-github-issues-design.md).
package feedback

import (
	"fmt"
	"strings"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// IssueContent renders a feedback request into a GitHub issue title, body, and
// label set. Deterministic — no network, no LLM.
func IssueContent(f *domain.FeedbackRequest) (title, body string, labels []string) {
	return issueTitle(f), issueBody(f), []string{
		"fitt:feedback", "type:" + f.Category, "prio:" + f.Priority,
	}
}

func issueTitle(f *domain.FeedbackRequest) string {
	summary := firstLine(f.Note)
	if summary == "" && len(f.Payload.Pins) > 0 {
		summary = f.Payload.Pins[0].Label
	}
	if summary == "" {
		summary = "no description"
	}
	if len(summary) > 60 {
		summary = strings.TrimSpace(summary[:60]) + "…"
	}
	return "[feedback] " + f.Category + ": " + summary
}

func issueBody(f *domain.FeedbackRequest) string {
	reporter := f.Reporter
	if reporter == "" {
		reporter = "anonymous"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Category:** %s · **Priority:** %s\n", f.Category, f.Priority)
	fmt.Fprintf(&b, "**Reported:** %s by %s\n", f.CreatedAt.Format(time.RFC3339), reporter)
	if f.PageURL != "" {
		fmt.Fprintf(&b, "**Page:** %s\n", f.PageURL)
	}
	if note := strings.TrimSpace(f.Note); note != "" {
		b.WriteString("\n" + note + "\n")
	}
	if len(f.Payload.Pins) > 0 {
		b.WriteString("\n### Pins\n")
		for _, p := range f.Payload.Pins {
			label := p.Label
			if label == "" {
				label = "element"
			}
			fmt.Fprintf(&b, "- 📍 **%s**", label)
			if p.Selector != "" {
				fmt.Fprintf(&b, " — `%s`", p.Selector)
			}
			b.WriteString("\n")
			if n := strings.TrimSpace(p.Note); n != "" {
				fmt.Fprintf(&b, "  %s\n", n)
			}
		}
	}
	fmt.Fprintf(&b, "\n<sub>Mirrored from CRN feedback %s. Screenshots viewable in the CRN Edit Request Panel.</sub>\n", f.ID)
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/feedback/ -run IssueContent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/feedback/issue_content.go internal/feedback/issue_content_test.go
git commit -m "feat(crn): feedback→issue content builder"
```

---

### Task 6: The watcher (reconcile loop)

**Files:**
- Create: `internal/feedback/watcher.go`
- Create: `internal/feedback/gh_issuer.go`
- Create: `internal/feedback/watcher_test.go`

**Interfaces:**
- Consumes: `github.RepoSlug` (Task 3), `github.Issue` + `github.CreateIssue`/`EnsureLabels` (Task 4), `IssueContent` (Task 5).
- Produces: `feedback.NewWatcher(store FeedbackStore, issuer Issuer, owner string, interval time.Duration, logger) *Watcher`; `(*Watcher).Run(ctx)`; `feedback.NewGHIssuer(logger) *GHIssuer`. `FeedbackStore` is a subset of `domain.Store` (satisfied by the real store from Task 2).

- [ ] **Step 1: Write the failing test**

Create `internal/feedback/watcher_test.go`:

```go
package feedback

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
	"github.com/Watthachai/fitt-coderunner/internal/github"
)

type fakeStore struct {
	needing  []*domain.FeedbackRequest
	projects map[uuid.UUID]*domain.Project
	setCalls map[uuid.UUID]int
}

func (f *fakeStore) ListFeedbackNeedingIssue(ctx context.Context, limit int) ([]*domain.FeedbackRequest, error) {
	return f.needing, nil
}
func (f *fakeStore) GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	if p, ok := f.projects[id]; ok {
		return p, nil
	}
	return nil, domain.ErrNotFound
}
func (f *fakeStore) SetFeedbackIssue(ctx context.Context, id uuid.UUID, number int, url string) error {
	if f.setCalls == nil {
		f.setCalls = map[uuid.UUID]int{}
	}
	f.setCalls[id] = number
	f.needing = nil // once mirrored it no longer needs an issue
	return nil
}

type fakeIssuer struct{ created int }

func (i *fakeIssuer) EnsureLabels(ctx context.Context, repoSlug string) error { return nil }
func (i *fakeIssuer) CreateIssue(ctx context.Context, repoSlug, title, body string, labels []string) (github.Issue, error) {
	i.created++
	n := 100 + i.created
	return github.Issue{Number: n, URL: "https://github.com/acme/repo/issues/" + strconv.Itoa(n)}, nil
}

func TestWatcherMirrorsOnce(t *testing.T) {
	pid, fid := uuid.New(), uuid.New()
	st := &fakeStore{
		needing:  []*domain.FeedbackRequest{{ID: fid, ProjectID: pid, Category: "bug", Priority: "low"}},
		projects: map[uuid.UUID]*domain.Project{pid: {ID: pid, Name: "Shop", RepoURL: "https://github.com/acme/repo.git"}},
	}
	iss := &fakeIssuer{}
	w := NewWatcher(st, iss, "acme", time.Second, nil)

	w.reconcileOnce(context.Background())
	if iss.created != 1 {
		t.Fatalf("created=%d want 1", iss.created)
	}
	if st.setCalls[fid] != 101 {
		t.Fatalf("SetFeedbackIssue number=%d want 101", st.setCalls[fid])
	}
	w.reconcileOnce(context.Background()) // nothing needing now
	if iss.created != 1 {
		t.Fatalf("created=%d want still 1", iss.created)
	}
}

func TestWatcherSkipsUnresolvedProject(t *testing.T) {
	fid := uuid.New()
	st := &fakeStore{needing: []*domain.FeedbackRequest{{ID: fid, ProjectID: uuid.New()}}}
	iss := &fakeIssuer{}
	w := NewWatcher(st, iss, "acme", time.Second, nil)

	w.reconcileOnce(context.Background())
	w.reconcileOnce(context.Background())
	if iss.created != 0 {
		t.Fatalf("must not create for an unresolvable project (created=%d)", iss.created)
	}
	if !w.skip[fid] {
		t.Fatalf("unresolvable feedback should be marked skip")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/feedback/ -run Watcher -v`
Expected: FAIL — `NewWatcher` / types undefined.

- [ ] **Step 3: Implement the watcher**

Create `internal/feedback/watcher.go`:

```go
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
```

Create `internal/feedback/gh_issuer.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/feedback/ -v`
Expected: PASS (IssueContent + both watcher tests).

- [ ] **Step 5: Commit**

```bash
git add internal/feedback/watcher.go internal/feedback/gh_issuer.go internal/feedback/watcher_test.go
git commit -m "feat(crn): feedback→issue watcher (reconcile loop) + gh issuer adapter"
```

---

### Task 7: Config + start the watcher in main

**Files:**
- Modify: `internal/config/config.go` (`Config` struct add field near `FeedbackIngestURL` ~L71; `Load()` set it ~near the `getEnvDuration` shutdown block)
- Modify: `cmd/server/main.go` (after `jobManager := ...` ~L97)

**Interfaces:**
- Consumes: `feedback.NewWatcher`, `feedback.NewGHIssuer` (Task 6); the real store `st` satisfies `feedback.FeedbackStore` (Task 2).
- Produces: `cfg.FeedbackIssuePollInterval time.Duration`.

- [ ] **Step 1: Add the config field**

In `internal/config/config.go`, add to the `Config` struct (after `FeedbackIngestURL`):

```go
	// FeedbackIssuePollInterval is how often the feedback→issue watcher scans for
	// un-mirrored feedback. Only used when GithubOwner is set.
	FeedbackIssuePollInterval time.Duration
```

In `Load()`, alongside the existing `getEnvDuration("CRN_SHUTDOWN_TIMEOUT", ...)` block, add:

```go
	pollInterval, err := getEnvDuration("CRN_FEEDBACK_ISSUE_POLL_INTERVAL", 20*time.Second)
	if err != nil {
		return nil, err
	}
	cfg.FeedbackIssuePollInterval = pollInterval
```

- [ ] **Step 2: Start the watcher in main**

In `cmd/server/main.go`, after `jobManager := jobs.NewManager(...)` (~L97) and before the HTTP server block, add:

```go
	// --- Feedback→GitHub watcher (owner model only) ---
	// Mirrors every in-demo feedback row into a GitHub issue so the panel's
	// history is visible in the repo. No-op when CRN_GITHUB_OWNER is unset.
	if cfg.GithubOwner != "" {
		watcher := feedback.NewWatcher(st, feedback.NewGHIssuer(logger),
			cfg.GithubOwner, cfg.FeedbackIssuePollInterval, logger)
		go watcher.Run(rootCtx)
		logger.Info("feedback→issue watcher started", "interval", cfg.FeedbackIssuePollInterval)
	}
```

Add the import `"github.com/Watthachai/fitt-coderunner/internal/feedback"` to `cmd/server/main.go`.

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go cmd/server/main.go
git commit -m "feat(crn): wire feedback→issue watcher + CRN_FEEDBACK_ISSUE_POLL_INTERVAL"
```

---

### Task 8: Approve creates/comments the issue; reject closes it

**Files:**
- Modify: `internal/api/api.go` — `handleApproveFeedback` (~L1102-1157) and `handleRejectFeedback` (~L1160-1176)

**Interfaces:**
- Consumes: `github.EnsureLabels`/`CreateIssue`/`CommentIssue`/`CloseIssue` (Task 4), `feedback.IssueContent` (Task 5), `s.store.SetFeedbackIssue` + `f.IssueNumber` (Task 2), `s.repoSlugForProject` (Task 3).
- Add imports to api.go if missing: `"fmt"` and `"github.com/Watthachai/fitt-coderunner/internal/feedback"`.

- [ ] **Step 1: Rewrite the approve body (issue ensure + issue_number in payload + comment)**

Replace the block from the `payload, err := json.Marshal(...)` line through the `SetFeedbackStatus(... "approved" ...)` block in `handleApproveFeedback` with:

```go
	repoSlug := s.repoSlugForProject(project)

	// Ensure the feedback is mirrored (the watcher may not have caught up yet).
	issueNumber := 0
	if f.IssueNumber != nil {
		issueNumber = *f.IssueNumber
	} else if repoSlug != "" {
		if err := github.EnsureLabels(r.Context(), repoSlug, s.logger); err != nil {
			s.logger.Warn("ensure labels failed", "err", err, "repo", repoSlug)
		}
		title, body, labels := feedback.IssueContent(f)
		iss, cerr := github.CreateIssue(r.Context(), repoSlug, title, body, labels, s.logger)
		if cerr != nil {
			s.logger.Warn("create issue on approve failed", "err", cerr, "feedback_id", id)
		} else {
			issueNumber = iss.Number
			if serr := s.store.SetFeedbackIssue(r.Context(), id, iss.Number, iss.URL); serr != nil {
				s.logger.Warn("persist feedback issue failed", "err", serr, "feedback_id", id)
			}
		}
	}

	payload, err := json.Marshal(map[string]any{
		"mode":         "edit",
		"change":       feedbackChangePrompt(f),
		"name":         project.Name,
		"issue_number": issueNumber,
	})
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "could not encode payload")
		return
	}

	job, err := s.jm.Enqueue(r.Context(), project.ID, project.OrgID, payload)
	if err != nil {
		s.logger.Error("enqueue feedback edit failed", "err", err, "feedback_id", id)
		s.writeError(w, r, http.StatusInternalServerError, "could not enqueue build")
		return
	}

	jobID := job.ID
	if err := s.store.SetFeedbackStatus(r.Context(), id, "approved", &jobID); err != nil {
		s.logger.Warn("link feedback to job failed", "err", err, "feedback_id", id)
	}

	// Best-effort: note the approval on the mirrored issue.
	if repoSlug != "" && issueNumber > 0 {
		note := fmt.Sprintf("🛠 Approved → edit build #%d queued.", job.BuildNo)
		if err := github.CommentIssue(r.Context(), repoSlug, issueNumber, note, s.logger); err != nil {
			s.logger.Warn("comment approve failed", "err", err, "issue", issueNumber)
		}
	}
```

(The `s.writeJSON(...)` accepted response at the end of the handler stays unchanged.)

- [ ] **Step 2: Rewrite the reject body (close the issue "not planned")**

Replace the body of `handleRejectFeedback` with:

```go
func (s *server) handleRejectFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid feedback id")
		return
	}

	// Best-effort: close the mirrored issue before marking rejected.
	if f, gerr := s.store.GetFeedback(r.Context(), id); gerr == nil && f.IssueNumber != nil {
		if project, perr := s.store.GetProject(r.Context(), f.ProjectID); perr == nil {
			if repoSlug := s.repoSlugForProject(project); repoSlug != "" {
				if cerr := github.CloseIssue(r.Context(), repoSlug, *f.IssueNumber,
					"not planned", "Closed as not planned by the operator.", s.logger); cerr != nil {
					s.logger.Warn("close rejected issue failed", "err", cerr, "issue", *f.IssueNumber)
				}
			}
		}
	}

	if err := s.store.SetFeedbackStatus(r.Context(), id, "rejected", nil); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "feedback not found")
			return
		}
		s.logger.Error("reject feedback failed", "err", err, "feedback_id", id)
		s.writeError(w, r, http.StatusInternalServerError, "could not update feedback")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "rejected"})
}
```

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: success (confirm `fmt` and `feedback` are imported in api.go).

- [ ] **Step 4: Manual verification (with the stack running + gh authed + CRN_GITHUB_OWNER set)**

Submit feedback from a demo, wait one poll interval, confirm an open issue appears (`gh issue list --repo <owner>/<name>`). Then:
Run: `curl -s -X POST http://localhost:8080/internal/feedback/<id>/reject`
Expected: `{"status":"rejected"}` and `gh issue view <n> --repo <slug>` shows state CLOSED (not planned) with the closing comment.

- [ ] **Step 5: Commit**

```bash
git add internal/api/api.go
git commit -m "feat(crn): approve mirrors+comments the issue, reject closes it (not planned)"
```

---

### Task 9: Build success posts a summary + closes the issue

**Files:**
- Modify: `internal/jobs/jobs.go` (the issue-comment block ~L544-548)

**Interfaces:**
- Consumes: `github.CloseIssue` (Task 4). In scope here: `job.BuildNo`, `pushBranch`, `commit`, `shortSHA` (all already local at that point).

- [ ] **Step 1: Replace comment-only with summary + close**

In `internal/jobs/jobs.go`, replace the block:

```go
		if ownerMode && spec.Mode == "edit" && spec.IssueNumber > 0 && repoSlug != "" {
			body := "Fixed by CRN in " + commit
			if err := github.CommentIssue(ctx, repoSlug, spec.IssueNumber, body, m.logger); err != nil {
				log.Warn("comment issue failed", "err", err, "issue", spec.IssueNumber)
			}
		}
```

with:

```go
		if ownerMode && spec.Mode == "edit" && spec.IssueNumber > 0 && repoSlug != "" {
			summary := fmt.Sprintf("🛠 Fixed by CRN — build #%d\n\nBranch `%s` · commit `%s`",
				job.BuildNo, pushBranch, shortSHA(commit))
			if err := github.CloseIssue(ctx, repoSlug, spec.IssueNumber, "completed", summary, m.logger); err != nil {
				log.Warn("close issue failed", "err", err, "issue", spec.IssueNumber)
			}
		}
```

(A build **failure** leaves the issue open — the operator sees it's not done. Failure comments are a documented follow-up, not v1.)

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: success (`fmt` is already imported in jobs.go).

- [ ] **Step 3: Manual verification**

Approve a mirrored feedback in the panel; after the edit build finishes, `gh issue view <n> --repo <slug>` shows the summary comment and state CLOSED (completed).

- [ ] **Step 4: Commit**

```bash
git add internal/jobs/jobs.go
git commit -m "feat(crn): edit build posts a summary and closes the driving issue"
```

---

### Task 10: Panel shows the issue link

**Files:**
- Modify: `frontend/app/lib/types.ts` (`FeedbackRequest` ~L229-240)
- Modify: `frontend/app/components/FeedbackModal.tsx` (`FeedbackCard` header ~L47-59)
- Modify: `frontend/app/globals.css` (add a `.fb-issue` rule near the other `.fb-*` rules)

**Interfaces:**
- Consumes: `issue_number`/`issue_url` now returned by the API (Task 2 domain fields).

- [ ] **Step 1: Add the type fields**

In `frontend/app/lib/types.ts`, add to `FeedbackRequest` (after `created_at`):

```ts
  created_at: string; // RFC3339
  issue_number?: number; // set once mirrored to GitHub
  issue_url?: string;
```

- [ ] **Step 2: Render the issue chip**

In `frontend/app/components/FeedbackModal.tsx`, inside `<header className="fb-card-head">`, after the `fb-meta` span, add:

```tsx
        {req.issue_url ? (
          <a
            className="pill fb-issue"
            href={req.issue_url}
            target="_blank"
            rel="noreferrer"
          >
            #{req.issue_number} ↗
          </a>
        ) : null}
```

- [ ] **Step 3: Style the chip**

In `frontend/app/globals.css`, near the other `.fb-*` rules, add:

```css
.fb-issue {
  text-decoration: none;
  color: #1cb0f6;
  border: 1px solid rgba(28, 176, 246, 0.4);
}
.fb-issue:hover {
  background: rgba(28, 176, 246, 0.1);
}
```

- [ ] **Step 4: Verify the dashboard builds**

Run: `cd frontend && npm run build`
Expected: type-check + build succeed.

- [ ] **Step 5: Commit**

```bash
git add frontend/app/lib/types.ts frontend/app/components/FeedbackModal.tsx frontend/app/globals.css
git commit -m "feat(dashboard): link each feedback card to its GitHub issue"
```

---

## Self-Review

**Spec coverage:**
- Watcher creates issues for un-mirrored feedback → Tasks 2 (query), 5 (content), 6 (watcher), 7 (wiring). ✓
- Human-gated approve → build with `issue_number` → Task 8. ✓
- Build success = summary comment + close → Task 9. ✓
- Reject = close "not planned" → Task 8. ✓
- Schema + apply-all migration (closes the `42501` class) → Task 1. ✓
- Shared repo-slug resolver → Task 3. ✓
- `gh` funcs → Task 4. ✓
- Panel issue link → Task 10. ✓
- Best-effort/no-op-when-owner-off / idempotency → covered in Tasks 6, 7, 8, 9 (all `gh` calls logged-and-swallowed; watcher gated in main; `issue_number` written only on success).
- v1 screenshots = text + panel link → issue body in Task 5 (no image); **follow-up:** embed screenshots by committing shots into the repo.

**Placeholder scan:** none — every code step contains complete code; tests contain real assertions.

**Type consistency:** `FeedbackStore`/`Issuer` method sets match the real `domain.Store` methods (Task 2) and `github.CreateIssue`/`EnsureLabels` signatures (Task 4). `IssueContent` returns `(title, body string, labels []string)` and is consumed with that arity in Tasks 6 and 8. `github.RepoSlug(owner, repoURL, name, id string)` is called identically in Task 3 (`repoSlugForProject`) and Task 6 (watcher). `CloseIssue(ctx, slug, number, reason, comment, logger)` matches its callers in Tasks 8 and 9.

**Intentional deviations from the spec (noted):** pin `box` coordinates are omitted from the issue body (opaque JSON, not human-friendly); build-failure issue comments are deferred (success closes; failure leaves the issue open).
