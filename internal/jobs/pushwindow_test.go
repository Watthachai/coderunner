package jobs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
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

// The token is asked for once the image is built, so it only has to live through the push.
func TestTheBuildAsksForATokenOnlyWhenTheImageIsReady(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	before(t, run, "stop strays", "docker build")
	before(t, run, "docker build", "operator: ready for a token")
	before(t, run, "operator: ready for a token", "docker push")
	if head := run.head(t); head == run.seed {
		t.Errorf("nothing was committed before the pause")
	}
}

func TestNoCredentialInTimeFailsAndNothingReachesTheRegistry(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{noCredential: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "no push credential") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	noRegistryWrite(t, run)
	if indexOf(run.events, "docker rmi -f") < 0 {
		t.Errorf("the image that was never pushed stays on the machine:\n%s", events(run))
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

// docker gets a config of its own for the build and another for the push, and never reads
// ~/.docker, which an agent can write. What the operator left is moved, not copied.
func TestThePushUsesOnlyTheCredentialItWasGiven(t *testing.T) {
	home := t.TempDir()
	run := runPipelineWith(t, pipelineOpts{env: map[string]string{"HOME": home}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	seen := map[string]string{}
	for _, line := range run.dockerConfigs {
		if rest, ok := strings.CutPrefix(line, "docker-config "); ok {
			verb, dir, _ := strings.Cut(rest, " ")
			if verb == "build" || verb == "push" || verb == "manifest" {
				seen[verb] = dir
			}
		}
		if rest, ok := strings.CutPrefix(line, "docker-auth build "); ok && rest != "{}" {
			t.Errorf("docker build held a login: %s", rest)
		}
		if rest, ok := strings.CutPrefix(line, "docker-auth push "); ok && rest != goodCredential {
			t.Errorf("docker push read %q", rest)
		}
	}
	for _, verb := range []string{"build", "push", "manifest"} {
		if seen[verb] == "" || seen[verb] == "none" || strings.HasPrefix(seen[verb], home) {
			t.Errorf("docker %s read the config at %q", verb, seen[verb])
		}
	}
	if seen["build"] == seen["push"] {
		t.Error("the build and the push shared a config")
	}
	if !strings.Contains(strings.Join(run.dockerConfigs, "\n"), "docker-auth push "+goodCredential) {
		t.Errorf("docker push did not hold the credential:\n%s", strings.Join(run.dockerConfigs, "\n"))
	}
	if _, err := os.Lstat(filepath.Join(run.credentialDir, "config.json")); err == nil {
		t.Error("the operator's file is still there")
	}
	for _, verb := range []string{"build", "push"} {
		if _, err := os.Stat(seen[verb]); err == nil {
			t.Errorf("the %s config was left behind: %s", verb, seen[verb])
		}
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

// --- the machine ------------------------------------------------------------------------

func TestAMachineTheAgentChangedStopsTheBuildBeforeTheCommit(t *testing.T) {
	for _, what := range []string{"program docker", "config ~/.docker/config.json", "listing ~/Library/LaunchAgents", "cron", "launchd labels"} {
		t.Run(what, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{
				agent: func(a agentRun) { change(t, a.spec.WorkDir) },
				machine: func(look int) buildstep.MachineState {
					state := buildstep.MachineState{what: "before"}
					if look > 1 {
						state[what] = "after"
					}
					return state
				},
			})
			failedAndNeverReleased(t, run)
			if !strings.Contains(failedMessage(run), what) {
				t.Errorf("the failure does not name it: %s", failedMessage(run))
			}
			if head := run.head(t); head != run.seed {
				t.Errorf("it committed: %s", head)
			}
			noRegistryWrite(t, run)
		})
	}
}

// The commit and the image build come after the first look, so there is a second one before
// a token can exist.
func TestAMachineThatChangesLaterStopsTheBuildBeforeAToken(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{
		agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		machine: func(look int) buildstep.MachineState {
			if look > 2 {
				return buildstep.MachineState{"program git": "after"}
			}
			return buildstep.MachineState{"program git": "before"}
		},
	})
	failedAndNeverReleased(t, run)
	if indexOf(run.events, "operator: ready for a token") >= 0 {
		t.Errorf("a token was asked for:\n%s", events(run))
	}
	noRegistryWrite(t, run)
}

func TestAProcessThatWillNotStopAtThePauseFailsBeforeAToken(t *testing.T) {
	looks := 0
	run := runPipelineWith(t, pipelineOpts{
		agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		strays: func() ([]string, error) {
			looks++
			if looks > 1 {
				return nil, errors.New("pid 8 keeps coming back")
			}
			return nil, nil
		},
	})
	failedAndNeverReleased(t, run)
	if indexOf(run.events, "operator: ready for a token") >= 0 {
		t.Errorf("a token was asked for:\n%s", events(run))
	}
}

// --- the token ---------------------------------------------------------------------------

func TestACredentialThatAppearsWhileTheAgentRunsFailsTheBuild(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		path := os.Getenv("CREDENTIAL_PATH")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		putCredential(t, path, goodCredential)
	}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "appeared") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if _, err := os.Lstat(os.Getenv("CREDENTIAL_PATH")); err == nil {
		t.Error("the file was left")
	}
	noRegistryWrite(t, run)
}

// One token, one push. A second one is not read, and the operator hears about it.
func TestASecondCredentialIsNotUsed(t *testing.T) {
	var second string
	run := runPipelineWith(t, pipelineOpts{
		agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		credential: func(t *testing.T, path string) {
			second = path
			t.Setenv("FAKE_PUSH_RUNS", "printf '%s' '"+goodCredential+"' > '"+path+"'; chmod 600 '"+path+"'")
			putCredential(t, path, goodCredential)
		},
	})
	released(t, run, "app.js", "changed")
	if _, err := os.Lstat(second); err == nil {
		t.Error("the second credential was left in place")
	}
	raw, _ := json.Marshal(run.store.trace)
	if !strings.Contains(string(raw), "second push credential") {
		t.Errorf("the operator is not told: %s", raw)
	}
	pushes := 0
	for _, e := range run.events {
		if strings.HasPrefix(e, "docker push") {
			pushes++
		}
	}
	if pushes != 1 {
		t.Errorf("%d pushes", pushes)
	}
}

// A tag is never pushed twice, whoever put it there.
func TestATagAlreadyInTheRegistryFailsBeforeAToken(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{
		agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		tags: func(tags buildstep.RegistryTags, ref buildstep.ImageRef, _ bool) {
			tags[ref.Repository][ref.Tag] = "sha256:someone-else"
		},
	})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "never pushed twice") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if indexOf(run.events, "operator: ready for a token") >= 0 {
		t.Errorf("a token was asked for:\n%s", events(run))
	}
	noRegistryWrite(t, run)
}

