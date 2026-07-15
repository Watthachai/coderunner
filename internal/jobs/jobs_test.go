package jobs

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

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

	m := &manager{ftcdvCallbackURL: srv.URL, ftcdvCallbackToken: "secret-tok", logger: slog.Default()}
	m.ftcCallback(uuid.New(), 7, "", "released", "")

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer secret-tok" {
		t.Errorf("auth = %q, want 'Bearer secret-tok'", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	for _, want := range []string{`"status":"released"`, `"build_no":7`, `"job_id"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body missing %q\n%s", want, gotBody)
		}
	}
}

func TestFTCCallbackDisabledWhenNoURL(t *testing.T) {
	m := &manager{logger: slog.Default()} // no URL configured
	// Must be a silent no-op (no panic, no network).
	m.ftcCallback(uuid.New(), 1, "", "building", "")
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
