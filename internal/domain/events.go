package domain

import (
	"encoding/json"
	"time"
)

// --- Raw Claude Code stream-json (CRN-architecture.md §7) ---
//
// `claude --output-format stream-json` emits one JSON object per line. The
// shapes below are the subset CRN cares about. Unknown fields are ignored;
// unknown event types are passed through untouched so the GUI can still show
// raw output. The claude package owns parsing each line into a ClaudeEvent.

// ClaudeEventType enumerates the `type` field of a raw stream-json line.
type ClaudeEventType string

const (
	ClaudeAssistant  ClaudeEventType = "assistant"
	ClaudeToolUse    ClaudeEventType = "tool_use"
	ClaudeToolResult ClaudeEventType = "tool_result"
	ClaudeResult     ClaudeEventType = "result" // terminal line: cost + session_id
	ClaudeSystem     ClaudeEventType = "system"
)

// ClaudeEvent is one decoded line from the Claude Code stream. Raw holds the
// original bytes so nothing is lost in translation. Typed fields are populated
// best-effort by the claude package depending on Type.
type ClaudeEvent struct {
	Type ClaudeEventType `json:"type"`

	// Populated for Type == ClaudeToolUse.
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`

	// Populated for Type == ClaudeAssistant (concatenated text blocks).
	Text string `json:"text,omitempty"`

	// Populated for Type == ClaudeResult.
	Subtype   string  `json:"subtype,omitempty"` // e.g. "success" | "error"
	CostUSD   float64 `json:"cost_usd,omitempty"`
	SessionID string  `json:"session_id,omitempty"`

	// Raw is the unmodified line as received from the CLI.
	Raw json.RawMessage `json:"-"`
}

// IsTerminal reports whether this event marks the end of a Claude Code run.
func (e ClaudeEvent) IsTerminal() bool {
	return e.Type == ClaudeResult
}

// --- Normalized WebSocket event (what the Dashboard consumes) ---

// WSEventKind is the normalized event family pushed to the Next.js dashboard.
type WSEventKind string

const (
	WSAssistantText WSEventKind = "assistant_text"
	WSToolCall      WSEventKind = "tool_call"
	WSToolResult    WSEventKind = "tool_result"
	WSBuildPhase    WSEventKind = "build_phase" // lifecycle: building/docker/push/done
	WSResult        WSEventKind = "result"
	WSError         WSEventKind = "error"
)

// BuildEventMsg is the normalized, GUI-friendly event CRN streams over the
// WebSocket (CRN-architecture.md §7 — the translated form). The store/notifier
// persistence type is BuildEvent; this is the wire/live type.
type BuildEventMsg struct {
	Kind      WSEventKind `json:"event"`
	Tool      string      `json:"tool,omitempty"`
	File      string      `json:"file,omitempty"`
	Text      string      `json:"text,omitempty"`
	Phase     string      `json:"phase,omitempty"`
	CostUSD   float64     `json:"cost_usd,omitempty"`
	SessionID string      `json:"session_id,omitempty"`
	JobID     string      `json:"job_id,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}
