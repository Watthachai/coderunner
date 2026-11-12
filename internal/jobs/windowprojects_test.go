package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
)

// Every build whose image goes to a customer project (CRN_PUSH_WINDOW_PROJECTS) pushes
// through the push window, whether or not the project holds a list of forbidden values: the
// same checks before a token, a token from an operator for that build only, and the tags
// read before and after. Only a listed project also has the list and the candidate gate.

const customerProject = "fitt/ftcdv-uat"

func windowBuild(o pipelineOpts) pipelineOpts {
	o.noList = true
	o.delivery = localDelivery
	o.windowProjects = []string{customerProject}
	return o
}

func TestAnUnlistedCustomerBuildPushesOnlyThroughTheWindow(t *testing.T) {
	run := runPipelineWith(t, windowBuild(pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) }}))
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	before(t, run, "docker build", "operator: ready for a token")
	before(t, run, "operator: ready for a token", "docker push")
	before(t, run, "docker push", "gitlab: the token cannot push")
	strayAfterBuild := false
	for i, e := range run.events {
		if e == "stop strays" && i > indexOf(run.events, "docker build") && i < indexOf(run.events, "operator: ready for a token") {
			strayAfterBuild = true
		}
	}
	if !strayAfterBuild {
		t.Errorf("what the agent left was not stopped again before the token:\n%s", events(run))
	}
	pushed := false
	for _, line := range run.dockerConfigs {
		if strings.HasPrefix(line, "docker-auth push ") {
			pushed = true
			if !strings.Contains(line, goodCredential) {
				t.Errorf("docker pushed with another login: %s", line)
			}
		}
		if strings.HasPrefix(line, "docker-config push ") && strings.HasSuffix(line, " none") {
			t.Errorf("docker pushed with its own login: %s", line)
		}
	}
	if !pushed {
		t.Errorf("docker push was not given the window's login:\n%s", strings.Join(run.dockerConfigs, "\n"))
	}
	if indexOf(run.events, "node accept-candidate.mjs") >= 0 {
		t.Errorf("the candidate gate ran on a project without a list:\n%s", events(run))
	}
	raw, _ := json.Marshal(run.store.trace)
	if !strings.Contains(string(raw), "GitLab confirms the token can no longer push") {
		t.Errorf("the build's record does not show the token was confirmed dead:\n%s", raw)
	}
}

// The agent's group dies with it, and what it left is gone before anything is committed.
func TestAnUnlistedCustomerBuildStopsItsAgent(t *testing.T) {
	asked := false
	run := runPipelineWith(t, windowBuild(pipelineOpts{agent: func(a agentRun) {
		asked = a.spec.StopGroupOnExit
		change(t, a.spec.WorkDir)
	}}))
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s", events(run))
	}
	if !asked {
		t.Error("the runner was not told to stop the agent's group")
	}
	before(t, run, "stop strays", "docker build")
}

// No token in time: nothing reaches the registry, the image goes, the build fails.
func TestAnUnlistedCustomerBuildWithoutATokenNeverPushes(t *testing.T) {
	run := runPipelineWith(t, windowBuild(pipelineOpts{noCredential: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }}))
	if hasStatus(run, "released") || !hasStatus(run, "failed") {
		t.Fatalf("want failed and never released:\n%s", events(run))
	}
	if !strings.Contains(failedMessage(run), "no push credential") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	noRegistryWrite(t, run)
	if indexOf(run.events, "docker rmi -f") < 0 {
		t.Errorf("the image that was never pushed stays on the machine:\n%s", events(run))
	}
	if _, err := os.Stat(filepath.Join(run.credentialDir, "READY")); err == nil {
		t.Error("READY was left behind")
	}
}

// A credential already in place while the agent is about to run stops the build first.
func TestAnUnlistedCustomerBuildRefusesAnEarlyCredential(t *testing.T) {
	run := runPipelineWith(t, windowBuild(pipelineOpts{credentialFirst: true, agent: func(a agentRun) { change(t, a.spec.WorkDir) }}))
	if hasStatus(run, "released") || !hasStatus(run, "failed") {
		t.Fatalf("want failed:\n%s", events(run))
	}
	if run.agentRan {
		t.Error("the agent ran with a credential in place")
	}
}

// The machine changed while the agent ran: no token is asked for.
func TestAnUnlistedCustomerBuildFromAChangedMachineNeverAsks(t *testing.T) {
	run := runPipelineWith(t, windowBuild(pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		machine: func(look int) buildstep.MachineState {
			if look > 1 {
				return buildstep.MachineState{"program docker": "replaced"}
			}
			return buildstep.MachineState{"program docker": "same"}
		}}))
	if hasStatus(run, "released") || !hasStatus(run, "failed") {
		t.Fatalf("want failed:\n%s", events(run))
	}
	if indexOf(run.events, "operator: ready for a token") >= 0 {
		t.Errorf("it asked for a token:\n%s", events(run))
	}
	noRegistryWrite(t, run)
}

// Anything else that changes in the registry while the window is open fails the build.
func TestAnUnlistedCustomerBuildSeesAnyOtherRegistryChange(t *testing.T) {
	run := runPipelineWith(t, windowBuild(pipelineOpts{agent: func(a agentRun) { change(t, a.spec.WorkDir) },
		tags: func(tags buildstep.RegistryTags, _ buildstep.ImageRef, pushed bool) {
			if pushed {
				tags["fitt/ftcdv-uat/crn-demo-other"]["v1"] = "sha256:replaced"
			}
		}}))
	if hasStatus(run, "released") || !hasStatus(run, "failed") {
		t.Fatalf("want failed:\n%s", events(run))
	}
	if !strings.Contains(failedMessage(run), "crn-demo-other:v1") {
		t.Errorf("the failure does not name the change: %s", failedMessage(run))
	}
}

// A project that is not a customer's keeps what it had: no window, docker's own login.
func TestABuildOfAnotherProjectDoesNotWait(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{noList: true, delivery: localDelivery, noCredential: true,
		windowProjects: []string{"fitt/cust-other"}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if indexOf(run.events, "stop strays") >= 0 || indexOf(run.events, "operator: ready for a token") >= 0 {
		t.Errorf("it waited for a window:\n%s", events(run))
	}
}

// A listed customer project keeps everything it had, the candidate gate included.
func TestAListedCustomerBuildStillHasItsGate(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{windowProjects: []string{customerProject}, agent: func(a agentRun) { change(t, a.spec.WorkDir) }})
	released(t, run, "app.js", "changed")
	if indexOf(run.events, "node accept-candidate.mjs") < 0 {
		t.Errorf("the candidate gate did not run:\n%s", events(run))
	}
}
