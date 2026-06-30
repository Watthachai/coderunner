// Package claude is the Claude Code adapter. It implements domain.ClaudeRunner
// by spawning `claude --output-format stream-json` and decoding each line into
// a domain.ClaudeEvent (CRN-architecture.md §7 — THE SPIKE).
//
// OWNED BY: the 'claude' implementer.
//
// What this builds (NewRunner's signature is the contract main.go wires against
// and MUST NOT change):
//
//   - NewRunner(binPath, projectsDir string, logger *slog.Logger) domain.ClaudeRunner
//
//   - Run(ctx, spec, emit): builds the command
//
//     claude -p <spec.Prompt> --output-format stream-json --verbose \
//     --dangerously-skip-permissions [--resume <spec.ResumeSessionID>]
//
//     with cmd.Dir = spec.WorkDir (falling back to projectsDir/<project_id>).
//     stdout is wired to a *bufio.Scanner with an enlarged buffer (a single
//     stream-json line can far exceed bufio's 64 KiB default). For EACH line we
//     json.Unmarshal into a private raw wire struct, translate it into a
//     domain.ClaudeEvent (preserving the original bytes in .Raw), and call
//     emit(ev) sequentially. The loop stops on ctx.Done(), a scanner error, or
//     the terminal "result" line. On context cancellation the whole process
//     GROUP is killed so no orphan children survive. A non-zero exit yields a
//     non-nil error; RunResult is populated from the terminal result line.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// maxLineBytes is the bufio.Scanner token cap. Claude Code can emit a single
// JSON line carrying a whole file's contents (e.g. a Write tool_use), which
// blows past bufio's 64 KiB default; 16 MiB gives generous headroom.
const maxLineBytes = 16 << 20 // 16 MiB

// runner is the unexported concrete ClaudeRunner. Constructors return the
// domain interface, never this type (keeps the contract surface minimal).
type runner struct {
	binPath     string
	projectsDir string
	logger      *slog.Logger
}

// compile-time guard: runner must satisfy the domain port.
var _ domain.ClaudeRunner = (*runner)(nil)

// NewRunner returns a domain.ClaudeRunner backed by the `claude` CLI at binPath.
// Signature is fixed by cmd/server/main.go.
func NewRunner(binPath, projectsDir string, logger *slog.Logger) domain.ClaudeRunner {
	if logger == nil {
		// Fail-fast convention: a nil logger is a wiring bug, not a runtime
		// condition to paper over. main.go always passes a real logger.
		logger = slog.Default()
	}
	return &runner{
		binPath:     binPath,
		projectsDir: projectsDir,
		logger:      logger,
	}
}

