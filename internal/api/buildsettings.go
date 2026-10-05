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
