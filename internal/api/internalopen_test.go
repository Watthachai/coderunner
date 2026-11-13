package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
)

// The operator asked (13 Nov) that the dashboard on their own laptop need no key. Only the
// address the connection really comes from counts: RealIP rewrites RemoteAddr from
// X-Forwarded-For, which any caller can send.
func openChain(open ...string) http.Handler {
	ips := map[netip.Addr]bool{}
	for _, ip := range open {
		ips[netip.MustParseAddr(ip)] = true
	}
	s := &server{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		internalTokens:  map[string]string{"operator": sha("op-secret")},
		internalOpenIPs: ips,
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return s.peerAddr(middleware.RealIP(s.internalAuth(ok)))
}

func TestAnOpenAddressNeedsNoKey(t *testing.T) {
	cases := []struct {
		name, from, forwarded string
		ws                    bool
		want                  int
	}{
		{"the operator's laptop", "172.168.1.128:51234", "", false, http.StatusOK},
		{"the operator's laptop, live log", "172.168.1.128:51234", "", true, http.StatusOK},
		{"another machine", "172.168.1.235:40000", "", false, http.StatusUnauthorized},
		{"another machine claiming the laptop", "172.168.1.235:40000", "172.168.1.128", false, http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/projects", nil)
			req.RemoteAddr = c.from
			if c.forwarded != "" {
				req.Header.Set("X-Forwarded-For", c.forwarded)
				req.Header.Set("X-Real-IP", c.forwarded)
			}
			if c.ws {
				req.Method = http.MethodGet
				req.Header.Set("Upgrade", "websocket")
			}
			rec := httptest.NewRecorder()
			openChain("172.168.1.128").ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestNoOpenAddressMeansEveryCallNeedsAKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/projects", nil)
	req.RemoteAddr = "172.168.1.128:51234"
	rec := httptest.NewRecorder()
	openChain().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}
