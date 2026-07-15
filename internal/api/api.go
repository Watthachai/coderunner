// Package api is the HTTP/WebSocket transport layer: a chi router exposing the
// CRN REST API (CRN-architecture.md §2.4) plus the live log WebSocket, the
// internal trigger endpoint for FTC DV, and health checks. It also houses the
// per-org API-key auth middleware.
//
// OWNED BY: the 'api' implementer.
//
// Routes registered by NewServer:
//
//	GET    /healthz                                 -> store.Ping
//	POST   /internal/trigger                        -> jm.HandleTrigger   (FTC DV signal)
//	GET    /internal/skills                         -> store.ListSkills
//	POST   /internal/skills/upload                  -> unzip + UpsertSkill + RecordSkillVersion
//	GET    /internal/skills/{name}                  -> store.GetSkill
//	PUT    /internal/skills/{name}                  -> store.UpsertSkill + RecordSkillVersion
//	DELETE /internal/skills/{name}                  -> store.DeleteSkill  (409 if built-in)
//	GET    /internal/skills/{name}/versions         -> store.ListSkillVersions
//	GET    /internal/projects/{id}/terminal         -> WebSocket: PTY shell in the project workdir (no auth)
//	Route /api/v1 (group, apiKeyAuth):
//	  POST /projects/{id}/edit-request              -> store.CreateEditRequest + jm.Enqueue
//	  GET  /projects/{id}/status                    -> jm.Status
//	  GET  /projects/{id}/jobs/{build_no}/logs      -> WebSocket: jm.Subscribe, stream BuildEventMsg
//	  POST /projects/{id}/rollback/{build_no}       -> TODO(crn): docker retag + notify
package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/Watthachai/fitt-coderunner/internal/claude"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
	"github.com/Watthachai/fitt-coderunner/internal/feedback"
	"github.com/Watthachai/fitt-coderunner/internal/github"
)

// ctxKey is an unexported context key type to avoid collisions.
type ctxKey int

const (
	// ctxKeyOrg holds the *domain.Org resolved by apiKeyAuth.
	ctxKeyOrg ctxKey = iota
)

// server holds the transport dependencies. It is unexported; NewServer returns
// the http.Handler (the chi router), not this concrete type.
type server struct {
	logger      *slog.Logger
	store       domain.Store
	jm          domain.JobManager
	gitRemote   string // advertised back to FBD in the ingest response
	githubOwner string // when set, the "one repo per project" model is active

	// projectsDir is the root holding per-project working dirs. The terminal WS
	// spawns the shell with its cwd at {projectsDir}/{project_id}.
	projectsDir string
	// terminalShell is the operator's optional shell override for the terminal
	// WS (CRN_TERMINAL_SHELL); empty falls back to $SHELL/zsh/bash/sh.
	terminalShell string

	// claudeBin is the path to the `claude` CLI used by the improve-skill endpoint.
	claudeBin string
	// claudeModel optionally pins the model for the improve-skill run (empty = CLI
	// default).
	claudeModel string
}

// defaultOrgID is the org an ingest call is attributed to when the body carries
// no org_id (FBD single-tenant dev). It is a fixed, valid UUID.
var defaultOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000000fb")

