package buildstep

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// fitt-feedback.js is the self-contained in-demo feedback widget. A build that
// asks for it ("feedback_widget": true) gets it inlined deterministically (never
// left to Claude to reproduce); customer deliveries ship without it by default.
//
//go:embed fitt-feedback.js
var feedbackWidgetJS string

// InjectFeedbackWidget inlines the feedback widget into every index-style HTML
// file under dir (before </body>), configured with this project's id and the
// PostgREST ingest URL the widget POSTs to. It is deterministic and best-effort:
// it returns the number of HTML files patched. A missing </body> gets the script
// appended. Skips .git / node_modules / .claude and already-injected files.
//
// Next.js App Router apps have no index.html (app/layout.tsx owns <body>); for
// those it falls back to dropping the widget in public/, adding a <script src>
// to the layout and a route the widget posts to (see feedbackRoute), so CRN's
// real (Next.js) output gets the widget too.
func InjectFeedbackWidget(dir, projectID, ingestURL string, logger *slog.Logger) (int, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if ingestURL == "" {
		return 0, nil // no ingest configured — nothing to inject
	}

	// 1. Static/HTML apps: inline the widget before </body> in every HTML file.
	patched, err := injectIntoHTML(dir, feedbackScriptTag(projectID, ingestURL), logger)
	if err != nil {
		return patched, err
	}
	if patched > 0 {
		return patched, nil
	}

	// 2. Next.js App Router (no index.html): public/ + a <script src> in layout.
	return injectIntoNextLayout(dir, projectID, ingestURL, logger), nil
}

// injectIntoHTML inlines tag before </body> in every .html file under dir,
// returning the number of files patched.
func injectIntoHTML(dir, tag string, logger *slog.Logger) (int, error) {
	patched := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries rather than abort the build
		}
		if d.IsDir() {
			switch d.Name() {
			// Skip build OUTPUT dirs: patching prerendered .html there is useless
			// (gitignored, regenerated) AND, because injectIntoHTML returns early
			// once anything is patched, it would suppress the Next-layout injection
			// that actually ships the widget. `.next` is the common culprit.
			case ".git", "node_modules", ".claude", "dist", "build", ".next", "out", ".turbo", ".vercel":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".html") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		html := string(raw)
		if strings.Contains(html, "data-fitt-feedback") {
			return nil // already injected
		}
		// Insert before the closing </body> (case-insensitive), preserving the
		// original tag's case by splitting the untouched source at that index.
		var out string
		if i := strings.LastIndex(strings.ToLower(html), "</body>"); i >= 0 {
			out = html[:i] + tag + "\n" + html[i:]
		} else {
			out = html + "\n" + tag + "\n"
		}
		if werr := os.WriteFile(path, []byte(out), 0o644); werr != nil {
			logger.Warn("write feedback-injected html failed", "path", path, "err", werr)
			return nil
		}
		patched++
		return nil
	})
	if err != nil {
		return patched, fmt.Errorf("buildstep: inject feedback widget: %w", err)
	}
	return patched, nil
}

