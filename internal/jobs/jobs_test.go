package jobs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
)

func TestResetWorkspace(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "proj")

	// Build a non-empty tree (file + nested dir + file) — what a prior build left.
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "src", "b.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := resetWorkspace(work, "job1", slog.Default()); err != nil {
		t.Fatalf("resetWorkspace: %v", err)
	}
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("workDir should be gone after reset, stat err = %v", err)
	}
	if olds, _ := filepath.Glob(work + ".stale-*"); len(olds) != 0 {
		t.Errorf("stale asides not cleaned up: %v", olds)
	}

	// A second reset on a now-missing workDir is a no-op (first-build path).
	if err := resetWorkspace(work, "job2", slog.Default()); err != nil {
		t.Errorf("reset on missing dir should be nil, got %v", err)
	}
}

func TestCodeFromToolInput(t *testing.T) {
	cases := []struct {
		name, tool, input     string
		wantBefore, wantAfter string
	}{
		{"write", "Write", `{"file_path":"a.ts","content":"line1\nline2"}`, "", "line1\nline2"},
		{"edit", "Edit", `{"file_path":"a.ts","old_string":"foo","new_string":"bar"}`, "foo", "bar"},
		{"multiedit", "MultiEdit", `{"edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d"}]}`, "a\nc\n", "b\nd\n"},
		{"bash", "Bash", `{"command":"ls"}`, "", ""},
		{"empty", "Write", ``, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before, after := codeFromToolInput(c.tool, json.RawMessage(c.input))
			if before != c.wantBefore || after != c.wantAfter {
				t.Errorf("got (%q,%q), want (%q,%q)", before, after, c.wantBefore, c.wantAfter)
			}
		})
	}
}

func TestCapDiff(t *testing.T) {
	if got := capDiff("short"); got != "short" {
		t.Errorf("short capped: %q", got)
	}
	long := strings.Repeat("x", maxDiffRunes+500)
	if got := capDiff(long); !strings.HasSuffix(got, "… (truncated)") {
		t.Errorf("long: missing truncation marker")
	}
	// Rune-safe: a multibyte string over the cap must not produce a broken rune.
	thai := strings.Repeat("ก", maxDiffRunes+10)
	got := capDiff(thai)
	if strings.ContainsRune(got, '�') {
		t.Errorf("cap split a multibyte rune")
	}
}

// allowAnyDial relaxes the zip_uri SSRF address screen for a test (httptest
// binds to loopback, which downloadZip blocks by default).
func allowAnyDial(t *testing.T) {
	t.Helper()
	orig := zipDialAllowed
	zipDialAllowed = func(string) error { return nil }
	t.Cleanup(func() { zipDialAllowed = orig })
}

func TestParsePayloadZipURI(t *testing.T) {
	allowAnyDial(t)
	const zipContent = "PK\x03\x04 fake zip bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(zipContent))
	}))
	defer srv.Close()

	payload := []byte(`{"name":"X","zip_name":"proto.zip","zip_uri":"` + srv.URL + `"}`)
	spec := parsePayload(json.RawMessage(payload), nil)

	if len(spec.Files) != 1 {
		t.Fatalf("zip_uri should materialize 1 file, got %d", len(spec.Files))
	}
	if spec.Files[0].Path != "proto.zip" {
		t.Errorf("path = %q, want proto.zip", spec.Files[0].Path)
	}
	if spec.Files[0].Content != zipContent {
		t.Errorf("downloaded content mismatch: %q", spec.Files[0].Content)
	}
}