// NewServer constructs the chi router with all CRN routes registered and
// returns it as an http.Handler. gitRemote is echoed back to FBD in the ingest
// response so the caller knows where the build will be pushed. githubOwner, when
// non-empty, activates the "one repo per project" model (per-project GitHub repo
// + issues endpoints). projectsDir is the per-project working-dir root used by
// the interactive terminal WS, and terminalShell is the optional shell override
// for that terminal.
func NewServer(logger *slog.Logger, store domain.Store, jm domain.JobManager, gitRemote, githubOwner, projectsDir, terminalShell, claudeBin, claudeModel string) http.Handler {
	s := &server{
		logger:        logger,
		store:         store,
		jm:            jm,
		gitRemote:     gitRemote,
		githubOwner:   githubOwner,
		projectsDir:   projectsDir,
		terminalShell: terminalShell,
		claudeBin:     claudeBin,
		claudeModel:   claudeModel,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(devCORS)
	r.Use(s.requestLogger)

	// Health check — no auth.
	r.Get("/healthz", s.handleHealthz)

	// Internal trigger from FTC DV. No API-key auth (it is a service-to-service
	// signal, not a tenant call).
	// TODO(crn): lock down POST /internal/trigger by network policy and/or a
	// shared secret header; today it is open within the trusted network.
	r.Post("/internal/trigger", s.handleTrigger)

	// Internal ingest from FBD (via the Next proxy /api/fittcore). No API-key
	// auth — it is a service-to-service call within the trusted network, like
	// /internal/trigger. Materializes a full project payload into a build.
	r.Post("/internal/projects", s.handleIngest)

	// No-auth internal live-log WebSocket so the CRN dashboard can watch a build
	// without an org API key. Same stream as the /api/v1 variant, no auth.
	r.Get("/internal/projects/{id}/jobs/{build_no}/logs", s.handleInternalLogsWS)

	// No-auth interactive per-project terminal WebSocket: an OS shell running in
	// a PTY in the project's working dir, bridged to the browser. SECURITY: this
	// is a remote shell on the CRN host with NO auth — fine for local/trusted
	// dev, MUST be locked down (auth + network policy) in prod. See terminal.go.
	r.Get("/internal/projects/{id}/terminal", s.handleTerminalWS)

	// No-auth operator-console read model. A single snapshot of vitals, the
	// in-flight builds, the queue, all projects, and a recent activity feed.
	r.Get("/internal/dashboard", s.handleDashboard)

	// No-auth build traces (durable per-build state history). The live WS stream
	// is discarded when a build ends; these read the persisted snapshot so the
	// console can show what a build did, what commit it produced, and where it
	// pushed — retroactively. /traces = recent across all projects; /builds =
	// one project's history; /jobs/{id}/trace = one build's full event replay.
	r.Get("/internal/traces", s.handleRecentTraces)
	r.Get("/internal/projects/{id}/builds", s.handleListBuilds)
	r.Get("/internal/jobs/{id}/trace", s.handleGetTrace)

	// No-auth skills CRUD for the operator console. Skills are the SKILL.md
	// bodies injected into every build; built-in skills are editable but not
	// deletable.
	r.Get("/internal/skills", s.handleListSkills)
	r.Post("/internal/skills/upload", s.handleUploadSkill)
	r.Get("/internal/skills/{name}", s.handleGetSkill)
	r.Put("/internal/skills/{name}", s.handlePutSkill)
	r.Delete("/internal/skills/{name}", s.handleDeleteSkill)
	r.Get("/internal/skills/{name}/versions", s.handleListSkillVersions)
	// AI "improve skill": runs Claude to improve/expand a SKILL.md body and returns
	// the improved markdown for the operator to review (does NOT save it).
	r.Post("/internal/skills/{name}/improve", s.handleImproveSkill)

	// No-auth internal edit-build trigger: creates an EDIT build for an existing
	// project — Claude edits the project's existing repo instead of rebuilding
	// from a zip.
	r.Post("/internal/projects/{id}/edit", s.handleEditProject)

	// No-auth GitHub issues for a project (only meaningful under the "one repo per
	// project" model, i.e. CRN_GITHUB_OWNER set). List the project repo's open
	// issues, or enqueue an EDIT build that fixes a given issue.
	r.Get("/internal/projects/{id}/issues", s.handleListIssues)
	r.Post("/internal/projects/{id}/issues/{number}/fix", s.handleFixIssue)

	// No-auth in-demo feedback (Edit Request Panel): demo widgets write feedback
	// via PostgREST; these read it and act on it. Approve enqueues an edit build.
	r.Get("/internal/feedback", s.handleListFeedback)
	r.Get("/internal/feedback/{id}", s.handleGetFeedback)
	r.Post("/internal/feedback/{id}/approve", s.handleApproveFeedback)
	r.Post("/internal/feedback/{id}/reject", s.handleRejectFeedback)

	// Tenant-facing API, guarded by per-org API key.
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.apiKeyAuth)

		r.Post("/projects/{id}/edit-request", s.handleEditRequest)
		r.Get("/projects/{id}/status", s.handleStatus)
		r.Get("/projects/{id}/jobs/{build_no}/logs", s.handleLogsWS)
		r.Post("/projects/{id}/rollback/{build_no}", s.handleRollback)
	})

	return r
}

// --- Middleware ---

// devCORS adds permissive CORS headers so the operator dashboard (served from a
// different origin/port than the API in dev, and same-origin-agnostic in prod)
// can call the API from the browser. A simple cross-origin GET is otherwise
// blocked by the browser when the response lacks Access-Control-Allow-Origin.
//
// TODO(crn): in production restrict Access-Control-Allow-Origin to the known
// dashboard origin(s) rather than reflecting any Origin.
func devCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs each request via slog with method, path, status, and the
// chi request id.
func (s *server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}

// apiKeyAuth resolves the calling org from the X-API-Key header. The raw key is
// sha256-hex hashed and looked up via store.OrgByAPIKeyHash (active keys only).
// On success the *domain.Org is stashed in the request context.
func (s *server) apiKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("X-API-Key")
		if raw == "" {
			s.writeError(w, r, http.StatusUnauthorized, "missing X-API-Key header")
			return
		}

		sum := sha256.Sum256([]byte(raw))
		hash := hex.EncodeToString(sum[:])

		org, err := s.store.OrgByAPIKeyHash(r.Context(), hash)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrUnauthorized) {
				s.writeError(w, r, http.StatusUnauthorized, "invalid or revoked API key")
				return
			}
			s.logger.Error("api key lookup failed", "err", err, "request_id", middleware.GetReqID(r.Context()))
			s.writeError(w, r, http.StatusInternalServerError, "auth lookup failed")
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeyOrg, org)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// orgFromContext returns the org stashed by apiKeyAuth. The bool is false if no
// org is present (route mounted without the auth middleware).
func orgFromContext(ctx context.Context) (*domain.Org, bool) {
	org, ok := ctx.Value(ctxKeyOrg).(*domain.Org)
	return org, ok
}

// --- Handlers ---

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		s.logger.Error("healthz ping failed", "err", err)
		s.writeError(w, r, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDashboard returns the operator-console read model: vitals, in-flight
// builds, the FIFO queue, all projects with their latest status, and a recent
// activity feed. No auth — it is an internal, trusted-network console.
func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	snap, err := s.store.DashboardSnapshot(r.Context())
	if err != nil {
		s.logger.Error("dashboard snapshot failed", "err", err,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "dashboard failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, snap)
}

// --- Build traces (no-auth: durable per-build state history) ---

// handleRecentTraces returns the most recent build traces across all projects
// (summary only, no event stream) as {"traces":[...]}. ?limit=N caps the count
// (default 30).
func (s *server) handleRecentTraces(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	traces, err := s.store.RecentBuildTraces(r.Context(), limit)
	if err != nil {
		s.logger.Error("recent traces failed", "err", err,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "recent traces failed")
		return
	}
	if traces == nil {
		traces = []*domain.BuildTrace{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"traces": traces})
}

// handleListBuilds returns one project's build history newest-first (summary
// only, no event stream) as {"builds":[...]}.
func (s *server) handleListBuilds(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}
	traces, err := s.store.ListBuildTraces(r.Context(), projectID)
	if err != nil {
		s.logger.Error("list builds failed", "err", err, "project_id", projectID,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "list builds failed")
		return
	}
	if traces == nil {
		traces = []*domain.BuildTrace{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"builds": traces})
}

// handleGetTrace returns one build's full trace (summary + event stream for
// replay) by job id. 404 when no trace exists for the job.
func (s *server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	jobID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid job id")
		return
	}
	trace, err := s.store.GetBuildTrace(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "trace not found")
			return
		}
		s.logger.Error("get trace failed", "err", err, "job_id", jobID,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "get trace failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, trace)
}

