package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
)

// The terminal is a shell on the build host with CRN's environment and no auth;
// unless the operator turns it on, the route does not exist.
func TestTerminalRouteIsGoneUnlessEnabled(t *testing.T) {
	projects := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewServer(logger, &settingsStore{}, nil, "", "", projects, "", false, "", buildsettings.Defaults{Model: "claude-opus-5", Effort: "xhigh"})
	project := uuid.New()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/projects/"+project.String()+"/terminal", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("terminal with CRN_TERMINAL_ENABLED off: %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(projects, project.String())); err == nil {
		t.Error("the disabled terminal still made the project's workdir")
	}
}