// injectIntoNextLayout handles Next.js App Router apps: it writes the widget to
// public/fitt-feedback.js and inserts a <script src> before </body> in the root
// layout (app/layout.tsx or src/app/layout.tsx). Returns 1 on success, else 0.
func injectIntoNextLayout(dir, projectID, ingestURL string, logger *slog.Logger) int {
	candidates := []string{
		filepath.Join(dir, "app", "layout.tsx"),
		filepath.Join(dir, "app", "layout.jsx"),
		filepath.Join(dir, "src", "app", "layout.tsx"),
		filepath.Join(dir, "src", "app", "layout.jsx"),
	}
	layoutPath := ""
	for _, c := range candidates {
		if info, statErr := os.Stat(c); statErr == nil && !info.IsDir() {
			layoutPath = c
			break
		}
	}
	if layoutPath == "" {
		return 0
	}
	raw, err := os.ReadFile(layoutPath)
	if err != nil {
		return 0
	}
	src := string(raw)
	if strings.Contains(src, "fitt-feedback") {
		return 0 // already injected
	}
	i := strings.Index(src, "</body>")
	if i < 0 {
		return 0 // no <body> to anchor the script to
	}

	pub := filepath.Join(dir, "public")
	if err := os.MkdirAll(pub, 0o755); err != nil {
		logger.Warn("create public dir failed", "err", err)
		return 0
	}
	if err := os.WriteFile(filepath.Join(pub, "fitt-feedback.js"), []byte(feedbackWidgetJS), 0o644); err != nil {
		logger.Warn("write public/fitt-feedback.js failed", "err", err)
		return 0
	}

	// The widget posts to the app's own route, which sends the report on to the
	// receiver FITT_FEEDBACK_URL names when the app runs. Reading the env in the
	// layout instead would freeze the build's value into every page Next
	// prerenders, and the browser would need to reach the receiver across origins.
	routePath, route := feedbackRoute(filepath.Dir(layoutPath), filepath.Ext(layoutPath), ingestURL)
	if err := os.MkdirAll(filepath.Dir(routePath), 0o755); err != nil {
		logger.Warn("create feedback route dir failed", "err", err)
		return 0
	}
	if err := os.WriteFile(routePath, []byte(route), 0o644); err != nil {
		logger.Warn("write feedback route failed", "path", routePath, "err", err)
		return 0
	}

	// A plain classic <script src> (not next/script) so document.currentScript and
	// its data-* config resolve during synchronous execution. Values are safe
	// (uuid + path) so no JSX-entity escaping is needed. data-tickets turns on the
	// 📋 window, which needs the route: a static page has no server to call fittcore.
	tickets := ""
	if ticketWindow {
		tickets = ` data-tickets="` + feedbackRoutePath + `"`
	}
	script := `<script src="/fitt-feedback.js" data-fitt-feedback="true" data-project="` +
		projectID + `" data-ingest="` + feedbackRoutePath + `"` + tickets + ` />`
	out := src[:i] + "        " + script + "\n      " + src[i:]
	if err := os.WriteFile(layoutPath, []byte(out), 0o644); err != nil {
		logger.Warn("write feedback-injected layout failed", "path", layoutPath, "err", err)
		return 0
	}
	logger.Info("injected feedback widget into next layout", "layout", layoutPath)
	return 1
}

// feedbackRoutePath is where a Next app's widget posts, on the app's own origin.
const feedbackRoutePath = "/api/fitt-feedback"

// feedbackRoute returns the path and source of the route that sends the widget's
// reports on to FITT_FEEDBACK_URL, read when each report arrives; ingestURL is
// the fallback when it is unset. appDir is the layout's directory and layoutExt
// its extension, so a JavaScript app gets a JavaScript route. The route refuses
// a report over 25 MB with 413 and answers 502 when the receiver redirects.
//
// With the ticket window on (ticketWindow), the same route serves the widget's 📋
// (?op=...): the customer logs in to their company's own fittcore, sees the app's
// tickets and sends the ones they pick out of the company to DV, whose reviewers
// choose what goes to CRN. The route holds no key; it passes on the customer's
// token, only to the fittcore FITT_FEEDBACK_URL names. It never calls DV's fittcore.
// What it asks of the customer's fittcore is the FITTCORE table, in one place: DV
// sends that API's spec later (16 Nov), so the paths there are placeholders.
func feedbackRoute(appDir, layoutExt, ingestURL string) (string, string) {
	fallback, _ := json.Marshal(ingestURL)
	src := feedbackRouteHead + "const FALLBACK = " + string(fallback) + ";\n" + feedbackRouteReport
	if ticketWindow {
		src = strings.Replace(src, ticketOpsHook, ticketOpsCall, 1) + feedbackRouteTickets
	} else {
		src = strings.Replace(src, ticketOpsHook, "", 1)
	}
	if layoutExt == ".jsx" {
		// The only TypeScript in the source is what «» marks.
		return filepath.Join(appDir, "api", "fitt-feedback", "route.js"), typeNoteRe.ReplaceAllString(src, "")
	}
	return filepath.Join(appDir, "api", "fitt-feedback", "route.ts"), strings.NewReplacer("«", "", "»", "").Replace(src)
}

// ticketWindow turns on the widget's 📋 window: data-tickets in a Next layout and the
// route's ticket ops. It stays off until DV sends the spec of the API on the
// customer's own fittcore (Jidapa, 16 Nov); the window and the ops are built against
// placeholders (FITTCORE in feedbackRouteTickets) until then.
var ticketWindow = false

const (
	ticketOpsHook = "  // ticket ops\n"
	ticketOpsCall = "  const op = new URL(request.url).searchParams.get(\"op\");\n  if (op !== null) return ticketOp(op, request);\n"
)

// typeNoteRe matches the TypeScript the route's source marks with «», which a
// JavaScript app's route leaves out.
var typeNoteRe = regexp.MustCompile(`«[^»]*»`)

