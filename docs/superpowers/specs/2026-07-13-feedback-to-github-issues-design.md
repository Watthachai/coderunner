# Feedback → GitHub Issues (mirror-all, human-gated build)

- **Date:** 2026-07-13
- **Status:** Design — approved decisions, pending spec review
- **Depends on:** `2026-07-07-feedback-loop-design.md` (the in-demo feedback loop),
  the "one repo per project + GitHub issues loop" (owner model).

## 1. Background

In-demo feedback already flows: the injected widget POSTs to PostgREST as the
unauthenticated `web_anon` role → `feedback_requests` (Postgres) → CRN reads it
for the Edit Request Panel → operator **approve** enqueues an edit build; the
build (owner model, `mode=edit`) already comments the resulting commit back on a
GitHub issue when the job carries an `issue_number` (`internal/jobs/jobs.go`
~L544). Separately, the "issues loop" (`/internal/projects/{id}/issues/{number}/fix`,
`handleFixIssue`) turns a GitHub issue into an edit build.

These two intakes never meet: **feedback lives only in Postgres and is invisible
in GitHub.** The operator wants the *history* of feedback visible in GitHub too.

## 2. Goal / Non-goals

**Goal:** Mirror every incoming feedback request to a GitHub issue in the
project's repo, and drive its lifecycle (approve → build → summary + close;
reject → close not-planned) so GitHub is a durable, human-readable history of all
feedback and what was done about it.

**Non-goals (this phase):**
- Embedding the region screenshot *inside* the issue (see §9 follow-ups).
- AI-generated issue titles/summaries (issues are templated deterministically).
- Two-way sync (issues edited on GitHub do **not** write back to Postgres).
- Changing the widget or the public PostgREST write path.

## 3. Key constraint that shapes the design

The widget POSTs unauthenticated from arbitrary demo origins and **cannot hold a
GitHub token**; keeping CRN unreachable from public demos is a deliberate goal of
the PostgREST intake. Therefore **CRN — not the widget — creates the issue**, and
because the insert happens inside PostgREST, CRN never sees it as an event. Issue
*creation* is thus inherently reactive → a **background watcher (poller)**.
Issue *state changes* (approve/reject) originate inside CRN handlers → handled
**inline**, no poller.

`feedback_requests` remains the source of truth; GitHub is a one-way mirror.

## 4. Architecture / flow

```
demo widget ──POST──▶ PostgREST ──▶ feedback_requests            (unchanged; public, CRN-private)
                                          │
                (A) watcher goroutine, every ~20s
                    finds rows WHERE issue_number IS NULL
                          │  gh issue create (open, labels)
                          ▼  write issue_number/url back
                    GitHub issue (open) ◀── shown as a link on the panel card
                                          │
                            operator reviews panel, clicks approve
                                          │  (B) ensure issue exists, comment
                                          │      "approved → building", label fitt:building
                                          ▼      enqueue edit build with issue_number
                                    edit build (mode=edit) ── Claude Code + Docker, rebuild demo
                                          │  on success (jobs.go ~L544, reused + extended):
                                          │    • comment a SUMMARY (what changed, build #, branch, demo URL)
                                          ▼    • gh issue close --reason completed
                                    GitHub issue (closed ✓)

              reject ──▶ (C) comment reason, gh issue close --reason "not planned"
```

**Trigger = poller (Option A).** Approve is **human-gated before the build**
(cost-safe: each build spawns Claude Code + Docker, and feedback is spammable).

## 5. Components (touchpoints)

