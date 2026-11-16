package buildstep

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The widget's 📋 window (16 Nov) lets a customer see the app's tickets and send the
// ones they pick out of their company to DV, whose reviewers choose what goes to CRN.
// It talks to the customer company's own fittcore, never DV's (Jidapa, 16 Nov). The
// browser talks to the app's own route only; the route keeps no key of its own and
// passes on the token the customer's fittcore gave them at login. DV sends the spec
// of that API later: fakeFittcore answers the placeholder paths in FITTCORE.

const (
	tkFeedback = "7c1e0000-0000-4000-8000-000000000001"
	tkOther    = "7c1e0000-0000-4000-8000-000000000002"
	tkToken    = "pcp_widget_tok"
)

// withTicketWindow turns the window on for one test.
func withTicketWindow(t *testing.T) {
	t.Helper()
	ticketWindow = true
	t.Cleanup(func() { ticketWindow = false })
}

type fittcoreCall struct{ method, target, auth, body string }

type fakeFittcore struct {
	*httptest.Server
	mu    sync.Mutex
	calls []fittcoreCall
}

func (f *fakeFittcore) got() []fittcoreCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fittcoreCall(nil), f.calls...)
}

func newFakeFittcore(t *testing.T) *fakeFittcore {
	f := &fakeFittcore{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, fittcoreCall{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(raw)})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		answer := func(status int, body string) { w.WriteHeader(status); _, _ = w.Write([]byte(body)) }
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/widget-auth/challenges":
			answer(201, `{"id":"ch_1","token":"pcp_widget_auth_x","approvalUrl":"http://fittcore.test/widget-auth/ch_1","expiresAt":"2026-11-16T10:10:00Z","suggestedPollIntervalMs":1000}`)
		case r.Method == "GET" && r.URL.Path == "/api/widget-auth/challenges/ch_1":
			answer(200, `{"status":"approved","accessToken":"`+tkToken+`","expiresAt":"2026-11-16T18:00:00Z","user":{"name":"ลูกค้า UAT"}}`)
		case r.Header.Get("Authorization") != "Bearer "+tkToken:
			answer(401, `{"error":"Unauthorized"}`)
		case r.Method == "GET" && r.URL.Path == "/api/widget/tickets":
			answer(200, `[{"id":"`+tkFeedback+`","status":"new","category":"bug","priority":"high","text":"ปุ่มบันทึกไม่ทำงาน","pageUrl":"http://app.test/bsa",`+
				`"pins":[{"label":"ปุ่ม","note":"ตรงนี้","box":{"x":1,"y":2,"w":3,"h":4},"regionShot":"data:image/png;base64,iVBORw0KGgo="},{"label":"อีกจุด","note":"","box":null,"regionShot":null}],`+
				`"crnBuildNo":null,"createdAt":"2026-11-16T03:00:00.000Z"}]`)
		case r.Method == "POST" && r.URL.Path == "/api/widget/tickets/send":
			answer(200, `{"sent":2}`)
		default:
			answer(404, `{"error":"Not found"}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// callFeedbackRoute calls the route's GET or POST in node, as the browser would, with
// FITT_FEEDBACK_URL=env, and returns "<status> <body>".
func callFeedbackRoute(t *testing.T, route, env, method, target, auth, body string) string {
	t.Helper()
	init, _ := json.Marshal(map[string]any{"method": method, "target": target, "auth": auth, "body": body})
	script := `const mod = await import(process.argv[1]);
const r = JSON.parse(process.argv[2]);
const headers = r.auth ? { authorization: r.auth } : {};
const req = new Request("http://app.test" + r.target, { method: r.method, headers, body: r.method === "POST" ? r.body : undefined });
const res = await mod[r.method](req);
console.log(res.status, await res.text());`
	cmd := exec.Command("node", "--input-type=module", "-e", script, route, string(init))
	cmd.Env = append(os.Environ(), "FITT_FEEDBACK_URL="+env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func eachLayout(t *testing.T, run func(t *testing.T, route string)) {
	withTicketWindow(t)
	for _, layout := range []string{"layout.tsx", "layout.jsx"} {
		t.Run(layout, func(t *testing.T) { run(t, feedbackRouteFor(t, layout)) })
	}
}

// ticketRoute is the route of a Next app with the window on.
func ticketRoute(t *testing.T) string {
	withTicketWindow(t)
	return feedbackRouteFor(t, "layout.tsx")
}

func TestTicketWindowLogsInThroughFittcore(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		fc := newFakeFittcore(t)
		env := fc.URL + "/api/ingest/feedback"

		out := callFeedbackRoute(t, route, env, "POST", "/api/fitt-feedback?op=login", "", `{"project":"proj-xyz"}`)
		if !strings.HasPrefix(out, "201 ") || !strings.Contains(out, `"approvalUrl":"http://fittcore.test/widget-auth/ch_1"`) {
			t.Errorf("login answered %q", out)
		}
		out = callFeedbackRoute(t, route, env, "POST", "/api/fitt-feedback?op=poll", "", `{"id":"ch_1","token":"pcp_widget_auth_x"}`)
		if !strings.HasPrefix(out, "200 ") || !strings.Contains(out, `"accessToken":"`+tkToken+`"`) {
			t.Errorf("poll answered %q", out)
		}

		calls := fc.got()
		if len(calls) != 2 {
			t.Fatalf("fittcore got %d calls, want 2: %+v", len(calls), calls)
		}
		var login map[string]string
		if err := json.Unmarshal([]byte(calls[0].body), &login); err != nil || calls[0].method != "POST" || calls[0].target != "/api/widget-auth/challenges" || login["projectId"] != "proj-xyz" {
			t.Errorf("login reached fittcore as %+v", calls[0])
		}
		if calls[1].method != "GET" || calls[1].target != "/api/widget-auth/challenges/ch_1?token=pcp_widget_auth_x" {
			t.Errorf("poll reached fittcore as %+v", calls[1])
		}
	})
}

func TestTicketWindowListsTheJobsTicketsWithTheCustomersToken(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		fc := newFakeFittcore(t)
		out := callFeedbackRoute(t, route, fc.URL+"/api/ingest/feedback", "GET", "/api/fitt-feedback?op=list", "Bearer "+tkToken, "")
		status, body, _ := strings.Cut(out, " ")
		if status != "200" {
			t.Fatalf("list answered %q", out)
		}
		calls := fc.got()
		if len(calls) != 1 || calls[0].method != "GET" || calls[0].target != "/api/widget/tickets" || calls[0].auth != "Bearer "+tkToken {
			t.Fatalf("fittcore got %+v", calls)
		}
		// Pictures stay on the server: a ticket can carry 50 pins of up to 1 MB each.
		// (DV's list will leave them out anyway; the window fetches one ticket's alone.)
		if strings.Contains(body, "regionShot") || strings.Contains(body, "base64") {
			t.Errorf("pin pictures reached the browser: %s", body)
		}
		var items []struct {
			ID, Status, Text string
			Pins             []struct {
				Label   string
				HasShot bool
			}
		}
		if err := json.Unmarshal([]byte(body), &items); err != nil {
			t.Fatalf("list body is not the tickets: %v\n%s", err, body)
		}
		if len(items) != 1 || items[0].ID != tkFeedback || items[0].Status != "new" || items[0].Text != "ปุ่มบันทึกไม่ทำงาน" ||
			len(items[0].Pins) != 2 || items[0].Pins[0].Label != "ปุ่ม" || !items[0].Pins[0].HasShot || items[0].Pins[1].HasShot {
			t.Errorf("tickets = %+v", items)
		}
	})
}

// The customer's pick goes to their fittcore, which sends it out of the company to DV.
func TestTicketWindowSendsTheTicketsTheCustomerPicked(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		fc := newFakeFittcore(t)
		ids := `["` + tkFeedback + `","` + tkOther + `"]`
		if out := callFeedbackRoute(t, route, fc.URL+"/api/ingest/feedback", "POST", "/api/fitt-feedback?op=send", "Bearer "+tkToken, `{"feedbackIds":`+ids+`}`); out != `200 {"sent":2}` {
			t.Errorf("send answered %q", out)
		}
		want := fittcoreCall{"POST", "/api/widget/tickets/send", "Bearer " + tkToken, `{"feedbackIds":` + ids + `}`}
		if calls := fc.got(); len(calls) != 1 || calls[0] != want {
			t.Errorf("fittcore got %+v\nwant %+v", calls, want)
		}
	})
}

// Anyone who can open the demo can call the route: reading or sending tickets takes
// the token of a customer who logged in to fittcore.
func TestTicketWindowNeedsTheCustomersOwnLogin(t *testing.T) {
	route := ticketRoute(t)
	fc := newFakeFittcore(t)
	env := fc.URL + "/api/ingest/feedback"
	for _, c := range []struct{ method, target, body string }{
		{"GET", "/api/fitt-feedback?op=list", ""},
		{"POST", "/api/fitt-feedback?op=send", `{"feedbackIds":["` + tkFeedback + `"]}`},
	} {
		if out := callFeedbackRoute(t, route, env, c.method, c.target, "", c.body); !strings.HasPrefix(out, "401 ") {
			t.Errorf("%s %s without a token answered %q", c.method, c.target, out)
		}
	}
	if calls := fc.got(); len(calls) != 0 {
		t.Errorf("fittcore was called without a token: %+v", calls)
	}
}

// The route builds fittcore's paths and bodies from what the browser sends, so it
// takes ids and nothing else: no path of the browser's choosing reaches fittcore.
func TestTicketWindowTakesOnlyIDs(t *testing.T) {
	route := ticketRoute(t)
	fc := newFakeFittcore(t)
	env := fc.URL + "/api/ingest/feedback"
	for _, c := range []struct{ method, target, body string }{
		{"POST", "/api/fitt-feedback?op=send", `{"feedbackIds":["../x"]}`},
		{"POST", "/api/fitt-feedback?op=send", `{"feedbackIds":[]}`},
		{"POST", "/api/fitt-feedback?op=send", `{"feedbackIds":"` + tkFeedback + `"}`},
		{"POST", "/api/fitt-feedback?op=send", `not json`},
		{"POST", "/api/fitt-feedback?op=poll", `{"id":"../me","token":"t"}`},
		{"POST", "/api/fitt-feedback?op=poll", `{"id":"ch_1","token":"a b"}`},
		{"POST", "/api/fitt-feedback?op=login", `{"project":"a/b"}`},
	} {
		if out := callFeedbackRoute(t, route, env, c.method, c.target, "Bearer "+tkToken, c.body); !strings.HasPrefix(out, "400 ") {
			t.Errorf("%s %s %s answered %q, want 400", c.method, c.target, c.body, out)
		}
	}
	for _, c := range []struct{ method, target string }{{"GET", "/api/fitt-feedback?op=me"}, {"POST", "/api/fitt-feedback?op=preview"}, {"GET", "/api/fitt-feedback"}} {
		if out := callFeedbackRoute(t, route, env, c.method, c.target, "Bearer "+tkToken, "{}"); !strings.HasPrefix(out, "404 ") {
			t.Errorf("%s %s answered %q, want 404", c.method, c.target, out)
		}
	}
	if calls := fc.got(); len(calls) != 0 {
		t.Errorf("fittcore got requests the route should have refused: %+v", calls)
	}
}

// The token goes only to the fittcore that takes the app's reports, the customer's own:
// FITT_FEEDBACK_URL must name its /api/ingest/feedback. CRN's own receiver is not one.
func TestTicketWindowTalksOnlyToTheFittcoreThatTakesReports(t *testing.T) {
	route := ticketRoute(t)
	fc := newFakeFittcore(t)
	for _, env := range []string{"", fc.URL + "/feedback_requests", fc.URL + "/api/ingest/feedback?to=elsewhere"} {
		out := callFeedbackRoute(t, route, env, "GET", "/api/fitt-feedback?op=list", "Bearer "+tkToken, "")
		if !strings.HasPrefix(out, "503 ") {
			t.Errorf("FITT_FEEDBACK_URL=%q: list answered %q, want 503", env, out)
		}
	}
	if calls := fc.got(); len(calls) != 0 {
		t.Errorf("the token went somewhere FITT_FEEDBACK_URL does not name as fittcore: %+v", calls)
	}
}

func TestTicketWindowDoesNotFollowFittcoresRedirects(t *testing.T) {
	route := ticketRoute(t)
	elsewhere := make(chan string, 4)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere <- r.Header.Get("Authorization")
	}))
	defer other.Close()
	fittcore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer fittcore.Close()

	out := callFeedbackRoute(t, route, fittcore.URL+"/api/ingest/feedback", "GET", "/api/fitt-feedback?op=list", "Bearer "+tkToken, "")
	if !strings.HasPrefix(out, "502 ") {
		t.Errorf("redirecting fittcore: list answered %q, want 502", out)
	}
	select {
	case auth := <-elsewhere:
		t.Errorf("the route followed the redirect (Authorization %q)", auth)
	default:
	}
}

// A Next app gets the 📋 window, because its server can call fittcore for the browser.
// A static page posts straight to the receiver and has no server to do that.
func TestOnlyANextAppGetsTheTicketWindow(t *testing.T) {
	withTicketWindow(t)
	next := t.TempDir()
	if err := os.MkdirAll(filepath.Join(next, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(next, "app", "layout.tsx"), []byte("<html><body>{children}</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _ := InjectFeedbackWidget(next, "p", "http://fittcore.test/api/ingest/feedback", nil); n != 1 {
		t.Fatalf("inject = %d", n)
	}
	if layout := readFile(t, filepath.Join(next, "app", "layout.tsx")); !strings.Contains(layout, `data-tickets="/api/fitt-feedback"`) {
		t.Errorf("the Next layout does not turn on the ticket window:\n%s", layout)
	}

	static := t.TempDir()
	if err := os.WriteFile(filepath.Join(static, "index.html"), []byte("<html><body></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _ := InjectFeedbackWidget(static, "p", "http://fittcore.test/api/ingest/feedback", nil); n != 1 {
		t.Fatalf("inject = %d", n)
	}
	html := readFile(t, filepath.Join(static, "index.html"))
	tag := html[strings.Index(html, "<script data-fitt-feedback"):]
	if tag = tag[:strings.Index(tag, ">")+1]; strings.Contains(tag, "data-tickets") {
		t.Errorf("a static page turned on the ticket window it has no server for: %s", tag)
	}
}

// Until DV sends the spec of the customer fittcore's API, builds ship without the 📋
// window: no data-tickets in the layout and no ticket ops in the route, so a ?op= post
// is a report like any other.
func TestTheTicketWindowStaysOffUntilDVsSpec(t *testing.T) {
	route := feedbackRouteFor(t, "layout.tsx")
	src := readFile(t, route)
	for _, s := range []string{"ticketOp", "op=", "widget-auth", "Bearer"} {
		if strings.Contains(src, s) {
			t.Errorf("the route has %q with the window off", s)
		}
	}
	layout := readFile(t, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(route))), "layout.tsx"))
	if strings.Contains(layout, "data-tickets") {
		t.Errorf("the layout turns the window on:\n%s", layout)
	}
}

// The widget side of the window (checked in a browser against a fake customer fittcore
// on 16 Nov; these pin down what DV asked for).
func TestTheWidgetShowsTheTicketWindowOnlyWithDataTickets(t *testing.T) {
	for _, want := range []string{
		`var TICKETS = cfg.tickets || "";`,
		`<button class='tfab' hidden`,
		"if (TICKETS) {\n    tfab.hidden = false;",
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

// The customer's token is good for one app; it lives in this tab and goes to the app's
// route only.
func TestTheWidgetKeepsTheCustomersTokenInTheTab(t *testing.T) {
	if strings.Contains(feedbackWidgetJS, "localStorage") {
		t.Error("the widget keeps something in localStorage, which outlives the tab")
	}
	for _, want := range []string{
		`sessionStorage.setItem(TICKET_SESSION, JSON.stringify(s))`,
		`headers.Authorization = "Bearer " + tsession.token;`,
		`var url = TICKETS + (TICKETS.indexOf("?") >= 0 ? "&" : "?") + "op=" + op;`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

// DV worded what a customer sees of a sent ticket, and a "ไม่ดำเนินการ" reason shows
// only when DV's reviewer chooses to: the widget never shows one itself (Jidapa, 16 Nov).
func TestTheWidgetShowsDVsStatusesAndNoReasons(t *testing.T) {
	for _, want := range []string{"ได้รับแล้ว", "กำลังแก้", "build #{n} รอตรวจ", "แก้แล้ว build #{n}", "ไม่ดำเนินการ"} {
		if !strings.Contains(feedbackWidgetJS, `"`+want+`"`) {
			t.Errorf("the widget lacks the status %q", want)
		}
	}
	for _, never := range []string{"wontDoReason", "f.reason", "send-to-crn", "preview-edit"} {
		if strings.Contains(feedbackWidgetJS, never) {
			t.Errorf("the widget mentions %q", never)
		}
	}
}

// Until the customer's fittcore has the API, the window says so instead of failing.
func TestTheWidgetSaysTheWindowIsNotOpenYet(t *testing.T) {
	for _, want := range []string{
		`function notOpen(e) { return e && (e.status === 404 || e.status === 501 || e.status === 503); }`,
		`<b>ยังไม่เปิดใช้</b>`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}
