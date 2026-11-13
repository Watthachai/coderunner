package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
)

// FITT Billing (13 Nov) is Express + Vite with deploy/billing.Dockerfile and no root
// package.json: its owner commits crn-image.json, and CRN builds the commit it pushed with
// that Dockerfile, tags the image with the commit and pushes it. No Next.js release check,
// no CRN Dockerfile, no widget.

const (
	billingImage      = `{"schema_version":1,"dockerfile":"deploy/billing.Dockerfile","revision_build_arg":"BILLING_REVISION","user":"node"}`
	billingDockerfile = "FROM node:22\nARG BILLING_REVISION\nLABEL org.opencontainers.image.revision=$BILLING_REVISION\nUSER node\n"
	billingIndex      = "<!doctype html><html><body><div id=app></div></body></html>\n"
	// What docker image inspect says of the image built as asked; @REVISION@ is the
	// BILLING_REVISION the build was given.
	builtAsAsked = `[{"Os":"linux","Architecture":"amd64","Config":{"User":"node","Labels":{"org.opencontainers.image.revision":"@REVISION@"}}}]`
)

func billingOpts(agent func(agentRun)) pipelineOpts {
	return pipelineOpts{
		helper: true, noList: true, delivery: noDeliveryFile, agent: agent,
		seed: map[string]string{
			"package.json":                           noDeliveryFile,
			"server/package.json":                    `{"name":"billing-server"}`,
			"web/index.html":                         billingIndex,
			buildstep.RepoImageFile:                  billingImage,
			"deploy/billing.Dockerfile":              billingDockerfile,
			"deploy/billing.Dockerfile.dockerignore": "node_modules\n",
		},
		env: map[string]string{"FAKE_IMAGE_JSON": builtAsAsked},
	}
}

func writeIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func addEnvExample(a agentRun) { writeIn(a.t, a.spec.WorkDir, ".env.example", "BILLING_ENV=\n") }

func TestAnAppWithItsOwnDockerfileIsBuiltFromThePushedCommitAndTaggedWithIt(t *testing.T) {
	run := runPipelineWith(t, billingOpts(addEnvExample))
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	head := run.head(t)
	if got := plainGit(t, run.remote, "show", head+":.env.example"); got != "BILLING_ENV=" {
		t.Errorf("the agent's change is not in the pushed commit: %q", got)
	}
	i := indexOf(run.events, "docker build")
	if i < 0 {
		t.Fatalf("no image was built:\n%s", events(run))
	}
	build := run.events[i]
	for _, want := range []string{"--platform linux/amd64", "/deploy/billing.Dockerfile -t ", "--build-arg BILLING_REVISION=" + head, "--label fitt.crn.build_no=3"} {
		if !strings.Contains(build, want) {
			t.Errorf("%s\nlacks %q", build, want)
		}
	}
	if strings.Contains(build, "--label org.opencontainers.image.revision") || strings.Contains(build, "fitt.crn.contract") {
		t.Errorf("CRN labelled what the owner's Dockerfile says, or a contract it did not check:\n%s", build)
	}
	// The context is the pushed commit: the agent's change in, no Dockerfile of CRN's.
	if got, err := os.ReadFile(filepath.Join(run.context, ".env.example")); err != nil || string(got) != "BILLING_ENV=\n" {
		t.Errorf("the image was not built from the pushed commit: %q %v", got, err)
	}
	for _, name := range []string{"Dockerfile", "docker-compose.customer.yml", "CRN_VERIFICATION.json"} {
		if _, err := os.Stat(filepath.Join(run.context, name)); err == nil {
			t.Errorf("CRN wrote %s into a repository that builds with its own Dockerfile", name)
		}
	}
	if indexOf(run.events, "node verify-delivery.mjs") >= 0 {
		t.Errorf("the Next.js release check ran:\n%s", events(run))
	}
	tag := ":" + head
	if j := indexOf(run.events, "crn-push push 172.168.1.234:5050/fitt/ftcdv-uat/crn-demo-app-"); j < 0 || !strings.HasSuffix(run.events[j], tag) {
		t.Fatalf("the helper did not push the commit's tag %s:\n%s", tag, events(run))
	}
	before(t, run, "docker image inspect", "crn-push push")
	noRegistryWrite(t, run)
	for _, b := range run.bodies {
		if b["status"] != "released" {
			continue
		}
		if ref, _ := b["image_ref"].(string); !strings.HasSuffix(ref, tag) {
			t.Errorf("released names %q, want the image tagged %s", ref, tag)
		}
		if b["image_digest"] != fakeDigest {
			t.Errorf("released carries digest %v, want %s", b["image_digest"], fakeDigest)
		}
	}
}

func TestAnAgentThatChangesTheOwnersDockerfileStopsTheBuild(t *testing.T) {
	run := runPipelineWith(t, billingOpts(func(a agentRun) {
		writeIn(a.t, a.spec.WorkDir, "deploy/billing.Dockerfile", "FROM node:22\nUSER root\n")
	}))
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "deploy/billing.Dockerfile") {
		t.Errorf("the failure does not name the file: %s", failedMessage(run))
	}
	if run.head(t) != run.seed || indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it pushed or built after the Dockerfile changed:\n%s", events(run))
	}
}

// Otherwise an agent could take a Next.js app out of the release check by writing one.
func TestAnAgentMayNotTakeANextAppOutOfTheReleaseCheck(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, agent: func(a agentRun) {
		writeIn(a.t, a.spec.WorkDir, buildstep.RepoImageFile, `{"schema_version":1,"dockerfile":"x.Dockerfile"}`)
		writeIn(a.t, a.spec.WorkDir, "x.Dockerfile", "FROM scratch\n")
	}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), buildstep.RepoImageFile) {
		t.Errorf("the failure does not name %s: %s", buildstep.RepoImageFile, failedMessage(run))
	}
	if run.head(t) != run.seed || indexOf(run.events, "docker build") >= 0 {
		t.Errorf("it pushed or built:\n%s", events(run))
	}
}

func TestAnImageThatDoesNotCarryItsCommitIsNeverPushed(t *testing.T) {
	o := billingOpts(addEnvExample)
	o.env["FAKE_IMAGE_JSON"] = strings.Replace(builtAsAsked, "@REVISION@", "449f94d6888d62444177057f1f8b8d23fcdd5ede", 1)
	run := runPipelineWith(t, o)
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "org.opencontainers.image.revision") {
		t.Errorf("the failure does not say what is wrong: %s", failedMessage(run))
	}
	if indexOf(run.events, "crn-push") >= 0 || indexOf(run.events, "docker save") >= 0 {
		t.Errorf("it pushed:\n%s", events(run))
	}
}

// CRN adds the widget to a Next.js app or plain HTML; an app with its own Dockerfile is
// its owner's (FITT Billing: no widget, web/ untouched), even when a build asks for one.
func TestTheWidgetIsNotAddedToAnAppWithItsOwnDockerfile(t *testing.T) {
	o := billingOpts(addEnvExample)
	o.widget = true
	o.manager = func(m *manager) { m.feedbackIngestURL = "http://127.0.0.1:3010/feedback_requests" }
	run := runPipelineWith(t, o)
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if got := plainGit(t, run.remote, "show", run.head(t)+":web/index.html"); got+"\n" != billingIndex {
		t.Errorf("web/index.html changed:\n%s", got)
	}
	raw, _ := json.Marshal(run.store.trace)
	if !strings.Contains(string(raw), "feedback widget not added") {
		t.Errorf("the build does not say the widget was left out:\n%s", raw)
	}
}