func TestParsePayloadZipURIFailureIsBestEffort(t *testing.T) {
	allowAnyDial(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	payload := []byte(`{"name":"X","zip_name":"proto.zip","zip_uri":"` + srv.URL + `"}`)
	spec := parsePayload(json.RawMessage(payload), nil) // nil logger must be safe

	if len(spec.Files) != 0 {
		t.Errorf("failed download should materialize nothing, got %d files", len(spec.Files))
	}
}

func TestFTCCallback(t *testing.T) {
	var gotMethod, gotAuth, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	org := uuid.New()
	m := &manager{ftcdvCallbackURL: srv.URL, ftcdvCallbackToken: "secret-tok", ftcdvCallbackOrgs: map[uuid.UUID]bool{org: true}, logger: slog.Default()}
	m.ftcCallback(uuid.New(), org, 7, "reg.test/crn/app:v7", "sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682", "acme/repo", "main", "released", "", buildstep.NewDemoEnvExample(4123))

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer secret-tok" {
		t.Errorf("auth = %q, want 'Bearer secret-tok'", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	for _, want := range []string{`"status":"released"`, `"build_no":7`, `"job_id"`, `"image_ref":"reg.test/crn/app:v7"`, `"image_digest":"sha256:714a220cd8777936c0e67865c20a9340f2494de2d4fc732332e366e3e0448682"`, `"git_remote":"acme/repo"`, `"git_branch":"main"`, `"env"`, `"DATABASE_URL"`, `"APP_PORT":"4123"`, `"DEV_EMAIL"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body missing %q\n%s", want, gotBody)
		}
	}
}

// Fittcore's callback URL receives only the builds of the orgs it orders; every
// other org's job id, image and env stay off it.
func TestFTCCallbackGoesOnlyToTheOrgsListed(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JobID string `json:"job_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body.JobID
	}))
	defer srv.Close()
	ftc, other := uuid.New(), uuid.New()
	m := &manager{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		ftcdvCallbackURL:  srv.URL,
		ftcdvCallbackOrgs: map[uuid.UUID]bool{ftc: true},
	}
	ftcJob, otherJob := uuid.New(), uuid.New()

	m.ftcCallback(otherJob, other, 1, "reg/other:v1", "", "", "", "released", "", map[string]string{"SECRET_NAME": ""})
	m.ftcCallback(ftcJob, ftc, 2, "reg/ftc:v2", "", "", "", "released", "", nil)

	select {
	case id := <-got:
		if id != ftcJob.String() {
			t.Errorf("callback carried job %s, want only Fittcore's %s", id, ftcJob)
		}
	default:
		t.Fatal("Fittcore's own build sent no callback")
	}
	select {
	case id := <-got:
		t.Errorf("another org's build reached the callback URL: %s", id)
	default:
	}
}

// Fittcore's callback endpoint presents a certificate from its own CA. The
// callback client trusts that CA and nothing else; without it the same server
// is refused.
func TestFTCCallbackTrustsTheCAItIsGiven(t *testing.T) {
	got := make(chan struct{}, 4)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- struct{}{}
	}))
	defer srv.Close()
	org := uuid.New()
	ca := x509.NewCertPool()
	ca.AddCert(srv.Certificate())
	send := func(client *http.Client) bool {
		m := &manager{
			logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
			ftcdvCallbackURL:  srv.URL,
			ftcdvCallbackOrgs: map[uuid.UUID]bool{org: true},
			ftcdvClient:       client,
		}
		m.ftcCallback(uuid.New(), org, 1, "", "", "", "", "building", "", nil)
		select {
		case <-got:
			return true
		default:
			return false
		}
	}
	if !send(callbackClient(ca)) {
		t.Error("callback with Fittcore's CA did not reach the server")
	}
	if send(callbackClient(nil)) {
		t.Error("callback reached a server whose CA the client was never given")
	}
}

func TestFTCCallbackOmitsAnUnknownDigest(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()

	org := uuid.New()
	m := &manager{ftcdvCallbackURL: srv.URL, ftcdvCallbackOrgs: map[uuid.UUID]bool{org: true}, logger: slog.Default()}
	m.ftcCallback(uuid.New(), org, 1, "branch:main", "", "", "", "building", "", nil)
	if strings.Contains(gotBody, "image_digest") {
		t.Errorf("building callback carries a digest: %s", gotBody)
	}
}

// fastRetry is the agreed callback retry schedule shrunk to test time.
var fastRetry = callbackRetry{window: 2 * time.Second, waits: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}, attempt: 300 * time.Millisecond}

