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
//  3. it stops what the agent left, and compares the machine with how it was before;
//  4. it commits, pushes to git, and builds the image, none of which needs the token;
//  5. it stops strays and compares again, reads every tag in the GitLab project, writes READY
//     and waits; an operator makes a token (deploy/macmini/push-window-run.sh);
//  6. docker pushes with that token alone; the digest is read back;
//  7. the login is removed, PUSHED is written, and the build waits until GitLab says the
//     token can no longer push, and reads every tag again: only this build's tag is new;
//  8. only then does the candidate gate run, and "released" can follow.
//
// A failure at any step is a failed build, and a watchdog limits steps 6 and 7.
const (
	// buildBudget ends a listed build before Fittcore's 4 hours from dispatch to a final event.
	buildBudget = 3*time.Hour + 45*time.Minute
	// pushCredentialWait is how long the pause waits. Fittcore's ceiling is 3 hours.
	pushCredentialWait = 2 * time.Hour
	// pushWindowLimit is how long a token may live: from its arrival to GitLab saying it is
	// dead. Fittcore asked for 10 to 15 minutes.
	pushWindowLimit    = 15 * time.Minute
	pushCredentialPoll = 2 * time.Second
	revokePoll         = 5 * time.Second

	pushCredentialFile = "config.json"
	pushReadyFile      = "READY"
	pushedFile         = "PUSHED"
)

func orDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func (m *manager) pushWait() time.Duration {
	return orDefault(m.pushCredentialWait, pushCredentialWait)
}

func (m *manager) credentialDirOf(job *domain.Job) string {
	return filepath.Join(m.pushCredentialDir, job.ProjectID.String())
}

// startBudget ends the build when its time is up, counted from when it was queued.
func (m *manager) startBudget(job *domain.Job, gate *jobGate, cancel context.CancelFunc) *time.Timer {
	left := orDefault(m.buildBudget, buildBudget)
	if !job.QueuedAt.IsZero() {
		left -= time.Since(job.QueuedAt)
	}
	return time.AfterFunc(left, func() {
		gate.overBudget.Store(true)
		cancel()
	})
}

// refuseEarlyCredential stops a build with a credential already in place, and clears what a
// build before it left.
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
	for _, name := range []string{pushReadyFile, pushedFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("clear an old %s: %w", name, err)
		}
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

// compareMachine fails if anything an agent could change has changed since before it ran.
func (m *manager) compareMachine(ctx context.Context, gate *jobGate) error {
	now, err := m.captureMachine(ctx)
	if err != nil {
		return err
	}
	if differ := gate.machine.Differences(now); len(differ) > 0 {
		return errors.New("this machine changed while the agent ran: " + strings.Join(differ, ", "))
	}
	return nil
}

// stopAgent ends all the agent left, before anything is committed, and fails if it changed
// the machine.
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
	if err := m.compareMachine(ctx, gate); err != nil {
		return fmt.Errorf("candidate acceptance: %w", err)
	}
	return nil
}

// pushWindow is an open window: its docker config, the login to watch, and the tags before.
type pushWindow struct {
	ctx       context.Context
	cancel    context.CancelFunc
	dockerDir string
	login     string
	tags      buildstep.RegistryTags
	ref       buildstep.ImageRef
}

// openPushWindow does every check that comes before a token, asks for one, and returns once
// it has it. The file the operator left is gone when this returns, whatever the outcome.
func (m *manager) openPushWindow(ctx context.Context, job *domain.Job, gate *jobGate, appTag string, log *slog.Logger) (*pushWindow, error) {
	if m.pushCredentialDir == "" {
		return nil, errors.New("no directory is set for a push credential")
	}
	ref, err := buildstep.ParseImageRef(appTag)
	if err != nil {
		return nil, err
	}
	if err := m.holdBuilds(ctx, job, gate); err != nil {
		return nil, err
	}
	dir := m.credentialDirOf(job)
	path := filepath.Join(dir, pushCredentialFile)
	if _, err := os.Lstat(path); err == nil {
		_ = os.Remove(path)
		m.publishText(job.ID, "A push credential appeared before this build asked for one: revoke that token.")
		return nil, errors.New("a push credential appeared before this build asked for one")
	}
	if _, err := m.stopStrays(ctx, gate.agentStarted, log); err != nil {
		return nil, err
	}
	if err := m.compareMachine(ctx, gate); err != nil {
		return nil, err
	}
	tags, err := m.readRegistryTags(ctx, ref)
	if err != nil {
		return nil, err
	}
	if _, exists := tags[ref.Repository][ref.Tag]; exists {
		return nil, fmt.Errorf("%s:%s is already in the registry, and a tag is never pushed twice", ref.Repository, ref.Tag)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ready := filepath.Join(dir, pushReadyFile)
	if err := os.WriteFile(ready, []byte(job.ID.String()+"\n"), 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(ready)
	info, err := os.Stat(ready)
	if err != nil {
		return nil, err
	}
	m.publishPhase(job.ID, "waiting_push_credential")
	m.publishText(job.ID, "Waiting for an operator: the image is built, and this build needs a registry token to push it.")
	log.Warn("waiting for an operator's push credential", "dir", dir, "wait", m.pushWait())

	host, _, _ := strings.Cut(appTag, "/")
	timeout := time.NewTimer(m.pushWait())
	defer timeout.Stop()
	tick := time.NewTicker(orDefault(m.pushCredentialPoll, pushCredentialPoll))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, fmt.Errorf("no push credential after %s; nothing was pushed", m.pushWait())
		case <-tick.C:
		}
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		credential, err := buildstep.ReadPushCredential(path, info.ModTime(), host)
		_ = os.Remove(path)
		if err != nil {
			m.publishText(job.ID, "The push credential was refused: revoke that token.")
			return nil, err
		}
		login, err := buildstep.LoginOf(credential)
		if err != nil {
			m.publishText(job.ID, "The push credential was refused: revoke that token.")
			return nil, err
		}
		dockerDir, err := buildstep.NewPushDockerConfig(credential)
		if err != nil {
			m.publishText(job.ID, "The push credential could not be used: revoke that token.")
			return nil, err
		}
		window := &pushWindow{dockerDir: dockerDir, login: login, tags: tags, ref: ref}
		window.ctx, window.cancel = context.WithTimeout(buildstep.WithDockerConfig(ctx, dockerDir), orDefault(m.pushWindowLimit, pushWindowLimit))
		m.publishText(job.ID, "Push credential received at "+time.Now().UTC().Format(time.RFC3339)+"; pushing.")
		return window, nil
	}
}

