# CRN — Real Claude Builds via Harness Skill, Skill Management, FBD Naming

> Design spec · 2026-06-30 · spans `~/fitt-coderunner` (CRN) and `~/fitt-builder-v2` (FBD)

## Problem

Today a CRN build with `CRN_RUN_CLAUDE=false` just drops the export zip in the
workdir and git-pushes it — nothing extracts or builds it, so the pushed repo is
a raw zip and the job finishes in ~2s. We want each build to open a **real Claude
Code session** that turns the FITT Builder export into a clean, runnable project.
Two adjacent gaps: project names are raw prompts (should be a generated product
name like "ExpenseFlow"), and there's no way to see/manage the harness skill.

## Pieces (independent; build in this order)

### Piece A — Real Claude build driven by a harness Skill

**Goal of one build (definition of done):** extract → set up → runs. Reproduce the
FBD prototype faithfully (no redesign).

- **Skill `fitt-build`** (a Claude Agent Skill, `SKILL.md`) encodes the harness:
  1. find the export zip in the workdir → extract it here
  2. read the briefs (IDEA / BRD / PRD)
  3. the source is a runnable Vite+React prototype — set it up cleanly:
     `npm install`, ensure `npm run build`/`dev` works, fix only what's needed to
     run. **Reproduce the prototype — do not redesign.**
  4. delete the raw zip (don't commit it)
  5. write a short `BUILD_NOTES.md`
- **Skills live in the DB** (`skills` table, shared with Piece C). On startup CRN
  seeds the built-in `fitt-build` skill if absent.
- **Injection (every workdir):** in `jobs.runJob`, when `RunClaude`, CRN writes
  each **enabled** skill to `{workdir}/.claude/skills/{name}/SKILL.md` before
  spawning `claude`. The prompt invokes the skill ("use the fitt-build skill to
  set up this export"). The injected `.claude/` is kept out of the pushed repo via
  `.git/info/exclude` (local exclude — no change to the project's own .gitignore).
- **Config:** `CRN_RUN_CLAUDE` default flips to **true**.
- **Lifecycle:** existing phases (materialize → claude → git → push) now stream a
  real `claude` phase to the dashboard. On Claude failure the job is `failed`
  (no push), as today.

### Piece B — FBD generates a product name

- When a demo is generated, FBD also produces a concise product name (e.g.
  "ExpenseFlow") and stores it as `project.name`, replacing the raw-prompt /
  "Untitled" default. Fixes the FBD sidebar, the CRN dashboard, and the repo at
  once. Exact insertion point in the generation flow: decided during planning.
- Existing projects keep their current names (no bulk rename in v1).

### Piece C — Skill management page (dashboard)

- **`skills` table:** `id, name (unique), description, body (SKILL.md text),
  enabled bool, is_builtin bool, updated_at`.
- **Store:** `ListSkills`, `GetSkill`, `UpsertSkill`, `SetSkillEnabled`,
  `DeleteSkill` (non-builtin only). Seed `fitt-build` (is_builtin) on startup.
- **API (no-auth `/internal`, like the rest):** `GET /internal/skills`,
  `GET /internal/skills/{name}`, `PUT /internal/skills/{name}` (create/update
  body+description+enabled), `DELETE /internal/skills/{name}` (non-builtin).
- **Frontend:** a "Skills" page in the dashboard — list skills (name, enabled,
  built-in badge), view/edit the SKILL.md body in a textarea, toggle enabled, add
  a new skill. Built-in skills are editable but not deletable. Add a small nav
  link from the console header.

## Data flow

```
FBD: gen demo → name = product name → export (zip + name)
  → CRN POST /internal/projects → job
  → runJob: inject enabled skills into {workdir}/.claude/skills/
            → spawn Claude (fitt-build) → extract+setup+run
            → git commit + push  crn/{project_id}   (.claude excluded)
  → dashboard: live claude phase + activity
```

## Out of scope (v1)

- The "ขอแก้"/edit flow (resume Claude session, pull the existing repo for
  incremental edits).
- Verify-matches-page (run the app + visual diff against FBD's demo).
- Branch naming by product-name slug; bulk-rename of existing projects.
- Per-org / per-skill scoping (all enabled skills inject into every build).

## Risks / notes

- Running `claude` per build costs time + money and needs `node`/`npm` + network
  in the workdir — inherent to the goal.
- Skill discovery: the spawned `claude -p` must pick up `{workdir}/.claude/skills/`.
  Verify during implementation; fall back to inlining the skill body in the prompt
  if project-dir skill discovery doesn't apply in print mode.