func callbackManager(url string, org uuid.UUID) *manager {
	return &manager{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		ftcdvCallbackURL:  url,
		ftcdvCallbackOrgs: map[uuid.UUID]bool{org: true},
		cbRetry:           fastRetry,
	}
}

// callbackRecorder answers callbacks with codes in turn (the last one repeats)
// and keeps what each send carried.
type callbackRecorder struct {
	codes      []int
	retryAfter string
	mu         sync.Mutex
	sends      []recordedSend
}

type recordedSend struct {
	body, correlation string
	at                time.Time
}

func (c *callbackRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	code := c.codes[min(len(c.sends), len(c.codes)-1)]
	c.sends = append(c.sends, recordedSend{string(b), r.Header.Get("X-Correlation-Id"), time.Now()})
	c.mu.Unlock()
	if c.retryAfter != "" {
		w.Header().Set("Retry-After", c.retryAfter)
	}
	if code >= 300 && code < 400 {
		w.Header().Set("Location", "/elsewhere")
	}
	w.WriteHeader(code)
}

func (c *callbackRecorder) got() []recordedSend {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]recordedSend(nil), c.sends...)
}

// sendTo sends one "building" callback for a new job through m to rec.
func sendTo(t *testing.T, rec *callbackRecorder, tune func(*manager)) []recordedSend {
	t.Helper()
	srv := httptest.NewServer(rec)
	defer srv.Close()
	org := uuid.New()
	m := callbackManager(srv.URL, org)
	if tune != nil {
		tune(m)
	}
	m.ftcCallback(uuid.New(), org, 1, "", "", "", "", "building", "", nil)
	return rec.got()
}

// An answer that may change on a second try is sent again, three sends at most,
// each with the same body and the same correlation id.
func TestFTCCallbackRetriesWhatMayPass(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503, 504} {
		sends := sendTo(t, &callbackRecorder{codes: []int{code, code, 200}}, nil)
		if len(sends) != 3 {
			t.Errorf("%d: %d sends, want 3", code, len(sends))
			continue
		}
		if sends[0].correlation == "" {
			t.Errorf("%d: no X-Correlation-Id", code)
		}
		for _, s := range sends[1:] {
			if s.body != sends[0].body || s.correlation != sends[0].correlation {
				t.Errorf("%d: a retry changed the body or correlation id:\n%+v\n%+v", code, sends[0], s)
			}
		}
	}
}

func TestFTCCallbackSendsAtMostThreeTimes(t *testing.T) {
	if sends := sendTo(t, &callbackRecorder{codes: []int{503}}, nil); len(sends) != 3 {
		t.Errorf("%d sends to an endpoint that keeps answering 503, want 3", len(sends))
	}
}

func TestFTCCallbackDoesNotRetryAnAnswerThatWillNotChange(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 422, 302} {
		if sends := sendTo(t, &callbackRecorder{codes: []int{code, 200}}, nil); len(sends) != 1 {
			t.Errorf("%d: %d sends, want 1", code, len(sends))
		}
	}
}

