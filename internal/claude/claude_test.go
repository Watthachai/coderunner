package claude

import (
	"slices"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

var baseArgs = []string{"-p", "build it", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}

func TestRunArgsPassTheBuildsModelAndEffort(t *testing.T) {
	got := runArgs(domain.RunSpec{Prompt: "build it", Model: "claude-opus-5-5", Effort: "xhigh"}, "claude-opus-5")
	want := append(slices.Clone(baseArgs), "--model", "claude-opus-5-5", "--effort", "xhigh")
	if !slices.Equal(got, want) {
		t.Errorf("runArgs =\n  %q\nwant\n  %q", got, want)
	}
}

// A spec without a model still names one: leaving --model off would hand the
// choice to the build box's settings.json, which says "sonnet".
func TestRunArgsFallBackToTheRunnersModel(t *testing.T) {
	got := runArgs(domain.RunSpec{Prompt: "build it"}, "claude-opus-5")
	want := append(slices.Clone(baseArgs), "--model", "claude-opus-5")
	if !slices.Equal(got, want) {
		t.Errorf("runArgs =\n  %q\nwant\n  %q", got, want)
	}
}

func TestRunArgsKeepResume(t *testing.T) {
	got := runArgs(domain.RunSpec{Prompt: "build it", ResumeSessionID: "sess-1", Model: "claude-opus-5", Effort: "high"}, "")
	want := append(slices.Clone(baseArgs), "--resume", "sess-1", "--model", "claude-opus-5", "--effort", "high")
	if !slices.Equal(got, want) {
		t.Errorf("runArgs =\n  %q\nwant\n  %q", got, want)
	}
}

func TestImproveArgsPassModelAndEffort(t *testing.T) {
	got := improveArgs("improve this", "claude-opus-5-5", "xhigh")
	want := []string{"-p", "improve this", "--dangerously-skip-permissions", "--model", "claude-opus-5-5", "--effort", "xhigh"}
	if !slices.Equal(got, want) {
		t.Errorf("improveArgs =\n  %q\nwant\n  %q", got, want)
	}
}

// Claude runs the build with every tool unlocked, and the request text can come
// from end users. Nothing of the Runner's own may reach it: database URLs, the
// callback token, ssh-agent. Only what the CLI, npm, Docker and the browser
// tests need gets through.
func TestChildEnvKeepsOnlyWhatTheBuildNeeds(t *testing.T) {
	got := childEnv([]string{
		"PATH=/opt/homebrew/bin:/usr/bin",
		"HOME=/Users/builder",
		"USER=builder",
		"SHELL=/bin/zsh",
		"TMPDIR=/var/folders/x/T/",
		"LANG=en_US.UTF-8",
		"LC_CTYPE=UTF-8",
		"https_proxy=http://proxy:3128",
		"NODE_EXTRA_CA_CERTS=/etc/ca.pem",
		"DOCKER_HOST=unix:///Users/builder/.docker/run/docker.sock",
		"PLAYWRIGHT_BROWSERS_PATH=/opt/pw",
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat-x=y",
		"ANTHROPIC_BASE_URL=https://api.anthropic.com",
		"CRN_DATABASE_URL=postgres://crn:secret@db/crn",
		"CRN_CENTRAL_DATABASE_URL=postgres://c:secret@db/c",
		"CRN_FTC_DV_CALLBACK_TOKEN=tok",
		"CRN_MONGO_URL=mongodb://u:p@m/x",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"GITHUB_TOKEN=ghp_x",
		"DATABASE_URL=postgres://u:p@h/d",
		"AWS_SECRET_ACCESS_KEY=x",
		"malformed-entry",
	})
	want := []string{
		"PATH=/opt/homebrew/bin:/usr/bin",
		"HOME=/Users/builder",
		"USER=builder",
		"SHELL=/bin/zsh",
		"TMPDIR=/var/folders/x/T/",
		"LANG=en_US.UTF-8",
		"LC_CTYPE=UTF-8",
		"https_proxy=http://proxy:3128",
		"NODE_EXTRA_CA_CERTS=/etc/ca.pem",
		"DOCKER_HOST=unix:///Users/builder/.docker/run/docker.sock",
		"PLAYWRIGHT_BROWSERS_PATH=/opt/pw",
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat-x=y",
		"ANTHROPIC_BASE_URL=https://api.anthropic.com",
	}
	if !slices.Equal(got, want) {
		t.Errorf("childEnv =\n  %q\nwant\n  %q", got, want)
	}
}

// exec.Cmd treats a nil Env as "inherit the parent's environment" and a
// non-nil empty one as "no environment". When nothing passes the filter it
// must return the empty slice, or exec would hand Claude everything.
func TestChildEnvIsNeverNil(t *testing.T) {
	if got := childEnv([]string{"CRN_DATABASE_URL=x"}); got == nil {
		t.Fatal("childEnv returned nil; exec.Cmd treats a nil Env as \"inherit the parent's environment\"")
	}
}