// --- Skills (no-auth operator console CRUD) ---

// handleListSkills returns every skill as {"skills":[...]}.
func (s *server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := s.store.ListSkills(r.Context())
	if err != nil {
		s.logger.Error("list skills failed", "err", err,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "list skills failed")
		return
	}
	// Always render [] rather than null when there are no skills.
	if skills == nil {
		skills = []*domain.Skill{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"skills": skills})
}

// handleGetSkill returns one skill by name (404 if absent).
func (s *server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	skill, err := s.store.GetSkill(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "skill not found")
			return
		}
		s.logger.Error("get skill failed", "err", err, "name", name,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "get skill failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, skill)
}

// putSkillBody is the PUT /internal/skills/{name} payload. The name comes from
// the path; the body carries the editable fields. Files is the OPTIONAL set of
// extra files (scripts/, references/, ...) keyed by path relative to the skill
// dir; SKILL.md is carried in Body, not here.
type putSkillBody struct {
	Description string            `json:"description"`
	Body        string            `json:"body"`
	Files       map[string]string `json:"files"`
	Enabled     bool              `json:"enabled"`
}

// handlePutSkill upserts a skill by name and returns the persisted Skill.
func (s *server) handlePutSkill(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		s.writeError(w, r, http.StatusBadRequest, "skill name is required")
		return
	}

	var body putSkillBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate every extra-file path: relative only, no absolute path, no ".."
	// escape — these are written under {workdir}/.claude/skills/{name}/ at build
	// time and must not break out of the skill dir.
	for relPath := range body.Files {
		if !validSkillFilePath(relPath) {
			s.writeError(w, r, http.StatusBadRequest, "invalid file path: "+relPath)
			return
		}
	}

	skill := &domain.Skill{
		Name:        name,
		Description: body.Description,
		Body:        normalizeSkillBody(body.Body, name, body.Description),
		Files:       body.Files,
		Enabled:     body.Enabled,
	}
	if err := s.store.UpsertSkill(r.Context(), skill); err != nil {
		s.logger.Error("upsert skill failed", "err", err, "name", name,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "upsert skill failed")
		return
	}
	// Record a version snapshot of the resulting state (a user-initiated edit).
	if err := s.store.RecordSkillVersion(r.Context(), name, "edited"); err != nil {
		s.logger.Error("record skill version failed", "err", err, "name", name,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "record skill version failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, skill)
}

// branchSlug turns a product name into a git-branch-safe slug (lowercase,
// [a-z0-9] runs joined by single '-', trimmed, capped, fallback "project"). It
// mirrors jobs.slugify so the branch advertised at ingest matches the one the
// build actually pushes.
// branchSlug delegates to github.Slugify — a single slug implementation shared
// with the feedback watcher's repo-slug resolution.
func branchSlug(s string) string { return github.Slugify(s) }

// validSkillFilePath reports whether p is a safe relative path for a skill's
// extra file: non-empty, not absolute, and not climbing above the skill dir via
// "..". Mirrors the buildstep.InjectSkills check so a bad path is rejected at
// the API boundary rather than at build time.
func validSkillFilePath(p string) bool {
	if p == "" {
		return false
	}
	clean := filepath.Clean(p)
	if filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// handleDeleteSkill removes a non-built-in skill by name (204), returning 409
// for a built-in skill and 404 when absent.
func (s *server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.store.DeleteSkill(r.Context(), name); err != nil {
		if errors.Is(err, domain.ErrSkillBuiltin) {
			s.writeError(w, r, http.StatusConflict, "built-in skill cannot be deleted")
			return
		}
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "skill not found")
			return
		}
		s.logger.Error("delete skill failed", "err", err, "name", name,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "delete skill failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListSkillVersions returns a skill's version history newest-first as
// {"versions":[...]}. An unknown skill yields an empty list (200).
func (s *server) handleListSkillVersions(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	versions, err := s.store.ListSkillVersions(r.Context(), name)
	if err != nil {
		s.logger.Error("list skill versions failed", "err", err, "name", name,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "list skill versions failed")
		return
	}
	if versions == nil {
		versions = []*domain.SkillVersion{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"versions": versions})
}

// improveSkillBody is the POST /internal/skills/{name}/improve payload: the
// current SKILL.md body to improve. The improved body is returned; it is NOT
// saved (the operator reviews it in the editor first).
type improveSkillBody struct {
	Body string `json:"body"`
}

// handleImproveSkill runs Claude to improve/expand a SKILL.md body and returns
// {"body": improved}. It does not persist anything.
func (s *server) handleImproveSkill(w http.ResponseWriter, r *http.Request) {
	if s.claudeBin == "" {
		s.writeError(w, r, http.StatusServiceUnavailable, "claude CLI is not configured")
		return
	}

	var body improveSkillBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		s.writeError(w, r, http.StatusBadRequest, "body is required")
		return
	}

	improved, err := claude.Improve(r.Context(), s.claudeBin, s.claudeModel, body.Body, s.logger)
	if err != nil {
		s.logger.Error("improve skill failed", "err", err, "name", chi.URLParam(r, "name"),
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "improve skill failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"body": improved})
}

// uploadMaxMemory bounds how much of a multipart upload is buffered in memory
// before spilling to a temp file (16 MiB — a skill zip is small).
const uploadMaxMemory = 16 << 20

// handleUploadSkill accepts a multipart/form-data upload (field "file" = a .zip
// of a skill folder) and installs it as an enabled, non-builtin skill. The zip
// must contain a SKILL.md (top level or inside a single common top dir); its
// content becomes the skill body and every OTHER file becomes an extra file
// keyed by relative path. The skill name is resolved from the SKILL.md YAML
// frontmatter "name:", falling back to the form "name" field, the zip's top
// dir, then the zip filename — slugified to a valid [a-z0-9-] name. A version
// snapshot ("uploaded") is recorded. Returns the persisted Skill (200).
func (s *server) handleUploadSkill(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(uploadMaxMemory); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "missing 'file' upload")
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "could not read upload")
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid zip archive")
		return
	}

	parsed, err := parseSkillZip(zr)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Resolve the name: frontmatter -> form "name" -> zip top dir -> zip filename.
	name := parsed.frontmatterName
	if name == "" {
		name = r.FormValue("name")
	}
	if name == "" {
		name = parsed.topDir
	}
	if name == "" {
		name = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	}
	// branchSlug always yields a valid non-empty [a-z0-9-] name (fallback
	// "project"), so the resulting skill name is always installable.
	name = branchSlug(name)

	skill := &domain.Skill{
		Name:        name,
		Description: parsed.description,
		Body:        normalizeSkillBody(parsed.body, name, parsed.description),
		Files:       parsed.files,
		Enabled:     true,
	}
	if err := s.store.UpsertSkill(r.Context(), skill); err != nil {
		s.logger.Error("upsert uploaded skill failed", "err", err, "name", name,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "upsert skill failed")
		return
	}
	if err := s.store.RecordSkillVersion(r.Context(), name, "uploaded"); err != nil {
		s.logger.Error("record uploaded skill version failed", "err", err, "name", name,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "record skill version failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, skill)
}

// parsedSkillZip is the extracted content of an uploaded skill zip.
type parsedSkillZip struct {
	body            string            // SKILL.md content
	description     string            // parsed from the SKILL.md frontmatter "description:"
	frontmatterName string            // parsed from the SKILL.md frontmatter "name:"
	topDir          string            // the single common top-level dir, if any
	files           map[string]string // every non-SKILL.md entry, relpath -> content
}

// parseSkillZip walks a zip archive, strips a single common top-level directory
// prefix if present, treats the entry whose basename is "SKILL.md" as the skill
// body (parsing its YAML frontmatter name/description), and collects every other
// file into a relpath->content map. Paths that are absolute or contain ".." are
// rejected. Returns an error when no SKILL.md is found.
func parseSkillZip(zr *zip.Reader) (parsedSkillZip, error) {
	var out parsedSkillZip
	out.files = map[string]string{}

	topDir := commonTopDir(zr)
	out.topDir = topDir

	foundSkillMD := false
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Normalize to forward slashes and strip the common top dir prefix.
		rel := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if topDir != "" {
			rel = strings.TrimPrefix(rel, topDir+"/")
		}
		if rel == "" || rel == "." {
			continue
		}
		// Reject path traversal / absolute paths (defense in depth: these are the
		// keys later written under the skill dir at build time).
		if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			return parsedSkillZip{}, errors.New("zip contains an unsafe path: " + f.Name)
		}

		content, err := readZipEntry(f)
		if err != nil {
			return parsedSkillZip{}, errors.New("could not read zip entry " + f.Name)
		}

		if path.Base(rel) == "SKILL.md" {
			out.body = content
			out.frontmatterName, out.description = parseSkillFrontmatter(content)
			foundSkillMD = true
			continue
		}
		out.files[rel] = content
	}

	if !foundSkillMD {
		return parsedSkillZip{}, errors.New("zip has no SKILL.md")
	}
	return out, nil
}

// commonTopDir returns the single top-level directory shared by every entry in
// the zip, or "" if entries live at more than one top level (or at the root).
func commonTopDir(zr *zip.Reader) string {
	top := ""
	for _, f := range zr.File {
		name := path.Clean(strings.ReplaceAll(f.Name, "\\", "/"))
		if name == "" || name == "." {
			continue
		}
		first := name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			first = name[:i]
		} else if !f.FileInfo().IsDir() {
			// A file at the root -> no common top dir.
			return ""
		}
		if top == "" {
			top = first
		} else if top != first {
			return ""
		}
	}
	return top
}

