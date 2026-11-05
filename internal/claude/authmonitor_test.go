package claude

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeCLI answers `claude auth status` and the probe with canned output.
type fakeCLI struct {
	status, probe       string
	statusErr, probeErr error
	calls               [][]string
}

func (f *fakeCLI) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
		return []byte(f.status), f.statusErr
	}
	return []byte(f.probe), f.probeErr
}

func (f *fakeCLI) probes() int {
	n := 0
	for _, c := range f.calls {
		if c[0] == "-p" {
			n++
		}
	}
	return n
}

var authNow = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

func testMonitor(cli *fakeCLI, expires *time.Time) *AuthMonitor {
	m := NewAuthMonitor("claude", expires, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.run = cli.run
	m.now = func() time.Time { return authNow }
	return m
}

func TestAuthStatusIsUnknownBeforeTheFirstCheck(t *testing.T) {
	if got := testMonitor(&fakeCLI{}, nil).Status(); got.State != "unknown" {
		t.Fatalf("state = %q, want unknown", got.State)
	}
}

func TestCheckStatusReadsTheLogin(t *testing.T) {
	m := testMonitor(&fakeCLI{status: `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty"}`}, nil)
	m.CheckStatus(context.Background())
	got := m.Status()
	if got.State != "ok" || !got.LoggedIn || got.AuthMethod != "claude.ai" || got.CheckedAt == nil || !got.CheckedAt.Equal(authNow) {
		t.Fatalf("status = %+v", got)
	}
}

// Logged out is the case the dashboard exists for: builds will fail until
// someone logs in on the build machine.
func TestCheckStatusSeesALoggedOutCLI(t *testing.T) {
	m := testMonitor(&fakeCLI{status: `{"loggedIn":false,"authMethod":"none"}`, statusErr: errors.New("exit status 1")}, nil)
	m.CheckStatus(context.Background())
	if got := m.Status(); got.State != "logged_out" || got.LoggedIn {
		t.Fatalf("status = %+v, want logged_out", got)
	}
}

func TestCheckStatusThatCannotRunIsAnError(t *testing.T) {
	m := testMonitor(&fakeCLI{status: "zsh: command not found: claude", statusErr: errors.New("exit status 127")}, nil)
	m.CheckStatus(context.Background())
	got := m.Status()
	if got.State != "error" || got.Detail == "" {
		t.Fatalf("status = %+v, want error with a detail", got)
	}
}

// The status only reads stored credentials; the daily probe is a real request,
// so a token the server no longer accepts shows up too.
func TestAFailedProbeMarksTheLoginBroken(t *testing.T) {
	cli := &fakeCLI{status: `{"loggedIn":true,"authMethod":"claude.ai"}`, probe: `{"type":"result","is_error":true,"result":"Invalid API key · Please run /login"}`, probeErr: errors.New("exit status 1")}
	m := testMonitor(cli, nil)
	m.CheckStatus(context.Background())
	m.Probe(context.Background())
	got := m.Status()
	if got.State != "probe_failed" || got.ProbeOK == nil || *got.ProbeOK || !strings.Contains(got.Detail, "Please run /login") {
		t.Fatalf("status = %+v, want probe_failed naming the reason", got)
	}

	cli.probe, cli.probeErr = `{"type":"result","is_error":false,"result":"OK"}`, nil
	m.Probe(context.Background())
	if got := m.Status(); got.State != "ok" || got.ProbeOK == nil || !*got.ProbeOK || got.Detail != "" {
		t.Fatalf("after a good probe: %+v, want ok", got)
	}
}

// The probe is as cheap as a request can be: smallest model, no tools, nothing
// saved as a session.
func TestTheProbeIsAMinimalRequest(t *testing.T) {
	cli := &fakeCLI{probe: `{"is_error":false,"result":"OK"}`}
	testMonitor(cli, nil).Probe(context.Background())
	args := strings.Join(cli.calls[0], " ")
	for _, want := range []string{"-p ", "--model haiku", "--tools ", "--output-format json", "--no-session-persistence", "--strict-mcp-config"} {
		if !strings.Contains(args, want) {
			t.Errorf("probe args %q lack %q", args, want)
		}
	}
}

// A long-lived token's expiry is set by the operator (CRN_CLAUDE_TOKEN_EXPIRES):
// the dashboard counts down and warns from 30 days out; an expired one is broken.
func TestTokenExpiryCountdown(t *testing.T) {
	ok := &fakeCLI{status: `{"loggedIn":true,"authMethod":"oauth_token"}`}
	for _, tc := range []struct {
		in    time.Duration
		days  int
		warn  bool
		state string
	}{
		{60 * 24 * time.Hour, 60, false, "ok"},
		{30 * 24 * time.Hour, 30, true, "ok"},
		{10 * 24 * time.Hour, 10, true, "ok"},
		{-24 * time.Hour, -1, true, "token_expired"},
	} {
		exp := authNow.Add(tc.in)
		m := testMonitor(ok, &exp)
		m.CheckStatus(context.Background())
		got := m.Status()
		if got.DaysLeft == nil || *got.DaysLeft != tc.days || got.ExpiryWarning != tc.warn || got.State != tc.state {
			t.Errorf("expires in %v: days %v warn %v state %q, want %d %v %q", tc.in, got.DaysLeft, got.ExpiryWarning, got.State, tc.days, tc.warn, tc.state)
		}
	}
	m := testMonitor(ok, nil)
	m.CheckStatus(context.Background())
	if got := m.Status(); got.DaysLeft != nil || got.ExpiryWarning || got.TokenExpiresAt != nil {
		t.Errorf("unknown expiry shows a countdown: %+v", got)
	}
}

// Each tick checks the status; the probe runs when it is due, and again on every
// tick while the login is broken, so a fixed login turns green within a tick.
func TestTickProbesWhenDueAndWhileBroken(t *testing.T) {
	cli := &fakeCLI{status: `{"loggedIn":true,"authMethod":"claude.ai"}`, probe: `{"is_error":true,"result":"Invalid API key"}`, probeErr: errors.New("exit status 1")}
	m := testMonitor(cli, nil)
	m.tick(context.Background(), true)
	if cli.probes() != 1 {
		t.Fatalf("probes after a due tick = %d, want 1", cli.probes())
	}
	m.tick(context.Background(), false)
	if cli.probes() != 2 {
		t.Fatalf("probes while broken = %d, want 2", cli.probes())
	}
	cli.probe, cli.probeErr = `{"is_error":false,"result":"OK"}`, nil
	m.tick(context.Background(), false)
	m.tick(context.Background(), false)
	if cli.probes() != 3 || m.Status().State != "ok" {
		t.Fatalf("probes %d state %q: want one more probe, then none while ok", cli.probes(), m.Status().State)
	}
}
