package jobs

import (
	"strings"
	"testing"
)

// The release check refuses an app whose switch knows no read-only (23 Nov 2026). An edit
// of an app that still carries version 1 tells the agent so, rather than leave it to
// notice that the skill changed.
func TestAnEditOfAnAppWithAnOlderSwitchAsksForVersion2(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ask        bool
	}{
		{"version 1", "// FITT service-status switch — shipped by Code Runner.\n", true},
		{"version 2", "export const SWITCH_VERSION = 2;\n", false},
	} {
		var prompt string
		run := runPipelineWith(t, pipelineOpts{
			seed: map[string]string{"src/platform/service-status.mjs": tc.body},
			agent: func(a agentRun) {
				if prompt == "" {
					prompt = a.spec.Prompt
				}
			},
		})
		if !hasStatus(run, "released") {
			t.Fatalf("%s: no released:\n%s\n%s", tc.name, events(run), run.logs)
		}
		if got := strings.Contains(prompt, "references/service-status.md"); got != tc.ask {
			t.Errorf("%s: asked for version 2 = %v, want %v:\n%s", tc.name, got, tc.ask, prompt)
		}
	}
}