// readZipEntry reads one zip entry's full content as a string.
func readZipEntry(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseSkillFrontmatter extracts "name:" and "description:" from a SKILL.md YAML
// frontmatter block (the leading "---" fenced section). Missing fields yield "".
func parseSkillFrontmatter(body string) (name, description string) {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if v, ok := frontmatterValue(line, "name"); ok {
			name = v
		}
		if v, ok := frontmatterValue(line, "description"); ok {
			description = v
		}
	}
	return name, description
}

// frontmatterValue parses a "key: value" frontmatter line, returning the trimmed
// (optionally quote-stripped) value when the key matches.
func frontmatterValue(line, key string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	prefix := key + ":"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	v := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
	v = strings.Trim(v, `"'`)
	return v, true
}

// normalizeSkillBody rewrites the SKILL.md frontmatter "name:" to match the
// skill's (kebab-case) record name so Claude Code will actually load it —
// uploaded skills often ship an invalid name (spaces/uppercase/&, e.g.
// "Vulnerability Scanning & Assessment"). Claude Code requires name to be
// [a-z0-9-] and match the skill directory (which CRN names by the record name).
// If the body has no frontmatter, a minimal valid block is prepended using the
// record name + description. Only the name is forced; an existing description is
// left untouched.
func normalizeSkillBody(body, name, description string) string {
	lines := strings.Split(body, "\n")

	// No opening fence -> prepend a minimal valid frontmatter block.
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body
	}

	// Find the closing fence and any existing name: line inside the block.
	closeIdx, nameIdx := -1, -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closeIdx = i
			break
		}
		if _, ok := frontmatterValue(lines[i], "name"); ok {
			nameIdx = i
		}
	}
	if closeIdx == -1 {
		// Malformed (unterminated) frontmatter — prepend a clean block instead.
		return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body
	}

	if nameIdx >= 0 {
		lines[nameIdx] = "name: " + name
		return strings.Join(lines, "\n")
	}
	// No name line inside the block — insert one right after the opening fence.
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[0], "name: "+name)
	out = append(out, lines[1:]...)
	return strings.Join(out, "\n")
}

