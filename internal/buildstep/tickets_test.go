package buildstep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The widget's 📋 window lets a signed-in app user see the app's tickets in their
// company's own fittcore and send the ones they pick out of the company to DV, whose
// reviewers choose what goes to CRN. DV's contract (CUSTOMER-FITTCORE-WIDGET-API part
// A rev1.2, 17 Nov 2026): the app's server asks the site's widget API for a token on
// behalf of the signed-in user, the same user approves it at their fittcore, and the
// token stays on the app's server, bound to that user's session. fakeSite answers as
// that contract says; no site has the API yet. Its ids look like the real ones, "sfb_"
// or "wch_" and 32 hex digits (DV, 17 Nov): the route keeps them as it gets them.

const (
	tkTicket = "sfb_3f2a9c0e1b7d4e8f9a6b5c4d3e2f1a0b"
	tkOther  = "sfb_0b1a2f3e4d5c6b7a8f9e4d7b1e0c9a2f"
	tkKey    = "press-0123456789abcdef"
)

// siteCall is one request the company fittcore got from the app's server.
type siteCall struct{ method, target, auth, user, origin, cookie, key, body string }

type fakeSite struct {
	*httptest.Server
	mu         sync.Mutex
	calls      []siteCall
	sessions   map[string]string // session id -> fittcore user id
	revoked    map[string]bool
	notEnabled bool
	// noWidgetAPI is a fittcore without /api/site/widget/v1 at all, as DV's own is: its
	// catch-all answers every path there 404 {"error":"not_found"}; htmlNotFound answers
	// it with a page instead.
	noWidgetAPI  bool
	htmlNotFound bool
	polled       map[string]int
	challenges   map[string]string // challenge id -> fittcore user id
	// emails is the email the fittcore gives for a user: uid@aec.test unless set here,
	// and none at all when set to "".
	emails map[string]string
}

// challengeID is the id fakeSite gives the challenge it starts for the user ref.
func challengeID(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return "wch_" + hex.EncodeToString(sum[:16])
}

