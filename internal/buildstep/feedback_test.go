package buildstep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectFeedbackWidget(t *testing.T) {
	dir := t.TempDir()
	html := "<!doctype html><html><head><title>Cafe</title></head><body><h1>Cafe</h1></body></html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := InjectFeedbackWidget(dir, "proj-123", "http://localhost:3010/feedback_requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("patched = %d, want 1", n)
	}

	out, _ := os.ReadFile(filepath.Join(dir, "index.html"))
	s := string(out)
	for _, want := range []string{
		`data-fitt-feedback`,                                    // marker attribute
		`data-project="proj-123"`,                               // baked project id
		`data-ingest="http://localhost:3010/feedback_requests"`, // baked ingest url
		"fitt-feedback-host",                                    // from the widget body
		"</script>\n</body>",                                    // inserted before </body>
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q", want)
		}
	}

	// Idempotent: a second pass must not double-inject.
	n2, err := InjectFeedbackWidget(dir, "proj-123", "http://localhost:3010/feedback_requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("second pass patched %d, want 0 (idempotent)", n2)
	}
}

func TestInjectFeedbackWidgetEmptyIngestSkips(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<body></body>"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := InjectFeedbackWidget(dir, "p", "", nil)
	if err != nil || n != 0 {
		t.Fatalf("empty ingest: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestInjectFeedbackWidgetNextLayout(t *testing.T) {
	dir := t.TempDir()
	// Next.js App Router app: app/layout.tsx with a <body>, NO index.html.
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutPath := filepath.Join(dir, "app", "layout.tsx")
	layout := "export default function RootLayout({ children }) {\n" +
		"  return (<html><body>{children}</body></html>);\n}\n"
	if err := os.WriteFile(layoutPath, []byte(layout), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := InjectFeedbackWidget(dir, "proj-xyz", "http://localhost:3010/feedback_requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("patched = %d, want 1 (next layout)", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "public", "fitt-feedback.js")); err != nil {
		t.Errorf("public/fitt-feedback.js not written: %v", err)
	}
	out, _ := os.ReadFile(layoutPath)
	s := string(out)
	for _, want := range []string{
		`src="/fitt-feedback.js"`,
		`data-project="proj-xyz"`,
		// data-ingest is a JSX expression reading RUNTIME env (build-time url is the fallback).
		`data-ingest={process.env.FITT_FEEDBACK_URL ?? "http://localhost:3010/feedback_requests"}`,
		"</body>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("layout missing %q", want)
		}
	}

	// Idempotent: a second pass must not double-inject.
	n2, err := InjectFeedbackWidget(dir, "proj-xyz", "http://localhost:3010/feedback_requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("second pass patched %d, want 0 (idempotent)", n2)
	}
}

// Regression: a built Next.js app has a gitignored .next/ dir full of prerendered
// .html. injectIntoHTML must skip it — otherwise it patches those throwaway pages,
// returns early, and the real app/layout.tsx never gets the widget script.
func TestInjectFeedbackWidgetSkipsNextBuildOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutPath := filepath.Join(dir, "app", "layout.tsx")
	layout := "export default function RootLayout({ children }) {\n" +
		"  return (<html><body>{children}</body></html>);\n}\n"
	if err := os.WriteFile(layoutPath, []byte(layout), 0o644); err != nil {
		t.Fatal(err)
	}
	nextDir := filepath.Join(dir, ".next", "server", "app")
	if err := os.MkdirAll(nextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nextHTML := filepath.Join(nextDir, "_not-found.html")
	if err := os.WriteFile(nextHTML, []byte("<html><body>404</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := InjectFeedbackWidget(dir, "proj-next", "http://localhost:3010/feedback_requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("patched = %d, want 1 (the layout, not .next)", n)
	}
	// The layout — not the .next html — must carry the widget.
	out, _ := os.ReadFile(layoutPath)
	if !strings.Contains(string(out), `src="/fitt-feedback.js"`) {
		t.Errorf("layout missing widget script (injection short-circuited by .next)")
	}
	if _, err := os.Stat(filepath.Join(dir, "public", "fitt-feedback.js")); err != nil {
		t.Errorf("public/fitt-feedback.js not written: %v", err)
	}
	nx, _ := os.ReadFile(nextHTML)
	if strings.Contains(string(nx), "data-fitt-feedback") {
		t.Errorf(".next html was patched but must be skipped")
	}
}