// triggerBody is the FTC DV signal payload for POST /internal/trigger.
type triggerBody struct {
	JobID     uuid.UUID `json:"job_id"`
	ProjectID uuid.UUID `json:"project_id"`
	OrgID     uuid.UUID `json:"org_id"`
}

func (s *server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	var body triggerBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.JobID == uuid.Nil || body.ProjectID == uuid.Nil || body.OrgID == uuid.Nil {
		s.writeError(w, r, http.StatusBadRequest, "job_id, project_id and org_id are required")
		return
	}

	t := domain.TriggerRequest{
		JobID:     body.JobID,
		ProjectID: body.ProjectID,
		OrgID:     body.OrgID,
	}
	if err := s.jm.HandleTrigger(r.Context(), t); err != nil {
		// ErrOrgLocked is a benign "already building" — the trigger is just a
		// nudge; the in-flight build will chain the next queued job itself.
		if errors.Is(err, domain.ErrOrgLocked) {
			s.writeJSON(w, r, http.StatusAccepted, map[string]string{"status": "org busy, queued"})
			return
		}
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "job not found")
			return
		}
		s.logger.Error("handle trigger failed", "err", err, "job_id", body.JobID)
		s.writeError(w, r, http.StatusInternalServerError, "trigger failed")
		return
	}
	s.writeJSON(w, r, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// ingestBody is the full FBD -> CRN ingest payload (the wire contract). The
// strict decoder (DisallowUnknownFields) requires EVERY field to be declared —
// so adding a field here MUST be matched by FBD's payload builder, and vice-versa.
//
// The prototype ships as ONE zip (base64), not a per-file array: the build agent
// extracts it in the workdir (see jobs.parsePayload). idea/brd/prd also ride
// along as plain text (they're inside the zip under docs/ too) so the build
// prompt can be composed without unzipping. Tag marks the channel ("alpha-test").
type ingestBody struct {
	OrgID     string   `json:"org_id"`
	OrgName   string   `json:"org_name"`
	ProjectID string   `json:"project_id"`
	Name      string   `json:"name"`
	Tag       string   `json:"tag"`
	Idea      string   `json:"idea"`
	BRD       string   `json:"brd"`
	PRD       string   `json:"prd"`
	Prompts   []string `json:"prompts"`
	ZipName   string   `json:"zip_name"`
	ZipBase64 string   `json:"zip_base64"`
	// ZipURI, when set (and zip_base64 empty), is a URL the build downloads the
	// prototype zip from instead of receiving it inline — for large exports and
	// cross-machine (LAN) transfer. The strict decoder requires this field to be
	// declared, so a caller may send either zip_base64 or zip_uri.
	ZipURI    string `json:"zip_uri"`
	FileCount int    `json:"file_count"`
	ZipBytes  int    `json:"zip_bytes"`
}

// handleIngest accepts a full project payload from FBD, ensures the org and
// project exist, and enqueues a build whose payload is the full ingest body
// (so the jobs layer can materialize files + compose the Claude prompt).
func (s *server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var body ingestBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid request body")
		return
	}

	// --- org: parse or default, then upsert ---
	orgID := defaultOrgID
	if body.OrgID != "" {
		id, err := uuid.Parse(body.OrgID)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid org_id")
			return
		}
		orgID = id
	}
	orgName := body.OrgName
	if orgName == "" {
		orgName = "FBD Default"
	}
	org := &domain.Org{ID: orgID, Name: orgName}
	if err := s.store.EnsureOrg(r.Context(), org); err != nil {
		s.logger.Error("ensure org failed", "err", err, "org_id", orgID)
		s.writeError(w, r, http.StatusInternalServerError, "could not ensure org")
		return
	}
	orgID = org.ID // EnsureOrg may have generated the id

	// --- project: parse or mint, create if missing (re-export reuses it) ---
	projectID := uuid.New()
	if body.ProjectID != "" {
		id, err := uuid.Parse(body.ProjectID)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "invalid project_id")
			return
		}
		projectID = id
	}

	_, err := s.store.GetProject(r.Context(), projectID)
	if errors.Is(err, domain.ErrNotFound) {
		name := body.Name
		if name == "" {
			name = "Untitled project"
		}
		proj := &domain.Project{
			ID:     projectID,
			OrgID:  orgID,
			Name:   name,
			Status: domain.ProjectActive,
		}
		if err := s.store.CreateProject(r.Context(), proj); err != nil {
			s.logger.Error("create project failed", "err", err, "project_id", projectID)
			s.writeError(w, r, http.StatusInternalServerError, "could not create project")
			return
		}
		projectID = proj.ID // CreateProject may have generated the id
	} else if err != nil {
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	// Re-encode the FULL ingest body (incl files) as the job payload so the jobs
	// layer materializes the files and composes the prompt from brd/prd/prompts.
	payloadBytes, err := json.Marshal(body)
	if err != nil {
		s.logger.Error("marshal job payload failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not encode payload")
		return
	}

	job, err := s.jm.Enqueue(r.Context(), projectID, orgID, payloadBytes)
	if err != nil {
		s.logger.Error("enqueue job failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not enqueue build")
		return
	}

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{
		"project_id": projectID,
		"job_id":     job.ID,
		"build_no":   job.BuildNo,
		"org_id":     orgID,
		"git_remote": s.gitRemote,
		"git_branch": "crn/" + branchSlug(body.Name) + "-" + projectID.String()[:8],
		"status":     job.Status,
	})
}

