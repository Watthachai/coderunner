package buildstep

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

	// A second pass puts this CRN's widget in again: one tag, the same page.
	if _, err := InjectFeedbackWidget(dir, "proj-123", "http://localhost:3010/feedback_requests", nil); err != nil {
		t.Fatal(err)
	}
	if again := readFile(t, filepath.Join(dir, "index.html")); again != s || strings.Count(again, "<script data-fitt-feedback") != 1 {
		t.Errorf("second pass changed the page or doubled the widget:\n%s", again)
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
		// The widget posts to the app's own route; the receiver is the route's business.
		`data-ingest="/api/fitt-feedback"`,
		"</body>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("layout missing %q", want)
		}
	}
	// A page Next prerenders at build time would bake an env read here for good.
	if strings.Contains(s, "process.env") {
		t.Error("layout reads process.env; a prerendered page keeps the build's value")
	}
	route := readFile(t, filepath.Join(dir, "app", "api", "fitt-feedback", "route.ts"))
	if !strings.Contains(route, "process.env.FITT_FEEDBACK_URL") || !strings.Contains(route, `"http://localhost:3010/feedback_requests"`) {
		t.Errorf("route must read FITT_FEEDBACK_URL with the build's URL as fallback:\n%s", route)
	}

	// A second pass puts this CRN's widget in again: one tag, the same layout and route.
	if _, err := InjectFeedbackWidget(dir, "proj-xyz", "http://localhost:3010/feedback_requests", nil); err != nil {
		t.Fatal(err)
	}
	if again := readFile(t, layoutPath); again != s || strings.Count(again, "fitt-feedback.js") != 1 {
		t.Errorf("second pass changed the layout or doubled the widget:\n%s", again)
	}
	if again := readFile(t, filepath.Join(dir, "app", "api", "fitt-feedback", "route.ts")); again != route {
		t.Error("second pass changed the route")
	}
}

// An app keeps the widget an earlier build put in until a build replaces it. Before 20
// Nov 2026 none did: OKR (build 13) and NTC (build 8) still carried the 27 Oct widget,
// without the 📋 window, the full-size pin picture or the route's 503 retry.
func TestInjectReplacesAnEarlierBuildsWidget(t *testing.T) {
	const ingest = "http://localhost:3010/feedback_requests"
	t.Run("next layout", func(t *testing.T) {
		dir := t.TempDir()
		layoutPath := filepath.Join(dir, "src", "app", "layout.tsx")
		writeTestFile(t, layoutPath, "export default function RootLayout({ children }) {\n"+
			"  return (\n    <html>\n      <body>\n        {children}\n"+
			`        <script src="/fitt-feedback.js" data-fitt-feedback="true" data-project="proj-xyz" data-ingest="/api/fitt-feedback" />`+
			"\n      </body>\n    </html>\n  );\n}\n")
		writeTestFile(t, filepath.Join(dir, "public", "fitt-feedback.js"), "/* the 27 Oct widget */")
		writeTestFile(t, filepath.Join(dir, "src", "app", "api", "fitt-feedback", "route.ts"), "// the 27 Oct fitt-feedback route\n")

		if _, err := InjectFeedbackWidget(dir, "proj-xyz", ingest, nil); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dir, "public", "fitt-feedback.js")); got != feedbackWidgetJS {
			t.Errorf("public/fitt-feedback.js is still the earlier widget: %.60q", got)
		}
		route := readFile(t, filepath.Join(dir, "src", "app", "api", "fitt-feedback", "route.ts"))
		if strings.Contains(route, "27 Oct") || !strings.Contains(route, "api/site/widget") || !strings.Contains(route, "Retry-After") {
			t.Errorf("the route is not this CRN's:\n%.300s", route)
		}
		layout := readFile(t, layoutPath)
		if strings.Count(layout, `src="/fitt-feedback.js"`) != 1 || !strings.Contains(layout, `data-tickets="/api/fitt-feedback"`) {
			t.Errorf("the layout does not load this CRN's widget once:\n%s", layout)
		}
	})
	t.Run("html", func(t *testing.T) {
		dir := t.TempDir()
		page := filepath.Join(dir, "index.html")
		writeTestFile(t, page, "<!doctype html><html><body><h1>Cafe</h1>"+
			`<script data-fitt-feedback data-project="proj-123">/* the 27 Oct widget */</script>`+"\n</body></html>")
		if _, err := InjectFeedbackWidget(dir, "proj-123", ingest, nil); err != nil {
			t.Fatal(err)
		}
		got := readFile(t, page)
		if strings.Contains(got, "27 Oct") || strings.Count(got, "<script data-fitt-feedback") != 1 || !strings.Contains(got, "fitt-feedback-host") {
			t.Errorf("the page does not carry this CRN's widget once:\n%.400s", got)
		}
	})
}