### 5.1 Schema — `migrations/0008_feedback_issue_link.sql`
Add to `feedback_requests` (mirrors the existing nullable `job_id`):
```sql
ALTER TABLE feedback_requests ADD COLUMN IF NOT EXISTS issue_number INT;
ALTER TABLE feedback_requests ADD COLUMN IF NOT EXISTS issue_url    TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_feedback_unmirrored
  ON feedback_requests (created_at) WHERE issue_number IS NULL;
```
No new grants for `web_anon` (it still only INSERTs; the new columns default to
NULL/'' on insert). **Migration application note:** `make migrate` currently
applies only `0001`, and compose's initdb mounts run only on a fresh volume —
add `0008` to both the compose mount list and a `make migrate` that applies all
pending files in order (prevents the silent "grant/column never applied" class of
bug that produced the earlier `42501`).

### 5.2 `internal/domain/feedback.go`
Add `IssueNumber *int` and `IssueURL string` to the `FeedbackRequest` struct
(pointer so "not yet mirrored" is distinguishable).

### 5.3 `internal/store/store.go`
- `ListFeedbackNeedingIssue(ctx, limit) ([]FeedbackRequest, error)` — rows with
  `issue_number IS NULL`, oldest first, capped (e.g. 20/tick).
- `SetFeedbackIssue(ctx, id, number, url) error`.
- Extend `ListFeedback`/`GetFeedback` SELECTs to return the two new columns.

### 5.4 `internal/github/github.go` (reuses the existing `gh` CLI `runGH` path)
- `CreateIssue(ctx, repoSlug, title, body string, labels []string) (Issue, error)`
  → `gh issue create --title --body --label … --json number,url`.
- `EnsureLabels(ctx, repoSlug, …) error` → `gh label create … ` idempotent
  (ignore "already exists"). Called once before first create per repo.
- `CloseIssue(ctx, repoSlug, number int, reason string) error` →
  `gh issue close <n> --reason completed|"not planned"`. Best-effort.
- `CommentIssue` already exists and is reused.

### 5.5 Watcher — `internal/feedback/watcher.go` (new)
A goroutine started from `cmd/server/main.go` (alongside the jobs dispatcher):
- Every `CRN_FEEDBACK_ISSUE_POLL_INTERVAL` (default `20s`): call
  `ListFeedbackNeedingIssue`, and for each row resolve the project's repo slug,
  build title+body (§6), `CreateIssue`, `SetFeedbackIssue`.
- **Repo-slug resolution must be shared:** `repoSlugForProject`/`repoSlugFromURL`
  currently live as methods/helpers in `internal/api/api.go`. Extract that logic
  into a package the watcher can also call (e.g. `domain` or `github`) so both the
  API handlers and the watcher resolve slugs identically. This is the one
  refactor this feature requires; it is in-scope because the watcher depends on it.
- **Guards:** if owner model is off (`CRN_GITHUB_OWNER` empty) or the project /
  repo can't be resolved, the watcher is a no-op for that row (logged, retried
  never or with a skip marker to avoid hot-looping — see below). All `gh` failures
  are logged and left for the next tick (creation stays idempotent because
  `issue_number` is written only on success).
- **Unknown project_id:** the widget tolerates unknown ids; the watcher skips
  rows whose project/repo can't be resolved. To avoid re-attempting them every
  tick forever, treat "no repo" as terminal for that row (leave `issue_number`
  NULL but do not spam `gh`; e.g. a small in-memory "unresolvable" set, or a
  sentinel — decided in the plan).