// editProjectBody is the POST /internal/projects/{id}/edit payload: a free-text
// description of what to change in the existing project.
type editProjectBody struct {
	Change string `json:"change"`
}

// handleEditProject creates an EDIT build for an existing project: Claude edits
// the project's existing repo in place (clone/pull the branch, apply the change,
// push the same branch) instead of rebuilding from a zip. No auth — like ingest,
// it is a trusted-network internal call. Responds 202 with the queued job.
func (s *server) handleEditProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}

	var body editProjectBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Change) == "" {
		s.writeError(w, r, http.StatusBadRequest, "change is required")
		return
	}

	project, err := s.store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	// The edit payload the jobs layer decodes: mode=edit routes runJob down the
	// clone/pull + edit path; name reproduces the same branch slug as the build.
	payload, err := json.Marshal(map[string]string{
		"mode":   "edit",
		"change": body.Change,
		"name":   project.Name,
	})
	if err != nil {
		s.logger.Error("marshal edit payload failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not encode payload")
		return
	}

	job, err := s.jm.Enqueue(r.Context(), project.ID, project.OrgID, payload)
	if err != nil {
		s.logger.Error("enqueue edit job failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not enqueue build")
		return
	}

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{
		"job_id":     job.ID,
		"build_no":   job.BuildNo,
		"git_branch": "crn/" + branchSlug(project.Name) + "-" + project.ID.String()[:8],
		"status":     job.Status,
	})
}

// --- In-demo feedback (Edit Request Panel) ---

// handleListFeedback returns feedback requests, optionally filtered by ?status=
// (e.g. "new"), newest first. -> { "feedback": [...] }.
func (s *server) handleListFeedback(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListFeedback(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		s.logger.Error("list feedback failed", "err", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not load feedback")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"feedback": items})
}

// handleGetFeedback returns one feedback request by id.
func (s *server) handleGetFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid feedback id")
		return
	}
	f, err := s.store.GetFeedback(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "feedback not found")
			return
		}
		s.logger.Error("get feedback failed", "err", err, "feedback_id", id)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, f)
}

// handleApproveFeedback turns one feedback request into an EDIT build: it
// composes a change prompt from the note + pinned elements and enqueues it the
// same way as POST /internal/projects/{id}/edit, then links the job back and
// marks the request approved.
func (s *server) handleApproveFeedback(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid feedback id")
		return
	}
	f, err := s.store.GetFeedback(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "feedback not found")
			return
		}
		s.logger.Error("get feedback failed", "err", err, "feedback_id", id)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	// Atomically claim the request so a concurrent double-approve can't enqueue
	// two edit builds.
	if won, cerr := s.store.ClaimFeedbackForApproval(r.Context(), id); cerr != nil {
		s.logger.Error("claim feedback failed", "err", cerr, "feedback_id", id)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	} else if !won {
		s.writeError(w, r, http.StatusConflict, "feedback already actioned")
		return
	}

	project, err := s.store.GetProject(r.Context(), f.ProjectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project for this feedback not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", f.ProjectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	repoSlug := s.repoSlugForProject(project)

	// Ensure the feedback is mirrored (the watcher may not have caught up yet).
	issueNumber := 0
	if f.IssueNumber != nil {
		issueNumber = *f.IssueNumber
	} else if repoSlug != "" {
		// Idempotency: adopt an existing mirrored issue if one already exists.
		if existing, found, ferr := github.FindIssueByFeedback(r.Context(), repoSlug, id.String(), s.logger); ferr == nil && found {
			issueNumber = existing.Number
			if _, serr := s.store.SetFeedbackIssue(r.Context(), id, existing.Number, existing.URL); serr != nil {
				s.logger.Warn("persist adopted issue failed", "err", serr, "feedback_id", id)
			}
		} else {
			if err := github.EnsureLabels(r.Context(), repoSlug, s.logger); err != nil {
				s.logger.Warn("ensure labels failed", "err", err, "repo", repoSlug)
			}
			title, body, labels := feedback.IssueContent(f)
			iss, cerr := github.CreateIssue(r.Context(), repoSlug, title, body, labels, s.logger)
			if cerr != nil {
				s.logger.Warn("create issue on approve failed", "err", cerr, "feedback_id", id)
			} else if won, serr := s.store.SetFeedbackIssue(r.Context(), id, iss.Number, iss.URL); serr != nil {
				s.logger.Warn("persist feedback issue failed", "err", serr, "feedback_id", id)
				issueNumber = iss.Number
			} else if won {
				issueNumber = iss.Number
			} else {
				// Lost the create race to the watcher — close our duplicate, adopt the winner.
				if e := github.CloseIssue(r.Context(), repoSlug, iss.Number, "not planned", "Duplicate — feedback already mirrored.", s.logger); e != nil {
					s.logger.Warn("close duplicate issue failed", "err", e, "issue", iss.Number)
				}
				if reloaded, rerr := s.store.GetFeedback(r.Context(), id); rerr == nil && reloaded.IssueNumber != nil {
					issueNumber = *reloaded.IssueNumber
				}
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

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{
		"job_id":   job.ID,
		"build_no": job.BuildNo,
		"status":   job.Status,
	})
}

// handleRejectFeedback marks a feedback request rejected (no build).
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

// feedbackChangePrompt composes a Claude edit prompt from a feedback request:
// the overall note, then each pinned element (label, ask, and CSS selector).
func feedbackChangePrompt(f *domain.FeedbackRequest) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(f.Note))
	for _, p := range f.Payload.Pins {
		b.WriteString("\n- ")
		if p.Label != "" {
			b.WriteString(p.Label + ": ")
		}
		b.WriteString(strings.TrimSpace(p.Note))
		if p.Selector != "" {
			b.WriteString(" (element: " + p.Selector + ")")
		}
	}
	return b.String()
}