const feedbackRouteHead = `// fitt-feedback: CRN adds this route with the 💬 feedback widget and removes it
// with the widget. The widget posts here, on the app's own origin, and the server
// sends the report on to the receiver FITT_FEEDBACK_URL names when the app runs.
// Anyone who can open the app can post here, so a report over MAX_BYTES is refused
// unread, and a receiver's redirect is not followed.
`

const feedbackRouteReport = `const MAX_BYTES = 25 * 1024 * 1024;

function tooLarge() {
  return new Response("feedback report is larger than 25 MB", { status: 413 });
}

// readBody reads the request's body, or answers null when it is larger than max.
async function readBody(request«: Request», max«: number») {
  if (Number(request.headers.get("content-length")) > max) return null;
  const chunks«: Uint8Array[]» = [];
  let size = 0;
  if (request.body) {
    const reader = request.body.getReader();
    for (let part = await reader.read(); !part.done; part = await reader.read()) {
      size += part.value.byteLength;
      if (size > max) {
        await reader.cancel();
        return null;
      }
      chunks.push(part.value);
    }
  }
  const body = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) {
    body.set(chunk, at);
    at += chunk.byteLength;
  }
  return body;
}

export async function POST(request«: Request»)«: Promise<Response>» {
  // ticket ops
  const body = await readBody(request, MAX_BYTES);
  if (!body) return tooLarge();

  const target = process.env.FITT_FEEDBACK_URL || FALLBACK;
  try {
    const res = await fetch(target, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body,
      redirect: "manual",
      signal: AbortSignal.timeout(15000),
    });
    if (res.ok) return new Response(null, { status: 204 });
    if (res.type === "opaqueredirect" || (res.status >= 300 && res.status < 400)) {
      return new Response("feedback receiver answered with a redirect, which is not followed", { status: 502 });
    }
    return new Response(await res.text(), { status: res.status });
  } catch {
    return new Response("feedback receiver unreachable", { status: 502 });
  }
}

`

