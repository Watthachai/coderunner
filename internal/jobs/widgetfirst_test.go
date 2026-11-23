package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent runs the app's own tests, so it must see the widget this build ships. OKR
// build 17 (23 Nov 2026): the branch held an earlier widget, the agent's tests passed on
// it, CRN then put in 70a2ef0's route, which reads FITT_TICKET_WINDOW, and the app's own
// test of env names failed at the release check, after the agent had finished.
func TestTheAgentSeesTheWidgetThisBuildShips(t *testing.T) {
	var route, prompt string
	o := pipelineOpts{
		widget: true,
		receipt: func(r map[string]any) {
			r["widget"].(map[string]any)["expected"] = true
		},
		seed: map[string]string{
			"app/layout.tsx":                 "<html><body>{children}</body></html>\n",
			"app/api/fitt-feedback/route.ts": "// an earlier build's widget route\nexport const old = process.env.FITT_FEEDBACK_URL;\n",
			"public/fitt-feedback.js":        "// an earlier build's widget\n",
		},
		manager: func(m *manager) { m.feedbackIngestURL = "http://127.0.0.1:3010/feedback_requests" },
		agent: func(a agentRun) {
			if prompt != "" {
				return // a later turn
			}
			prompt = a.spec.Prompt
			raw, err := os.ReadFile(filepath.Join(a.spec.WorkDir, "app", "api", "fitt-feedback", "route.ts"))
			route = string(raw)
			if err != nil {
				route = err.Error()
			}
		},
	}
	run := runPipelineWith(t, o)
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if !strings.Contains(route, "FITT_TICKET_WINDOW") {
		t.Fatalf("the agent ran beside an earlier widget, not this build's:\n%s", route)
	}
	if !strings.Contains(prompt, "FITT_TICKET_WINDOW") {
		t.Errorf("the agent is not told which variables the widget reads:\n%s", prompt)
	}
}
