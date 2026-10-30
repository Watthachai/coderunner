package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
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
		proto  string
		want   int
	}{
		{"no token", http.MethodPost, "/internal/projects", "", false, "", http.StatusUnauthorized},
		{"unknown token", http.MethodPost, "/internal/projects", "guess", false, "", http.StatusUnauthorized},
		{"Fittcore's token", http.MethodPost, "/internal/projects", "ftc-secret", false, "", http.StatusOK},
		{"operator's token", http.MethodPost, "/internal/jobs/x/cancel", "op-secret", false, "", http.StatusOK},
		{"token in the query of a plain request", http.MethodPost, "/internal/jobs/x/cancel?access_token=op-secret", "", false, "", http.StatusUnauthorized},
		{"token in a WebSocket query", http.MethodGet, "/internal/projects/p/jobs/1/logs?access_token=op-secret", "", true, "", http.StatusUnauthorized},
		{"token as the WebSocket subprotocol", http.MethodGet, "/internal/projects/p/jobs/1/logs", "", true, "crn-bearer, op-secret", http.StatusOK},
		{"wrong token as the WebSocket subprotocol", http.MethodGet, "/internal/projects/p/jobs/1/logs", "", true, "crn-bearer, guess", http.StatusUnauthorized},
		{"health check stays open", http.MethodGet, "/healthz", "", false, "", http.StatusOK},
		{"tenant API keeps its own auth", http.MethodGet, "/api/v1/projects/p/status", "", false, "", http.StatusOK},
		{"preflight passes to CORS", http.MethodOptions, "/internal/projects", "", false, "", http.StatusOK},
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
			if c.proto != "" {
				req.Header.Set("Sec-WebSocket-Protocol", c.proto)
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

// A browser that offers subprotocols drops the connection unless the server
// picks one of them, so CRN's WebSockets pick crn-bearer.
func TestInternalWebSocketsPickTheBearerSubprotocol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, internalWSAccept)
		if err != nil {
			return
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{Subprotocols: []string{"crn-bearer", "op-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if got := conn.Subprotocol(); got != "crn-bearer" {
		t.Errorf("subprotocol picked = %q, want crn-bearer", got)
	}
}

// The dashboard sends its key in Authorization, which makes the browser ask
// first; a preflight that does not list the header blocks every call.
func TestPreflightAllowsTheAuthorizationHeader(t *testing.T) {
	h := devCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/internal/dashboard", nil)
	req.Header.Set("Origin", "http://172.168.1.171:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	allowed := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowed, "authorization") {
		t.Errorf("preflight allows headers %q, want Authorization among them", allowed)
	}
}
