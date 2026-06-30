// Package api is the HTTP/WebSocket transport layer: a chi router exposing the
// CRN REST API (CRN-architecture.md §2.4) plus the live log WebSocket, the
// internal trigger endpoint for FTC DV, and health checks. It also houses the
// per-org API-key auth middleware.
//
// OWNED BY: the 'api' implementer.
//
// Routes registered by NewServer:
//
//	GET  /healthz                                   -> store.Ping
//	POST /internal/trigger                          -> jm.HandleTrigger   (FTC DV signal)
//	Route /api/v1 (group, apiKeyAuth):
//	  POST /projects/{id}/edit-request              -> store.CreateEditRequest + jm.Enqueue
//	  GET  /projects/{id}/status                    -> jm.Status
//	  GET  /projects/{id}/jobs/{build_no}/logs      -> WebSocket: jm.Subscribe, stream BuildEventMsg
//	  POST /projects/{id}/rollback/{build_no}       -> TODO(crn): docker retag + notify
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
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
	logger    *slog.Logger
	store     domain.Store
	jm        domain.JobManager
	gitRemote string // advertised back to FBD in the ingest response
}

// defaultOrgID is the org an ingest call is attributed to when the body carries
// no org_id (FBD single-tenant dev). It is a fixed, valid UUID.
var defaultOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000000fb")

// NewServer constructs the chi router with all CRN routes registered and
// returns it as an http.Handler. gitRemote is echoed back to FBD in the ingest
// response so the caller knows where the build will be pushed.
func NewServer(logger *slog.Logger, store domain.Store, jm domain.JobManager, gitRemote string) http.Handler {
	s := &server{logger: logger, store: store, jm: jm, gitRemote: gitRemote}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
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

// ingestFile is one file in an ingest payload.
type ingestFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ingestBody is the full FBD -> CRN ingest payload (the wire contract). The
// strict decoder (DisallowUnknownFields) requires EVERY field to be declared.
type ingestBody struct {
	OrgID     string       `json:"org_id"`
	OrgName   string       `json:"org_name"`
	ProjectID string       `json:"project_id"`
	Name      string       `json:"name"`
	BRD       string       `json:"brd"`
	PRD       string       `json:"prd"`
	Prompts   []string     `json:"prompts"`
	Files     []ingestFile `json:"files"`
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
		"git_branch": "crn/" + projectID.String(),
		"status":     job.Status,
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
		// TODO(crn): restrict OriginPatterns to the dashboard origin(s) in prod;
		// the request host is always allowed by coder/websocket.
	})
	if err != nil {
		// Accept already wrote an HTTP error response.
		s.logger.Warn("ws accept failed", "err", err)
		return
	}
	// CloseNow on any abnormal exit; the normal-path close is issued below.
	defer conn.CloseNow()

	events, unsubscribe := s.jm.Subscribe(r.Context(), jobID)
	defer unsubscribe()

	ctx := r.Context()
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
