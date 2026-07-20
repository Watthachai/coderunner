"use client";

// CLAUDE STREAM — the mission-control hero surface. One live xterm.js terminal
// per in-flight build that renders the streamed BuildEventMsg feed as a real,
// ANSI-colored CLI: tool calls, assistant reasoning, results, phases. Reads
// like ground-truth telemetry coming off the vehicle, not a UI list.
//
// It shares the same WS feed as the rest of the console (FeedEvent[] from
// useJobStream, lifted by the parent) and only writes DELTAS: every render it
// appends events whose stable _id is greater than the last one written, so the
// terminal stays cheap no matter how fast the build streams.

import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import type { FeedEvent } from "../lib/types";
import { lineDiff } from "../lib/linediff";

// The stream well is a touch darker/bluer than the panel so the terminal reads
// as a separate instrument surface set into the console.
const TERM_THEME = {
  background: "#0a0c10",
  foreground: "#d6deeb",
  cursor: "#64cefb",
  cursorAccent: "#0a0c10",
  selectionBackground: "rgba(100, 206, 251, 0.25)",
  black: "#0a0c10",
  brightBlack: "#5f6b7a",
  red: "#f87171",
  brightRed: "#f87171",
  green: "#54d98c",
  brightGreen: "#54d98c",
  yellow: "#f5b545",
  brightYellow: "#f5b545",
  blue: "#64cefb",
  brightBlue: "#64cefb",
  magenta: "#c792ea",
  brightMagenta: "#c792ea",
  cyan: "#64cefb",
  brightCyan: "#89ddff",
  white: "#d6deeb",
  brightWhite: "#ffffff",
} as const;

// --- ANSI ---------------------------------------------------------------
const RESET = "\x1b[0m";
const DIM = "\x1b[38;5;250m"; // reasoning text
const GRAY = "\x1b[38;5;244m"; // phase markers
const CYAN_BOLD = "\x1b[1;36m"; // tool-call arrow
const BLUE = "\x1b[38;5;39m"; // tool-call detail
const GREEN = "\x1b[32m"; // result tick
const GREEN_BOLD = "\x1b[1;32m"; // build complete
const RED_BOLD = "\x1b[1;31m"; // error
const DIFF_ADD = "\x1b[38;5;42m"; // + added lines (green)
const DIFF_DEL = "\x1b[38;5;203m"; // - removed lines (red)
const DIFF_CTX = "\x1b[38;5;244m"; // unchanged context (gray)
const MAX_DIFF_LINES = 40;

// A Write/Edit's code as a colored unified diff, capped so a big file doesn't
// flood the terminal. Write => all-green additions; Edit => green/red hunks.
function renderDiff(before: string, after: string): string {
  const lines = lineDiff(before, after);
  let out = "";
  for (const ln of lines.slice(0, MAX_DIFF_LINES)) {
    const color =
      ln.kind === "add" ? DIFF_ADD : ln.kind === "del" ? DIFF_DEL : DIFF_CTX;
    const sign = ln.kind === "add" ? "+" : ln.kind === "del" ? "-" : " ";
    out += `${color}  ${sign} ${ln.text.replace(/\t/g, "  ")}${RESET}\r\n`;
  }
  if (lines.length > MAX_DIFF_LINES) {
    out += `${DIFF_CTX}  … ${lines.length - MAX_DIFF_LINES} more lines${RESET}\r\n`;
  }
  return out;
}