// PUSHED is how the operator's script knows to revoke: there while the build waits for that,
// gone afterwards.
func TestTheOperatorIsToldWhenThePushIsOver(t *testing.T) {
	sawPushed := false
	run := runPipelineWith(t, pipelineOpts{
		agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		login: func(int) (bool, error) {
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("CREDENTIAL_PATH")), "PUSHED"))
			sawPushed = err == nil && strings.Contains(string(raw), fakeDigest)
			return false, nil
		},
	})
	released(t, run, "app.js", "changed")
	if !sawPushed {
		t.Error("PUSHED was not there, with the digest, while the build waited for the revoke")
	}
	if _, err := os.Stat(filepath.Join(run.credentialDir, "PUSHED")); err == nil {
		t.Error("PUSHED was left behind")
	}
}

func TestARegistryItCannotReadFailsTheBuild(t *testing.T) {
	cases := map[string]func(bool) error{
		"before a token": func(bool) error { return errors.New("gitlab said 502") },
		"after the push": func(pushed bool) error {
			if pushed {
				return errors.New("gitlab said 502")
			}
			return nil
		},
	}
	for name, tagsErr := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) }, tagsErr: tagsErr})
			failedAndNeverReleased(t, run)
			if !strings.Contains(failedMessage(run), "gitlab said 502") {
				t.Errorf("the failure does not say why: %s", failedMessage(run))
			}
			if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
				t.Errorf("the gate ran:\n%s", events(run))
			}
		})
	}
}

// The gate waits for GitLab to say the token is dead, not for anyone's word.
func TestTheGateRunsOnlyAfterGitLabSaysTheTokenIsDead(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{
		agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		login: func(n int) (bool, error) { return n < 4, nil },
	})
	released(t, run, "app.js", "changed")
	before(t, run, "docker push", "gitlab: the token cannot push")
	before(t, run, "gitlab: the token cannot push", "node accept-candidate.mjs")
}

func TestARevokeThatCannotBeConfirmedFailsTheBuild(t *testing.T) {
	cases := map[string]func(int) (bool, error){
		"the token still works":  func(int) (bool, error) { return true, nil },
		"gitlab does not answer": func(int) (bool, error) { return false, errors.New("no answer") },
	}
	for name, login := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{
				agent:   func(a agentRun) { change(t, a.spec.WorkDir) },
				login:   login,
				manager: func(m *manager) { m.pushWindowLimit = 200 * time.Millisecond },
			})
			failedAndNeverReleased(t, run)
			if !strings.Contains(failedMessage(run), "not confirmed revoked") {
				t.Errorf("the failure does not say why: %s", failedMessage(run))
			}
			if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
				t.Errorf("the gate ran:\n%s", events(run))
			}
			raw, _ := json.Marshal(run.store.trace)
			if !strings.Contains(string(raw), "stop this lane") {
				t.Errorf("the trace does not say to stop: %s", raw)
			}
		})
	}
}