// After 409 CRN sends nothing more for that build until the two sides agree;
// other builds are not affected.
func TestFTCCallbackStopsTheBuildAt409(t *testing.T) {
	rec := &callbackRecorder{codes: []int{409, 200}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	org := uuid.New()
	m := callbackManager(srv.URL, org)
	job := uuid.New()
	m.ftcCallback(job, org, 1, "", "", "", "", "building", "", nil)
	m.ftcCallback(job, org, 1, "", "", "", "", "released", "", nil)
	if n := len(rec.got()); n != 1 {
		t.Fatalf("%d sends for a build that got 409, want 1", n)
	}
	m.ftcCallback(uuid.New(), org, 1, "", "", "", "", "building", "", nil)
	if n := len(rec.got()); n != 2 {
		t.Errorf("another build's callback was held back by the 409 (%d sends, want 2)", n)
	}
}

func TestFTCCallbackWaitsForRetryAfterWithinTheMinute(t *testing.T) {
	window := func(m *manager) { m.cbRetry.window = 3 * time.Second }
	sends := sendTo(t, &callbackRecorder{codes: []int{429, 200}, retryAfter: "1"}, window)
	if len(sends) != 2 {
		t.Fatalf("%d sends after 429 with Retry-After: 1, want 2", len(sends))
	}
	if gap := sends[1].at.Sub(sends[0].at); gap < time.Second {
		t.Errorf("sent again after %v, before Retry-After's 1s", gap)
	}
	if sends := sendTo(t, &callbackRecorder{codes: []int{429, 200}, retryAfter: "5"}, window); len(sends) != 1 {
		t.Errorf("%d sends when Retry-After falls outside the window, want 1", len(sends))
	}
}

func TestFTCCallbackRetriesATimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // the server notices the client hang up only after the body
		if calls.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
	}))
	defer srv.Close()
	org := uuid.New()
	callbackManager(srv.URL, org).ftcCallback(uuid.New(), org, 1, "", "", "", "", "building", "", nil)
	if n := calls.Load(); n != 2 {
		t.Errorf("%d sends after the first timed out, want 2", n)
	}
}

func TestFTCCallbackRetriesARefusedConnection(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	var dials atomic.Int32
	org := uuid.New()
	m := callbackManager("http://"+addr+"/cb", org)
	m.ftcdvClient = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, a)
	}}}
	m.ftcCallback(uuid.New(), org, 1, "", "", "", "", "building", "", nil)
	if n := dials.Load(); n != 3 {
		t.Errorf("%d connection attempts to a refusing port, want 3", n)
	}
}

func TestFTCCallbackDoesNotRetryAnUntrustedCertificate(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.StartTLS()
	defer srv.Close()
	org := uuid.New()
	m := callbackManager(srv.URL, org)
	m.ftcdvClient = callbackClient(nil)
	m.ftcCallback(uuid.New(), org, 1, "", "", "", "", "building", "", nil)
	if n := conns.Load(); n != 1 {
		t.Errorf("%d connections to a server whose certificate does not verify, want 1", n)
	}
}

// A build's callbacks reach FTC DV in the order they were sent: "released"
// waits while "building" is still being retried.
func TestFTCCallbacksOfABuildGoInOrder(t *testing.T) {
	first := make(chan struct{})
	var mu sync.Mutex
	var statuses []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		statuses = append(statuses, body.Status)
		n := len(statuses)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			close(first)
		}
	}))
	defer srv.Close()
	org := uuid.New()
	m := callbackManager(srv.URL, org)
	m.cbRetry.waits = []time.Duration{200 * time.Millisecond, 200 * time.Millisecond}
	job := uuid.New()
	done := make(chan struct{}, 2)
	go func() { m.ftcCallback(job, org, 1, "", "", "", "", "building", "", nil); done <- struct{}{} }()
	<-first
	go func() { m.ftcCallback(job, org, 1, "", "", "", "", "released", "", nil); done <- struct{}{} }()
	<-done
	<-done
	if got := strings.Join(statuses, ","); got != "building,building,released" {
		t.Errorf("FTC DV received %s, want building,building,released", got)
	}
}