// A layout that mentions the widget without loading it cannot take this CRN's: the build
// is told, rather than shipping an app that lost the widget it had.
func TestInjectSaysWhenItCannotPutTheWidgetBack(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "app", "layout.tsx"),
		"// CRN adds fitt-feedback here\nexport default function L({ children }) { return (<html><body>{children}</body></html>); }\n")
	writeTestFile(t, filepath.Join(dir, "public", "fitt-feedback.js"), "/* the 27 Oct widget */")
	_, err := InjectFeedbackWidget(dir, "p", "http://localhost:3010/feedback_requests", nil)
	if err == nil || !strings.Contains(err.Error(), "could not be put in") {
		t.Fatalf("err = %v, want the widget could not be put in", err)
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

// The widget is added after the AI has written and passed its own browser tests,
// and CRN's gate then reruns those tests against the shipped image. Playwright
// pierces open shadow roots, so an app test such as
// expect(page.getByRole("button")).toHaveCount(1) counted the 💬 button and the
// gate failed a build the AI had passed (Hello Fitt, 21 Oct). A closed root keeps
// the widget on screen and clickable while Playwright cannot see into it.
func TestFeedbackWidgetIsHiddenFromAppTests(t *testing.T) {
	if !strings.Contains(feedbackWidgetJS, `attachShadow({ mode: "closed" })`) {
		t.Error(`widget must attach a closed shadow root so the app's own browser tests cannot see its controls`)
	}
	if strings.Contains(feedbackWidgetJS, `mode: "open"`) {
		t.Error(`widget still attaches an open shadow root`)
	}
	// host.shadowRoot is null for a closed root; the widget must keep using the
	// reference attachShadow returned.
	if strings.Contains(feedbackWidgetJS, ".shadowRoot") {
		t.Error(`widget reads .shadowRoot, which is null for a closed root`)
	}
}

// Customer deliveries ship without the widget unless the build asks for it.
// RemoveFeedbackWidget takes out a widget an earlier build left behind, so an
// edit build that turns it off leaves no widget code in the source.
func TestRemoveFeedbackWidgetHTML(t *testing.T) {
	dir := t.TempDir()
	html := "<!doctype html><html><body><h1>Cafe</h1></body></html>"
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InjectFeedbackWidget(dir, "p", "http://localhost:3010/feedback_requests", nil); err != nil {
		t.Fatal(err)
	}
	if !HasFeedbackWidget(dir) {
		t.Fatal("HasFeedbackWidget = false after inject")
	}

	n, err := RemoveFeedbackWidget(dir, nil)
	if err != nil || n != 1 {
		t.Fatalf("remove: n=%d err=%v, want 1/nil", n, err)
	}
	if got := readFile(t, path); got != html {
		t.Errorf("html after remove = %q, want the original %q", got, html)
	}
	if HasFeedbackWidget(dir) {
		t.Error("HasFeedbackWidget = true after remove")
	}
	if n, _ := RemoveFeedbackWidget(dir, nil); n != 0 {
		t.Errorf("second remove = %d, want 0", n)
	}
}

func TestRemoveFeedbackWidgetNextLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutPath := filepath.Join(dir, "app", "layout.tsx")
	layout := "export default function RootLayout({ children }) {\n" +
		"  return (\n    <html>\n      <body>\n        {children}\n      </body>\n    </html>\n  );\n}\n"
	if err := os.WriteFile(layoutPath, []byte(layout), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _ := InjectFeedbackWidget(dir, "p", "http://localhost:3010/feedback_requests", nil); n != 1 {
		t.Fatalf("inject = %d, want 1", n)
	}
	if !HasFeedbackWidget(dir) {
		t.Fatal("HasFeedbackWidget = false after inject")
	}

	n, err := RemoveFeedbackWidget(dir, nil)
	if err != nil || n != 1 {
		t.Fatalf("remove: n=%d err=%v, want 1/nil", n, err)
	}
	if got := readFile(t, layoutPath); got != layout {
		t.Errorf("layout after remove =\n%s\nwant the original\n%s", got, layout)
	}
	if _, err := os.Stat(filepath.Join(dir, "public", "fitt-feedback.js")); !os.IsNotExist(err) {
		t.Errorf("public/fitt-feedback.js still present (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app", "api")); !os.IsNotExist(err) {
		t.Errorf("app/api (the feedback route) still present (err=%v)", err)
	}
	if HasFeedbackWidget(dir) {
		t.Error("HasFeedbackWidget = true after remove")
	}
}

// The route keeps the app's own routes: removing the widget takes out only
// api/fitt-feedback.
func TestRemoveFeedbackWidgetKeepsTheAppsOwnRoutes(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "app", "api", "health", "route.ts")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("export function GET() { return Response.json({ ok: true }); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "layout.tsx"), []byte("<html><body>{children}</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _ := InjectFeedbackWidget(dir, "p", "http://localhost:3010/feedback_requests", nil); n != 1 {
		t.Fatalf("inject = %d, want 1", n)
	}
	if _, err := RemoveFeedbackWidget(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Errorf("the app's own route went with the widget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app", "api", "fitt-feedback")); !os.IsNotExist(err) {
		t.Errorf("api/fitt-feedback still present (err=%v)", err)
	}
}

// The widget posts to the app's own origin and the app's server sends the report
// on, so the receiver is the one FITT_FEEDBACK_URL names when the app runs, on
// every page, and the receiver needs no CORS. Runs the generated route in node.
// feedbackRouteFor injects the widget into a Next app whose root layout is
// app/<layout> and returns the route it writes. The build's URL is only the
// fallback; this one never answers.
func feedbackRouteFor(t *testing.T, layout string) string {
	t.Helper()
	if out, err := exec.Command("node", "-p", "process.features.typescript").Output(); err != nil || strings.TrimSpace(string(out)) == "false" {
		t.Skip("needs node with TypeScript type stripping")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", layout), []byte("<html><body>{children}</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _ := InjectFeedbackWidget(dir, "p", "http://127.0.0.1:9/feedback_requests", nil); n != 1 {
		t.Fatalf("inject = %d, want 1", n)
	}
	ext := ".ts"
	if filepath.Ext(layout) == ".jsx" {
		ext = ".js"
	}
	return filepath.Join(dir, "app", "api", "fitt-feedback", "route"+ext)
}

// postToFeedbackRoute calls the route's POST in node with FITT_FEEDBACK_URL=env
// and a body made by the JavaScript expression body, and returns "<status> <text>".
func postToFeedbackRoute(t *testing.T, route, env, body string) string {
	t.Helper()
	answer, _ := postToFeedbackRouteLogged(t, route, env, body)
	return answer
}

// postToFeedbackRouteLogged is postToFeedbackRoute that also returns the lines the
// route logged.
func postToFeedbackRouteLogged(t *testing.T, route, env, body string) (string, []string) {
	t.Helper()
	script := `const { POST } = await import(process.argv[1]);
const res = await POST(new Request("http://app.test/api/fitt-feedback", { method: "POST", headers: { "x-fitt-feedback": "1" }, body: ` + body + ` }));
console.log(res.status, await res.text());`
	cmd := exec.Command("node", "--input-type=module", "-e", script, route)
	cmd.Env = append(os.Environ(), "FITT_FEEDBACK_URL="+env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var answer, logged []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "[fitt-feedback] ") {
			logged = append(logged, line)
		} else {
			answer = append(answer, line)
		}
	}
	return strings.Join(answer, "\n"), logged
}

func TestFeedbackRouteForwardsToTheReceiverNamedAtRunTime(t *testing.T) {
	route := feedbackRouteFor(t, "layout.tsx")
	type received struct{ body, contentType string }
	got := make(chan received, 4)
	status := http.StatusCreated
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got <- received{string(raw), r.Header.Get("Content-Type")}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("receiver said so"))
	}))
	defer receiver.Close()
	report := `'{"note":"ปุ่มสีเขียว"}'`

	if out := postToFeedbackRoute(t, route, receiver.URL+"/api/ingest/feedback", report); out != "204" {
		t.Errorf("accepted report: route answered %q, want 204", out)
	}
	select {
	case r := <-got:
		if r.body != `{"note":"ปุ่มสีเขียว"}` || r.contentType != "application/json" {
			t.Errorf("receiver got %+v", r)
		}
	default:
		t.Fatal("the report never reached FITT_FEEDBACK_URL")
	}

	status = http.StatusRequestEntityTooLarge
	if out := postToFeedbackRoute(t, route, receiver.URL+"/api/ingest/feedback", report); out != "413 receiver said so" {
		t.Errorf("refused report: route answered %q, want the receiver's 413", out)
	}
	<-got

	if out := postToFeedbackRoute(t, route, "", report); !strings.HasPrefix(out, "502") {
		t.Errorf("unreachable receiver: route answered %q, want 502", out)
	}
}