// Run launches Claude Code for one job and streams decoded events to emit.
//
// emit is invoked sequentially (never concurrently). The first non-nil error
// returned by emit aborts the run, kills the process, and is propagated.
func (r *runner) Run(ctx context.Context, spec domain.RunSpec, emit func(domain.ClaudeEvent) error) (domain.RunResult, error) {
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = filepath.Join(r.projectsDir, spec.ProjectID.String())
	}

	args := []string{
		"-p", spec.Prompt,
		"--output-format", "stream-json",
		"--verbose", // required for stream-json to emit intermediate events
		"--dangerously-skip-permissions",
	}
	if spec.ResumeSessionID != "" {
		args = append(args, "--resume", spec.ResumeSessionID)
	}

	log := r.logger.With(
		"job_id", spec.JobID,
		"project_id", spec.ProjectID,
		"workdir", workDir,
		"resume", spec.ResumeSessionID != "",
	)

	cmd := exec.CommandContext(ctx, r.binPath, args...)
	cmd.Dir = workDir
	// New process group so we can signal the whole tree on cancel and never
	// leak orphan children (claude spawns subprocesses). Setpgid puts the child
	// in its own group whose pgid == child pid.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// exec.CommandContext kills only the immediate process on ctx cancel; we
	// override Cancel to signal the entire group instead.
	cmd.Cancel = func() error {
		return killGroup(cmd, syscall.SIGKILL)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return domain.RunResult{}, fmt.Errorf("claude: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return domain.RunResult{}, fmt.Errorf("claude: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return domain.RunResult{}, fmt.Errorf("claude: start %q: %w", r.binPath, err)
	}
	log.Info("claude started", "pid", cmd.Process.Pid)

	// Drain stderr concurrently into the logger so a noisy/blocking stderr can
	// never deadlock the child (its stderr pipe filling up would stall it).
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		drainStderr(stderr, log)
	}()

	// Scan stdout, translate, and emit. result captures the terminal line.
	var result domain.RunResult
	var sawResult bool
	scanErr := scanStream(stdout, log, func(ev domain.ClaudeEvent) error {
		if ev.IsTerminal() {
			sawResult = true
			result = domain.RunResult{
				SessionID: ev.SessionID,
				CostUSD:   ev.CostUSD,
				// "success" subtype (or empty) => success; "error" => failure.
				Success: ev.Subtype == "" || ev.Subtype == "success",
			}
		}
		return emit(ev)
	})

	// Ensure stderr drain finishes before Wait so the goroutine sees EOF.
	wg.Wait()

	// Wait reaps the process. With our Cancel hook a cancelled ctx surfaces as a
	// signal-kill exit; we report ctx.Err() in that case for clean semantics.
	waitErr := cmd.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		log.Info("claude cancelled", "err", ctxErr)
		return result, fmt.Errorf("claude: cancelled: %w", ctxErr)
	}

	// An emit/scanner error takes precedence over exit status: it means the
	// caller asked us to stop, so the run is aborted regardless of exit code.
	if scanErr != nil {
		// Kill the group to avoid leaking the child if it is still running.
		_ = killGroup(cmd, syscall.SIGKILL)
		return result, fmt.Errorf("claude: stream: %w", scanErr)
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return result, fmt.Errorf("claude: exited non-zero (code %d): %w", exitErr.ExitCode(), waitErr)
		}
		return result, fmt.Errorf("claude: wait: %w", waitErr)
	}

	if !sawResult {
		// Process exited 0 but never produced a terminal result line. Treat as
		// a failed run so the caller doesn't record a phantom success.
		log.Warn("claude exited without a result line")
		return result, errors.New("claude: process ended without a terminal result event")
	}

	log.Info("claude finished", "success", result.Success, "cost_usd", result.CostUSD, "session_id", result.SessionID)
	return result, nil
}

// scanStream reads NDJSON lines from r, decodes each into a domain.ClaudeEvent,
// and calls emit in order. Garbage / non-JSON lines are skipped with a debug
// log. It returns the first emit error or a scanner read error; io.EOF is
// normal termination and is reported as nil. Scanning also stops after the
// terminal result line is emitted.
func scanStream(r io.Reader, log *slog.Logger, emit func(domain.ClaudeEvent) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)

	for sc.Scan() {
		line := sc.Bytes()
		// Trim and skip blank lines (CLIs sometimes emit keep-alive newlines).
		if len(line) == 0 || len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		ev, ok := decodeLine(line, log)
		if !ok {
			continue
		}

		if err := emit(ev); err != nil {
			return err
		}

		if ev.IsTerminal() {
			// Terminal result line reached; stop scanning. We do NOT drain the
			// rest of stdout — the process is expected to exit right after.
			return nil
		}
	}

	if err := sc.Err(); err != nil {
		// bufio.ErrTooLong et al. surface here; a closed pipe on kill shows up
		// as a read error too, but in that path ctx.Err()/waitErr dominate.
		return fmt.Errorf("scan: %w", err)
	}
	return nil
}

