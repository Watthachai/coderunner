package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

type dashStore struct{ domain.Store }

func (dashStore) DashboardSnapshot(context.Context) (*domain.DashboardSnapshot, error) {
	return &domain.DashboardSnapshot{}, nil
}

// The console polls the dashboard; the Claude login it shows rides along.
func TestDashboardCarriesTheClaudeLogin(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), store: dashStore{},
		claudeAuth: func() domain.ClaudeAuthStatus {
			return domain.ClaudeAuthStatus{State: "logged_out", Detail: "not logged in"}
		}}
	w := httptest.NewRecorder()
	s.handleDashboard(w, httptest.NewRequest("GET", "/internal/dashboard", nil))
	var got struct {
		ClaudeAuth *domain.ClaudeAuthStatus `json:"claude_auth"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.ClaudeAuth == nil || got.ClaudeAuth.State != "logged_out" {
		t.Fatalf("dashboard %d %s: claude_auth = %+v", w.Code, w.Body.String(), got.ClaudeAuth)
	}

	s.claudeAuth = nil
	w = httptest.NewRecorder()
	s.handleDashboard(w, httptest.NewRequest("GET", "/internal/dashboard", nil))
	got.ClaudeAuth = nil
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.ClaudeAuth != nil {
		t.Fatalf("without a monitor the dashboard still names a login: %s", w.Body.String())
	}
}