// repoSlugForProject returns the "owner/name" GitHub slug for a project under
// the "one repo per project" model, or "" when that model is disabled
// (s.githubOwner empty). It prefers parsing the project's stored repo_url and
// falls back to deriving "owner/crn-<slug>-<id8>" from the configured owner +
// the project name/id (matching what the jobs layer creates).
func (s *server) repoSlugForProject(p *domain.Project) string {
	return github.RepoSlug(s.githubOwner, p.RepoURL, p.Name, p.ID.String())
}

// handleListIssues returns the open GitHub issues of a project's repo as
// {"issues":[...]}. When the "one repo per project" model is disabled, or the
// project has no repo yet, it returns {"issues":[]} (never an error).
func (s *server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}

	project, err := s.store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	repoSlug := s.repoSlugForProject(project)
	if repoSlug == "" {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"issues": []github.Issue{}})
		return
	}

	issues, err := github.ListIssues(r.Context(), repoSlug, s.logger)
	if err != nil {
		s.logger.Error("list issues failed", "err", err, "repo", repoSlug,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "list issues failed")
		return
	}
	if issues == nil {
		issues = []github.Issue{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"issues": issues})
}

// handleFixIssue enqueues an EDIT build whose change is a GitHub issue's title +
// body, remembering the issue number so the build comments on it when done.
// Responds 202 with the queued job. Requires the "one repo per project" model.
func (s *server) handleFixIssue(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil || number <= 0 {
		s.writeError(w, r, http.StatusBadRequest, "invalid issue number")
		return
	}

	project, err := s.store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	repoSlug := s.repoSlugForProject(project)
	if repoSlug == "" {
		s.writeError(w, r, http.StatusBadRequest, "project has no GitHub repo (CRN_GITHUB_OWNER not set)")
		return
	}

	// Resolve the issue's title + body: they become the edit instruction.
	issues, err := github.ListIssues(r.Context(), repoSlug, s.logger)
	if err != nil {
		s.logger.Error("list issues failed", "err", err, "repo", repoSlug,
			"request_id", middleware.GetReqID(r.Context()))
		s.writeError(w, r, http.StatusInternalServerError, "list issues failed")
		return
	}
	var found *github.Issue
	for i := range issues {
		if issues[i].Number == number {
			found = &issues[i]
			break
		}
	}
	if found == nil {
		s.writeError(w, r, http.StatusNotFound, "issue not found")
		return
	}

	change := found.Title
	if strings.TrimSpace(found.Body) != "" {
		change = found.Title + "\n\n" + found.Body
	}

	// The edit payload the jobs layer decodes: mode=edit routes runJob down the
	// clone/pull + edit path; issue_number makes the build comment on the issue.
	payload, err := json.Marshal(map[string]any{
		"mode":         "edit",
		"change":       change,
		"name":         project.Name,
		"issue_number": number,
	})
	if err != nil {
		s.logger.Error("marshal fix-issue payload failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not encode payload")
		return
	}

	job, err := s.jm.Enqueue(r.Context(), project.ID, project.OrgID, payload)
	if err != nil {
		s.logger.Error("enqueue fix-issue job failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not enqueue build")
		return
	}

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{
		"job_id":   job.ID,
		"build_no": job.BuildNo,
		"status":   job.Status,
	})
}

// editRequestBody is the POST /projects/{id}/edit-request payload.
type editRequestBody struct {
	Requester   string              `json:"requester"`
	DiffRequest json.RawMessage     `json:"diff_request"`
	Priority    domain.EditPriority `json:"priority"`
}

func (s *server) handleEditRequest(w http.ResponseWriter, r *http.Request) {
	org, ok := orgFromContext(r.Context())
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, "missing org context")
		return
	}

	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}

	var body editRequestBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.DiffRequest) == 0 {
		s.writeError(w, r, http.StatusBadRequest, "diff_request is required")
		return
	}
	priority := body.Priority
	if priority == "" {
		priority = domain.PriorityNormal
	}

	// Enforce tenant isolation: the project must belong to the calling org.
	project, err := s.store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}
	if project.OrgID != org.ID {
		// Do not leak existence to another tenant.
		s.writeError(w, r, http.StatusNotFound, "project not found")
		return
	}

	er := &domain.EditRequest{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Requester:   body.Requester,
		DiffRequest: body.DiffRequest,
		Priority:    priority,
		Status:      domain.EditPending,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.store.CreateEditRequest(r.Context(), er); err != nil {
		s.logger.Error("create edit request failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not create edit request")
		return
	}

	// Hand off to the job manager: the edit request payload becomes the job's
	// requirement. The manager records the job (queued) and kicks it off if the
	// org is idle.
	job, err := s.jm.Enqueue(r.Context(), projectID, org.ID, body.DiffRequest)
	if err != nil {
		s.logger.Error("enqueue job failed", "err", err, "edit_request_id", er.ID)
		s.writeError(w, r, http.StatusInternalServerError, "could not enqueue build")
		return
	}

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{
		"edit_request_id": er.ID,
		"job_id":          job.ID,
		"build_no":        job.BuildNo,
		"status":          job.Status,
	})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	org, ok := orgFromContext(r.Context())
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, "missing org context")
		return
	}

	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}

	project, err := s.store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}
	if project.OrgID != org.ID {
		s.writeError(w, r, http.StatusNotFound, "project not found")
		return
	}

	view, err := s.jm.Status(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "no status for project")
			return
		}
		s.logger.Error("status failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "status failed")
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

// handleLogsWS is the tenant-facing (API-key-authenticated) live-log WebSocket.
// It enforces that the project belongs to the calling org, then streams.
func (s *server) handleLogsWS(w http.ResponseWriter, r *http.Request) {
	org, ok := orgFromContext(r.Context())
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, "missing org context")
		return
	}

	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}

	// Resolve the project up front so we can return a clean HTTP error and
	// enforce tenant isolation before upgrading.
	project, err := s.store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}
	if project.OrgID != org.ID {
		s.writeError(w, r, http.StatusNotFound, "project not found")
		return
	}

	s.streamJobLogsWS(w, r, projectID)
}