// Collapse whitespace so multi-line assistant text stays on one flowing line
// (xterm soft-wraps it to the terminal width).
function oneLine(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

// Human-meaningful detail for a tool call. Bash reads as a shell prompt; other
// tools show their target file when present, else the bare tool name.
function toolDetail(e: FeedEvent): string {
  if (e.tool === "Bash") {
    const cmd = oneLine(e.text ?? "");
    return cmd ? `$ ${cmd}` : "$";
  }
  if (e.file) return e.file;
  const t = oneLine(e.text ?? "");
  return t || "";
}

// Format one event as an ANSI line (trailing newline included) for term.write.
// Returns null for events with nothing worth printing (e.g. empty text).
function render(e: FeedEvent): string | null {
  switch (e.event) {
    case "tool_call": {
      const tool = e.tool ?? "tool";
      const detail = toolDetail(e);
      const tail = detail ? ` ${BLUE}${detail}${RESET}` : "";
      let out = `${CYAN_BOLD}▸ ${tool}${RESET}${tail}\r\n`;
      if (e.before || e.after) out += renderDiff(e.before ?? "", e.after ?? "");
      return out;
    }
    case "assistant_text": {
      const t = oneLine(e.text ?? "");
      if (!t) return null;
      return `${DIM}${t}${RESET}\r\n`;
    }
    case "tool_result": {
      const detail = e.file ? oneLine(e.file) : "";
      const tail = detail ? ` ${detail}` : "";
      return `${GREEN}  ✓${RESET}${GRAY}${tail}${RESET}\r\n`;
    }
    case "build_phase": {
      const phase = e.phase ?? "phase";
      return `${GRAY}◆ ${phase}${RESET}\r\n`;
    }
    case "result": {
      const cost =
        typeof e.cost_usd === "number" ? ` · $${e.cost_usd.toFixed(4)}` : "";
      return `${GREEN_BOLD}● build complete${RESET}${GRAY}${cost}${RESET}\r\n`;
    }
    case "error": {
      const t = oneLine(e.text ?? "error");
      return `${RED_BOLD}✗ ${t}${RESET}\r\n`;
    }
    default:
      return null;
  }
}

type StreamPhase = "connecting" | "live" | "done";

export function BuildConsole({
  events,
  sessionId,
  phase,
}: {
  events: FeedEvent[];
  sessionId: string | null;
  phase: StreamPhase;
}) {
  const mountRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  // Highest FeedEvent._id already written to the terminal. -1 => nothing yet.
  const lastIdRef = useRef<number>(-1);

  // Create the terminal once. It persists for the life of the build; deltas are
  // written by the effect below. Cleanup disposes xterm + observers on unmount.
  useEffect(() => {
    const mount = mountRef.current;
    if (!mount) return;

    const term = new Terminal({
      convertEol: false,
      cursorBlink: true,
      disableStdin: true,
      scrollback: 5000,
      fontFamily:
        'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
      fontSize: 13,
      lineHeight: 1.35,
      theme: { ...TERM_THEME },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(mount);
    termRef.current = term;
    fitRef.current = fit;

    // FitAddon throws if the host has no measurable size yet; a later resize
    // observation retries.
    const doFit = (): void => {
      try {
        fit.fit();
      } catch {
        /* not laid out yet */
      }
    };
    doFit();

    const ro = new ResizeObserver(doFit);
    ro.observe(mount);
    window.addEventListener("resize", doFit);

    return () => {
      window.removeEventListener("resize", doFit);
      ro.disconnect();
      termRef.current = null;
      fitRef.current = null;
      lastIdRef.current = -1;
      term.dispose();
    };
  }, []);

  // Write deltas. Events arrive append-only with monotonically increasing _id,
  // so anything past lastIdRef is new. A reset (clear/reconnect) rewinds _id to
  // 0; detect that and clear the terminal so we don't miss the fresh head.
  useEffect(() => {
    const term = termRef.current;
    if (!term) return;

    if (events.length > 0 && events[0]._id <= lastIdRef.current) {
      // Feed was reset (new build target). Start the surface over.
      term.clear();
      lastIdRef.current = -1;
    }

    let wrote = false;
    for (const e of events) {
      if (e._id <= lastIdRef.current) continue;
      lastIdRef.current = e._id;
      const line = render(e);
      if (line) {
        term.write(line);
        wrote = true;
      }
    }
    if (wrote) term.scrollToBottom();
  }, [events]);

  const sid = sessionId ? sessionId.slice(0, 8) : null;

  return (
    <div className="stream">
      <div className="stream-bar">
        <span className="stream-title">
          <span className="stream-glyph">▮</span> STREAM
        </span>
        {sid ? <span className="stream-sid">{sid}</span> : null}
        <span className={`stream-live stream-live--${phase}`}>
          <span className="stream-live-dot" />
          {phase === "live"
            ? "LIVE"
            : phase === "connecting"
              ? "LINKING"
              : "CLOSED"}
        </span>
      </div>
      <div className="stream-mount" ref={mountRef} />
    </div>
  );
}
