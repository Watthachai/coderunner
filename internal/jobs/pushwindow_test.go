package jobs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A listed project's build holds a registry credential only in the push window: after its
// agent and everything the agent left are gone, from an operator, and only until the push
// and its digest are done.

func events(run pipelineRun) string { return strings.Join(run.events, "\n") }

// before says one event came ahead of another, and both happened.
func before(t *testing.T, run pipelineRun, first, second string) {
	t.Helper()
	i, j := indexOf(run.events, first), indexOf(run.events, second)
	if i < 0 || j < 0 || i >= j {
		t.Errorf("want %q (at %d) before %q (at %d):\n%s", first, i, second, j, events(run))
	}
}

func noRegistryWrite(t *testing.T, run pipelineRun) {
	t.Helper()
	if indexOf(run.events, "docker push") >= 0 {
		t.Errorf("it pushed:\n%s", events(run))
	}
}

func TestTheBuildStopsWhatTheAgentLeftBeforeItCommits(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	before(t, run, "stop strays", "operator: ready for a token")
	if !strings.Contains(run.logs, "the agent's processes are stopped") {
		t.Errorf("not in the log:\n%s", run.logs)
	}
	i := strings.Index(run.logs, "the agent's processes are stopped")
	if j := strings.Index(run.logs, "git pushed"); j < 0 || i > j {
		t.Errorf("the push came first (%d, %d)", i, j)
	}
}

func TestTheAgentRunsWithItsGroupStoppedOnExit(t *testing.T) {
	asked := false
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		asked = a.spec.StopGroupOnExit
		change(t, a.spec.WorkDir)
	}})
	released(t, run, "app.js", "changed")
	if !asked {
		t.Error("the runner was not told to stop the agent's group")
	}
}

// A process that cannot be stopped, or a list that cannot be read: nothing is committed.
func TestAProcessThatWillNotStopFailsTheBuild(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{
		agent:  func(a agentRun) { change(t, a.spec.WorkDir) },
		strays: func() ([]string, error) { return nil, errors.New("pid 7 keeps coming back") },
	})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "pid 7") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if head := run.head(t); head != run.seed {
		t.Errorf("the remote moved to %s", head)
	}
	noRegistryWrite(t, run)
}

// A container is outside the process tree and can reach the image store.
func TestAContainerTheAgentLeftIsRemoved(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		f, err := os.OpenFile(os.Getenv("FAKE_CONTAINERS"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString("agent-container\n"); err != nil {
			t.Fatal(err)
		}
	}})
	released(t, run, "app.js", "changed")
	before(t, run, "docker rm -f agent-container", "operator: ready for a token")
	if indexOf(run.events, "docker rm -f old-container") >= 0 {
		t.Errorf("it removed a container older than the agent:\n%s", events(run))
	}
}

func TestTheBuildWaitsForAnOperatorBeforeBuildingTheImage(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	before(t, run, "operator: ready for a token", "docker build")
	before(t, run, "docker build", "docker push")
	if head := run.head(t); indexOf(run.events, "callback released") < 0 || head == run.seed {
		t.Errorf("released = %v, head %s", hasStatus(run, "released"), head)
	}
}

func TestNoCredentialInTimeFailsAndNothingReachesTheRegistry(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{noCredential: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "no push credential") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	noRegistryWrite(t, run)
	if indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it built without one:\n%s", events(run))
	}
	if _, err := os.Stat(filepath.Join(run.credentialDir, "READY")); err == nil {
		t.Error("READY was left behind: an operator would send a token to nobody")
	}
}

// A credential in place while an agent runs is what this is all to prevent.
func TestACredentialAlreadyThereStopsTheBuildBeforeTheAgent(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{credentialFirst: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	if run.agentRan {
		t.Error("the agent ran with a credential in place")
	}
	if !strings.Contains(failedMessage(run), "push credential") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
}

func TestACredentialThatIsNotAsAgreedIsRefused(t *testing.T) {
	write := func(body string, mode os.FileMode) func(t *testing.T, path string) {
		return func(t *testing.T, path string) {
			putCredential(t, path, body)
			if err := os.Chmod(path, mode); err != nil {
				t.Error(err)
			}
		}
	}
	cases := map[string]func(t *testing.T, path string){
		"readable by others": write(goodCredential, 0o644),
		"not json":           write("user:token", 0o600),
		"another registry":   write(`{"auths":{"example.test":{"auth":"`+testToken+`"}}}`, 0o600),
		"two logins":         write(`{"auths":{"172.168.1.234:5050":{"auth":"`+testToken+`"},"example.test":{"auth":"`+testToken+`"}}}`, 0o600),
		"an empty login":     write(`{"auths":{"172.168.1.234:5050":{"auth":""}}}`, 0o600),
		"not base64":         write(`{"auths":{"172.168.1.234:5050":{"auth":"%%%"}}}`, 0o600),
		"no separator":       write(`{"auths":{"172.168.1.234:5050":{"auth":"bm9zZXBhcmF0b3I="}}}`, 0o600),
		"a credential store": write(`{"credsStore":"evil","auths":{"172.168.1.234:5050":{"auth":"`+testToken+`"}}}`, 0o600),
		"a helper":           write(`{"credHelpers":{"172.168.1.234:5050":"evil"},"auths":{"172.168.1.234:5050":{"auth":"`+testToken+`"}}}`, 0o600),
		"too large":          write(`{"auths":{"172.168.1.234:5050":{"auth":"`+base64.StdEncoding.EncodeToString([]byte("user:"+strings.Repeat("x", 9000)))+`"}}}`, 0o600),
		"a link": func(t *testing.T, path string) {
			target := path + ".real"
			if err := os.WriteFile(target, []byte(goodCredential), 0o600); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Error(err)
			}
		},
		"older than the pause": func(t *testing.T, path string) {
			putCredential(t, path, goodCredential)
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Error(err)
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{credential: plant, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
			failedAndNeverReleased(t, run)
			noRegistryWrite(t, run)
			if strings.Contains(failedMessage(run)+run.logs, testToken) {
				t.Error("the token was printed")
			}
		})
	}
}

