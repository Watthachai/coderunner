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
	logger *slog.Logger
	store  domain.Store
	jm     domain.JobManager
}

// NewServer constructs the chi router with all CRN routes registered and
// returns it as an http.Handler. Signature is fixed by cmd/server/main.go.
func NewServer(logger *slog.Logger, store domain.Store, jm domain.JobManager) http.Handler {
	s := &server{logger: logger, store: store, jm: jm}

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

// handleLogsWS upgrades to a WebSocket and streams the live BuildEventMsg
// stream for the job identified by {id}/{build_no} until the channel closes or
// the client disconnects.
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
	buildNo, err := parseBuildNo(chi.URLParam(r, "build_no"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid build_no")
		return
	}

	// Resolve the job before upgrading so we can answer with a clean HTTP error
	// (you cannot send a meaningful HTTP status after the WS handshake).
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

// findJobByBuildNo resolves the job id for a project's build number.
//
// TODO(crn): the Store port has no GetJobByBuildNo method, so this scaffold
// cannot resolve a job id from (project_id, build_no) yet. Wire this once the
// store exposes a lookup (or the WS route is keyed by job_id instead). Until
// then it reports ErrNotFound so the handler returns a clean 404 rather than
// streaming a bogus subscription.
func (s *server) findJobByBuildNo(ctx context.Context, projectID uuid.UUID, buildNo int) (uuid.UUID, error) {
	_ = ctx
	_ = projectID
	_ = buildNo
	return uuid.Nil, domain.ErrNotFound
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
