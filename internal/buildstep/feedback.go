package buildstep

import (
	_ "embed"
	"encoding/json"
	"errors"
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
//
// A widget an earlier build put in is taken out first (RemoveFeedbackWidget) and this
// CRN's put in its place, so a change to the widget or its route reaches an app at its
// next build. Until 20 Nov 2026 an app kept its first widget for good: OKR (build 13)
// and NTC (build 8) still had the 27 Oct one. If the earlier widget is out but this
// CRN's cannot go in, it says so rather than leave the app without one.
func InjectFeedbackWidget(dir, projectID, ingestURL string, logger *slog.Logger) (int, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if ingestURL == "" {
		return 0, nil // no ingest configured — nothing to inject
	}
	earlier := HasFeedbackWidget(dir)
	if earlier {
		if _, err := RemoveFeedbackWidget(dir, logger); err != nil {
			return 0, fmt.Errorf("buildstep: take out the earlier feedback widget: %w", err)
		}
	}

	// 1. Static/HTML apps: inline the widget before </body> in every HTML file.
	patched, err := injectIntoHTML(dir, feedbackScriptTag(projectID, ingestURL), logger)
	if err != nil {
		return patched, err
	}
	if patched == 0 {
		// 2. Next.js App Router (no index.html): public/ + a <script src> in layout.
		patched = injectIntoNextLayout(dir, projectID, ingestURL, logger)
	}
	if patched == 0 && earlier {
		return 0, errors.New("buildstep: the app had a feedback widget, but this CRN's could not be put in its place")
	}
	if earlier && patched > 0 {
		logger.Info("replaced the earlier feedback widget with this CRN's")
	}
	return patched, nil
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
	src := feedbackRouteHead + "const FALLBACK = " + string(fallback) + ";\n" + feedbackRouteReport + feedbackRouteAssist
	if ticketWindow {
		src = strings.Replace(src, ticketOpsHook, ticketOpsCall, 1) + feedbackRouteTickets
		src = strings.Replace(src, senderHook, senderCall, 1)
	} else {
		src = strings.Replace(src, ticketOpsHook, "", 1)
		src = strings.Replace(src, senderHook, "", 1)
	}
	if layoutExt == ".jsx" {
		// The only TypeScript in the source is what «» marks.
		return filepath.Join(appDir, "api", "fitt-feedback", "route.js"), typeNoteRe.ReplaceAllString(src, "")
	}
	return filepath.Join(appDir, "api", "fitt-feedback", "route.ts"), strings.NewReplacer("«", "", "»", "").Replace(src)
}

// ticketWindow puts the widget's 📋 window into every build: data-tickets in a Next
// layout and the route's ticket ops (since 20 Nov 2026). Whether it shows is the app
// server's call at run time (DV, 23 Nov): only with FITT_TICKET_WINDOW=1, the same
// origin in FITT_FEEDBACK_URL and PLATFORM_FITTCORE_ORIGIN, and a company fittcore that
// has DV's widget API; otherwise 📋 is not shown and the route calls no one, or asks the
// site again at most once per 5 minutes.
var ticketWindow = true

const (
	ticketOpsHook = "  // ticket ops\n"
	ticketOpsCall = "  const op = new URL(request.url).searchParams.get(\"op\");\n  if (op !== null) return ticketOp(op, request);\n"
	senderHook    = "  // sender\n"
	senderCall    = "  const signed = await withSender(request, body);\n  if (signed === false) return new Response(\"feedback report is not a JSON object\", { status: 400 });\n  if (signed.report !== null) body = bytesOf(signed.report);\n  if (signed.cookie) headers.cookie = signed.cookie;\n"
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
// The company fittcore takes only two reports at a time per app machine and turns
// the rest away with 503 and Retry-After (DV, 17 Nov). Such a report is sent again
// after the wait it asks for, at most AGAIN times; a wait longer than MAX_WAIT_MS is
// not sat out while the user watches, and the user hears the report was not sent.
const AGAIN = 2;
const MAX_WAIT_MS = 10 * 1000;

// waitFor is how long a 503 asks the route to wait, from its Retry-After (seconds
// or a date): a second when it names none.
function waitFor(res«: Response») {
  const v = (res.headers.get("retry-after") || "").trim();
  if (/^\d+$/.test(v)) return Number(v) * 1000;
  const at = v ? Date.parse(v) : NaN;
  return Number.isNaN(at) ? 1000 : Math.max(0, at - Date.now());
}

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

// sizeOf is how big a report is: the note's length, how many pins and the longest
// pin note, counted as JavaScript counts a string's length. Never what was written.
function sizeOf(body«: Uint8Array») {
  let r«: { note?: unknown; payload?: { pins?: unknown } } | null» = null;
  try {
    r = JSON.parse(new TextDecoder().decode(body));
  } catch {
    r = null;
  }
  const pins = r && r.payload && Array.isArray(r.payload.pins) ? r.payload.pins : [];
  let longest = 0;
  for (const p of pins) {
    if (p && typeof p.note === "string") longest = Math.max(longest, p.note.length);
  }
  return { bytes: body.byteLength, note: r && typeof r.note === "string" ? r.note.length : null, pins: pins.length, pin_note_max: longest };
}

// logReport logs one line for each report the route sends on: its size and what the
// receiver answered (DV asked, 24 Nov 2026, to trace a 422 to what was sent).
function logReport(size«: ReturnType<typeof sizeOf>», receiver«: number | string») {
  console.info("[fitt-feedback] " + JSON.stringify({ ...size, receiver }));
}

export async function POST(request«: Request»)«: Promise<Response>» {
  if (new URL(request.url).searchParams.get("op") === "assist") return assist(request);
  // ticket ops
  let body = await readBody(request, MAX_BYTES);
  if (!body) return tooLarge();
  const headers«: Record<string, string>» = { "content-type": "application/json" };
  // sender
  const size = sizeOf(body);

  const target = process.env.FITT_FEEDBACK_URL || FALLBACK;
  try {
    for (let sent = 1; ; sent++) {
      const res = await fetch(target, {
        method: "POST",
        headers,
        body,
        redirect: "manual",
        signal: AbortSignal.timeout(15000),
      });
      if (res.ok) {
        logReport(size, res.status);
        return new Response(null, { status: 204 });
      }
      if (res.type === "opaqueredirect" || (res.status >= 300 && res.status < 400)) {
        logReport(size, "redirect");
        return new Response("feedback receiver answered with a redirect, which is not followed", { status: 502 });
      }
      const wait = res.status === 503 && sent <= AGAIN ? waitFor(res) : MAX_WAIT_MS + 1;
      if (wait > MAX_WAIT_MS) {
        logReport(size, res.status);
        return new Response(await res.text(), { status: res.status });
      }
      await res.body?.cancel();
      await new Promise((done) => setTimeout(done, wait));
    }
  } catch {
    logReport(size, "unreachable");
    return new Response("feedback receiver unreachable", { status: 502 });
  }
}

`

// feedbackRouteAssist is the route's 💬 assistant (23 Nov 2026), in every route.
const feedbackRouteAssist = `
// The 💬 assistant (the assist op): customers asked for changes as questions ("ย้ายปุ่ม
// บันทึกไปไว้ใต้ตารางได้ไหม") and builds answered instead of changing. Here the customer
// talks with the AI FITT_FEEDBACK_AI_URL names, which asks back what it needs and drafts
// the change as a request, for the customer to check and send as an ordinary report.
// This server calls the AI, with FITT_FEEDBACK_AI_KEY when set: the browser sees
// neither. The instructions are CRN's; the AI service passes them and the schema on to
// its model. Without FITT_FEEDBACK_AI_URL the widget shows no assistant.
// Pictures and documents the customer attaches go to the AI with every turn of the chat.
const ASSIST_MAX_BYTES = 14 * 1024 * 1024;
const ASSIST_FILES = 10;
const ASSIST_FILE_TYPES = [
  "image/png", "image/jpeg", "image/webp", "application/pdf", "text/plain", "text/csv",
  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
];
const BASE64 = /^[A-Za-z0-9+/]+={0,2}$/;
const ASSIST_TIMEOUT_MS = 30000;
const ASSIST_MESSAGES = 20;
const ASSIST_TEXT = 2000;
const ASSIST_REPLY = 4000;
const ASSIST_DRAFT = 4000;
const ASSIST_KINDS = ["bug", "feature", "style"];
const ASSIST_PRIORITIES = ["low", "med", "high"];
const ASSIST_INSTRUCTIONS = [
  "You help a customer of this web app tell its team what they want changed. Write every reply in Thai, short and polite (ครับ), at most three sentences.",
  "Customers often ask for a change as a question, such as \"ย้ายปุ่มบันทึกไปไว้ใต้ตารางได้ไหม\": that is a request for the change, not a question to answer.",
  "Ask one question at a time, and only what you need: which page or element (suggest pinning it on the page with 📍), and what it should become.",
  "The customers are business owners, not developers, and often write a few words such as \"เปลี่ยนเป็น กว้าง xยาว\". Before you draft, you need three things: the screen, the element by the words the customer sees on it (button, field, menu, table, column or document), and what it should become. Take what context or a pin already tells you and do not ask it again.",
  "Never guess a screen, menu, button, field or document the customer did not name and the context does not show: ask. When the customer means one of several, name them and let the customer choose.",
  "For a new document, report or menu, ask what it must show (its fields, totals or columns, and who uses it) unless a file the customer attached says so.",
  "You never promise that a change will be made, and never say it can be done: you write the request, and the team decides.",
  "When you know what to change, set draft. draft.text is the change written as a request in Thai, one change per line, never a question; keep the earlier changes of this conversation in it, each once. Each line names its screen, then what to do (ย้าย, เปลี่ยน, เพิ่ม, ลบ, แก้), the element by its words and what it should become, such as: หน้าจอรุ่นรถ: เปลี่ยนชื่อช่อง \"สูง\" เป็น \"ยาว\". For something wrong, add what it shows now and what it should show.",
  "draft.category is bug when something is broken, style when only the look changes, feature otherwise. draft.priority is high when the customer says it is urgent or blocks their work, low when they say it can wait, med otherwise.",
  "When the customer only asks how to use the app, answer briefly and set draft to null. Otherwise set draft to null until you know what to change.",
  "context.page is the page's address and, after หน้าจอ, the title of the screen the customer is on. context.pins are what they pinned: each says what it is by its words (ปุ่ม, ช่อง, เมนู, หัวตาราง, ข้อความ), the box or window it is in and the screen; a crop (พื้นที่) says the words inside it.",
  "files are what the customer attached: pictures (crops of the page, screenshots) and documents. Read them fully. When a change comes from a file, put in draft.text what the team needs from it (for a new menu from a document: its fields, lists and steps) and name the file, since the team may not have the file itself.",
].join("\n");
const ASSIST_SCHEMA = {
  type: "object",
  properties: {
    reply: { type: "string" },
    draft: {
      type: "object",
      nullable: true,
      properties: {
        text: { type: "string" },
        category: { type: "string", enum: ASSIST_KINDS },
        priority: { type: "string", enum: ASSIST_PRIORITIES },
      },
      required: ["text", "category", "priority"],
    },
  },
  required: ["reply"],
};

«type AssistMessage = { role: string; text: string };
type AssistFile = { name: string; type: string; data: string };
»
function assistAnswer(status«: number», body«: unknown») {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", "cache-control": "no-store", "x-content-type-options": "nosniff" },
  });
}

