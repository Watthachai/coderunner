"use client";

// An interactive xterm.js terminal bound to one project's backend PTY.
//
// Wire protocol (must match the Go handler at
// GET /internal/projects/{id}/terminal — no auth, InsecureSkipVerify):
//   server -> client: raw PTY output as BINARY frames -> written to xterm
//   client -> server: TEXT frames, each a JSON object:
//     { "type": "input",  "data": "<utf-8 keystrokes>" }
//     { "type": "resize", "cols": <int>, "rows": <int> }

import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { terminalWsUrl } from "../lib/config";

type ConnState = "connecting" | "connected" | "closed";

const CONN_LABEL: Record<ConnState, string> = {
  connecting: "connecting",
  connected: "connected",
  closed: "closed",
};

// Terminal theme — the midnight-studio tokens from app/globals.css.
const TERM_THEME = {
  background: "#0c0c0e",
  foreground: "#ededed",
  cursor: "#64cefb",
  cursorAccent: "#0c0c0e",
  selectionBackground: "rgba(100, 206, 251, 0.25)",
  black: "#0a0a0a",
  brightBlack: "#6b7280",
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
  white: "#ededed",
  brightWhite: "#ffffff",
} as const;

export function ProjectTerminal({ projectId }: { projectId: string }) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [state, setState] = useState<ConnState>("connecting");

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const term = new Terminal({
      convertEol: false,
      cursorBlink: true,
      fontFamily:
        'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
      fontSize: 13,
      theme: { ...TERM_THEME },
    });
    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(container);

    // Fit safely: FitAddon throws if the container has no measurable size yet.
    const fit = (): void => {
      try {
        fitAddon.fit();
      } catch {
        // container not laid out yet; a later resize event will retry.
      }
    };
    fit();

    const ws = new WebSocket(terminalWsUrl(projectId));
    ws.binaryType = "arraybuffer";

    // Send the current terminal size to the PTY (only when the socket is open).
    const sendResize = (): void => {
      if (ws.readyState !== WebSocket.OPEN) return;
      ws.send(
        JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }),
      );
    };

    ws.onopen = () => {
      setState("connected");
      fit();
      sendResize();
      term.focus();
    };

    ws.onmessage = (ev: MessageEvent) => {
      const data = ev.data;
      if (data instanceof ArrayBuffer) {
        term.write(new Uint8Array(data));
      } else if (data instanceof Blob) {
        // Fallback path if binaryType negotiation ever yields a Blob.
        void data.arrayBuffer().then((buf) => term.write(new Uint8Array(buf)));
      } else if (typeof data === "string") {
        term.write(data);
      }
    };

    ws.onclose = () => setState("closed");
    ws.onerror = () => setState("closed");

    // Keystrokes -> PTY.
    const onDataDisposable = term.onData((d: string) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data: d }));
      }
    });

    // Re-fit + report new size when the panel or window resizes.
    const handleResize = (): void => {
      fit();
      sendResize();
    };
    const ro = new ResizeObserver(handleResize);
    ro.observe(container);
    window.addEventListener("resize", handleResize);

    return () => {
      window.removeEventListener("resize", handleResize);
      ro.disconnect();
      onDataDisposable.dispose();
      // Prevent onclose from firing a setState after unmount.
      ws.onclose = null;
      ws.onerror = null;
      ws.onmessage = null;
      ws.onopen = null;
      if (
        ws.readyState === WebSocket.OPEN ||
        ws.readyState === WebSocket.CONNECTING
      ) {
        ws.close();
      }
      term.dispose();
    };
  }, [projectId]);

  return (
    <div className="term-shell">
      <div className="term-bar">
        <span className="term-bar-title">pty</span>
        <span className={`term-status term-status--${state}`}>
          <span className="term-status-dot" />
          {CONN_LABEL[state]}
        </span>
      </div>
      <div className="term-mount" ref={containerRef} />
    </div>
  );
}