// Anyone who can open the app can post to the route, so it must not hold an
// unbounded body in memory or pass one on.
func TestFeedbackRouteRefusesReportsOverTwentyFiveMB(t *testing.T) {
	for _, layout := range []string{"layout.tsx", "layout.jsx"} {
		t.Run(layout, func(t *testing.T) {
			route := feedbackRouteFor(t, layout)
			reached := make(chan int, 4)
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				reached <- len(raw)
				w.WriteHeader(http.StatusCreated)
			}))
			defer receiver.Close()

			out := postToFeedbackRoute(t, route, receiver.URL, `"x".repeat(25 * 1024 * 1024 + 1)`)
			if !strings.HasPrefix(out, "413") {
				t.Errorf("oversized report: route answered %q, want 413", out)
			}
			select {
			case n := <-reached:
				t.Errorf("an oversized report reached the receiver (%d bytes)", n)
			default:
			}

			if out := postToFeedbackRoute(t, route, receiver.URL, `"x".repeat(25 * 1024 * 1024)`); out != "204" {
				t.Errorf("report of exactly 25 MB: route answered %q, want 204", out)
			}
			if n := <-reached; n != 25*1024*1024 {
				t.Errorf("receiver got %d bytes, want %d", n, 25*1024*1024)
			}
		})
	}
}