// feedbackRouteTickets is the route's 📋 part, there when ticketWindow is on.
const feedbackRouteTickets = `
// The widget's 📋 window comes here too (?op=...): the customer logs in to their
// company's own fittcore, sees this app's tickets and sends the ones they pick on to
// DV. This route holds no key. The widget sends the token the customer's fittcore
// gave them, which goes only to the fittcore FITT_FEEDBACK_URL names (its
// /api/ingest/feedback). It never calls DV's fittcore.
const OP_MAX_BYTES = 64 * 1024;
const INGEST_PATH = "/api/ingest/feedback";
const ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const NAME = /^[A-Za-z0-9_-]{1,128}$/;
const TOKEN = /^[\x21-\x7e]{1,2048}$/;

// What the ticket window asks of the customer's fittcore, relative to its /api.
// Placeholders until DV's spec: the token is for this app alone, so no path names it.
const FITTCORE = {
  login: () => "/widget-auth/challenges",
  poll: (id«: string», token«: string») => "/widget-auth/challenges/" + id + "?token=" + encodeURIComponent(token),
  list: () => "/widget/tickets",
  send: () => "/widget/tickets/send",
};

export const dynamic = "force-dynamic";

function json(status«: number», body«: unknown») {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", "cache-control": "no-store" },
  });
}

export async function GET(request«: Request»)«: Promise<Response>» {
  if (new URL(request.url).searchParams.get("op") !== "list") return json(404, { error: "unknown op" });
  const token = bearer(request);
  if (!token) return json(401, { error: "log in to fittcore first" });
  const res = await fittcore("GET", FITTCORE.list(), token);
  if (!res.ok) return res;
  try {
    return json(200, withoutPictures(await res.json()));
  } catch {
    return json(502, { error: "fittcore answered with something other than JSON" });
  }
}

async function ticketOp(op«: string», request«: Request») {
  if (op !== "login" && op !== "poll" && op !== "send") return json(404, { error: "unknown op" });
  const raw = await readBody(request, OP_MAX_BYTES);
  if (!raw) return json(413, { error: "request is larger than 64 KB" });
  const b = fields(raw);
  if (!b) return json(400, { error: "the body is not a JSON object" });

  if (op === "login") {
    if (typeof b.project !== "string" || !NAME.test(b.project)) return json(400, { error: "project is the widget's project id" });
    return fittcore("POST", FITTCORE.login(), "", { projectId: b.project, clientName: "CRN feedback widget" });
  }
  if (op === "poll") {
    if (typeof b.id !== "string" || !NAME.test(b.id) || typeof b.token !== "string" || !TOKEN.test(b.token)) {
      return json(400, { error: "id and token are the ones login answered" });
    }
    return fittcore("GET", FITTCORE.poll(b.id, b.token), "");
  }

  const token = bearer(request);
  if (!token) return json(401, { error: "log in to fittcore first" });
  const ids = b.feedbackIds;
  if (!Array.isArray(ids) || ids.length === 0 || ids.length > 200 || !ids.every((id) => typeof id === "string" && ID.test(id))) {
    return json(400, { error: "feedbackIds are the ids of the tickets to send" });
  }
  return fittcore("POST", FITTCORE.send(), token, { feedbackIds: ids });
}

function fields(raw«: Uint8Array») {
  try {
    const v = JSON.parse(new TextDecoder().decode(raw));
    return v && typeof v === "object" && !Array.isArray(v) ? v : null;
  } catch {
    return null;
  }
}

function bearer(request«: Request») {
  const h = request.headers.get("authorization") || "";
  const token = h.startsWith("Bearer ") ? h.slice(7) : "";
  return TOKEN.test(token) ? token : "";
}

// fittcoreAPI is the /api of the fittcore that takes this app's reports, or "" when
// FITT_FEEDBACK_URL names some other receiver: the customer's token goes nowhere else.
function fittcoreAPI() {
  try {
    const u = new URL(process.env.FITT_FEEDBACK_URL || FALLBACK);
    if (u.search || u.hash || !u.pathname.endsWith(INGEST_PATH)) return "";
    return u.origin + u.pathname.slice(0, -"/ingest/feedback".length);
  } catch {
    return "";
  }
}

async function fittcore(method«: string», path«: string», token«: string», body«?: unknown») {
  const api = fittcoreAPI();
  if (!api) return json(503, { error: "the ticket window needs FITT_FEEDBACK_URL to name fittcore's " + INGEST_PATH });
  const headers«: Record<string, string>» = { accept: "application/json" };
  if (token) headers.authorization = "Bearer " + token;
  if (body !== undefined) headers["content-type"] = "application/json";
  try {
    const res = await fetch(api + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      redirect: "manual",
      signal: AbortSignal.timeout(15000),
    });
    if (res.type === "opaqueredirect" || (res.status >= 300 && res.status < 400)) {
      return json(502, { error: "fittcore answered with a redirect, which is not followed" });
    }
    const text = await res.text();
    return new Response(res.status === 204 || res.status === 205 ? null : text, {
      status: res.status,
      headers: { "content-type": res.headers.get("content-type") || "application/json", "cache-control": "no-store" },
    });
  } catch {
    return json(502, { error: "fittcore unreachable" });
  }
}

// Pin pictures stay on the server: a ticket may carry 50 pins of up to 1 MB each.
function withoutPictures(items«: unknown») {
  if (!Array.isArray(items)) return items;
  return items.map((f) => {
    const pins«: unknown» = f && typeof f === "object" ? f.pins : null;
    if (!Array.isArray(pins)) return f;
    return { ...f, pins: pins.map(pinWithoutPicture) };
  });
}

function pinWithoutPicture(p«: unknown») {
  if (!p || typeof p !== "object") return p;
  const pin«: Record<string, unknown>» = { ...p };
  const hasShot = !!pin.regionShot;
  delete pin.regionShot;
  return { ...pin, hasShot };
}
`

// feedbackRouteFiles are the routes feedbackRoute may have written under dir.
func feedbackRouteFiles(dir string) []string {
	var files []string
	for _, appDir := range []string{filepath.Join(dir, "app"), filepath.Join(dir, "src", "app")} {
		for _, ext := range []string{".ts", ".js"} {
			files = append(files, filepath.Join(appDir, "api", "fitt-feedback", "route"+ext))
		}
	}
	return files
}

// The widget is opt-in per build, so a build that leaves it off must also take
// out one an earlier build put in (an edit build starts from the pushed repo).
var (
	// What injectIntoHTML adds: the inline tag plus the newline after it.
	inlineWidgetRe = regexp.MustCompile(`(?s)<script data-fitt-feedback[^>]*>.*?</script>\n?`)
	// What injectIntoNextLayout adds, byte for byte, so removal restores the layout.
	layoutWidgetExactRe = regexp.MustCompile(` {8}<script src="/fitt-feedback\.js" data-fitt-feedback="true"[^\n]*/>\n {6}`)
	// The same tag after a formatter or the AI has moved it.
	layoutWidgetLooseRe = regexp.MustCompile(`\s*<script\s+src="/fitt-feedback\.js"[\s\S]*?/>`)
)