// rawLine is the subset of the `claude --output-format stream-json` wire shape
// CRN decodes. The real CLI nests assistant text and tool calls under
// message.content[] and reports cost/session on the terminal "result" line;
// we translate that nested shape into the flat domain.ClaudeEvent. Unknown
// fields are ignored; unknown event types pass through with Type preserved.
type rawLine struct {
	Type string `json:"type"`

	// Terminal "result" line fields. The CLI uses total_cost_usd; we also
	// accept cost_usd as a fallback for forward/backward compatibility.
	Subtype      string  `json:"subtype"`
	SessionID    string  `json:"session_id"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	CostUSD      float64 `json:"cost_usd"`

	// "assistant" lines carry a nested anthropic message envelope.
	Message *rawMessage `json:"message"`
}

type rawMessage struct {
	Content []rawContentBlock `json:"content"`
}

// rawContentBlock is one block inside an assistant message: either a text block
// or a tool_use block.
type rawContentBlock struct {
	Type  string          `json:"type"` // "text" | "tool_use"
	Text  string          `json:"text"`
	Name  string          `json:"name"`  // tool name (tool_use)
	Input json.RawMessage `json:"input"` // tool input (tool_use)
}

// decodeLine turns one raw NDJSON line into a domain.ClaudeEvent. ok is false
// for non-JSON / unusable lines (logged at debug and skipped by the caller).
// Raw always carries a copy of the original bytes on success.
func decodeLine(line []byte, log *slog.Logger) (domain.ClaudeEvent, bool) {
	var raw rawLine
	if err := json.Unmarshal(line, &raw); err != nil {
		// Partial or garbage line (e.g. a stray log statement on stdout). Skip.
		log.Debug("skipping non-JSON stream line", "err", err, "bytes", len(line))
		return domain.ClaudeEvent{}, false
	}
	if raw.Type == "" {
		log.Debug("skipping stream line with no type", "bytes", len(line))
		return domain.ClaudeEvent{}, false
	}

	// json.Unmarshal aliases into the caller's buffer; copy so the event keeps
	// stable bytes after the scanner reuses its buffer on the next Scan().
	rawCopy := make(json.RawMessage, len(line))
	copy(rawCopy, line)

	ev := domain.ClaudeEvent{
		Type: domain.ClaudeEventType(raw.Type),
		Raw:  rawCopy,
	}

	switch domain.ClaudeEventType(raw.Type) {
	case domain.ClaudeResult:
		ev.Subtype = raw.Subtype
		ev.SessionID = raw.SessionID
		ev.CostUSD = raw.TotalCostUSD
		if ev.CostUSD == 0 {
			ev.CostUSD = raw.CostUSD
		}

	case domain.ClaudeAssistant:
		// Flatten text blocks and surface the first tool_use, if any. The CLI
		// can interleave text + tool_use within one assistant turn.
		if raw.Message != nil {
			var sb strings.Builder
			for i := range raw.Message.Content {
				b := raw.Message.Content[i]
				switch b.Type {
				case "text":
					sb.WriteString(b.Text)
				case "tool_use":
					if ev.ToolName == "" {
						ev.ToolName = b.Name
						ev.ToolInput = b.Input
					}
				}
			}
			ev.Text = sb.String()
		}

	default:
		// system / tool_result / unknown: Type + Raw are enough; the GUI can
		// render Raw. No typed fields to populate.
	}

	return ev, true
}

// drainStderr copies the child's stderr into the logger line-by-line so it is
// never lost and never blocks the child by filling its pipe buffer.
func drainStderr(r io.Reader, log *slog.Logger) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			log.Warn("claude stderr", "line", line)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		log.Debug("stderr scan ended", "err", err)
	}
}

// killGroup signals the process group led by cmd (pgid == child pid, established
// via Setpgid). Negating the pid targets the whole group so subprocesses spawned
// by claude are reaped too. It is a no-op if the process never started or has
// already exited.
func killGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	if err := syscall.Kill(-pgid, sig); err != nil {
		// ESRCH => already gone; not an error worth surfacing.
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		// Fall back to killing just the leader if the group signal failed.
		_ = cmd.Process.Signal(sig)
		return err
	}
	return nil
}
