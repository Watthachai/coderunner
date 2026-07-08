# Design — In-Demo Feedback Loop (Widget → FITTCORE Edit Request Panel)

- **Date:** 2026-07-07
- **Status:** Approved design, pending spec review → writing-plans
- **Repos touched:** `fitt-coderunner` (CRN: panel, widget bundle, build-time inject) · `fitt-builder-v2` (FBD: Supabase schema/RLS/storage — reuses existing Supabase wiring)

> **Revision (2026-07-07, during implementation) — self-hosted PostgREST, no Supabase.**
> Operator preference: keep everything on our own Postgres. Net effect on this design:
> - The "central DB" is the existing `crn` Postgres; CRN reads feedback with its
>   **existing pgx pool** — no `CRN_CENTRAL_DATABASE_URL`, no second pool.
> - The public write tier is a **PostgREST container** (docker-compose, host `:3010`)
>   exposing `feedback_requests` to an INSERT-only `web_anon` role — CRN itself stays local-only.
> - Screenshots are stored **inline as base64 data URIs** in `payload` (no object storage).
> Everything else (contract, panel, approve→edit build, widget, deterministic inject) stands.
> **Plan 1 is implemented + verified** (migration 0007 + PostgREST + CRN endpoints + wired modal);
> **Plan 2 (widget + inject) is next.**

## Context

Today an end-user testing a CRN-built demo has no way to say "make this button bigger" from inside the demo — they'd have to write a fresh brief. We want to close the loop: **demo → feedback → iterate**. Every project CRN builds should always carry a feedback control so a tester can drop a screenshot and/or click UI elements to annotate what they want changed; those requests land in a panel in the CRN dashboard where an operator approves them into an edit build that redeploys.

Notably, the intake side is **already planned**: `CRN-architecture.md` §2.4 specifies an "Edit Request Panel" (Incoming requests · Queue position · [Approve][Reject][Prioritize]) backed by an `edit_requests` table (§2.2) and a `POST .../edit-request` contract (§5). This design implements that panel and adds the in-demo widget that feeds it.

## Constraints (locked during brainstorming)

1. **CRN stays local-only** — never publicly reachable. The public write path must not touch CRN directly.
2. **Deterministic widget** — every demo gets the *same* widget, guaranteed (not Claude-generated per build).
3. **Level-3 richness** — multi-pin annotations, element targeting (selector + region screenshot), category (bug/feature/style), priority, free-text note.
4. **Reuse existing infra** — FBD already has `@supabase/supabase-js` + `@supabase/ssr` (`lib/supabase/server.ts`, used by orgs/team-chat/storage). CRN already has `CRN_CENTRAL_DATABASE_URL` (a central-DB read path) and the just-built durable **build traces**.

## Architecture — Supabase as the public bus, CRN reads locally

```
[ Demo (deployed remote) + Feedback Widget ]
      │  widget writes DIRECTLY to Supabase (public anon key + RLS insert)
      │  screenshots → Supabase Storage
      ▼
[ Supabase Postgres = central DB ] ── feedback_requests (status='new') ──┐
      ▲                                                                   │
      │  CRN (local) reads via CRN_CENTRAL_DATABASE_URL (outbound only)   │
      ▼                                                                   │
[ CRN Edit Request Panel (A) ] approve → edit build (existing) → redeploy ┘
```

**Why this satisfies the constraints:** the widget can write to Supabase because the anon key is publishable and RLS restricts anon to INSERT-only on `feedback_requests` (+ upload to the storage bucket), never SELECT. CRN only *reads* Supabase over an outbound connection — it is never exposed. No new ingest service; Supabase PostgREST + Storage *is* the public tier.

