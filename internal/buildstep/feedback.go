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
// (?op=...) under DV's contract with the company's own fittcore (CUSTOMER-FITTCORE-
// WIDGET-API part A rev1.2): the signed-in app user approves a token for themselves
// at their fittcore, sees the app's tickets and sends the ones they pick out of the
// company to DV, whose reviewers choose what goes to CRN. The token stays on the
// app's server, bound to that user's session; it never calls DV's fittcore.
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
// route's ticket ops. It stays off until the company fittcores have DV's widget API
// (17 Nov: not installed at any site yet); a site without it shows "ยังไม่เปิดใช้".
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
// The widget's 📋 window comes here too (?op=...). It follows DV's contract with the
// company's own fittcore (CUSTOMER-FITTCORE-WIDGET-API part A rev1.2, 17 Nov 2026):
// the signed-in app user asks their fittcore for a token, approves it there
// themselves, then sees the app's tickets and sends the ones they pick out of the
// company to DV. The token and the poll secret stay on this server, bound to that
// user's app session (fittcore session id + user id), and go only to the fittcore
// FITT_FEEDBACK_URL names. The browser sees the user code and the approval link,
// never the token. Nothing here calls DV's fittcore, the Gateway or CRN.
const OP_MAX_BYTES = 16 * 1024;
const WIDGET_API = "/api/site/widget/v1";
const CLIENT_NAME = "FITT feedback widget";
const TIMEOUT_MS = 5000;
const CLOSED_FOR_MS = 5 * 60 * 1000;
const SWEEP_MS = 5 * 60 * 1000;
const PROJECT = /^[A-Za-z0-9_-]{1,128}$/;
const TICKET = /^[A-Za-z0-9_-]{1,64}$/;
const KEY = /^[A-Za-z0-9_-]{16,64}$/;
const CODE = /^[A-Z0-9]{4}-[A-Z0-9]{4}$/;
const SECRET = /^[\x21-\x7e]{1,512}$/;
const IMAGE_TYPES = ["image/png", "image/jpeg", "image/webp"];

«type Challenge = { id: string; secret: string; code: string; url: string; expires: number; interval: number; next: number };
type Held = { ref: string; cookie: string; sessionExpires: number; token: string; tokenExpires: number; name: string; challenge: Challenge | null };
type Me = { key: string; ref: string; until: number };
»
// One entry per app session: a token is never shared between users or sessions.
const held«: Map<string, Held>» = new Map();
// Until when the company fittcore counts as not offering the window: it is asked at
// most once per CLOSED_FOR_MS after it answers 404 widget_not_enabled or not at all.
let closedUntil = 0;
let sweeping = false;

export const dynamic = "force-dynamic";

function json(status«: number», body«: unknown», headers«?: Record<string, string>») {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", "cache-control": "no-store", ...(headers || {}) },
  });
}

function notOpen() {
  return json(503, { error: "not_open" });
}

function originOf(value«: string | undefined») {
  try {
    const u = new URL(value || "");
    if ((u.protocol !== "http:" && u.protocol !== "https:") || u.username || u.password) return "";
    return u.origin;
  } catch {
    return "";
  }
}

// site is the company fittcore that both takes this app's reports (FITT_FEEDBACK_URL)
// and logs its users in (PLATFORM_FITTCORE_ORIGIN), or "" when they are not the same
// one: then the window is not open, and no challenge starts (DV, 17 Nov).
function site() {
  const reports = originOf(process.env.FITT_FEEDBACK_URL);
  return reports && reports === originOf(process.env.PLATFORM_FITTCORE_ORIGIN) ? reports : "";
}

