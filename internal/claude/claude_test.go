package claude

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// fakeClaude writes a `claude` that prints lines as its stream-json output.
func fakeClaude(t *testing.T, lines ...string) string {
	t.Helper()
	script := "#!/bin/sh\ncat <<'STREAM'\n" + strings.Join(lines, "\n") + "\nSTREAM\n"
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// A resumed session can print a result before any work (a background task's
// notification left over from the last run), and a run prints another result
// after each turn a notification starts. The run's outcome is the last one, and
// everything printed before it is still the build's stream.
func TestRunReadsToTheLastResult(t *testing.T) {
	bin := fakeClaude(t,
		`{"type":"system","subtype":"init","session_id":"a786"}`,
		`{"type":"result","subtype":"success","total_cost_usd":11.14,"session_id":"a786"}`,
		`{"type":"assistant","session_id":"a786","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"app/page.tsx"}}]}}`,
		`{"type":"result","subtype":"success","total_cost_usd":16.96,"session_id":"a786"}`,
	)
	r := NewRunner(bin, t.TempDir(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	var tools []string
	res, err := r.Run(context.Background(), domain.RunSpec{Prompt: "edit it", WorkDir: t.TempDir(), ResumeSessionID: "a786"}, func(ev domain.ClaudeEvent) error {
		if ev.ToolName != "" {
			tools = append(tools, ev.ToolName)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CostUSD != 16.96 || !res.Success || res.SessionID != "a786" {
		t.Errorf("result = %+v, want the last result line (16.96, success, a786)", res)
	}
	if !slices.Equal(tools, []string{"Edit"}) {
		t.Errorf("tool calls seen = %q, want the Edit after the early result", tools)
	}
}

// The last result decides the outcome: a run that ends in an error failed,
// whatever an earlier turn reported.
func TestRunFailsWhenTheLastResultIsAnError(t *testing.T) {
	bin := fakeClaude(t,
		`{"type":"result","subtype":"success","total_cost_usd":2,"session_id":"s"}`,
		`{"type":"result","subtype":"error_during_execution","total_cost_usd":3,"session_id":"s"}`,
	)
	r := NewRunner(bin, t.TempDir(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := r.Run(context.Background(), domain.RunSpec{Prompt: "p", WorkDir: t.TempDir()}, func(domain.ClaudeEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || res.CostUSD != 3 {
		t.Errorf("result = %+v, want the last line's failure and cost 3", res)
	}
}