// --- the registry ------------------------------------------------------------------------

func TestAnyOtherChangeInTheRegistryFailsTheBuild(t *testing.T) {
	const other = "fitt/ftcdv-uat/crn-demo-other"
	cases := map[string]func(tags buildstep.RegistryTags, ref buildstep.ImageRef){
		"another image's tag moved": func(tags buildstep.RegistryTags, _ buildstep.ImageRef) { tags[other]["v1"] = "sha256:moved" },
		"another image got a tag":   func(tags buildstep.RegistryTags, _ buildstep.ImageRef) { tags[other]["v2"] = "sha256:new" },
		"another image lost a tag":  func(tags buildstep.RegistryTags, _ buildstep.ImageRef) { delete(tags[other], "v1") },
		"another image is gone":     func(tags buildstep.RegistryTags, _ buildstep.ImageRef) { delete(tags, other) },
		"a new image": func(tags buildstep.RegistryTags, _ buildstep.ImageRef) {
			tags["fitt/ftcdv-uat/new"] = map[string]string{"v1": "sha256:x"}
		},
		"v2 moved":                func(tags buildstep.RegistryTags, ref buildstep.ImageRef) { tags[ref.Repository]["v2"] = "sha256:moved" },
		"this image got a latest": func(tags buildstep.RegistryTags, ref buildstep.ImageRef) { tags[ref.Repository]["latest"] = "sha256:x" },
		"the tag is another image": func(tags buildstep.RegistryTags, ref buildstep.ImageRef) {
			tags[ref.Repository][ref.Tag] = "sha256:not-what-was-pushed"
		},
		"the tag is not there": func(tags buildstep.RegistryTags, ref buildstep.ImageRef) { delete(tags[ref.Repository], ref.Tag) },
	}
	for name, change2 := range cases {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, pipelineOpts{
				agent: func(a agentRun) { change(t, a.spec.WorkDir) },
				tags: func(tags buildstep.RegistryTags, ref buildstep.ImageRef, pushed bool) {
					if pushed {
						change2(tags, ref)
					}
				},
			})
			failedAndNeverReleased(t, run)
			if !strings.Contains(failedMessage(run), "the registry changed") {
				t.Errorf("the failure does not say why: %s", failedMessage(run))
			}
			if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
				t.Errorf("the gate ran:\n%s", events(run))
			}
			raw, _ := json.Marshal(run.store.trace)
			if !strings.Contains(string(raw), "stop this lane") {
				t.Errorf("the trace does not say to stop: %s", raw)
			}
		})
	}
}

// --- what Fittcore reads ----------------------------------------------------------------

// Fittcore's ceilings: 4 hours from dispatch to a final event, 3 for the pause; it asked for a
// token to live 10 to 15 minutes.
func TestTheLimitsStayInsideFittcoresCeilings(t *testing.T) {
	if buildBudget >= 4*time.Hour {
		t.Errorf("the budget is %s", buildBudget)
	}
	if pushCredentialWait > 3*time.Hour {
		t.Errorf("the pause is %s", pushCredentialWait)
	}
	if pushWindowLimit < 10*time.Minute || pushWindowLimit > 15*time.Minute {
		t.Errorf("the window is %s", pushWindowLimit)
	}
}

func TestABuildOverItsBudgetIsFailedNotCancelled(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{
		credential: func(*testing.T, string) {},
		agent:      func(a agentRun) { change(t, a.spec.WorkDir) },
		manager:    func(m *manager) { m.buildBudget = 300 * time.Millisecond },
	})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "time budget") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	for _, e := range eventsOf(run) {
		if e == "build_cancelled" {
			t.Error("reported as cancelled by an operator")
		}
	}
}

// Fittcore's clock starts at dispatch, so time in the queue counts.
func TestTimeInTheQueueCountsAgainstTheBudget(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{
		queuedAgo:  time.Hour,
		credential: func(*testing.T, string) {},
		agent:      func(a agentRun) { change(t, a.spec.WorkDir) },
		manager:    func(m *manager) { m.buildBudget = time.Hour + 300*time.Millisecond },
	})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "time budget") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
}

func eventsOf(run pipelineRun) []string {
	var got []string
	for _, e := range run.events {
		if name, ok := strings.CutPrefix(e, "event "); ok {
			got = append(got, name)
		}
	}
	return got
}

