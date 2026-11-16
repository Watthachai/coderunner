package jobs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// OKR #6 (16 Nov): the agent fixed 2 of 16 items, handed the other 14 to two background
// agents, ended its turn to wait for them, and the session ended with both still working.
// CRN committed and shipped the two. Claude now runs with background tasks off; should a
// run still start one, what it was doing may not be in the tree, so nothing is committed.
func TestWorkLeftInTheBackgroundIsNeverShipped(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		input, _ := json.Marshal(map[string]any{"description": "Investigate BSA feedback items", "prompt": "…", "run_in_background": true})
		if err := a.emit(domain.ClaudeEvent{Type: domain.ClaudeAssistant, ToolName: "Agent", ToolInput: input}); err != nil {
			t.Fatal(err)
		}
	}})
	failedAndNeverReleased(t, run)
	for _, want := range []string{"background", "Investigate BSA feedback items"} {
		if !strings.Contains(failedMessage(run), want) {
			t.Errorf("the failure does not say %q: %s", want, failedMessage(run))
		}
	}
	if run.head(t) != run.seed {
		t.Errorf("it pushed a commit after the agent left work in the background:\n%s", events(run))
	}
}

// A sub-agent that runs in the foreground returns before the agent goes on: nothing to stop.
func TestASubAgentInTheForegroundIsFine(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		input, _ := json.Marshal(map[string]any{"description": "Investigate BSA feedback items", "prompt": "…", "run_in_background": false})
		if err := a.emit(domain.ClaudeEvent{Type: domain.ClaudeAssistant, ToolName: "Agent", ToolInput: input}); err != nil {
			t.Fatal(err)
		}
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
}

// Two sub-agents started in one message: the stream's event names only the first tool
// call, so the check reads every call the line carries.
func TestTheSecondOfTwoCallsInOneMessageIsSeen(t *testing.T) {
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, agent: func(a agentRun) {
		change(t, a.spec.WorkDir)
		line := `{"type":"assistant","message":{"content":[` +
			`{"type":"tool_use","name":"Agent","input":{"description":"Investigate BSA feedback items","prompt":"…"}},` +
			`{"type":"tool_use","name":"Agent","input":{"description":"Investigate Dealer feedback items","prompt":"…","run_in_background":true}}]}}`
		first, _ := json.Marshal(map[string]any{"description": "Investigate BSA feedback items", "prompt": "…"})
		if err := a.emit(domain.ClaudeEvent{Type: domain.ClaudeAssistant, ToolName: "Agent", ToolInput: first, Raw: json.RawMessage(line)}); err != nil {
			t.Fatal(err)
		}
	}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "Investigate Dealer feedback items") {
		t.Errorf("the failure does not name the background agent: %s", failedMessage(run))
	}
}

// A change request can carry many items (OKR #6: 16 from UAT). The agent does each one in
// its own session and ends by saying, item by item, what it did, so whoever reads the
// build sees what is in it, and gets an answer to the items that are questions.
func TestEditPromptsAskForEveryItemAndAReportOnEach(t *testing.T) {
	prompts := map[string]string{
		"edit":       editPrompt("1. Rename the sidebar label\n2. How do I see a deal's owner?"),
		"repo image": repoImagePrompt("1. Rename the sidebar label", &buildstep.RepoImage{SchemaVersion: 1, Dockerfile: "deploy/billing.Dockerfile"}),
	}
	for name, p := range prompts {
		for _, want := range []string{"every item", "yourself", "item by item", "not changed, and why", "answer"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt lacks %q:\n%s", name, want, p)
			}
		}
	}
}