// HasFeedbackWidget reports whether the app under dir carries the widget: an
// inlined tag in an HTML file, the tag in a Next root layout, the public copy or
// the route it posts to.
func HasFeedbackWidget(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "public", "fitt-feedback.js")); err == nil {
		return true
	}
	for _, route := range feedbackRouteFiles(dir) {
		if raw, err := os.ReadFile(route); err == nil && strings.Contains(string(raw), "fitt-feedback") {
			return true
		}
	}
	for _, layout := range nextLayoutCandidates(dir) {
		if raw, err := os.ReadFile(layout); err == nil && strings.Contains(string(raw), "fitt-feedback") {
			return true
		}
	}
	found := false
	_ = walkAppHTML(dir, func(_ string, html string) {
		if strings.Contains(html, "data-fitt-feedback") {
			found = true
		}
	})
	return found
}

// RemoveFeedbackWidget takes the widget out of the app under dir, undoing
// InjectFeedbackWidget: the inlined tag from every HTML file, the tag from the
// Next root layout, public/fitt-feedback.js and the route. It returns the number
// of HTML files and layouts changed (the layout, its public copy and the route
// count as one).
func RemoveFeedbackWidget(dir string, logger *slog.Logger) (int, error) {
	if logger == nil {
		logger = slog.Default()
	}
	changed := 0
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	keep(walkAppHTML(dir, func(path, html string) {
		if !strings.Contains(html, "data-fitt-feedback") {
			return
		}
		out := inlineWidgetRe.ReplaceAllString(html, "")
		if out == html {
			return
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			keep(fmt.Errorf("buildstep: remove feedback widget from %s: %w", path, err))
			return
		}
		changed++
	}))

	layoutChanged := false
	for _, layout := range nextLayoutCandidates(dir) {
		raw, err := os.ReadFile(layout)
		if err != nil || !strings.Contains(string(raw), "fitt-feedback") {
			continue
		}
		src := string(raw)
		out := layoutWidgetExactRe.ReplaceAllString(src, "")
		out = layoutWidgetLooseRe.ReplaceAllString(out, "")
		if out == src {
			// Only a mention (a comment, say): no tag loads the widget, nothing to undo.
			logger.Warn("layout mentions fitt-feedback but holds no widget tag", "layout", layout)
			continue
		}
		if err := os.WriteFile(layout, []byte(out), 0o644); err != nil {
			keep(fmt.Errorf("buildstep: remove feedback widget from %s: %w", layout, err))
			continue
		}
		layoutChanged = true
	}
	public := filepath.Join(dir, "public", "fitt-feedback.js")
	if err := os.Remove(public); err == nil {
		layoutChanged = true
	} else if !os.IsNotExist(err) {
		keep(fmt.Errorf("buildstep: remove %s: %w", public, err))
	}
	for _, route := range feedbackRouteFiles(dir) {
		raw, err := os.ReadFile(route)
		if err != nil || !strings.Contains(string(raw), "fitt-feedback") {
			continue
		}
		if err := os.Remove(route); err != nil {
			keep(fmt.Errorf("buildstep: remove %s: %w", route, err))
			continue
		}
		layoutChanged = true
		// The directories go too when nothing of the app's own is left in them.
		routeDir := filepath.Dir(route)
		_ = os.Remove(routeDir)
		_ = os.Remove(filepath.Dir(routeDir))
	}
	if layoutChanged {
		changed++
	}
	if changed > 0 {
		logger.Info("removed feedback widget", "files", changed)
	}
	return changed, firstErr
}

// walkAppHTML calls fn with every .html file under dir that is app source,
// skipping the same build output and tooling dirs injectIntoHTML skips.
func walkAppHTML(dir string, fn func(path, html string)) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".claude", "dist", "build", ".next", "out", ".turbo", ".vercel":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".html") {
			return nil
		}
		if raw, err := os.ReadFile(path); err == nil {
			fn(path, string(raw))
		}
		return nil
	})
}

func nextLayoutCandidates(dir string) []string {
	return []string{
		filepath.Join(dir, "app", "layout.tsx"),
		filepath.Join(dir, "app", "layout.jsx"),
		filepath.Join(dir, "src", "app", "layout.tsx"),
		filepath.Join(dir, "src", "app", "layout.jsx"),
	}
}

// feedbackScriptTag wraps the embedded widget in an inline <script> carrying the
// per-build config the widget reads from document.currentScript.dataset.
func feedbackScriptTag(projectID, ingestURL string) string {
	return `<script data-fitt-feedback data-project="` + htmlAttrEscape(projectID) +
		`" data-ingest="` + htmlAttrEscape(ingestURL) + `">` + "\n" +
		feedbackWidgetJS + "\n</script>"
}

func htmlAttrEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;",
	).Replace(s)
}