Rejected alternatives: widget→CRN directly (violates local-only); a custom FBD API ingest route (redundant with Supabase's PostgREST + adds a hop); a dedicated ingest microservice (over-engineered for this).

## Shared Contract (the interface between A and B)

**Table `feedback_requests`** (lives in the central/Supabase DB — an evolution of the planned `edit_requests`):

| column | type | notes |
|---|---|---|
| `id` | uuid pk | |
| `project_id` | uuid | matches CRN's project id (baked into the widget at build time) |
| `status` | text | `new` → `reviewing` → `approved` → `building` → `done` / `rejected` |
| `category` | text | `bug` \| `feature` \| `style` |
| `priority` | text | `low` \| `med` \| `high` |
| `note` | text | overall free-text ask |
| `page_url` | text | where feedback was given |
| `reporter` | text | optional (name/email); nullable |
| `payload` | jsonb | see below |
| `job_id` | uuid null | set when merged into an edit build (links to the trace) |
| `created_at` | timestamptz | default now() |

**`payload` JSONB shape:**
```json
{
  "pins": [
    { "selector": "main > button.cta", "label": "CTA button",
      "note": "ทำให้ใหญ่ขึ้นและเป็นสีเขียว",
      "box": { "x": 120, "y": 340, "w": 180, "h": 44 },
      "region_shot": "feedback-shots/<project>/<uuid>.png" }
  ],
  "full_shot": "feedback-shots/<project>/<uuid>.png",
  "viewport": { "w": 1440, "h": 900 },
  "user_agent": "..."
}
```

**Storage:** Supabase Storage bucket `feedback-shots`, public-read (screenshots of demos are low-sensitivity), path `feedback-shots/<project_id>/<uuid>.png`. Rows store the object path; the panel resolves public URLs.

**RLS policies:** anon role may `INSERT` into `feedback_requests` and `INSERT` (upload) into the `feedback-shots` bucket; anon may **not** `SELECT`/`UPDATE`/`DELETE`. CRN connects as the service/owner role (via `CRN_CENTRAL_DATABASE_URL`) with full read + status updates. Unknown `project_id`s are tolerated at insert and simply ignored by the panel (which reconciles against CRN's known projects); abuse is bounded by Supabase rate limits.

**Config:** CRN sets `CRN_CENTRAL_DATABASE_URL` to the Supabase Postgres connection string. The widget is injected with `{ supabaseUrl, supabaseAnonKey, projectId }`.

## Feature A — Edit Request Panel (CRN dashboard)

- **Data:** a new read path in CRN over the central DB: `store.ListFeedback(ctx, status)` / `GetFeedback(ctx, id)` / `SetFeedbackStatus(...)`. Reads from a second pgx pool bound to `CRN_CENTRAL_DATABASE_URL` (falls back to local in standalone dev, where the table then lives locally).
- **API (CRN, internal, no-auth — same pattern as the trace endpoints):** `GET /internal/feedback?status=new`, `GET /internal/feedback/{id}`, `POST /internal/feedback/{id}/approve` (→ enqueues an edit build), `POST /internal/feedback/{id}/reject`.
- **Frontend:** a `FeedbackPanel` client component + `useFeedback()` poll hook (mirrors the existing `useDashboard`/`useTraces` pattern). Card per request in the existing Duolingo style (`.panel`/`.bc`/`.pill`): category + priority pill, note, a **screenshot gallery**, and a **pin list** (each pin shows its region screenshot, `selector`, and note). Actions: **Approve → build**, **Reject**, **Prioritize**.
- **Approve → edit build:** reuse the existing edit path. Approve composes a **text** change prompt from `note` + each pin's `label` + `selector` + per-pin `note`, then calls the same mechanism as `POST /internal/projects/{id}/edit` (`internal/jobs` edit mode). Claude Code acts on this text (the selectors point it at the right elements); the **screenshots are for the operator's review in the panel**, not fed to Claude (image-to-Claude is a future, multimodal enhancement — out of scope). The resulting `job_id` is written back to `feedback_requests.job_id`, and status walks `approved → building → done`, linking each request to the durable **trace** from the state-trace feature.
- **Placement (per the IA decision earlier this session):** feedback is project-scoped, so the panel is reachable from the project page (`/projects/[id]`, near Build history) and surfaces a global "new feedback" count on the dashboard. No separate top-level nav (consistent with dropping `/traces`).
- **Default:** approve is **manual** (operator clicks). Auto-build-on-arrival is explicitly out of scope for v1.

## Feature B — Feedback Widget + deterministic inject

- **Widget bundle:** one self-contained JS/CSS artifact (no dependency on the demo's framework), rendering a floating button in a corner. State machine: idle → note form → **point mode**.
  - **Point mode:** the user clicks any element; the widget highlights it, records a stable `selector` + bounding `box`, captures a **region screenshot** (`html2canvas`), and lets them type a per-pin note. **Multiple pins** per request.
  - Before submit: choose **category** + **priority**, optional reporter, overall note.
  - **Submit:** upload region + full screenshots to Supabase Storage, then `INSERT` one `feedback_requests` row via `supabase-js` (bundled) using the injected anon key. Optimistic success toast; failure ret/queues.
- **Deterministic inject (CRN build step):** after Claude finishes generating and **before** `docker build`, CRN adds the widget to the workspace: copy the prebuilt `fitt-feedback.js` into the app's served static/public dir and inject `<script src="/fitt-feedback.js" data-project="<id>" data-supabase-url="..." data-anon-key="...">` into the HTML entry. Lives as a new step in `internal/buildstep` invoked from `internal/jobs` alongside the git/docker phases. Updating the widget once updates every future build; existing demos pick it up on their next build. (Known detail: the HTML-entry injection targets the common `index.html` case; per-stack variance is handled in the plan.)

## Unit boundaries

- **`feedback-widget/`** (new, in CRN repo): the browser bundle. Input: injected config (dataset attrs). Output: Supabase rows + storage objects. Depends on: `supabase-js`, `html2canvas`. Testable standalone in a plain HTML page.
- **`internal/buildstep` inject step:** Input: workspace dir + config. Output: mutated workspace. Depends on: filesystem only.
- **CRN store feedback methods + API + `FeedbackPanel`/`useFeedback`:** mirror the existing trace stack (store method → port → handler → hook → component).
- **Supabase schema/RLS/storage:** declarative migration (in FBD's Supabase project).

## Implementation decomposition (built in order)

0. **Slice 0 — Mock intake UI (frontend-only, no backend):** a floating **Feedback modal** in the CRN dashboard, driven by **seeded mock data** matching the contract (category/priority/note/pins/screenshots as inline SVG placeholders), with Approve/Prioritize/Reject acting on local state. Purpose: validate "how an incoming request looks in CRN" and the intake UX before any Supabase wiring. The mock source is a single module that the real `useFeedback` hook later drops in for. *(This is the current step, requested to visualize first.)*
1. **Plan 1 — Contract + Panel (A):** Supabase `feedback_requests` table + `feedback-shots` bucket + RLS; CRN central-DB read methods; `/internal/feedback*` endpoints; wire the modal/panel to `useFeedback`; approve → edit build + trace link. *Then B has a real target; can be exercised by inserting a row by hand.*
2. **Plan 2 — Widget (B):** the Level-3 widget bundle + `internal/buildstep` deterministic inject + build-time config injection. *Consumes Plan 1's contract. User will exercise this against a real repo exported from FITT Builder.*

## Verification (end-to-end)

- **Plan 1:** insert a `feedback_requests` row (+ a screenshot object) directly in Supabase → it appears in the panel → click Approve → an edit build is enqueued, `job_id` back-filled, and the resulting build shows in the project's trace history.
- **Plan 2:** run a real build → open the deployed demo → the widget button is present → pin two elements with notes + category/priority → submit → confirm a `feedback_requests` row + screenshots in Supabase → confirm it lands in the panel (closing the loop with Plan 1).
- Widget bundle also verified in isolation on a static HTML page (point mode, multi-pin, screenshot upload) before wiring the inject.

## Risks / open items

- **Stack-agnostic inject:** the HTML-entry injection assumes a servable `index.html`; SSR-only apps may need a different hook — resolved per-stack in Plan 2.
- **Screenshot size/cost:** region shots are small; the full-page shot is optional and compressed. Storage is cheap; revisit if volume grows.
- **Central-DB config is load-bearing:** the widget can only write to Supabase (it can't reach a local Postgres), so for the loop to work end-to-end **`CRN_CENTRAL_DATABASE_URL` must point at the same Supabase project the widget writes to** — both sides must meet in one DB. If it's left unset (pointing CRN at its local Postgres only), the panel won't see widget submissions. This is a one-line config, but it's a hard requirement for the feature, not optional.