// assistText is v when it is text no longer than max, or null.
function assistText(v«: unknown», max«: number»)«: string | null» {
  return typeof v === "string" && v.length <= max ? v : null;
}

async function assist(request«: Request»)«: Promise<Response>» {
  const url = process.env.FITT_FEEDBACK_AI_URL;
  const raw = await readBody(request, ASSIST_MAX_BYTES);
  if (!raw) return assistAnswer(413, { error: "payload_too_large" });
  let b«: { messages?: unknown; context?: unknown; files?: unknown } | null» = null;
  try {
    b = JSON.parse(new TextDecoder().decode(raw));
  } catch {
    b = null;
  }
  if (!b || typeof b !== "object" || !Array.isArray(b.messages) || b.messages.length > ASSIST_MESSAGES) {
    return assistAnswer(400, { error: "invalid_request" });
  }
  const messages«: AssistMessage[]» = [];
  for (const m of b.messages« as { role?: unknown; text?: unknown }[]») {
    const text = m ? assistText(m.text, ASSIST_TEXT) : null;
    if (!m || (m.role !== "user" && m.role !== "assistant") || text === null) return assistAnswer(400, { error: "invalid_request" });
    messages.push({ role: m.role, text });
  }
  const files«: AssistFile[]» = [];
  if (b.files !== undefined) {
    if (!Array.isArray(b.files) || b.files.length > ASSIST_FILES) return assistAnswer(400, { error: "invalid_request" });
    for (const f of b.files« as { name?: unknown; type?: unknown; data?: unknown }[]») {
      const name = f ? assistText(f.name, 200) : null;
      const ok = f && name && typeof f.type === "string" && ASSIST_FILE_TYPES.includes(f.type) &&
        typeof f.data === "string" && f.data.length % 4 === 0 && BASE64.test(f.data);
      if (!ok) return assistAnswer(400, { error: "invalid_request" });
      files.push({ name: name« as string», type: f.type« as string», data: f.data« as string» });
    }
  }
  // The widget asks with no messages, on every page, to learn whether to show the
  // assistant: answered plainly either way, so an app without one logs no error for it.
  if (!messages.length) return assistAnswer(200, { on: Boolean(url) });
  if (!url) return assistAnswer(503, { error: "not_open" });
  const c = (b.context && typeof b.context === "object" ? b.context : {})« as { page?: unknown; pins?: unknown }»;
  const context = {
    page: typeof c.page === "string" ? c.page.slice(0, 500) : "",
    pins: Array.isArray(c.pins)
      ? c.pins.filter((p«: unknown») => typeof p === "string").slice(0, 10).map((p«: string») => p.slice(0, 300))
      : [],
  };
  const headers«: Record<string, string>» = { "content-type": "application/json", accept: "application/json" };
  if (process.env.FITT_FEEDBACK_AI_KEY) headers.authorization = "Bearer " + process.env.FITT_FEEDBACK_AI_KEY;
  let res«: Response»;
  try {
    res = await fetch(url, {
      method: "POST",
      headers,
      body: JSON.stringify({ instructions: ASSIST_INSTRUCTIONS, messages, context, files, schema: ASSIST_SCHEMA }),
      redirect: "manual",
      signal: AbortSignal.timeout(ASSIST_TIMEOUT_MS),
    });
  } catch {
    return assistAnswer(502, { error: "ai_unreachable" });
  }
  if (!res.ok || res.type === "opaqueredirect") {
    await res.body?.cancel();
    return assistAnswer(502, { error: "ai_unreachable" });
  }
  const bad = assistAnswer(502, { error: "bad_answer" });
  const v = (await res.json().catch(() => null))« as { reply?: unknown; draft?: unknown } | null»;
  const reply = v && typeof v === "object" ? assistText(v.reply, ASSIST_REPLY) : null;
  if (!v || !reply || !reply.trim()) return bad;
  let draft = null;
  if (v.draft != null) {
    const d = v.draft« as { text?: unknown; category?: unknown; priority?: unknown }»;
    const text = typeof d === "object" ? assistText(d.text, ASSIST_DRAFT) : null;
    if (!text || !text.trim() || !ASSIST_KINDS.includes(d.category« as string») || !ASSIST_PRIORITIES.includes(d.priority« as string»)) return bad;
    draft = { text: text.trim(), category: d.category, priority: d.priority };
  }
  return assistAnswer(200, { reply: reply.trim(), draft });
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
// The company fittcore takes a report's reporter of at most this many characters.
const REPORTER_MAX = 400;
// The name of a cookie of the company fittcore's session (fittcoreCookies).
const FITTCORE_COOKIE = /^(?:__Secure-|__Host-)?paperclip-[A-Za-z0-9_-]+\.(?:session_token|session_data|dont_remember)$/;

«type Challenge = { id: string; secret: string; code: string; url: string; expires: number; interval: number; next: number };
type Held = { ref: string; cookie: string; sessionExpires: number; token: string; tokenExpires: number; name: string; challenge: Challenge | null };
type Me = { key: string; ref: string; until: number; email: string };
»
// One entry per app session: a token is never shared between users or sessions.
const held«: Map<string, Held>» = new Map();
// Until when the company fittcore counts as not offering the window: it is asked at
// most once per CLOSED_FOR_MS after it answers 404 widget_not_enabled, a 404 that is
// not the widget's own, or not at all. openUntil is the same for the other answer.
let closedUntil = 0;
let openUntil = 0;
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
// and logs its users in (PLATFORM_FITTCORE_ORIGIN) (DV, 17 Nov), or "" when they are not
// the same one or the app's server has not turned the window on with
// FITT_TICKET_WINDOW=1 (DV, 23 Nov). Then the window is not open: 📋 is not shown, no
// challenge starts, and nobody is asked anything.
function site() {
  if (process.env.FITT_TICKET_WINDOW !== "1") return "";
  const reports = originOf(process.env.FITT_FEEDBACK_URL);
  return reports && reports === originOf(process.env.PLATFORM_FITTCORE_ORIGIN) ? reports : "";
}

// isOpen says whether the site offers the window, asking it at most once per
// CLOSED_FOR_MS, as this server, with no one's token or cookie.
async function isOpen() {
  if (!site() || Date.now() < closedUntil) return false;
  if (Date.now() < openUntil) return true;
  const probe = await call("GET", "/tickets?status=open&limit=1", null);
  if (!probe) return false;
  await probe.text().catch(() => "");
  openUntil = Date.now() + CLOSED_FOR_MS;
  return true;
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
    const email = typeof user.email === "string" && user.email.length <= REPORTER_MAX ? user.email : "";
    const found«: Me» = { key: session.id + "\n" + user.id, ref: user.id, until: Number.isNaN(until) ? 0 : until, email };
    return found;
  } catch {
    closedUntil = Date.now() + CLOSED_FOR_MS;
    return undefined;
  }
}