// closePushWindow removes the login and checks it is gone, tells the operator, waits until
// GitLab says the token cannot push, and reads every tag again. digest is what the push
// produced, empty if it did not. Each failure says what is left to do.
func (m *manager) closePushWindow(ctx context.Context, job *domain.Job, gate *jobGate, window *pushWindow, digest string, log *slog.Logger) error {
	defer window.cancel()
	removeErr := buildstep.RemovePushDockerConfig(window.dockerDir)
	pushed := filepath.Join(m.credentialDirOf(job), pushedFile)
	_ = os.WriteFile(pushed, []byte(job.ID.String()+" "+digest+"\n"), 0o600)
	defer os.Remove(pushed)
	if stray := filepath.Join(m.credentialDirOf(job), pushCredentialFile); removeOnce(stray) {
		m.publishText(job.ID, "A second push credential arrived and was not used: revoke that token too.")
	}
	log.Warn("the push is over: revoke the registry token")
	m.publishText(job.ID, "The push is over at "+time.Now().UTC().Format(time.RFC3339)+" ("+digest+") and this build no longer holds the login: revoke the registry token now.")
	if removeErr != nil {
		return removeErr
	}
	if err := m.waitForRevoke(window, log); err != nil {
		m.publishText(job.ID, "The registry token could NOT be confirmed revoked: revoke it, and stop this lane until both sides have talked.")
		return err
	}
	m.publishText(job.ID, "GitLab confirms the token can no longer push, at "+time.Now().UTC().Format(time.RFC3339)+".")
	after, err := m.readRegistryTags(ctx, window.ref)
	if err != nil {
		return err
	}
	if err := buildstep.CompareRegistryTags(window.tags, after, window.ref.Repository, window.ref.Tag, digest); err != nil {
		m.publishText(job.ID, "The registry holds something this build did not push: stop this lane until both sides have talked.")
		return err
	}
	m.releaseBuilds(gate)
	return nil
}

func removeOnce(path string) bool {
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	return os.Remove(path) == nil
}

// waitForRevoke asks GitLab until the login cannot push. The window's watchdog bounds it. An
// answer that says nothing keeps it waiting; it never counts as revoked.
func (m *manager) waitForRevoke(window *pushWindow, log *slog.Logger) error {
	tick := time.NewTicker(orDefault(m.revokePoll, revokePoll))
	defer tick.Stop()
	for {
		alive, err := m.loginCanPush(window.ctx, window.ref, window.login)
		if err == nil && !alive {
			return nil
		}
		if err != nil {
			log.Warn("no answer about the registry token", "err", err)
		}
		select {
		case <-window.ctx.Done():
			return fmt.Errorf("the registry token was not confirmed revoked within %s", orDefault(m.pushWindowLimit, pushWindowLimit))
		case <-tick.C:
		}
	}
}

// pushThroughWindow is steps 5 to 7. It returns the digest only when all of them held.
func (m *manager) pushThroughWindow(ctx context.Context, job *domain.Job, gate *jobGate, appTag string, log *slog.Logger) (string, error) {
	window, err := m.openPushWindow(ctx, job, gate, appTag, log)
	if err != nil {
		buildstep.RemoveLocalImage(context.WithoutCancel(ctx), appTag, log)
		return "", fmt.Errorf("candidate acceptance: %w", err)
	}
	digest := ""
	pushErr := buildstep.PushImageOnce(window.ctx, appTag, log)
	if pushErr == nil {
		digest, pushErr = buildstep.PushedDigest(window.ctx, appTag, log)
	}
	closeErr := m.closePushWindow(ctx, job, gate, window, digest, log)
	if pushErr != nil {
		m.publish(job.ID, domain.BuildEventMsg{Kind: domain.WSError, Text: "docker push (app) failed: " + pushErr.Error(), JobID: job.ID.String(), Timestamp: time.Now().UTC()})
		return "", fmt.Errorf("docker push (app) failed: %w", errors.Join(pushErr, closeErr))
	}
	if closeErr != nil {
		return "", fmt.Errorf("candidate acceptance: %w", closeErr)
	}
	return digest, nil
}
