package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func sha(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// guarded runs one request through internalAuth with tokens and reports the
// status and whether the handler behind it ran.
func guarded(t *testing.T, tokens map[string]string, req *http.Request) (int, bool) {
	t.Helper()
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), internalTokens: tokens}
	ran := false
	h := s.internalAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, ran
}

func TestInternalRoutesNeedAKnownBearerOnceTokensAreSet(t *testing.T) {
	tokens := map[string]string{"fittcore": sha("ftc-secret"), "operator": sha("op-secret")}
	cases := []struct {
		name   string
		method string
		target string
		bearer string
		ws     bool
		want   int
	}{
		{"no token", http.MethodPost, "/internal/projects", "", false, http.StatusUnauthorized},
		{"unknown token", http.MethodPost, "/internal/projects", "guess", false, http.StatusUnauthorized},
		{"Fittcore's token", http.MethodPost, "/internal/projects", "ftc-secret", false, http.StatusOK},
		{"operator's token", http.MethodPost, "/internal/jobs/x/cancel", "op-secret", false, http.StatusOK},
		{"token in the query of a plain request", http.MethodPost, "/internal/jobs/x/cancel?access_token=op-secret", "", false, http.StatusUnauthorized},
		{"token in the query of a WebSocket handshake", http.MethodGet, "/internal/projects/p/jobs/1/logs?access_token=op-secret", "", true, http.StatusOK},
		{"wrong token in a WebSocket query", http.MethodGet, "/internal/projects/p/jobs/1/logs?access_token=guess", "", true, http.StatusUnauthorized},
		{"health check stays open", http.MethodGet, "/healthz", "", false, http.StatusOK},
		{"tenant API keeps its own auth", http.MethodGet, "/api/v1/projects/p/status", "", false, http.StatusOK},
		{"preflight passes to CORS", http.MethodOptions, "/internal/projects", "", false, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.target, nil)
			if c.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+c.bearer)
			}
			if c.ws {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			code, ran := guarded(t, tokens, req)
			if code != c.want || ran != (c.want == http.StatusOK) {
				t.Errorf("status %d, handler ran %v; want %d", code, ran, c.want)
			}
		})
	}
}

// Until the tokens are set, /internal stays as open as it was, so CRN can be
// deployed ahead of the switch.
func TestInternalRoutesStayOpenWithoutTokens(t *testing.T) {
	if code, ran := guarded(t, nil, httptest.NewRequest(http.MethodPost, "/internal/projects", nil)); code != http.StatusOK || !ran {
		t.Errorf("status %d, handler ran %v; want the request through", code, ran)
	}
}