func (f *fakeSite) got(prefix string) []siteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []siteCall
	for _, c := range f.calls {
		if strings.HasPrefix(c.target, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeSite) widgetCalls() []siteCall { return f.got("/api/site/widget/v1") }

func newFakeSite(t *testing.T) *fakeSite {
	f := &fakeSite{sessions: map[string]string{"s1": "u1", "s2": "u2", "s3": "u1"}, revoked: map[string]bool{}, polled: map[string]int{}, challenges: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, siteCall{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Widget-User"),
			r.Header.Get("Origin"), r.Header.Get("Cookie"), r.Header.Get("Idempotency-Key"), string(raw)})
		answer := func(status int, body string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
		sid := ""
		if c, err := r.Cookie("fc"); err == nil {
			sid = c.Value
		}
		switch r.URL.Path {
		case "/api/auth/get-session":
			if uid, ok := f.sessions[sid]; ok {
				email, set := f.emails[uid]
				if !set {
					email = uid + "@aec.test"
				}
				mail := ""
				if email != "" {
					mail = `,"email":"` + email + `"`
				}
				answer(200, `{"session":{"id":"`+sid+`","userId":"`+uid+`","expiresAt":"2099-01-01T00:00:00Z"},"user":{"id":"`+uid+`","name":"ผู้ใช้ `+uid+`"`+mail+`}}`)
			} else {
				answer(200, `null`)
			}
			return
		case "/api/site/feedback":
			answer(201, `{"id":"`+tkTicket+`"}`)
			return
		case "/test/logout":
			delete(f.sessions, r.URL.Query().Get("sid"))
			answer(200, `{}`)
			return
		case "/test/revoke":
			f.revoked[r.URL.Query().Get("user")] = true
			answer(200, `{}`)
			return
		}
		path, ok := strings.CutPrefix(r.URL.Path, "/api/site/widget/v1")
		if f.htmlNotFound && ok {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(404)
			_, _ = w.Write([]byte("<!DOCTYPE html><title>404</title>"))
			return
		}
		if !ok || f.noWidgetAPI {
			answer(404, `{"error":"not_found"}`)
			return
		}
		if f.notEnabled {
			answer(404, `{"error":"widget_not_enabled","message":"ยังไม่เปิดใช้"}`)
			return
		}
		switch {
		case r.Method == "POST" && path == "/auth/challenges":
			var b struct {
				AppUserRef string `json:"app_user_ref"`
			}
			_ = json.Unmarshal(raw, &b)
			f.challenges[challengeID(b.AppUserRef)] = b.AppUserRef
			answer(201, `{"challenge_id":"`+challengeID(b.AppUserRef)+`","poll_secret":"secret-`+b.AppUserRef+`","user_code":"K7QD-M2XP",`+
				`"approval_url":"http://site.test/widget-approve?code=K7QD-M2XP","expires_at":"2099-01-01T00:00:00Z","interval_seconds":3}`)
			return
		case r.Method == "POST" && strings.HasPrefix(path, "/auth/challenges/wch_") && strings.HasSuffix(path, "/token"):
			uid, known := f.challenges[strings.TrimSuffix(strings.TrimPrefix(path, "/auth/challenges/"), "/token")]
			if !known || !strings.Contains(string(raw), `"poll_secret":"secret-`+uid+`"`) {
				answer(410, `{"error":"expired_token"}`)
				return
			}
			f.polled[uid]++
			if uid == "slow" && f.polled[uid] == 1 {
				answer(428, `{"error":"authorization_pending"}`)
				return
			}
			answer(200, `{"access_token":"fsw_`+uid+`","token_type":"Bearer","expires_at":"2099-01-01T00:00:00Z","app":"fittbsa","approved_by":{"display_name":"สมชาย ใจดี"}}`)
			return
		}
		uid := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fsw_")
		switch {
		case r.Header.Get("Authorization") == "" || uid == r.Header.Get("Authorization") || f.revoked[uid]:
			answer(401, `{"error":"invalid_token","message":"สิทธิ์หมดอายุ"}`)
		case r.Header.Get("X-Widget-User") != uid:
			answer(401, `{"error":"token_user_mismatch"}`)
		case r.Method == "DELETE" && path == "/auth/token":
			f.revoked[uid] = true
			w.WriteHeader(204)
		case r.Method == "GET" && path == "/tickets":
			answer(200, `{"source":{"app":"fittbsa","forward_mode":"triage"},"tickets":[{"id":"`+tkTicket+`","text":"ปุ่มบันทึกกดแล้วไม่ไป","status":"pending_send","status_label":"รอผู้ดูแลตรวจก่อนส่ง","build_no":null,"customer_reason":null,"image_count":1}],"next_cursor":null}`)
		case r.Method == "POST" && path == "/tickets/submit":
			answer(200, `{"results":[{"id":"`+tkTicket+`","result":"queued","status":"sending","status_label":"กำลังส่ง"}]}`)
		case r.Method == "GET" && path == "/tickets/"+tkTicket+"/images":
			answer(200, `{"images":[{"image_id":"pin-1","kind":"region_shot","mime":"image/png","bytes":4}]}`)
		case r.Method == "GET" && path == "/tickets/"+tkTicket+"/images/pin-1":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG"))
		case r.Method == "GET" && path == "/tickets/"+tkTicket+"/images/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		default:
			answer(404, `{"error":"not_found"}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// step is one thing the browser (or the test) does to the route, in one server
// process, so what the route keeps between requests is kept.
type step struct {
	Method  string `json:"method,omitempty"`
	Target  string `json:"target,omitempty"`
	Cookie  string `json:"cookie,omitempty"`
	Body    string `json:"body,omitempty"`
	Advance int    `json:"advance,omitempty"` // ms the clock moves first
	Sweep   bool   `json:"sweep,omitempty"`   // run the route's periodic sweep
	Poke    string `json:"poke,omitempty"`    // POST this URL (the fake's controls)
	Pad     int    `json:"pad,omitempty"`     // each "@@PAD@@" in Body becomes this many "A"s, in node
}

type answer struct {
	Status  int    `json:"status"`
	Body    string `json:"body"`
	Type    string `json:"type"`
	Nosniff string `json:"nosniff"`
}

// runSteps runs the steps against an app that sends reports to feedbackURL, logs its
// users in through the same fittcore, as DV requires, and has the window turned on.
func runSteps(t *testing.T, route, feedbackURL string, steps ...step) []answer {
	t.Helper()
	platform := ""
	if u, err := url.Parse(feedbackURL); err == nil && u.Host != "" {
		platform = u.Scheme + "://" + u.Host
	}
	return runStepsEnv(t, route, map[string]string{"FITT_FEEDBACK_URL": feedbackURL, "PLATFORM_FITTCORE_ORIGIN": platform, "FITT_TICKET_WINDOW": "1"}, steps...)
}

func runStepsEnv(t *testing.T, route string, env map[string]string, steps ...step) []answer {
	t.Helper()
	raw, _ := json.Marshal(steps)
	script := `const real = Date.now; let offset = 0; Date.now = () => real() + offset;
let tick = null; globalThis.setInterval = (fn) => { tick = fn; return { unref() {} }; };
const mod = await import(process.argv[1]);
const out = [];
for (const s of JSON.parse(process.argv[2])) {
  offset += s.advance || 0;
  if (s.sweep) { if (tick) await tick(); out.push({ status: 0, body: "", type: "", nosniff: "" }); continue; }
  if (s.poke) { await fetch(s.poke, { method: "POST" }); out.push({ status: 0, body: "", type: "", nosniff: "" }); continue; }
  const headers = s.cookie ? { cookie: s.cookie } : {};
  const sent = s.pad ? (s.body || "").split("@@PAD@@").join("A".repeat(s.pad)) : (s.body || "");
  const req = new Request("http://app.test" + s.target, { method: s.method, headers, body: s.method === "POST" ? sent : undefined });
  const res = await mod[s.method](req);
  const body = Buffer.from(await res.arrayBuffer()).toString("utf8");
  out.push({ status: res.status, body, type: res.headers.get("content-type") || "", nosniff: res.headers.get("x-content-type-options") || "" });
}
console.log(JSON.stringify(out));`
	cmd := exec.Command("node", "--input-type=module", "-e", script, route, string(raw))
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	// The answers are the last line: the route logs a line of its own for each report.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var answers []answer
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &answers); err != nil {
		t.Fatalf("node said %s", out)
	}
	return answers
}

// withTicketWindow turns the window on for one test.
func withTicketWindow(t *testing.T) {
	t.Helper()
	was := ticketWindow
	ticketWindow = true
	t.Cleanup(func() { ticketWindow = was })
}

func ticketRoute(t *testing.T) string {
	withTicketWindow(t)
	return feedbackRouteFor(t, "layout.tsx")
}

func eachLayout(t *testing.T, run func(t *testing.T, route string)) {
	withTicketWindow(t)
	for _, layout := range []string{"layout.tsx", "layout.jsx"} {
		t.Run(layout, func(t *testing.T) { run(t, feedbackRouteFor(t, layout)) })
	}
}

func siteEnv(f *fakeSite) string { return f.URL + "/api/site/feedback" }

func stateAs(cookie string) step {
	return step{Method: "GET", Target: "/api/fitt-feedback?op=state", Cookie: cookie}
}

func loginAs(cookie string) step {
	return step{Method: "POST", Target: "/api/fitt-feedback?op=login", Cookie: cookie, Body: `{"project":"proj-xyz"}`}
}

// openAt is what the widget asks when the page loads: whether to show 📋 at all.
func openAt() step {
	return step{Method: "GET", Target: "/api/fitt-feedback?op=open"}
}

func pollAs(cookie string) step {
	return step{Method: "POST", Target: "/api/fitt-feedback?op=poll", Cookie: cookie}
}

func listAs(cookie string) step {
	return step{Method: "GET", Target: "/api/fitt-feedback?op=list", Cookie: cookie}
}

func TestTicketWindowGivesTheTokenToTheServerNotTheBrowser(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		site := newFakeSite(t)
		a := runSteps(t, route, siteEnv(site), stateAs("fc=s1"), loginAs("fc=s1"), pollAs("fc=s1"), listAs("fc=s1"))
		if a[0].Status != 200 || a[0].Body != `{"ready":false}` {
			t.Errorf("state before login = %+v", a[0])
		}
		if a[1].Status != 200 || !strings.Contains(a[1].Body, `"code":"K7QD-M2XP"`) || !strings.Contains(a[1].Body, `"url":"http://site.test/widget-approve?code=K7QD-M2XP"`) {
			t.Errorf("login = %+v", a[1])
		}
		if a[2].Status != 200 || !strings.Contains(a[2].Body, `"ready":true`) || !strings.Contains(a[2].Body, "สมชาย ใจดี") {
			t.Errorf("poll = %+v", a[2])
		}
		if a[3].Status != 200 || !strings.Contains(a[3].Body, `"status_label":"รอผู้ดูแลตรวจก่อนส่ง"`) {
			t.Errorf("list = %+v", a[3])
		}
		for i, x := range a {
			for _, secret := range []string{"fsw_", "secret-", "wch_"} {
				if strings.Contains(x.Body, secret) {
					t.Errorf("answer %d gave the browser %q: %s", i, secret, x.Body)
				}
			}
		}

		calls := site.widgetCalls()
		if len(calls) != 4 {
			t.Fatalf("the site got %d widget calls, want probe, challenge, poll, list: %+v", len(calls), calls)
		}
		var challenge map[string]string
		_ = json.Unmarshal([]byte(calls[1].body), &challenge)
		if calls[1].target != "/api/site/widget/v1/auth/challenges" || challenge["project_id"] != "proj-xyz" || challenge["app_user_ref"] != "u1" || challenge["client_name"] == "" {
			t.Errorf("challenge = %+v", calls[1])
		}
		if calls[2].target != "/api/site/widget/v1/auth/challenges/"+challengeID("u1")+"/token" || calls[2].body != `{"poll_secret":"secret-u1"}` {
			t.Errorf("poll = %+v", calls[2])
		}
		if calls[3].target != "/api/site/widget/v1/tickets?status=all&limit=50" || calls[3].auth != "Bearer fsw_u1" || calls[3].user != "u1" {
			t.Errorf("list = %+v", calls[3])
		}
		for _, c := range calls {
			if c.origin != "" || c.cookie != "" {
				t.Errorf("a widget call carried Origin %q or a cookie %q: %+v", c.origin, c.cookie, c)
			}
		}
		for _, c := range site.got("/api/auth/get-session") {
			if c.cookie != "fc=s1" {
				t.Errorf("the user was looked up with cookie %q", c.cookie)
			}
		}
	})
}

// A token is the approving user's, for that app session: another user, or the same
// user in another session, starts over.
func TestTicketWindowKeepsEachUsersTokenToTheirSession(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	a := runSteps(t, route, siteEnv(site), loginAs("fc=s1"), pollAs("fc=s1"),
		stateAs("fc=s2"), listAs("fc=s2"), stateAs("fc=s3"), listAs("fc=s3"), listAs("fc=s1"))
	for _, i := range []int{2, 4} {
		if a[i].Body != `{"ready":false}` {
			t.Errorf("state %d for another session = %+v, want not ready", i, a[i])
		}
	}
	for _, i := range []int{3, 5} {
		if a[i].Status != 401 {
			t.Errorf("list %d for another session = %+v, want 401", i, a[i])
		}
	}
	if a[6].Status != 200 {
		t.Errorf("the approving session's own list = %+v", a[6])
	}
	for _, c := range site.widgetCalls() {
		if c.auth != "" && (c.auth != "Bearer fsw_u1" || c.user != "u1") {
			t.Errorf("a token went out for someone else: %+v", c)
		}
	}
	if n := len(site.got("/api/site/widget/v1/tickets?status=all")); n != 1 {
		t.Errorf("the site listed tickets %d times, want once (the approving session's)", n)
	}
}

func TestTicketWindowNeedsTheAppLogin(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	a := runSteps(t, route, siteEnv(site), stateAs(""), stateAs("fc=nobody"), loginAs("fc=nobody"), listAs(""))
	for i, x := range a {
		if x.Status != 401 || x.Body != `{"error":"app_login"}` {
			t.Errorf("answer %d without an app session = %+v", i, x)
		}
	}
	if calls := site.widgetCalls(); len(calls) != 0 {
		t.Errorf("the widget API was called without an app session: %+v", calls)
	}
}

// DV, 17 Nov: the fittcore the app logs its users in through (PLATFORM_FITTCORE_ORIGIN)
// must be the one FITT_FEEDBACK_URL names; otherwise the window is not open and no
// challenge starts.
func TestTicketWindowIsNotOpenWhenTheAppLogsInElsewhere(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	for _, platform := range []string{"", "http://other-fittcore.test:3100", site.URL + "x"} {
		a := runStepsEnv(t, route, map[string]string{"FITT_FEEDBACK_URL": siteEnv(site), "PLATFORM_FITTCORE_ORIGIN": platform, "FITT_TICKET_WINDOW": "1"},
			stateAs("fc=s1"), loginAs("fc=s1"), openAt())
		for i, x := range a {
			if x.Status != 503 || x.Body != `{"error":"not_open"}` {
				t.Errorf("PLATFORM_FITTCORE_ORIGIN=%q: answer %d = %+v, want not_open", platform, i, x)
			}
		}
	}
	if site.got("/"); len(site.got("/")) != 0 {
		t.Errorf("the site was asked anything at all: %+v", site.got("/"))
	}
}

// The route keeps to fittcore's pace: one poll per interval, whatever the browser does.
func TestTicketWindowPollsAtFittcoresPace(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	site.sessions["slow"] = "slow"
	a := runSteps(t, route, siteEnv(site), loginAs("fc=slow"), pollAs("fc=slow"), pollAs("fc=slow"),
		step{Method: "POST", Target: "/api/fitt-feedback?op=poll", Cookie: "fc=slow", Advance: 3100})
	if !strings.Contains(a[1].Body, `"pending"`) || !strings.Contains(a[2].Body, `"pending"`) || !strings.Contains(a[3].Body, `"ready":true`) {
		t.Errorf("polls = %+v", a[1:])
	}
	if n := len(site.got("/api/site/widget/v1/auth/challenges/" + challengeID("slow") + "/token")); n != 2 {
		t.Errorf("the site was polled %d times, want 2", n)
	}
}

func TestTicketWindowSendsOnePressWithItsKey(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	submit := func(body string) step {
		return step{Method: "POST", Target: "/api/fitt-feedback?op=submit", Cookie: "fc=s1", Body: body}
	}
	a := runSteps(t, route, siteEnv(site), loginAs("fc=s1"), pollAs("fc=s1"),
		submit(`{"ticket_ids":["`+tkTicket+`","`+tkOther+`"],"key":"`+tkKey+`"}`),
		submit(`{"ticket_ids":["`+tkTicket+`","`+tkTicket+`"],"key":"`+tkKey+`"}`),
		submit(`{"ticket_ids":[],"key":"`+tkKey+`"}`),
		submit(`{"ticket_ids":["../me"],"key":"`+tkKey+`"}`),
		submit(`{"ticket_ids":["`+tkTicket+`"],"key":"short"}`),
		submit(`{"ticket_ids":["`+tkTicket+`"]}`),
		submit(`not json`))
	if a[2].Status != 200 || !strings.Contains(a[2].Body, `"result":"queued"`) {
		t.Errorf("submit = %+v", a[2])
	}
	for i := 3; i < len(a); i++ {
		if a[i].Status != 400 {
			t.Errorf("submit %d = %+v, want 400", i, a[i])
		}
	}
	many := make([]string, 51)
	for i := range many {
		many[i] = `"t` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"`
	}
	if b := runSteps(t, route, siteEnv(site), loginAs("fc=s1"), pollAs("fc=s1"),
		submit(`{"ticket_ids":[`+strings.Join(many, ",")+`],"key":"`+tkKey+`"}`)); b[2].Status != 400 {
		t.Errorf("51 tickets = %+v, want 400", b[2])
	}
	calls := site.got("/api/site/widget/v1/tickets/submit")
	want := siteCall{"POST", "/api/site/widget/v1/tickets/submit", "Bearer fsw_u1", "u1", "", "", tkKey, `{"ticket_ids":["` + tkTicket + `","` + tkOther + `"]}`}
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("the site got %+v\nwant %+v", calls, want)
	}
}

func TestTicketWindowShowsATicketsPictures(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	img := func(q string) step { return step{Method: "GET", Target: "/api/fitt-feedback?op=" + q, Cookie: "fc=s1"} }
	a := runSteps(t, route, siteEnv(site), loginAs("fc=s1"), pollAs("fc=s1"),
		img("images&id="+tkTicket), img("image&id="+tkTicket+"&image=pin-1"), img("image&id="+tkTicket+"&image=page"),
		img("image&id=../x&image=pin-1"), img("image&id="+tkTicket))
	if a[2].Status != 200 || !strings.Contains(a[2].Body, `"image_id":"pin-1"`) {
		t.Errorf("images = %+v", a[2])
	}
	if a[3].Status != 200 || a[3].Type != "image/png" || a[3].Nosniff != "nosniff" || !strings.HasSuffix(a[3].Body, "PNG") {
		t.Errorf("image = %+v", a[3])
	}
	if a[4].Status != 502 || strings.Contains(a[4].Body, "<script>") {
		t.Errorf("a picture that is not an image reached the browser: %+v", a[4])
	}
	if a[5].Status != 400 || a[6].Status != 400 {
		t.Errorf("bad ids answered %+v / %+v, want 400", a[5], a[6])
	}
}

// A site without the widget API answers 404 widget_not_enabled; the window says it is
// not open yet and asks again at most once per 5 minutes.
func TestTicketWindowIsNotOpenWhereTheSiteLacksIt(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	site.notEnabled = true
	a := runSteps(t, route, siteEnv(site), stateAs("fc=s1"), stateAs("fc=s1"), loginAs("fc=s1"),
		step{Method: "GET", Target: "/api/fitt-feedback?op=state", Cookie: "fc=s1", Advance: 5*60*1000 + 1000})
	for i, x := range a {
		if x.Status != 503 || x.Body != `{"error":"not_open"}` {
			t.Errorf("answer %d = %+v, want not_open", i, x)
		}
	}
	if n := len(site.widgetCalls()); n != 2 {
		t.Errorf("the site was asked %d times, want once, then once more after 5 minutes", n)
	}

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	for _, env := range []string{"", down.URL + "/api/site/feedback"} {
		if a := runSteps(t, route, env, stateAs("fc=s1")); a[0].Status != 503 {
			t.Errorf("FITT_FEEDBACK_URL=%q: state = %+v, want not_open", env, a[0])
		}
	}
}

// DV, 23 Nov: the window opens only where the app's server turns it on, with
// FITT_TICKET_WINDOW=1. Without it, the same fittcore for reports and logins is not
// enough: 📋 is not shown and the site is asked nothing. OKR, DV's own app, leaves it off.
func TestTicketWindowIsOffUnlessTheAppTurnsItOn(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	for _, on := range []string{"", "0", "true", "yes", " 1"} {
		a := runStepsEnv(t, route, map[string]string{"FITT_FEEDBACK_URL": siteEnv(site), "PLATFORM_FITTCORE_ORIGIN": site.URL, "FITT_TICKET_WINDOW": on},
			openAt(), stateAs("fc=s1"), loginAs("fc=s1"))
		for i, x := range a {
			if x.Status != 503 || x.Body != `{"error":"not_open"}` {
				t.Errorf("FITT_TICKET_WINDOW=%q: answer %d = %+v, want not_open", on, i, x)
			}
		}
	}
	if len(site.got("/")) != 0 {
		t.Errorf("the site was asked anything at all: %+v", site.got("/"))
	}
}

// The widget asks whether to show 📋 when a page loads. The route asks the site once,
// without anyone's token or cookie, and keeps the answer for 5 minutes either way.
func TestTicketWindowSaysWhetherToShowTheButton(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	a := runSteps(t, route, siteEnv(site), openAt(), openAt())
	for i, x := range a {
		if x.Status != 200 || x.Body != `{"open":true}` {
			t.Errorf("answer %d = %+v, want open", i, x)
		}
	}
	calls := site.widgetCalls()
	if len(calls) != 1 {
		t.Fatalf("the site was asked %d times, want once: %+v", len(calls), calls)
	}
	if c := calls[0]; c.auth != "" || c.cookie != "" || c.user != "" || c.origin != "" {
		t.Errorf("the question carried someone's credentials: %+v", c)
	}
	if a := runSteps(t, route, siteEnv(site), step{Method: "GET", Target: "/api/fitt-feedback?op=open", Advance: 5*60*1000 + 1000}); a[0].Body != `{"open":true}` {
		t.Errorf("after 5 minutes: %+v", a[0])
	}
}

// DV, 23 Nov: OKR on UAT showed "ขอสิทธิ์จาก fittcore ก่อน" against DV's own fittcore,
// which has no /api/site/widget/v1: its catch-all answered 404 {"error":"not_found"},
// the route took that for the widget's own answer, and "ขอรหัสอนุมัติ" failed with 404.
// Only widget_not_enabled and the not_found of a ticket's pictures are the widget's;
// any other 404 means the site does not have it.
func TestTicketWindowIsNotOpenWhereFittcoreHasNoWidgetAPI(t *testing.T) {
	for name, set := range map[string]func(*fakeSite){
		"json not_found": func(f *fakeSite) { f.noWidgetAPI = true },
		"html page":      func(f *fakeSite) { f.htmlNotFound = true },
	} {
		t.Run(name, func(t *testing.T) {
			route := ticketRoute(t)
			site := newFakeSite(t)
			set(site)
			a := runSteps(t, route, siteEnv(site), openAt(), stateAs("fc=s1"), loginAs("fc=s1"))
			for i, x := range a {
				if x.Status != 503 || x.Body != `{"error":"not_open"}` {
					t.Errorf("answer %d = %+v, want not_open", i, x)
				}
			}
			if n := len(site.widgetCalls()); n != 1 {
				t.Errorf("the site was asked %d times, want once in 5 minutes", n)
			}
		})
	}
}

// A ticket or picture that is not there is the widget's own 404, and says so.
func TestTicketWindowSaysAPictureIsNotThere(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	a := runSteps(t, route, siteEnv(site), loginAs("fc=s1"), pollAs("fc=s1"),
		step{Method: "GET", Target: "/api/fitt-feedback?op=images&id=" + tkOther, Cookie: "fc=s1"},
		step{Method: "GET", Target: "/api/fitt-feedback?op=list", Cookie: "fc=s1"})
	if a[2].Status != 404 || !strings.Contains(a[2].Body, "not_found") {
		t.Errorf("pictures of a ticket that is not there: %+v, want 404 not_found", a[2])
	}
	if a[3].Status != 200 {
		t.Errorf("after it, the list: %+v, want the window still open", a[3])
	}
}

func TestTicketWindowWithdrawsTheTokenWhenTheSessionEnds(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	a := runSteps(t, route, siteEnv(site), loginAs("fc=s1"), pollAs("fc=s1"),
		step{Sweep: true},
		step{Poke: site.URL + "/test/logout?sid=s1"},
		step{Sweep: true})
	if !strings.Contains(a[1].Body, `"ready":true`) {
		t.Fatalf("poll = %+v", a[1])
	}
	deletes := site.got("/api/site/widget/v1/auth/token")
	if len(deletes) != 1 || deletes[0].method != "DELETE" || deletes[0].auth != "Bearer fsw_u1" || deletes[0].user != "u1" {
		t.Errorf("after logout the site got %+v, want one DELETE /auth/token with u1's token", deletes)
	}

	site2 := newFakeSite(t)
	runSteps(t, route, siteEnv(site2), loginAs("fc=s1"), pollAs("fc=s1"),
		step{Method: "POST", Target: "/api/fitt-feedback?op=logout", Cookie: "fc=s1"}, listAs("fc=s1"))
	if d := site2.got("/api/site/widget/v1/auth/token"); len(d) != 1 || d[0].method != "DELETE" {
		t.Errorf("the window's own logout sent %+v", d)
	}
}

func TestTicketWindowForgetsATokenFittcoreRefuses(t *testing.T) {
	route := ticketRoute(t)
	site := newFakeSite(t)
	a := runSteps(t, route, siteEnv(site), loginAs("fc=s1"), pollAs("fc=s1"),
		step{Poke: site.URL + "/test/revoke?user=u1"}, listAs("fc=s1"), stateAs("fc=s1"))
	if a[3].Status != 401 || !strings.Contains(a[3].Body, "invalid_token") {
		t.Errorf("list with a refused token = %+v", a[3])
	}
	if a[4].Body != `{"ready":false}` {
		t.Errorf("state after the refusal = %+v, want a new approval", a[4])
	}
}

func TestTicketWindowDoesNotFollowRedirects(t *testing.T) {
	route := ticketRoute(t)
	elsewhere := make(chan string, 4)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere <- r.URL.Path }))
	defer other.Close()
	site := newFakeSite(t)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/get-session" {
			site.Config.Handler.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	a := runSteps(t, route, redirecting.URL+"/api/site/feedback", loginAs("fc=s1"))
	if a[0].Status != 502 {
		t.Errorf("login against a redirecting site = %+v, want 502", a[0])
	}
	select {
	case p := <-elsewhere:
		t.Errorf("the route followed a redirect to %s", p)
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
	if n, _ := InjectFeedbackWidget(next, "p", "http://fittcore.test/api/site/feedback", nil); n != 1 {
		t.Fatalf("inject = %d", n)
	}
	if layout := readFile(t, filepath.Join(next, "app", "layout.tsx")); !strings.Contains(layout, `data-tickets="/api/fitt-feedback"`) {
		t.Errorf("the Next layout does not turn on the ticket window:\n%s", layout)
	}

	static := t.TempDir()
	if err := os.WriteFile(filepath.Join(static, "index.html"), []byte("<html><body></body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _ := InjectFeedbackWidget(static, "p", "http://fittcore.test/api/site/feedback", nil); n != 1 {
		t.Fatalf("inject = %d", n)
	}
	html := readFile(t, filepath.Join(static, "index.html"))
	tag := html[strings.Index(html, "<script data-fitt-feedback"):]
	if tag = tag[:strings.Index(tag, ">")+1]; strings.Contains(tag, "data-tickets") {
		t.Errorf("a static page turned on the ticket window it has no server for: %s", tag)
	}
}

// Every build ships the 📋 window (since 20 Nov 2026: every site gets it); a site without
// DV's widget API shows "ยังไม่เปิดใช้".
func TestEveryBuildShipsTheTicketWindow(t *testing.T) {
	route := feedbackRouteFor(t, "layout.tsx")
	src := readFile(t, route)
	for _, s := range []string{"ticketOp", "op=", "api/site/widget", "x-widget-user"} {
		if !strings.Contains(src, s) {
			t.Errorf("the route lacks %q", s)
		}
	}
	layout := readFile(t, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(route))), "layout.tsx"))
	if !strings.Contains(layout, `data-tickets="/api/fitt-feedback"`) {
		t.Errorf("the layout does not turn the window on:\n%s", layout)
	}
}

// With the switch off, a build ships without the window: no data-tickets in the layout
// and no ticket ops in the route, so a ?op= post is a report like any other.
func TestTheTicketWindowCanStillBeTurnedOff(t *testing.T) {
	was := ticketWindow
	ticketWindow = false
	t.Cleanup(func() { ticketWindow = was })
	route := feedbackRouteFor(t, "layout.tsx")
	src := readFile(t, route)
	for _, s := range []string{"ticketOp", "op=", "api/site/widget", "x-widget-user"} {
		if strings.Contains(src, s) {
			t.Errorf("the route has %q with the window off", s)
		}
	}
	layout := readFile(t, filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(route))), "layout.tsx"))
	if strings.Contains(layout, "data-tickets") {
		t.Errorf("the layout turns the window on:\n%s", layout)
	}
}

// The widget side (checked in a browser against a fake site on 17 Nov; these pin down
// what DV's contract asks of it).
func TestTheWidgetShowsTheTicketWindowOnlyWithDataTickets(t *testing.T) {
	for _, want := range []string{
		`var TICKETS = cfg.tickets || "";`,
		`<button class='tfab' hidden`,
		`ticketCall("GET", "open").then(function (r) {`,
		`if (r.status === 200 && r.data.open === true) showTicketButton(true);`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
	if strings.Contains(feedbackWidgetJS, "if (TICKETS) {\n    tfab.hidden = false;") {
		t.Error("the widget shows 📋 before the route says the window is open")
	}
}

// DV, 23 Nov: once the site says the window is not open, 📋 goes away when the window
// closes, so nobody is asked to press for a code that cannot come.
func TestTheWidgetTakesTheTicketButtonAwayWhenTheWindowIsNotOpen(t *testing.T) {
	want := `if (tk.view === "closed") { showTicketButton(false); fab.focus(); return; }`
	if !strings.Contains(feedbackWidgetJS, want) {
		t.Errorf("the widget lacks %s", want)
	}
}

// The token and the poll secret live on the app's server; the page never holds them.
func TestTheWidgetNeverHoldsTheToken(t *testing.T) {
	for _, never := range []string{"access_token", "poll_secret", "Bearer", "sessionStorage", "localStorage", "Authorization"} {
		if strings.Contains(feedbackWidgetJS, never) {
			t.Errorf("the widget mentions %q", never)
		}
	}
}

// Status words and reasons are fittcore's: status_label as it comes, customer_reason
// only when DV wrote one. The widget words none of its own.
func TestTheWidgetShowsFittcoresOwnWords(t *testing.T) {
	for _, want := range []string{
		`esc(f.status_label)`,
		`f.status === "closed" && f.customer_reason ? "<p class='treply'><b>จาก DV</b> " + esc(f.customer_reason)`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
	for _, never := range []string{"ได้รับแล้ว", "กำลังแก้", "รอตรวจ", "แก้แล้ว build", "ไม่ดำเนินการ", "ขึ้น UAT", "wontDoReason"} {
		if strings.Contains(feedbackWidgetJS, never) {
			t.Errorf("the widget words a status itself: %q", never)
		}
	}
}

// One press of "ยืนยันส่ง" is one Idempotency-Key; a retry after a failure resends it.
// An app that forwards every ticket by itself has no send button.
func TestTheWidgetSendsOnePressWithOneKey(t *testing.T) {
	for _, want := range []string{
		`tk.confirm = { ids: picked(), key: newKey(), error: "" };`,
		`{ ticket_ids: c.ids, key: c.key }`,
		`var canSend = mode() === "triage";`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

func TestTheWidgetSaysTheWindowIsNotOpenYet(t *testing.T) {
	for _, want := range []string{
		`if (r.status === 503 && r.data.error === "not_open") { show("closed"); return true; }`,
		`<b>ยังไม่เปิดใช้</b>`,
	} {
		if !strings.Contains(feedbackWidgetJS, want) {
			t.Errorf("the widget lacks %s", want)
		}
	}
}

// reportAs is a 💬 report the widget posts, as the user with cookie.
func reportAs(cookie, body string) step {
	return step{Method: "POST", Target: "/api/fitt-feedback", Cookie: cookie, Body: body}
}

// The company fittcore shows each app user the tickets that are theirs by app_user_ref,
// the fittcore id of who sent the report (DV, 25 Nov 2026: AEC build 11 sent none, so a
// user's 📋 showed nothing of theirs). Where the window is on, the route asks the
// company fittcore who the user is, as 📋 does, and puts their id in the report, top
// level and in payload.app_user.ref when the app sends one; never their name. A
// report from nobody signed in goes on as it came, and where the window is off the
// route asks no one and changes nothing.
func TestAReportSaysWhoSentItWhereTheWindowIsOn(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		site := newFakeSite(t)
		plain := `{"note":"ปุ่มบันทึกกดไม่ได้","page_url":"/","category":"bug","priority":"med","reporter":"","payload":{"pins":[]}}`
		withApp := `{"note":"แก้สี","app_user_ref":"someone-else","payload":{"pins":[],"app_user":{"ref":"someone-else"}}}`
		a := runSteps(t, route, siteEnv(site), reportAs("fc=s1", plain), reportAs("fc=s2", withApp), reportAs("", plain), reportAs("fc=gone", plain),
			reportAs("", withApp), reportAs("fc=gone", withApp), reportAs("fc=s1", `["not","a","report"]`), reportAs("fc=s1", `{"note":`))
		for i, x := range a[:6] {
			if x.Status != 204 {
				t.Fatalf("report %d answered %+v", i, x)
			}
		}
		for i, x := range a[6:] {
			if x.Status != 400 {
				t.Errorf("a report that is not a JSON object (%d) answered %+v", i, x)
			}
		}
		sent := site.got("/api/site/feedback")
		if len(sent) != 6 {
			t.Fatalf("the site got %d reports", len(sent))
		}
		// Who sent a report is only ever what the company fittcore says: one the browser
		// names itself is taken out when nobody signed in can be found.
		for i := 4; i < 6; i++ {
			if strings.Contains(sent[i].body, "someone-else") || strings.Contains(sent[i].body, "app_user_ref") {
				t.Errorf("report %d went with the sender the browser named: %s", i, sent[i].body)
			}
		}
		var first, second map[string]any
		_ = json.Unmarshal([]byte(sent[0].body), &first)
		_ = json.Unmarshal([]byte(sent[1].body), &second)
		if first["app_user_ref"] != "u1" || first["note"] != "ปุ่มบันทึกกดไม่ได้" {
			t.Errorf("a signed-in user's report went as %s", sent[0].body)
		}
		if _, has := first["app_user_name"]; has || strings.Contains(sent[0].body, "ผู้ใช้ u1") {
			t.Errorf("the report carries the user's name: %s", sent[0].body)
		}
		if p, _ := second["payload"].(map[string]any); second["app_user_ref"] != "u2" || p["app_user"].(map[string]any)["ref"] != "u2" {
			t.Errorf("the app's own sender was not made the signed-in one: %s", sent[1].body)
		}
		for i := 2; i < 4; i++ {
			if sent[i].body != plain {
				t.Errorf("report %d from nobody signed in went as %s", i, sent[i].body)
			}
			if sent[i].cookie != "" {
				t.Errorf("the report carried a cookie to the site: %q", sent[i].cookie)
			}
		}
		if n := len(site.got("/api/auth/get-session")); n != 4 {
			t.Errorf("get-session was asked %d times, want 4 (not for a report without a cookie or one that is not a report)", n)
		}
	})
	t.Run("window off", func(t *testing.T) {
		site := newFakeSite(t)
		plain := `{"note":"ปุ่มบันทึกกดไม่ได้","reporter":"boss@aec.test","payload":{"pins":[]}}`
		a := runStepsEnv(t, ticketRoute(t), map[string]string{"FITT_FEEDBACK_URL": siteEnv(site), "PLATFORM_FITTCORE_ORIGIN": site.URL}, reportAs("fc=s1", plain))
		if a[0].Status != 204 {
			t.Fatalf("answered %+v", a[0])
		}
		if sent := site.got("/api/site/feedback"); len(sent) != 1 || sent[0].body != plain {
			t.Errorf("the site got %+v", sent)
		}
		if n := len(site.got("/api/auth/get-session")); n != 0 {
			t.Errorf("get-session was asked %d times with the window off", n)
		}
	})
}

// The site's own fittcore keeps reporter, the text a 💬 report names its sender by, and
// never passes it on to DV; an app's team-activity page counts each user's reports by
// it against their email (Jidapa, 25 Nov 2026: AEC's column found no one, as reporter was
// always ""). Where the window is on, the route puts there the email the company
// fittcore gives for the signed-in user, the fittcore the report goes to, and "" when
// nobody signed in can be found: a reporter the browser names itself never goes. The
// fittcore takes a reporter of at most 400 characters, so a longer email goes as "".
func TestAReportNamesItsSenderByEmailWhereTheWindowIsOn(t *testing.T) {
	eachLayout(t, func(t *testing.T, route string) {
		site := newFakeSite(t)
		site.sessions["s4"], site.sessions["s5"] = "u4", "u5"
		site.emails = map[string]string{"u4": strings.Repeat("a", 392) + "@aec.test", "u5": ""}
		spoof := `{"note":"แก้สี","reporter":"boss@aec.test","payload":{"pins":[]}}`
		bare := `{"note":"แก้สี","payload":{"pins":[]}}`
		a := runSteps(t, route, siteEnv(site), reportAs("fc=s1", spoof), reportAs("fc=s2", bare), reportAs("", spoof),
			reportAs("fc=gone", bare), reportAs("fc=s4", spoof), reportAs("fc=s5", spoof))
		for i, x := range a {
			if x.Status != 204 {
				t.Fatalf("report %d answered %+v", i, x)
			}
		}
		want := []string{"u1@aec.test", "u2@aec.test", "", "", "", ""}
		sent := site.got("/api/site/feedback")
		if len(sent) != len(want) {
			t.Fatalf("the site got %d reports", len(sent))
		}
		for i, s := range sent {
			var r map[string]any
			if err := json.Unmarshal([]byte(s.body), &r); err != nil {
				t.Fatalf("report %d went as %s", i, s.body)
			}
			if got, ok := r["reporter"].(string); !ok || got != want[i] {
				t.Errorf("report %d went with reporter %v, want %q: %s", i, r["reporter"], want[i], s.body)
			}
		}
		var first map[string]any
		_ = json.Unmarshal([]byte(sent[0].body), &first)
		if first["app_user_ref"] != "u1" || first["note"] != "แก้สี" || strings.Contains(sent[0].body, "ผู้ใช้ u1") {
			t.Errorf("a signed-in user's report went as %s", sent[0].body)
		}
	})
}
