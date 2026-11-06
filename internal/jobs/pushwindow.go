package jobs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// The push window is the only time a listed project's build holds a registry credential.
// An agent runs as CRN's OS user, so a credential in the Keychain, the environment or a file
// is one it can use. So the token does not exist while an agent runs:
//
//  1. the agent's process group dies with it (domain.RunSpec.StopGroupOnExit);
//  2. the build waits for every other build, and keeps new ones from starting;
//  3. it stops what the agent left: stray processes and new containers;
//  4. after the commit, it writes READY and waits; an operator looks, makes a short-lived
//     token, and leaves it as a docker config (deploy/macmini/push-window.sh);
//  5. docker builds and pushes with that config alone;
//  6. once the digest is known the login is gone, new builds may start, and the trace tells
//     the operator to revoke the token.
const (
	pushCredentialWait = 6 * time.Hour
	pushCredentialPoll = 2 * time.Second
	pushCredentialFile = "config.json"
	pushReadyFile      = "READY"
)

func (m *manager) pushWait() time.Duration {
	if m.pushCredentialWait > 0 {
		return m.pushCredentialWait
	}
	return pushCredentialWait
}

func (m *manager) pushPoll() time.Duration {
	if m.pushCredentialPoll > 0 {
		return m.pushCredentialPoll
	}
	return pushCredentialPoll
}

func (m *manager) credentialDirOf(job *domain.Job) string {
	return filepath.Join(m.pushCredentialDir, job.ProjectID.String())
}

// refuseEarlyCredential stops a build with a credential already in place, and clears a READY
// a build before it left.
func (m *manager) refuseEarlyCredential(job *domain.Job) error {
	if m.pushCredentialDir == "" {
		return nil
	}
	dir := m.credentialDirOf(job)
	if _, err := os.Lstat(filepath.Join(dir, pushCredentialFile)); err == nil {
		return errors.New("a push credential is already in place, and an agent is about to run: remove it and revoke the token")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read the push credential directory: %w", err)
	}
	if err := os.Remove(filepath.Join(dir, pushReadyFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clear an old READY: %w", err)
	}
	return nil
}

// holdBuilds takes every build slot but the one this build runs in, waiting for the builds
// that hold them. No other agent runs until releaseBuilds.
func (m *manager) holdBuilds(ctx context.Context, job *domain.Job, gate *jobGate) error {
	if gate.held >= cap(m.slots)-1 {
		return nil
	}
	m.publishText(job.ID, "Waiting for other builds to finish; none may run while this build's push is open.")
	timeout := time.NewTimer(m.pushWait())
	defer timeout.Stop()
	for gate.held < cap(m.slots)-1 {
		select {
		case m.slots <- struct{}{}:
			gate.held++
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("other builds were still running after %s", m.pushWait())
		}
	}
	return nil
}

func (m *manager) releaseBuilds(gate *jobGate) {
	for ; gate.held > 0; gate.held-- {
		m.freeSlot()
	}
}

// stopAgent ends all the agent left, before anything is committed.
func (m *manager) stopAgent(ctx context.Context, job *domain.Job, gate *jobGate, log *slog.Logger) error {
	if err := m.holdBuilds(ctx, job, gate); err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
	}
	processes, err := m.stopStrays(ctx, gate.agentStarted, log)
	if err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
	}
	containers, err := buildstep.RemoveContainersExcept(ctx, gate.containers, log)
	if err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
	}
	log.Info("the agent's processes are stopped", "processes", len(processes), "containers", len(containers))
	m.publishText(job.ID, fmt.Sprintf("The agent is stopped: %d process(es) and %d container(s) it left were ended.", len(processes), len(containers)))
	for _, p := range processes {
		m.publishText(job.ID, "stopped: "+p)
	}
	return nil
}

// openPushWindow waits for the operator's credential and returns a ctx whose docker commands
// use it alone. The file the operator left is gone when this returns, whatever the outcome.
func (m *manager) openPushWindow(ctx context.Context, job *domain.Job, gate *jobGate, appTag string, log *slog.Logger) (context.Context, error) {
	if m.pushCredentialDir == "" {
		return ctx, errors.New("candidate acceptance: no directory is set for a push credential")
	}
	if err := m.holdBuilds(ctx, job, gate); err != nil {
		return ctx, fmt.Errorf("candidate acceptance: %w", err)
	}
	dir := m.credentialDirOf(job)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ctx, fmt.Errorf("candidate acceptance: %w", err)
	}
	ready := filepath.Join(dir, pushReadyFile)
	if err := os.WriteFile(ready, []byte(job.ID.String()+"\n"), 0o600); err != nil {
		return ctx, fmt.Errorf("candidate acceptance: %w", err)
	}
	defer os.Remove(ready)
	info, err := os.Stat(ready)
	if err != nil {
		return ctx, fmt.Errorf("candidate acceptance: %w", err)
	}
	m.publishPhase(job.ID, "waiting_push_credential")
	m.publishText(job.ID, "Waiting for an operator: this build needs a registry token before it can build and push its image.")
	log.Warn("waiting for an operator's push credential", "dir", dir, "wait", m.pushWait())

	host, _, _ := strings.Cut(appTag, "/")
	path := filepath.Join(dir, pushCredentialFile)
	timeout := time.NewTimer(m.pushWait())
	defer timeout.Stop()
	tick := time.NewTicker(m.pushPoll())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx, ctx.Err()
		case <-timeout.C:
			return ctx, fmt.Errorf("candidate acceptance: no push credential after %s; nothing was built or pushed", m.pushWait())
		case <-tick.C:
		}
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		credential, err := buildstep.ReadPushCredential(path, info.ModTime(), host)
		_ = os.Remove(path)
		if err != nil {
			m.publishText(job.ID, "The push credential was refused: revoke that token.")
			return ctx, fmt.Errorf("candidate acceptance: %w", err)
		}
		gate.dockerDir, err = buildstep.NewDockerConfig(credential)
		if err != nil {
			m.publishText(job.ID, "The push credential could not be used: revoke that token.")
			return ctx, fmt.Errorf("candidate acceptance: %w", err)
		}
		gate.credentialLive = true
		m.publishText(job.ID, "Push credential received; building and pushing.")
		return buildstep.WithDockerConfig(ctx, gate.dockerDir), nil
	}
}

// endPushCredential drops the login, lets other builds start and tells the operator. It runs
// as soon as the digest is known, and again on the way out in case that never happened.
func (m *manager) endPushCredential(job *domain.Job, gate *jobGate, log *slog.Logger) {
	if !gate.credentialLive {
		return
	}
	gate.credentialLive = false
	if err := buildstep.ClearDockerCredential(gate.dockerDir); err != nil {
		log.Error("clear the push credential", "err", err)
		_ = os.RemoveAll(gate.dockerDir)
	}
	m.releaseBuilds(gate)
	log.Warn("the push is over: revoke the registry token")
	m.publishText(job.ID, "The push is over and this build no longer holds the login: revoke the registry token now.")
}
