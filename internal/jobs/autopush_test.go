package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
)

// With a push helper (cmd/crn-push), a listed project's build pushes by itself: the same
// checks as the push window, then the helper, which alone holds the login, adds the one tag.

func TestWithAPushHelperTheBuildPushesWithoutAnOperator(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	tag := "172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-app-"
	if i := indexOf(run.events, "crn-push push "+tag); i < 0 || !strings.HasSuffix(run.events[i], ":v3") {
		t.Fatalf("the helper did not push this build's tag:\n%s", events(run))
	}
	strayAfterBuild := false
	for i, e := range run.events {
		if e == "stop strays" && i > indexOf(run.events, "docker build") && i < indexOf(run.events, "docker save") {
			strayAfterBuild = true
		}
	}
	if !strayAfterBuild {
		t.Errorf("what the agent left was not stopped again between the image build and the push:\n%s", events(run))
	}
	before(t, run, "docker build", "docker save")
	before(t, run, "docker save", "crn-push push")
	before(t, run, "crn-push push", "node accept-candidate.mjs")
	noRegistryWrite(t, run)
	if indexOf(run.events, "operator: ready for a token") >= 0 {
		t.Errorf("it waited for an operator:\n%s", events(run))
	}
	if _, err := os.Stat(filepath.Join(run.credentialDir, "READY")); err == nil {
		t.Error("it wrote READY: an operator would be asked for a token")
	}
	got, err := os.ReadFile(os.Getenv("HELPER_STDIN"))
	if err != nil || !strings.HasPrefix(string(got), "oci layout of "+tag) {
		t.Errorf("the helper was not given docker save's image: %q %v", got, err)
	}
	raw, _ := json.Marshal(run.store.trace)
	text := string(raw)
	for _, want := range []string{fakeDigest, "only new tag", "3 tags before, 4 after"} {
		if !strings.Contains(text, want) {
			t.Errorf("the build's record does not say %q:\n%s", want, text)
		}
	}
	for _, b := range run.bodies {
		if b["status"] == "released" && b["image_digest"] != fakeDigest {
			t.Errorf("released carries digest %v, want %s", b["image_digest"], fakeDigest)
		}
	}
}

// What the helper is run with is CRN's own environment: no docker config and no login.
func TestThePushHelperIsGivenNoDockerConfig(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	env, err := os.ReadFile(os.Getenv("HELPER_ENV"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "DOCKER_CONFIG=") {
		t.Errorf("the helper was handed a docker config:\n%s", env)
	}
}

func TestAPushHelperThatRefusesFailsTheBuildAndTheImageIsRemoved(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, env: map[string]string{"FAKE_HELPER_FAILS": "an agent is running"}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "an agent is running") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if indexOf(run.events, "docker rmi -f") < 0 {
		t.Errorf("the image that was never pushed stays on the machine:\n%s", events(run))
	}
	if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
		t.Errorf("the gate ran on nothing:\n%s", events(run))
	}
}

func TestWithAPushHelperATagAlreadyThereIsNeverPushed(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		tags: func(tags buildstep.RegistryTags, ref buildstep.ImageRef, _ bool) {
			tags[ref.Repository][ref.Tag] = "sha256:customers"
		}})
	failedAndNeverReleased(t, run)
	if indexOf(run.events, "crn-push") >= 0 || indexOf(run.events, "docker save") >= 0 {
		t.Errorf("it pushed over a tag that was there:\n%s", events(run))
	}
}

func TestWithAPushHelperAnythingElseChangingInTheRegistryFailsTheBuild(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		tags: func(tags buildstep.RegistryTags, _ buildstep.ImageRef, pushed bool) {
			if pushed {
				tags["fitt/ftcdv-uat/crn-demo-other"]["v1"] = "sha256:replaced"
			}
		}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "crn-demo-other:v1") {
		t.Errorf("the failure does not name the change: %s", failedMessage(run))
	}
	if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
		t.Errorf("the gate ran after the registry changed:\n%s", events(run))
	}
}

func TestWithAPushHelperADigestThatIsNotTheRegistrysFailsTheBuild(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, env: map[string]string{"FAKE_HELPER_DIGEST": "sha256:" + strings.Repeat("9", 64)}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
}

// The machine is compared before the helper runs, as before a token.
func TestWithAPushHelperAMachineThatChangedIsNeverPushed(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		machine: func(look int) buildstep.MachineState {
			if look > 2 {
				return buildstep.MachineState{"program docker": "replaced"}
			}
			return buildstep.MachineState{"program docker": "same"}
		}})
	failedAndNeverReleased(t, run)
	if indexOf(run.events, "crn-push") >= 0 {
		t.Errorf("it pushed from a changed machine:\n%s", events(run))
	}
}

// A project without a list pushes through the helper too, with nothing of the window.
const localDelivery = `{"schema_version":1,"database":true}`

func TestAnUnlistedBuildPushesThroughTheHelper(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if indexOf(run.events, "crn-push push 172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-app-") < 0 {
		t.Errorf("the helper did not push:\n%s", events(run))
	}
	noRegistryWrite(t, run)
	for _, b := range run.bodies {
		if b["status"] == "released" && b["image_digest"] != fakeDigest {
			t.Errorf("released carries digest %v, want %s", b["image_digest"], fakeDigest)
		}
	}
}

// The helper refuses while any agent runs; another build's agent may, so the build waits.
func TestAnUnlistedBuildWaitsWhileAnotherAgentRuns(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery,
		env:     map[string]string{"FAKE_HELPER_BUSY_TIMES": "2"},
		manager: func(m *manager) { m.helperRetry = 5 * time.Millisecond },
		agent:   func(a agentRun) { change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if n := strings.Count(events(run), "crn-push push"); n != 3 {
		t.Errorf("the helper was asked %d times, want 3:\n%s", n, events(run))
	}
}

func TestAnUnlistedBuildStopsWaitingInTime(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery,
		env:     map[string]string{"FAKE_HELPER_BUSY_TIMES": "1000"},
		manager: func(m *manager) { m.helperRetry = 5 * time.Millisecond; m.helperWait = 50 * time.Millisecond },
		agent:   func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "an agent is running") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
}

// Any other refusal is final: a tag that is there is never pushed again.
func TestAnUnlistedBuildDoesNotRetryAnotherRefusal(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery,
		env:     map[string]string{"FAKE_HELPER_FAILS": "the tag is already in the registry"},
		manager: func(m *manager) { m.helperRetry = 5 * time.Millisecond },
		agent:   func(a agentRun) { change(t, a.spec.WorkDir) }})
	failedAndNeverReleased(t, run)
	if n := strings.Count(events(run), "crn-push push"); n != 1 {
		t.Errorf("the helper was asked %d times, want 1:\n%s", n, events(run))
	}
}