### 5.6 Approve — `handleApproveFeedback`
1. Load feedback. **Ensure issue exists**: if `issue_number` IS NULL (watcher
   hasn't caught up), create it synchronously now (same builder as the watcher).
2. `CommentIssue`: "🛠 approved → build #N queued". Add label `fitt:building`
   (best-effort).
3. Enqueue the edit build with `issue_number` in the payload (the same payload
   shape `handleFixIssue` builds: `mode=edit`, `issue_number`), so the build runs
   through the existing owner-model edit path.
4. Mark feedback `approved`, set `job_id`.

### 5.7 Reject — `handleRejectFeedback`
Ensure issue exists → `CommentIssue` with the reason → `CloseIssue "not planned"`
→ mark feedback `rejected`. Best-effort GitHub calls.

### 5.8 Build completion — `internal/jobs/jobs.go` (~L544, extend)
Today: on a successful owner-mode `mode=edit` build with `IssueNumber > 0`, it
`CommentIssue`s the commit back. Extend to:
- Make the comment a **summary**: what changed (Claude's edit summary / commit
  subject + body), build number/branch, and the demo URL.
- After a successful comment, `CloseIssue(..., "completed")`.
- On build **failure**, comment the failure (no close) so the issue stays open.

### 5.9 Panel UI — `frontend/app/components/FeedbackModal.tsx`, `lib/types.ts`, `lib/useFeedback.ts`
- `FeedbackRequest` type gains `issue_number?: number`, `issue_url?: string`.
- Each card shows an issue chip/link (`#123 ↗`) when mirrored, so the operator
  jumps to GitHub. (Rendering only; no behavior change.)

## 6. Issue content (templated from feedback fields)

**Title:** `[feedback] <category>: <first ~60 chars of note, else first pin label>`

**Body:**
```
**Category:** <category> · **Priority:** <priority>
**Reported:** <created_at> by <reporter or "anonymous">
**Page:** <page_url>

<note>

### Pins
- 📍 **<label>** — `<selector>`  (box <x>,<y> <w>×<h>)
  <pin note>
- …

<sub>Mirrored from CRN feedback <id>. Screenshots viewable in the CRN Edit Request Panel.</sub>
```

**Labels:** `fitt:feedback`, `type:<category>` (`bug`/`feature`/`style`),
`prio:<priority>`. Lifecycle labels: `fitt:building` (on approve), removed/settled
on close.

## 7. Lifecycle → issue state

| Feedback event | GitHub issue |
|---|---|
| new (watcher) | create · **open** · labels feedback/type/prio |
| approve | comment "approved → building" · label `fitt:building` · enqueue build |
| build success | **summary comment** (changes, build #, demo URL) · **close completed** |
| build failure | comment failure · stays open |
| reject | comment reason · **close "not planned"** |

## 8. Error handling & idempotency
- Every `gh` call is best-effort and logged; a GitHub/`gh` outage never blocks
  feedback intake (PostgREST) or builds.
- Creation is exactly-once: `issue_number` is written only after a successful
  create; the watcher's query excludes rows that already have one; approve's
  ensure-issue also writes it back, so a race between watcher and approve results
  in at most one issue (the loser sees `issue_number` already set and skips).
- Prereq: `gh` authenticated in CRN's environment and `CRN_GITHUB_OWNER` set
  (same dependency `ListIssues`/`CommentIssue` already have). Feature is a
  clean no-op when the owner model is off.

## 9. Out of scope / follow-ups
- **Screenshot in the issue** — commit the decoded `region_shot` into the repo
  (e.g. `.fitt/feedback/<id>.svg`) and reference it, or use the GitHub attachment
  API. Deferred to avoid a clone/commit/push per feedback in v1.
- AI-summarized issue titles/bodies (LLM per feedback).
- Reopen-on-related-feedback; two-way sync.

## 10. Testing
- **Pure builders** (title/body/labels from a `FeedbackRequest`): table tests.
- **Store**: `ListFeedbackNeedingIssue` / `SetFeedbackIssue` against the test DB
  harness (`feedback_test.go` pattern).
- **`gh` wrappers**: keep behind `runGH` so they're mockable; assert argv.
- **Watcher**: unit-test the reconcile step with a fake store + fake gh (created
  once, skipped when `issue_number` set, skipped when repo unresolved).
- **Manual e2e**: submit feedback from a demo → watcher creates an open issue →
  approve in the panel → build → summary comment + issue closed; reject → closed
  "not planned".

## 11. Open questions
None — scope (mirror all), trigger (watcher/poller), approve-gate (human approves
before build), and screenshots (text + panel link for v1) are decided.