// Every way a listed build can end, as Fittcore reads it. Before the gate passes the only
// event is build_started. Each failure is build_failed and callback failed, never build_done
// and never released. CRN_TEST_EVENT_SAMPLES keeps what each path left.
func TestEveryPathEndsAsFittcoreReadsIt(t *testing.T) {
	agent := func(a agentRun) { change(t, a.spec.WorkDir) }
	never := func(*testing.T, string) {}
	paths := map[string]pipelineOpts{
		"token-not-as-agreed": {agent: agent, credential: func(t *testing.T, path string) { putCredential(t, path, "user:token") }},
		"token-timeout":       {agent: agent, noCredential: true},
		"token-before-agent":  {agent: agent, credentialFirst: true},
		"push-fails":          {agent: agent, env: map[string]string{"FAKE_PUSH_FAILS": "1"}},
		"image-build-fails":   {agent: agent, env: map[string]string{"FAKE_BUILD_FAILS": "1"}},
		"digest-unknown":      {agent: agent, env: map[string]string{"FAKE_LOCAL_DIGEST": "not-a-digest"}},
		"digest-mismatch":     {agent: agent, env: map[string]string{"FAKE_REGISTRY_DIGEST": "sha256:" + strings.Repeat("9", 64)}},
		"revoke-unconfirmed":  {agent: agent, login: func(int) (bool, error) { return true, nil }, manager: func(m *manager) { m.pushWindowLimit = 200 * time.Millisecond }},
		"registry-changed": {agent: agent, tags: func(tags buildstep.RegistryTags, _ buildstep.ImageRef, pushed bool) {
			if pushed {
				tags["fitt/ftcdv-uat/crn-demo-other"]["v1"] = "sha256:moved"
			}
		}},
		"machine-changed": {agent: agent, machine: func(look int) buildstep.MachineState {
			return buildstep.MachineState{"program docker": fmt.Sprint(look)}
		}},
		"stray-will-not-stop": {agent: agent, strays: func() ([]string, error) { return nil, errors.New("pid 7 keeps coming back") }},
		"gate-fails":          {agent: agent, accept: "failed"},
		"gate-crashes":        {agent: agent, accept: "crash"},
		"over-budget":         {agent: agent, credential: never, manager: func(m *manager) { m.buildBudget = 300 * time.Millisecond }},
	}
	for name, opts := range paths {
		t.Run(name, func(t *testing.T) {
			run := runPipelineWith(t, opts)
			failedAndNeverReleased(t, run)
			if got := strings.Join(eventsOf(run), ","); got != "build_started,build_failed" {
				t.Errorf("events = %s", got)
			}
			if indexOf(run.events, "callback failed") < 0 {
				t.Errorf("no failed callback:\n%s", events(run))
			}
			keepSample(t, name, run)
		})
	}
	t.Run("success", func(t *testing.T) {
		run := runPipelineWith(t, pipelineOpts{agent: agent})
		released(t, run, "app.js", "changed")
		if got := strings.Join(eventsOf(run), ","); got != "build_started,build_done" {
			t.Errorf("events = %s", got)
		}
		before(t, run, "node accept-candidate.mjs", "event build_done")
		before(t, run, "node accept-candidate.mjs", "callback released")
		keepSample(t, "success", run)
	})
}

// keepSample writes what a path left, with the token checked to be absent.
func keepSample(t *testing.T, name string, run pipelineRun) {
	t.Helper()
	trace, err := json.Marshal(run.store.trace)
	if err != nil {
		t.Fatal(err)
	}
	bodies, err := json.Marshal(run.bodies)
	if err != nil {
		t.Fatal(err)
	}
	for _, seen := range []string{string(trace), string(bodies), run.logs, strings.Join(run.events, "\n")} {
		if strings.Contains(seen, testToken) || strings.Contains(seen, "test-token") {
			t.Errorf("the token is in what this path left")
		}
	}
	dir := os.Getenv("CRN_TEST_EVENT_SAMPLES")
	if dir == "" {
		return
	}
	var texts []string
	var decoded struct {
		Events []struct {
			Text string `json:"text"`
		} `json:"events"`
	}
	if err := json.Unmarshal(trace, &decoded); err == nil {
		for _, e := range decoded.Events {
			if e.Text != "" {
				texts = append(texts, e.Text)
			}
		}
	}
	sample := map[string]any{
		"path": name, "order": run.events, "build_events": eventsOf(run), "callbacks": run.bodies,
		"trace_text": texts,
		"log":        windowLog(run.logs),
	}
	raw, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// windowLog keeps the lines of the log that are about the window, level and message only: a
// sample does not need temporary paths.
func windowLog(logs string) []string {
	var kept []string
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "level=WARN") && !strings.Contains(line, "level=ERROR") && !strings.Contains(line, "the agent's processes") {
			continue
		}
		if i := strings.Index(line, "level="); i >= 0 {
			line = line[i:]
		}
		kept = append(kept, line)
	}
	return kept
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