// The company fittcore takes two reports at a time per app machine and turns the
// rest away with 503 and Retry-After (DV, 17 Nov). The route waits as asked and
// sends the report again, at most twice, before the user hears it was not sent.
func TestFeedbackRouteSendsAgainWhenTheSiteIsBusy(t *testing.T) {
	for _, layout := range []string{"layout.tsx", "layout.jsx"} {
		t.Run(layout, func(t *testing.T) {
			route := feedbackRouteFor(t, layout)
			var mu sync.Mutex
			var bodies []string
			var at []time.Time
			busy, wait := 0, ""
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				mu.Lock()
				defer mu.Unlock()
				bodies, at = append(bodies, string(raw)), append(at, time.Now())
				if busy > 0 {
					busy--
					if wait != "" {
						w.Header().Set("Retry-After", wait)
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":"busy"}`))
					return
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer receiver.Close()
			send := func(turnAway int, retryAfter string) (string, []string, []time.Time) {
				mu.Lock()
				busy, wait, bodies, at = turnAway, retryAfter, nil, nil
				mu.Unlock()
				out := postToFeedbackRoute(t, route, receiver.URL, `'{"note":"x"}'`)
				mu.Lock()
				defer mu.Unlock()
				return out, bodies, at
			}

			out, got, when := send(1, "1")
			if out != "204" || len(got) != 2 || got[0] != `{"note":"x"}` || got[1] != got[0] {
				t.Errorf("busy once: route answered %q after %d sends %q, want 204 after the same report twice", out, len(got), got)
			} else if gap := when[1].Sub(when[0]); gap < 950*time.Millisecond {
				t.Errorf("sent again after %v, before the Retry-After of 1 s", gap)
			}

			if out, got, _ := send(1, time.Now().Add(time.Second).UTC().Format(http.TimeFormat)); out != "204" || len(got) != 2 {
				t.Errorf("Retry-After as a date: route answered %q after %d sends, want 204 after 2", out, len(got))
			}

			if out, got, _ := send(1, ""); out != "204" || len(got) != 2 {
				t.Errorf("503 without Retry-After: route answered %q after %d sends, want 204 after 2", out, len(got))
			}

			if out, got, _ := send(3, "0"); out != `503 {"error":"busy"}` || len(got) != 3 {
				t.Errorf("busy throughout: route answered %q after %d sends, want the site's 503 after 3", out, len(got))
			}

			if out, got, _ := send(1, "120"); out != `503 {"error":"busy"}` || len(got) != 1 {
				t.Errorf("Retry-After of 2 minutes: route answered %q after %d sends, want the site's 503 at once", out, len(got))
			}
		})
	}
}

// A receiver that redirects must not steer reports to a host nobody let through
// the firewall.
func TestFeedbackRouteDoesNotFollowRedirects(t *testing.T) {
	route := feedbackRouteFor(t, "layout.tsx")
	elsewhere := make(chan struct{}, 4)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer other.Close()
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer receiver.Close()

	if out := postToFeedbackRoute(t, route, receiver.URL, `'{"note":"x"}'`); !strings.HasPrefix(out, "502") {
		t.Errorf("redirecting receiver: route answered %q, want 502", out)
	}
	select {
	case <-elsewhere:
		t.Error("the route followed the redirect")
	default:
	}
}

func TestRemoveFeedbackWidgetLeavesPlainAppsAlone(t *testing.T) {
	dir := t.TempDir()
	html := "<!doctype html><html><body><script>start()</script></body></html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	if HasFeedbackWidget(dir) {
		t.Error("HasFeedbackWidget = true for an app that never had it")
	}
	if n, err := RemoveFeedbackWidget(dir, nil); err != nil || n != 0 {
		t.Fatalf("remove: n=%d err=%v, want 0/nil", n, err)
	}
	if got := readFile(t, filepath.Join(dir, "index.html")); got != html {
		t.Errorf("plain app changed: %q", got)
	}
}

// Fittcore keeps the pictures customers pin and takes PNG, JPEG or WebP only. The
// widget snapshots a pin as SVG, paints it onto a canvas and sends PNG, or no
// picture when the browser will not paint it; the SVG itself never leaves the page.
// Painting is asynchronous, so sending waits for the pictures still being painted.
func TestFeedbackWidgetSendsPinPicturesAsPNG(t *testing.T) {
	if strings.Contains(feedbackWidgetJS, `return "data:image/svg+xml`) {
		t.Error("a capture still returns the SVG snapshot as the pin's picture")
	}
	if !strings.Contains(feedbackWidgetJS, `function svgToPNG(`) || !strings.Contains(feedbackWidgetJS, `resolve(c.toDataURL("image/png"))`) {
		t.Error("the snapshot is not painted to PNG")
	}
	if !strings.Contains(feedbackWidgetJS, `Promise.all(shots)`) {
		t.Error("sending does not wait for the pictures still being painted")
	}
}

// A pinned or cropped region shows as a 64px thumbnail in the panel. The operator asked
// (13 Nov) to see it whole before sending: a click on the thumbnail opens it full size,
// and a click anywhere or Esc closes it.
func TestFeedbackWidgetOpensAPinPictureFullSize(t *testing.T) {
	for _, want := range []string{
		`<div class='zoom' hidden`,
		`.pin img{width:64px;height:auto;max-height:48px;border-radius:4px;border:1px solid #eee;object-fit:cover;cursor:zoom-in}`,
		`function openZoom(src)`,
		`zoom.addEventListener("click", closeZoom)`,
		`if (e.key === "Escape" && !zoom.hidden) closeZoom();`,
		`if (shot) shot.addEventListener("click", function () { openZoom(p.region_shot); });`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