// who is the app user behind this request as the company fittcore knows them (the
// app logs its users in through it): null when it knows no such session, undefined
// when it could not be asked.
async function who(cookie«: string») {
  const origin = site();
  if (!cookie || !origin) return null;
  try {
    const res = await fetch(origin + "/api/auth/get-session", {
      headers: { cookie, accept: "application/json" },
      redirect: "manual",
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
    if (!res.ok) return null;
    const v = await res.json();
    const s = v && typeof v === "object" && v.data ? v.data : v;
    const user = s && s.user;
    const session = s && s.session;
    if (!user || !session || typeof user.id !== "string" || user.id.length < 1 || user.id.length > 128) return null;
    if (session.userId !== user.id || typeof session.id !== "string" || !session.id) return null;
    const until = typeof session.expiresAt === "string" ? Date.parse(session.expiresAt) : NaN;
    if (until <= Date.now()) return null;
    const found«: Me» = { key: session.id + "\n" + user.id, ref: user.id, until: Number.isNaN(until) ? 0 : until };
    return found;
  } catch {
    closedUntil = Date.now() + CLOSED_FOR_MS;
    return undefined;
  }
}

function entry(me«: Me», cookie«: string») {
  let h = held.get(me.key);
  if (!h) {
    h = { ref: me.ref, cookie, sessionExpires: me.until, token: "", tokenExpires: 0, name: "", challenge: null };
    held.set(me.key, h);
  }
  h.cookie = cookie;
  h.sessionExpires = me.until;
  if (h.token && h.tokenExpires <= Date.now()) forget(h);
  return h;
}

function forget(h«: Held») {
  h.token = "";
  h.tokenExpires = 0;
  h.name = "";
}

// revoke withdraws the user's token at fittcore and forgets it here.
async function revoke(h«: Held») {
  if (h.token) {
    const res = await call("DELETE", "/auth/token", h);
    if (res) await res.text().catch(() => "");
  }
  forget(h);
}

// sweep withdraws the token of a session that ended: logged out, expired, or a token
// past its 8 hours. The app tells this route nothing when a user logs out, so it asks.
async function sweep() {
  for (const [key, h] of held) {
    const now = Date.now();
    if (h.challenge && h.challenge.expires <= now) h.challenge = null;
    if (h.token && h.tokenExpires <= now) forget(h);
    if (h.token) {
      const me = h.sessionExpires && h.sessionExpires <= now ? null : await who(h.cookie);
      if (me === null || (me && me.key !== key)) await revoke(h);
    }
    if (!h.token && !h.challenge) held.delete(key);
  }
}

function startSweep() {
  if (sweeping) return;
  sweeping = true;
  const timer = setInterval(sweep, SWEEP_MS)« as unknown as { unref?: () => void }»;
  if (timer.unref) timer.unref();
}

// call asks the company fittcore's widget API as this server: no Origin, no cookie,
// and the token with the user it was issued to. null means the site does not offer
// the window now.
async function call(method«: string», path«: string», h«: Held | null», body«?: unknown», key«?: string») {
  const origin = site();
  if (!origin || Date.now() < closedUntil) return null;
  const headers«: Record<string, string>» = { accept: "application/json" };
  if (h && h.token) {
    headers.authorization = "Bearer " + h.token;
    headers["x-widget-user"] = h.ref;
  }
  if (body !== undefined) headers["content-type"] = "application/json";
  if (key) headers["idempotency-key"] = key;
  let res«: Response»;
  try {
    res = await fetch(origin + WIDGET_API + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      redirect: "manual",
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
  } catch {
    closedUntil = Date.now() + CLOSED_FOR_MS;
    return null;
  }
  if (res.type === "opaqueredirect" || (res.status >= 300 && res.status < 400)) {
    return json(502, { error: "redirect", message: "fittcore answered with a redirect, which is not followed" });
  }
  if (res.status === 404) {
    const text = await res.text().catch(() => "");
    let code = "";
    try {
      code = String(JSON.parse(text).error || "");
    } catch {
      code = "";
    }
    if (!code || code === "widget_not_enabled") {
      closedUntil = Date.now() + CLOSED_FOR_MS;
      return null;
    }
    return new Response(text, { status: 404, headers: { "content-type": "application/json" } });
  }
  return res;
}

// relay hands fittcore's JSON answer to the browser. A token fittcore no longer takes
// is forgotten, so the window asks the user to approve a new one.
async function relay(res«: Response», h«: Held | null») {
  const text = await res.text().catch(() => "");
  let data«: unknown» = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = { error: "bad_answer", message: "" };
  }
  const code = data && typeof data === "object" ? (data« as { error?: unknown }»).error : undefined;
  if (h && (res.status === 401 || (res.status === 403 && code === "approver_not_eligible"))) forget(h);
  const extra«: Record<string, string>» = {};
  const retry = res.headers.get("retry-after");
  if (retry) extra["retry-after"] = retry;
  return json(res.status, data, extra);
}

function shown(c«: Challenge») {
  return { code: c.code, url: c.url, expires: c.expires, interval: c.interval };
}

// me answers the session behind the request, or the Response that ends it.
async function me(request«: Request») {
  if (!site() || Date.now() < closedUntil) return notOpen();
  const cookie = request.headers.get("cookie") || "";
  const m = await who(cookie);
  if (m === undefined) return notOpen();
  if (!m) return json(401, { error: "app_login" });
  return entry(m, cookie);
}

export async function GET(request«: Request»)«: Promise<Response>» {
  const q = new URL(request.url).searchParams;
  const op = q.get("op");
  if (op !== "state" && op !== "list" && op !== "images" && op !== "image") return json(404, { error: "unknown_op" });
  const h = await me(request);
  if (h instanceof Response) return h;

  if (op === "state") {
    if (h.token) return json(200, { ready: true, name: h.name });
    const c = h.challenge;
    if (c && c.expires > Date.now()) return json(200, { ready: false, pending: shown(c) });
    // Without a token, ask whether the site offers the window at all.
    const probe = await call("GET", "/tickets?status=open&limit=1", null);
    if (!probe) return notOpen();
    await probe.text().catch(() => "");
    return json(200, { ready: false });
  }
  if (!h.token) return json(401, { error: "invalid_token" });

  if (op === "list") {
    const status = q.get("status") === "open" ? "open" : "all";
    const cursor = q.get("cursor") || "";
    if (cursor.length > 512) return json(400, { error: "invalid_request" });
    const res = await call("GET", "/tickets?status=" + status + "&limit=50" + (cursor ? "&cursor=" + encodeURIComponent(cursor) : ""), h);
    return res ? relay(res, h) : notOpen();
  }
  const id = q.get("id") || "";
  if (!TICKET.test(id)) return json(400, { error: "invalid_request" });
  if (op === "images") {
    const res = await call("GET", "/tickets/" + id + "/images", h);
    return res ? relay(res, h) : notOpen();
  }
  const image = q.get("image") || "";
  if (!TICKET.test(image)) return json(400, { error: "invalid_request" });
  const res = await call("GET", "/tickets/" + id + "/images/" + image, h);
  if (!res) return notOpen();
  if (!res.ok) return relay(res, h);
  const type = (res.headers.get("content-type") || "").split(";")[0].trim().toLowerCase();
  if (!IMAGE_TYPES.includes(type)) {
    await res.body?.cancel();
    return json(502, { error: "bad_image" });
  }
  return new Response(res.body, {
    status: 200,
    headers: { "content-type": type, "cache-control": "no-store", "x-content-type-options": "nosniff" },
  });
}

async function ticketOp(op«: string», request«: Request») {
  if (op !== "login" && op !== "poll" && op !== "submit" && op !== "logout") return json(404, { error: "unknown_op" });
  const raw = await readBody(request, OP_MAX_BYTES);
  if (!raw) return json(413, { error: "payload_too_large" });
  const b = raw.byteLength ? fields(raw) : {};
  if (!b) return json(400, { error: "invalid_request" });
  const h = await me(request);
  if (h instanceof Response) return h;

  if (op === "logout") {
    await revoke(h);
    h.challenge = null;
    return json(200, { ready: false });
  }
  if (op === "login") {
    if (h.token) return json(200, { ready: true, name: h.name });
    if (typeof b.project !== "string" || !PROJECT.test(b.project)) return json(400, { error: "invalid_request" });
    const pending = h.challenge;
    if (pending && pending.expires > Date.now()) return json(200, { ready: false, pending: shown(pending) });
    const res = await call("POST", "/auth/challenges", null, { project_id: b.project, app_user_ref: h.ref, client_name: CLIENT_NAME });
    if (!res) return notOpen();
    if (res.status !== 201 && res.status !== 200) return relay(res, null);
    const v = await res.json().catch(() => null);
    if (!v || typeof v.challenge_id !== "string" || !TICKET.test(v.challenge_id) || typeof v.poll_secret !== "string" ||
      !SECRET.test(v.poll_secret) || typeof v.user_code !== "string" || !CODE.test(v.user_code) ||
      typeof v.approval_url !== "string" || !/^https?:\/\//.test(v.approval_url)) {
      return json(502, { error: "bad_answer", message: "" });
    }
    const expires = Date.parse(v.expires_at);
    h.challenge = {
      id: v.challenge_id,
      secret: v.poll_secret,
      code: v.user_code,
      url: v.approval_url,
      expires: Number.isNaN(expires) ? Date.now() + 10 * 60 * 1000 : expires,
      interval: Math.max(3, Number(v.interval_seconds) || 3) * 1000,
      next: 0,
    };
    startSweep();
    return json(200, { ready: false, pending: shown(h.challenge) });
  }
  if (op === "poll") {
    if (h.token) return json(200, { ready: true, name: h.name });
    const c = h.challenge;
    if (!c || c.expires <= Date.now()) {
      h.challenge = null;
      return json(200, { ready: false, expired: true });
    }
    if (Date.now() < c.next) return json(200, { ready: false, pending: shown(c) });
    c.next = Date.now() + c.interval;
    const res = await call("POST", "/auth/challenges/" + c.id + "/token", null, { poll_secret: c.secret });
    if (!res) return notOpen();
    const v = await res.json().catch(() => null);
    if (res.status === 200 && v && typeof v.access_token === "string" && SECRET.test(v.access_token)) {
      const expires = Date.parse(v.expires_at);
      h.token = v.access_token;
      h.tokenExpires = Number.isNaN(expires) ? Date.now() + 8 * 3600 * 1000 : expires;
      h.name = v.approved_by && typeof v.approved_by.display_name === "string" ? v.approved_by.display_name : "";
      h.challenge = null;
      startSweep();
      return json(200, { ready: true, name: h.name });
    }
    const code = v && v.error;
    if (res.status === 428 || code === "authorization_pending") return json(200, { ready: false, pending: shown(c) });
    if (res.status === 429 || code === "slow_down") {
      c.interval += 3000;
      c.next = Date.now() + c.interval;
      return json(200, { ready: false, pending: shown(c) });
    }
    h.challenge = null;
    if (res.status === 403) return json(200, { ready: false, denied: true });
    return json(200, { ready: false, expired: true });
  }

  // submit
  if (!h.token) return json(401, { error: "invalid_token" });
  const ids = b.ticket_ids;
  if (!Array.isArray(ids) || ids.length < 1 || ids.length > 50 || new Set(ids).size !== ids.length ||
    !ids.every((id) => typeof id === "string" && TICKET.test(id)) || typeof b.key !== "string" || !KEY.test(b.key)) {
    return json(400, { error: "invalid_request" });
  }
  const res = await call("POST", "/tickets/submit", h, { ticket_ids: ids }, b.key);
  return res ? relay(res, h) : notOpen();
}

function fields(raw«: Uint8Array») {
  try {
    const v = JSON.parse(new TextDecoder().decode(raw));
    return v && typeof v === "object" && !Array.isArray(v) ? v : null;
  } catch {
    return null;
  }
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
