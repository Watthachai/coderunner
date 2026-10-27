package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