// withSender is a 💬 report with the fittcore id of the signed-in app user in it, as
// app_user_ref and, when the app sends one, payload.app_user.ref: the company fittcore
// shows each user the tickets that are theirs by it (DV, 25 Nov 2026). Its reporter is
// the user's email: the company fittcore keeps it, never passing it on to DV, and an
// app's team-activity page counts each user's reports by it (Jidapa, 25 Nov 2026). The
// email goes only to the fittcore it came from, as site() is both. Where the window is
// on, who sent a report is only ever what the company fittcore says: a sender the
// browser names itself is taken out, and the route asks the company fittcore who the
// user is, as 📋 does; never their name. A report from nobody signed in, or while the
// fittcore cannot be asked, goes without an id and with reporter "". The company
// fittcore also asks for itself who sent a report and trusts no sender a report names
// (DV, 25 Nov 2026: OKR's reports go straight to DV's fittcore), so a report from a
// user it knows goes with that user's fittcore cookies (fittcoreCookies), from this
// server only, to the fittcore they came from; otherwise with no cookie. report null
// leaves the report as it came; false is a report that is not a JSON object, which is
// refused.
async function withSender(request«: Request», body«: Uint8Array»)«: Promise<{ report: string | null; cookie: string } | false>» {
  if (!site()) return { report: null, cookie: "" };
  let r«: unknown»;
  try {
    r = JSON.parse(new TextDecoder().decode(body));
  } catch {
    return false;
  }
  if (!r || typeof r !== "object" || Array.isArray(r)) return false;
  const report = r« as Record<string, unknown>»;
  const p = report.payload;
  const u = p && typeof p === "object" ? (p« as Record<string, unknown>»).app_user : undefined;
  const app = u && typeof u === "object" && !Array.isArray(u) ? u« as Record<string, unknown>» : null;
  let changed = "app_user_ref" in report || Boolean(app && "ref" in app);
  delete report.app_user_ref;
  if (app) delete app.ref;
  const cookie = request.headers.get("cookie") || "";
  const m = cookie && Date.now() >= closedUntil ? await who(cookie) : null;
  if (m) {
    report.app_user_ref = m.ref;
    if (app) app.ref = m.ref;
    changed = true;
  }
  const reporter = m ? m.email : "";
  if (report.reporter !== reporter) {
    report.reporter = reporter;
    changed = true;
  }
  return { report: changed ? JSON.stringify(report) : null, cookie: m ? fittcoreCookies(cookie) : "" };
}