// handleInternalLogsWS is the no-auth internal variant used by the CRN
// dashboard. It skips the org check (service-to-service / trusted network) but
// shares the exact streaming body with the tenant-facing route.
func (s *server) handleInternalLogsWS(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}

	// Confirm the project exists so a bad id returns a clean 404 pre-upgrade.
	if _, err := s.store.GetProject(r.Context(), projectID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	s.streamJobLogsWS(w, r, projectID)
}

// streamJobLogsWS resolves the job for {build_no}, upgrades the connection, and
// streams the live BuildEventMsg feed until the job ends or the client
// disconnects. Both the tenant and internal WS handlers funnel through here so
// the upgrade + fan-out loop lives in one place. The caller has already
// resolved (and authorized) the project.
func (s *server) streamJobLogsWS(w http.ResponseWriter, r *http.Request, projectID uuid.UUID) {
	buildNo, err := parseBuildNo(chi.URLParam(r, "build_no"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid build_no")
		return
	}

	jobID, err := s.findJobByBuildNo(r.Context(), projectID, buildNo)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "build not found")
			return
		}
		s.logger.Error("resolve job failed", "err", err, "project_id", projectID, "build_no", buildNo)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The dashboard is served from a different origin/port (e.g. :3001) than
		// the API (:8080), so the default same-origin check rejects the handshake
		// with a 403. Skip it in dev to match devCORS.
		// TODO(crn): set OriginPatterns to the known dashboard origin(s) in prod
		// instead of skipping verification.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote an HTTP error response.
		s.logger.Warn("ws accept failed", "err", err)
		return
	}
	// CloseNow on any abnormal exit; the normal-path close is issued below.
	defer conn.CloseNow()

	history, events, unsubscribe := s.jm.Subscribe(r.Context(), jobID)
	defer unsubscribe()

	ctx := r.Context()
	// Replay buffered history first so a client that connects or refreshes
	// mid-build sees what already happened instead of a blank console.
	for _, msg := range history {
		if err := wsjson.Write(ctx, conn, msg); err != nil {
			s.logger.Warn("ws replay write failed", "err", err, "job_id", jobID)
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "client gone")
			return
		case msg, open := <-events:
			if !open {
				// Job finished — close cleanly.
				_ = conn.Close(websocket.StatusNormalClosure, "stream complete")
				return
			}
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				s.logger.Warn("ws write failed", "err", err, "job_id", jobID)
				return
			}
		}
	}
}

func (s *server) handleRollback(w http.ResponseWriter, r *http.Request) {
	org, ok := orgFromContext(r.Context())
	if !ok {
		s.writeError(w, r, http.StatusUnauthorized, "missing org context")
		return
	}

	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}
	buildNo, err := parseBuildNo(chi.URLParam(r, "build_no"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid build_no")
		return
	}

	project, err := s.store.GetProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.writeError(w, r, http.StatusNotFound, "project not found")
			return
		}
		s.logger.Error("get project failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "lookup failed")
		return
	}
	if project.OrgID != org.ID {
		s.writeError(w, r, http.StatusNotFound, "project not found")
		return
	}

	// TODO(crn): real rollback — re-tag the docker image for build_no as the
	// current deploy (docker pull {tag}:v{build_no} -> retag :latest -> push),
	// then notifier.Notify a build_done event so FBD/FTC DV re-deploy. The HTTP
	// shape is final; only the docker/notify body is deferred.
	s.logger.Info("rollback requested (not yet implemented)",
		"project_id", projectID, "build_no", buildNo, "org_id", org.ID)
	s.writeError(w, r, http.StatusNotImplemented, "rollback not yet implemented")
}

// --- Helpers ---

// findJobByBuildNo resolves the job id for a project's build number via the
// store, returning domain.ErrNotFound (-> 404) when the build does not exist.
func (s *server) findJobByBuildNo(ctx context.Context, projectID uuid.UUID, buildNo int) (uuid.UUID, error) {
	job, err := s.store.JobByBuildNo(ctx, projectID, buildNo)
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

// parseBuildNo parses the {build_no} path segment, tolerating an optional 'v'
// prefix (e.g. "v7" or "7").
func parseBuildNo(s string) (int, error) {
	if len(s) > 0 && (s[0] == 'v' || s[0] == 'V') {
		s = s[1:]
	}
	n := 0
	if s == "" {
		return 0, errors.New("empty build_no")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("non-numeric build_no")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// decodeJSON strictly decodes the request body into v.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// writeJSON writes v as a JSON response with the given status code.
func (s *server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("write json response failed", "err", err,
			"request_id", middleware.GetReqID(r.Context()))
	}
}

// writeError writes a JSON error envelope with the given status code.
func (s *server) writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.writeJSON(w, r, status, map[string]string{"error": msg})
}
