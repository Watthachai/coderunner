package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// AuthMonitor watches the Claude Code login every build depends on, so the
// operator console can say "logged out" before a build fails on it.
//
// CheckStatus runs `claude auth status`: free, it reads the stored credentials.
// Probe sends one minimal real request (Haiku, no tools, no saved session): it
// proves the server still accepts them. Run checks the status on every tick and
// probes once per probe interval — and on every tick while the login is broken,
// so a fixed login turns green within a tick.
type AuthMonitor struct {
	bin       string
	expiresAt *time.Time
	logger    *slog.Logger
	// run executes the CLI with args (seams for tests).
	run func(ctx context.Context, args ...string) ([]byte, error)
	now func() time.Time

	mu        sync.Mutex
	checked   bool
	checkedAt time.Time
	loggedIn  bool
	method    string
	statusErr string
	probed    bool
	probeAt   time.Time
	probeOK   bool
	probeErr  string
	lastState string
}

// NewAuthMonitor watches the CLI at bin. expiresAt is the operator-set expiry of
// a long-lived token (nil when unknown, e.g. a keychain login).
func NewAuthMonitor(bin string, expiresAt *time.Time, logger *slog.Logger) *AuthMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	m := &AuthMonitor{bin: bin, expiresAt: expiresAt, logger: logger.With("component", "claude.auth"), now: time.Now}
	m.run = m.runCLI
	return m
}

// runCLI runs the CLI with the same environment builds give Claude, from a
// scratch directory so no project's settings or memory apply.
func (m *AuthMonitor) runCLI(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "crn-claude-auth-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, m.bin, args...)
	cmd.Env = claudeEnv()
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return out.Bytes(), err
}

// CheckStatus reads the stored login (`claude auth status`, JSON by default).
func (m *AuthMonitor) CheckStatus(ctx context.Context) {
	out, err := m.run(ctx, "auth", "status", "--json")
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	parseErr := json.Unmarshal(lastJSON(out), &st)
	m.mu.Lock()
	m.checked, m.checkedAt = true, m.now()
	switch {
	case parseErr == nil:
		// `auth status` exits non-zero when logged out; the JSON still says so.
		m.loggedIn, m.method, m.statusErr = st.LoggedIn, st.AuthMethod, ""
	case err != nil:
		m.loggedIn, m.method, m.statusErr = false, "", shortDetail(string(out)+" "+err.Error())
	default:
		m.loggedIn, m.method, m.statusErr = false, "", "unreadable auth status: "+shortDetail(string(out))
	}
	m.mu.Unlock()
	m.logChange()
}

// Probe sends one minimal real request and records whether it was accepted.
func (m *AuthMonitor) Probe(ctx context.Context) {
	out, err := m.run(ctx, "-p", "Reply with the single word OK.", "--model", "haiku", "--tools", "", "--output-format", "json", "--no-session-persistence", "--strict-mcp-config")
	var res struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	parseErr := json.Unmarshal(lastJSON(out), &res)
	ok := err == nil && parseErr == nil && !res.IsError
	detail := ""
	if !ok {
		switch {
		case parseErr == nil && res.Result != "":
			detail = shortDetail(res.Result)
		case err != nil:
			detail = shortDetail(string(out) + " " + err.Error())
		default:
			detail = "unreadable probe result: " + shortDetail(string(out))
		}
	}
	m.mu.Lock()
	m.probed, m.probeAt, m.probeOK, m.probeErr = true, m.now(), ok, detail
	m.mu.Unlock()
	m.logChange()
}

// Status is the snapshot the console shows.
func (m *AuthMonitor) Status() domain.ClaudeAuthStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *AuthMonitor) statusLocked() domain.ClaudeAuthStatus {
	s := domain.ClaudeAuthStatus{State: "unknown", LoggedIn: m.loggedIn, AuthMethod: m.method}
	if m.checked {
		at := m.checkedAt
		s.CheckedAt = &at
	}
	if m.probed {
		at, ok := m.probeAt, m.probeOK
		s.ProbeAt, s.ProbeOK = &at, &ok
	}
	expired := false
	if m.expiresAt != nil {
		exp := *m.expiresAt
		days := int(math.Floor(exp.Sub(m.now()).Hours() / 24))
		s.TokenExpiresAt, s.DaysLeft = &exp, &days
		s.ExpiryWarning = days <= 30
		expired = !m.now().Before(exp)
	}
	switch {
	case !m.checked:
	case m.statusErr != "":
		s.State, s.Detail = "error", m.statusErr
	case !m.loggedIn:
		s.State, s.Detail = "logged_out", "Claude Code is not logged in on the build machine"
	case expired:
		s.State, s.Detail = "token_expired", "the long-lived Claude Code token has expired"
	case m.probed && !m.probeOK:
		s.State, s.Detail = "probe_failed", m.probeErr
	default:
		s.State = "ok"
	}
	return s
}

// logChange logs when the state moves, so the server log carries the history.
func (m *AuthMonitor) logChange() {
	m.mu.Lock()
	s := m.statusLocked()
	prev := m.lastState
	m.lastState = s.State
	m.mu.Unlock()
	if s.State == prev {
		return
	}
	if s.State == "ok" {
		m.logger.Info("claude auth ok", "auth_method", s.AuthMethod)
		return
	}
	m.logger.Warn("claude auth not ok", "state", s.State, "detail", s.Detail)
}

// tick is one round of Run: the status always, the probe when due or while the
// login is broken (but logged in, so a probe can tell).
func (m *AuthMonitor) tick(ctx context.Context, probeDue bool) {
	m.CheckStatus(ctx)
	st := m.Status()
	if st.LoggedIn && st.State != "error" && (probeDue || st.State != "ok") {
		m.Probe(ctx)
	}
}

// Run checks every checkEvery and probes every probeEvery (and right away) until
// ctx ends.
func (m *AuthMonitor) Run(ctx context.Context, checkEvery, probeEvery time.Duration) {
	lastProbe := time.Time{}
	for {
		due := lastProbe.IsZero() || m.now().Sub(lastProbe) >= probeEvery
		m.tick(ctx, due)
		if due {
			lastProbe = m.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkEvery):
		}
	}
}

// lastJSON is the last line of out that looks like a JSON object (the CLI may
// print warnings first).
func lastJSON(out []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "{") {
			return []byte(l)
		}
	}
	return bytes.TrimSpace(out)
}

// shortDetail keeps an error short and free of anything that looks like a key.
func shortDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "sk-ant-") || len(f) > 60 {
			s = strings.ReplaceAll(s, f, "[redacted]")
		}
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