// fittcoreCookies is the part of a request's cookie header that is the company
// fittcore's own session. Every fittcore names its cookies paperclip-<instance id>
// .session_token, .session_data and .dont_remember, with __Secure- or __Host- in front
// over https (DV, 25 Nov 2026). Never any other cookie the app's origin holds, not even
// an app's own better-auth session (better-auth.session_token).
function fittcoreCookies(header«: string») {
  return header
    .split(";")
    .map((c) => c.trim())
    .filter((c) => FITTCORE_COOKIE.test(c.slice(0, c.indexOf("=") < 0 ? 0 : c.indexOf("="))))
    .join("; ");
}

// bytesOf is text as the bytes a report is sent as.
function bytesOf(text«: string») {
  const bytes = new TextEncoder().encode(text);
  const out = new Uint8Array(bytes.byteLength);
  out.set(bytes);
  return out;
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
    // The widget's own 404s are widget_not_enabled and the not_found of a ticket's
    // pictures. Any other is a fittcore without the widget API (DV's own answers every
    // path there 404 not_found, 23 Nov): not open, like widget_not_enabled.
    if (code !== "not_found" || !/^\/tickets\/[^/]+\/images(\/|$)/.test(path)) {
      closedUntil = Date.now() + CLOSED_FOR_MS;
      openUntil = 0;
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
  // open is what the widget asks when a page loads: whether to show 📋 at all.
  if (op === "open") return (await isOpen()) ? json(200, { open: true }) : notOpen();
  if (op !== "state" && op !== "list" && op !== "images" && op !== "image") return json(404, { error: "unknown_op" });
  const h = await me(request);
  if (h instanceof Response) return h;

  if (op === "state") {
    if (h.token) return json(200, { ready: true, name: h.name });
    const c = h.challenge;
    if (c && c.expires > Date.now()) return json(200, { ready: false, pending: shown(c) });
    // Without a token, ask whether the site offers the window at all.
    if (!(await isOpen())) return notOpen();
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