func TestRetryableSend(t *testing.T) {
	post := func(err error) error { return &url.Error{Op: "Post", URL: "https://ftc.test/cb", Err: err} }
	sys := func(e syscall.Errno) error {
		return post(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", e)})
	}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", sys(syscall.ECONNREFUSED), true},
		{"connection reset", sys(syscall.ECONNRESET), true},
		{"timeout", post(context.DeadlineExceeded), true},
		{"no route to host", sys(syscall.EHOSTUNREACH), false},
		{"EOF", post(io.EOF), false},
		{"untrusted certificate", post(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), false},
	} {
		if got := retryableSend(c.err); got != c.want {
			t.Errorf("%s: retryable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSourceHash(t *testing.T) {
	a := []buildstep.FileEntry{{Path: "a.ts", Content: "x"}, {Path: "b.ts", Content: "y"}}
	// Order-independent: same files in any order → same hash.
	b := []buildstep.FileEntry{{Path: "b.ts", Content: "y"}, {Path: "a.ts", Content: "x"}}
	if sourceHash(a) != sourceHash(b) {
		t.Error("hash must be order-independent")
	}
	// Content-sensitive.
	c := []buildstep.FileEntry{{Path: "a.ts", Content: "x"}, {Path: "b.ts", Content: "Y"}}
	if sourceHash(a) == sourceHash(c) {
		t.Error("hash must change when content changes")
	}
	// Length-prefix prevents a concatenation collision between {"ab","c"} and {"a","bc"}.
	if sourceHash([]buildstep.FileEntry{{Path: "ab", Content: "c"}}) ==
		sourceHash([]buildstep.FileEntry{{Path: "a", Content: "bc"}}) {
		t.Error("length-prefix must prevent concatenation collision")
	}
	// No files (e.g. an edit build) → "" (reuse disabled).
	if sourceHash(nil) != "" {
		t.Error("empty files must hash to empty string")
	}
}

func TestFTCCallbackDisabledWhenNoURL(t *testing.T) {
	m := &manager{logger: slog.Default()} // no URL configured
	// Must be a silent no-op (no panic, no network).
	m.ftcCallback(uuid.New(), uuid.New(), 1, "", "", "", "", "building", "", nil)
}

func TestDownloadZipBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	if _, err := downloadZip(srv.URL); err == nil {
		t.Fatal("expected loopback address to be blocked (SSRF guard)")
	}
}

func TestDownloadZipRejectsNonHTTPScheme(t *testing.T) {
	if _, err := downloadZip("file:///etc/passwd"); err == nil {
		t.Fatal("expected non-http scheme to be rejected")
	}
}

// The 💬 feedback widget is opt-in. A build request turns it on only with
// "feedback_widget": true; without that a new build ships no widget. An edit
// that does not say keeps whatever the project already has, so a demo's
// feedback loop survives its own fix builds.
func TestParsePayloadFeedbackWidget(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name    string
		payload string
		want    *bool
	}{
		{"build, not asked", `{"name":"Hello Fitt"}`, nil},
		{"build, on", `{"name":"Hello Fitt","feedback_widget":true}`, &yes},
		{"build, off", `{"name":"Hello Fitt","feedback_widget":false}`, &no},
		{"edit, not asked", `{"mode":"edit","change":"x","name":"Hello Fitt"}`, nil},
		{"edit, off", `{"mode":"edit","change":"x","name":"Hello Fitt","feedback_widget":false}`, &no},
	} {
		got := parsePayload(json.RawMessage(tc.payload), nil).FeedbackWidget
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: FeedbackWidget = %v, want %v", tc.name, fmtBoolPtr(got), fmtBoolPtr(tc.want))
		}
	}
}

func TestFeedbackWidgetPlan(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name              string
		spec              payloadSpec
		inject, removeOld bool
	}{
		{"build, not asked: off", payloadSpec{}, false, true},
		{"build, on", payloadSpec{FeedbackWidget: &yes}, true, false},
		{"build, off", payloadSpec{FeedbackWidget: &no}, false, true},
		{"edit, not asked: keep as is", payloadSpec{Mode: "edit"}, false, false},
		{"edit, on", payloadSpec{Mode: "edit", FeedbackWidget: &yes}, true, false},
		{"edit, off: remove", payloadSpec{Mode: "edit", FeedbackWidget: &no}, false, true},
	} {
		inject, removeOld := feedbackWidgetPlan(tc.spec)
		if inject != tc.inject || removeOld != tc.removeOld {
			t.Errorf("%s: inject=%v remove=%v, want inject=%v remove=%v", tc.name, inject, removeOld, tc.inject, tc.removeOld)
		}
	}
}

func fmtBoolPtr(b *bool) string {
	if b == nil {
		return "<not asked>"
	}
	return fmt.Sprint(*b)
}
