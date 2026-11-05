package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// withheldFailure replaces a failure message that holds a real value.
const withheldFailure = "The failure message held a forbidden value, so it is withheld here; see this build's trace on CRN"

// jobGate is what holds for a build of a project with a list of forbidden values: the
// list as pinned before the agent started, and a scanner reading all the build prints.
// It reports "released" only through beforeRelease. A project without a list has no gate.
type jobGate struct {
	list *buildstep.ForbiddenList
	scan *buildstep.ForbiddenScanner
	// base is the branch's head before the agent ran; "" for a new build.
	base string
	// accepted is set when the candidate gate passed on the registry's digest.
	accepted bool
}

// openGate pins the project's list. No list means no gate; a list that is not as agreed
// is an error, so the build stops before the agent costs anything.
func (m *manager) openGate(job *domain.Job) (*jobGate, error) {
	if m.forbiddenValuesDir == "" {
		return nil, nil
	}
	list, err := buildstep.PinForbiddenList(filepath.Join(m.forbiddenValuesDir, job.ProjectID.String()+".txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gate := &jobGate{list: list, scan: list.NewScanner()}
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	if m.gates == nil {
		m.gates = map[uuid.UUID]*jobGate{}
	}
	m.gates[job.ID] = gate
	return gate, nil
}

func (m *manager) closeGate(jobID uuid.UUID) {
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	delete(m.gates, jobID)
}

func (m *manager) gateOf(jobID uuid.UUID) *jobGate {
	m.gateMu.RLock()
	defer m.gateMu.RUnlock()
	return m.gates[jobID]
}

// readCommands has the gate read what every command of this build prints, and lists in
// the trace each layer docker push mounted, for the build summary.
func (m *manager) readCommands(ctx context.Context, jobID uuid.UUID, gate *jobGate) context.Context {
	return buildstep.WithCommandOutput(ctx, func(name string, output []byte) {
		gate.scan.Scan(name, output)
		if name != "docker push" {
			return
		}
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, "Mounted from") {
				m.publishText(jobID, "docker push: "+strings.TrimSpace(line))
			}
		}
	})
}

func (m *manager) publishText(jobID uuid.UUID, text string) {
	m.publish(jobID, domain.BuildEventMsg{Kind: domain.WSAssistantText, Text: text, JobID: jobID.String(), Timestamp: time.Now().UTC()})
}

// check is whether the build may go on: the list has not moved and nothing printed so
// far holds a real value.
func (g *jobGate) check() error {
	if err := g.list.CheckUnchanged(); err != nil {
		return err
	}
	return g.scan.Problem()
}

// beforeCommit stops a build before it pushes anything when it could not pass anyway. A
// listed project must stay external-tls-only and a Next app, or the gate never runs on
// it; an external-tls-only app without a list has no gate to pass.
func (m *manager) beforeCommit(workDir string, gate *jobGate) error {
	if !m.buildImage {
		return nil
	}
	mode, err := buildstep.ReadDBMode(workDir)
	if err != nil {
		return err
	}
	if gate == nil {
		if mode == buildstep.DBModeExternalTLS {
			return errors.New("candidate acceptance: no list of forbidden values for this project")
		}
		return nil
	}
	if mode != buildstep.DBModeExternalTLS {
		return fmt.Errorf("candidate acceptance: this project holds a list of forbidden values, so it must be %s", buildstep.DBModeExternalTLS)
	}
	if !buildstep.IsNextApp(workDir) {
		return errors.New("candidate acceptance: this project holds a list of forbidden values, so it must be an app the image pipeline builds")
	}
	return gate.check()
}

// afterPush confirms the remote holds the commit, and reads every object the build
// added: a value committed and removed again is not in the final tree, but it was pushed.
func (g *jobGate) afterPush(ctx context.Context, workDir, remote, branch, commit string, log *slog.Logger) error {
	head, err := buildstep.RemoteHead(ctx, workDir, remote, branch, log)
	if err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
	}
	if head != commit {
		return fmt.Errorf("candidate acceptance: the remote's %s is on %s, the build pushed %s", branch, head, commit)
	}
	if err := buildstep.ScanCommitHistory(ctx, workDir, g.base, commit, g.scan); err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
	}
	return g.check()
}

// beforeRelease is the last gate before "released": the candidate passed, the list has
// not moved, and nothing the build printed, nor the callback about to go, holds a real
// value. Placeholders are not looked for here: the request names them by design.
func (g *jobGate) beforeRelease(callback []byte) error {
	if !g.accepted {
		return errors.New("candidate acceptance: this project holds a list of forbidden values, and no candidate of this build was accepted")
	}
	g.scan.Scan("the released callback", callback)
	return g.check()
}

// safeMessage is msg, unless it holds a real value: a failure message leaves CRN.
func (m *manager) safeMessage(jobID uuid.UUID, msg string) string {
	if gate := m.gateOf(jobID); gate != nil && gate.list.Holds([]byte(msg)) {
		return withheldFailure
	}
	return msg
}

// readEvent has the gate read one live event, as the trace will store it.
func (m *manager) readEvent(jobID uuid.UUID, msg domain.BuildEventMsg) {
	gate := m.gateOf(jobID)
	if gate == nil {
		return
	}
	if raw, err := json.Marshal(msg); err == nil {
		gate.scan.Scan("the live events", raw)
	}
}

// --- CRN's own log lines, while a gated build runs -------------------------------------------

// gateLogHandler writes every record through to the real handler, and has each open gate
// read it as well, so a value that reached a log line of CRN counts too.
type gateLogHandler struct {
	real, text slog.Handler
}

func (h gateLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.real.Enabled(ctx, level)
}

func (h gateLogHandler) Handle(ctx context.Context, r slog.Record) error {
	_ = h.text.Handle(ctx, r.Clone())
	return h.real.Handle(ctx, r)
}

func (h gateLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return gateLogHandler{real: h.real.WithAttrs(attrs), text: h.text.WithAttrs(attrs)}
}

func (h gateLogHandler) WithGroup(name string) slog.Handler {
	return gateLogHandler{real: h.real.WithGroup(name), text: h.text.WithGroup(name)}
}

// gateLogWriter hands one formatted record to every open gate.
type gateLogWriter struct{ m *manager }

func (w gateLogWriter) Write(p []byte) (int, error) {
	w.m.gateMu.RLock()
	defer w.m.gateMu.RUnlock()
	for _, gate := range w.m.gates {
		gate.scan.Scan("CRN's log", p)
	}
	return len(p), nil
}

// readLogs makes the manager's logger pass every record by the open gates.
func (m *manager) readLogs() {
	text := slog.NewTextHandler(gateLogWriter{m: m}, &slog.HandlerOptions{Level: slog.LevelDebug})
	m.logger = slog.New(gateLogHandler{real: m.logger.Handler(), text: text})
}
