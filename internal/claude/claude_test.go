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