// docker gets a config of its own for the build and the push, and never reads ~/.docker,
// which an agent can write. What the operator left is moved, not copied.
func TestThePushUsesOnlyTheCredentialItWasGiven(t *testing.T) {
	home := t.TempDir()
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"HOME": home}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	var pushConfig string
	for _, line := range run.dockerConfigs {
		if rest, ok := strings.CutPrefix(line, "docker-config push "); ok {
			pushConfig = rest
		}
		if rest, ok := strings.CutPrefix(line, "docker-auth push "); ok && rest != goodCredential {
			t.Errorf("docker push read %q", rest)
		}
		if rest, ok := strings.CutPrefix(line, "docker-config build "); ok && (rest == "none" || strings.HasPrefix(rest, home)) {
			t.Errorf("docker build read %s", rest)
		}
	}
	if pushConfig == "" || pushConfig == "none" || strings.HasPrefix(pushConfig, home) {
		t.Errorf("docker push read the config at %q", pushConfig)
	}
	if !strings.Contains(strings.Join(run.dockerConfigs, "\n"), "docker-auth push "+goodCredential) {
		t.Errorf("docker push did not hold the credential:\n%s", strings.Join(run.dockerConfigs, "\n"))
	}
	if _, err := os.Lstat(filepath.Join(run.credentialDir, "config.json")); err == nil {
		t.Error("the operator's file is still there")
	}
	if _, err := os.Stat(pushConfig); err == nil {
		t.Error("the build left its docker config behind")
	}
}

// The token is finished with once the digest is known, ahead of everything after it.
func TestTheCredentialIsGoneBeforeTheGateRuns(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	sawPush := false
	for _, line := range run.dockerConfigs {
		if strings.HasPrefix(line, "docker-auth push ") {
			sawPush = true
			continue
		}
		if sawPush && strings.HasPrefix(line, "docker-auth ") && !strings.HasPrefix(line, "docker-auth image") && strings.Contains(line, testToken) {
			t.Errorf("after the push and its digest docker still held the token: %s", line)
		}
	}
	if !sawPush {
		t.Fatalf("no push seen:\n%s", strings.Join(run.dockerConfigs, "\n"))
	}
	raw, err := json.Marshal(run.store.trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "revoke") {
		t.Errorf("the trace does not tell the operator to revoke:\n%s", raw)
	}
	seen := string(raw) + run.logs
	if strings.Contains(seen, testToken) {
		t.Error("the token is in the trace or the log")
	}
}

// A push that fails still ends with the operator told, and nothing kept.
func TestAFailedPushTellsTheOperatorToRevoke(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"FAKE_PUSH_FAILS": "1"}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	raw, err := json.Marshal(run.store.trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "revoke") {
		t.Errorf("the trace does not tell the operator to revoke:\n%s", raw)
	}
	if strings.Contains(string(raw)+run.logs, testToken) {
		t.Error("the token is in the trace or the log")
	}
}

// With other builds running, another agent could read the token. The pause waits for them,
// keeps new ones from starting, and gives their slots back afterwards.
func TestThePauseWaitsForOtherBuildsAndHoldsNewOnesBack(t *testing.T) {
	other := make(chan struct{})
	var m *manager
	var freed atomic.Bool
	sweptAfterOther, heldAtToken := false, false
	run := runPipelineWith(t, pipelineOpts{
		maxConcurrent: 3,
		otherBuild:    other,
		manager:       func(got *manager) { m = got },
		agent: func(a agentRun) {
			change(t, a.spec.WorkDir)
			time.AfterFunc(100*time.Millisecond, func() {
				freed.Store(true)
				close(other)
			})
		},
		strays: func() ([]string, error) {
			sweptAfterOther = freed.Load()
			return nil, nil
		},
		credential: func(t *testing.T, path string) {
			heldAtToken = len(m.slots) == cap(m.slots)
			putCredential(t, path, goodCredential)
		},
	})
	released(t, run, "app.js", "changed")
	if !sweptAfterOther {
		t.Error("it swept for strays while another build could still be running")
	}
	if !heldAtToken {
		t.Error("the slots were not all held when the operator was asked for a token")
	}
	if n := len(m.slots); n != 0 {
		t.Errorf("%d slots still held after the build", n)
	}
}

func TestABuildThatIsWaitedOnTooLongFails(t *testing.T) {
	other := make(chan struct{})
	defer close(other)
	run := runPipelineWith(t, pipelineOpts{
		maxConcurrent: 2,
		otherBuild:    other,
		noCredential:  true,
		agent:         func(a agentRun) { change(t, a.spec.WorkDir) },
	})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "other builds") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if head := run.head(t); head != run.seed {
		t.Errorf("it committed while another agent could still be running: %s", head)
	}
}

// A project without a list has none of this.
func TestAProjectWithoutAListHasNoWindow(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{noList: true, delivery: `{"schema_version":1,"database":true}`, noCredential: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if indexOf(run.events, "stop strays") >= 0 || indexOf(run.events, "docker ps") >= 0 {
		t.Errorf("it swept for an unlisted project:\n%s", events(run))
	}
	for _, line := range run.dockerConfigs {
		if strings.HasPrefix(line, "docker-config ") && !strings.HasSuffix(line, " none") {
			t.Errorf("docker was given another config: %s", line)
		}
	}
}
