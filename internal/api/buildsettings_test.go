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
	return NewServer(logger, st, nil, "", "", "", "", false, "", defaults, nil)
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
