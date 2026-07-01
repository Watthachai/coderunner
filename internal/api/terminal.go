// Interactive per-project terminal over WebSocket. handleTerminalWS spawns an
// OS shell in a PTY, with its cwd set to the project's working dir, and bridges
// it to the browser: raw PTY output flows out as binary frames; JSON control
// frames (input / resize) flow in.
//
// SECURITY: this endpoint is a REMOTE SHELL on the CRN host with NO auth (it
// mirrors the no-auth /internal log WS: InsecureSkipVerify origin, trusted
// network). That is fine for local/trusted dev, but in production it MUST be
// locked down — put it behind authentication and a network policy so an
// untrusted client can never open a shell on the build host.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// terminalMsg is a client -> server control frame (sent as a TEXT frame). type
// is "input" (data carries UTF-8 keystrokes to write to the pty) or "resize"
// (cols/rows carry the new terminal size). Unknown types are ignored.
type terminalMsg struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// handleTerminalWS upgrades the connection and runs an interactive shell in a
// PTY rooted at the project's working dir. See the package/file doc for the
// wire protocol and the security caveat.
func (s *server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "invalid project id")
		return
	}

	// The shell needs a valid cwd even before any build has materialized files,
	// so ensure the per-project workdir exists.
	workDir := filepath.Join(s.projectsDir, projectID.String())
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		s.logger.Error("terminal workdir create failed", "err", err, "project_id", projectID)
		s.writeError(w, r, http.StatusInternalServerError, "could not prepare workdir")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Same rationale as the log WS: the dashboard is served from a different
		// origin/port than the API, so skip the same-origin check in dev.
		// TODO(crn): set OriginPatterns to the known dashboard origin(s) in prod.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote an HTTP error response.
		s.logger.Warn("terminal ws accept failed", "err", err, "project_id", projectID)
		return
	}
	defer conn.CloseNow()

	// ctx is cancelled when either goroutine hits an error/EOF; that unblocks the
	// other side and triggers the deferred process-group kill below.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	shell := s.pickShell()
	cmd := exec.CommandContext(ctx, shell)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// NOTE: do NOT set Setpgid here — pty.Start sets Setsid+Setctty to make the
	// PTY the controlling terminal, and Setsid together with Setpgid makes
	// fork/exec fail with EPERM ("operation not permitted"). Setsid already puts
	// the shell in its own session and process group (pgid == pid), so the
	// group-kill below still reaches the shell and all its children.

	ptmx, err := pty.Start(cmd)
	if err != nil {
		s.logger.Error("terminal pty start failed", "err", err, "project_id", projectID, "shell", shell)
		_ = conn.Close(websocket.StatusInternalError, "pty start failed")
		return
	}
	s.logger.Info("terminal opened", "project_id", projectID, "shell", shell)
	defer func() {
		_ = ptmx.Close()
		// Kill the whole process group so the shell and its children die when the
		// socket closes. pgid == pid because pty.Start's Setsid makes the shell a
		// session/group leader.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
		s.logger.Info("terminal closed", "project_id", projectID)
	}()

	// Goroutine A: pty -> ws. Read PTY output and forward each chunk as a binary
	// frame. Exits (cancelling ctx) on read error/EOF — e.g. the shell exited.
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Main loop: ws -> pty. Decode TEXT control frames and apply them.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			// Client gone or ctx cancelled by goroutine A; the deferred kill runs.
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg terminalMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // ignore malformed frames
		}
		switch msg.Type {
		case "input":
			if _, err := ptmx.WriteString(msg.Data); err != nil {
				return
			}
		case "resize":
			_ = pty.Setsize(ptmx, &pty.Winsize{
				Rows: uint16(msg.Rows),
				Cols: uint16(msg.Cols),
			})
		default:
			// ignore unknown types
		}
	}
}

// pickShell resolves the shell for the terminal: the operator override
// (CRN_TERMINAL_SHELL), then $SHELL, then /bin/zsh, /bin/bash, /bin/sh — the
// first candidate that exists at runtime. /bin/sh is the last-resort default
// even if it does not stat (POSIX guarantees it on any sane host).
func (s *server) pickShell() string {
	candidates := []string{s.terminalShell, os.Getenv("SHELL"), "/bin/zsh", "/bin/bash", "/bin/sh"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "/bin/sh"
}
