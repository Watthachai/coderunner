package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// OKR #6 (16 Nov): 16 items from UAT, the agent did 2, and CRN shipped them, because
// nothing held the agent to each item. CRN now counts the items, asks for a report on
// each in CRN_ITEMS.json, checks it against the tree, and hands the agent back what is
// wrong once before it fails the build. A big request also goes to a second, read-only
// agent that checks each item against the code.

const twoItemChange = "1. Rename the sidebar label\n2. How do I see a deal's owner?"

// turns records every run of the agent: its spec, in order.
type turns struct {
	mu    sync.Mutex
	specs []domain.RunSpec
}

func (r *turns) add(s domain.RunSpec) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, s)
	return len(r.specs)
}

func (r *turns) all() []domain.RunSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.specs)
}

// doneLabel changes app.js, adds a test for it, and is item 1 done.
func doneLabel(t *testing.T, dir string) buildstep.ItemResult {
	t.Helper()
	change(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "label.test.js"), []byte("test('the sidebar label is new', () => {})\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return buildstep.ItemResult{ID: "1", Status: "done", Summary: "เปลี่ยนป้ายแล้ว", Files: []string{"app.js"},
		Tests: []buildstep.ItemTest{{File: "tests/label.test.js", Name: "the sidebar label is new"}}}
}

var answeredOwner = buildstep.ItemResult{ID: "2", Status: "answered", Summary: "ตอบแล้ว", Note: "เมนูรายชื่อดีลมีคอลัมน์ Sales"}

func report(t *testing.T, a agentRun, items ...buildstep.ItemResult) {
	t.Helper()
	raw, err := json.Marshal(buildstep.ItemReport{Job: a.spec.JobID.String(), Items: items})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.spec.WorkDir, buildstep.ItemsFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func traceText(run pipelineRun) string {
	var b strings.Builder
	if run.store.trace != nil {
		for _, e := range run.store.trace.Events {
			b.WriteString(e.Text + "\n")
		}
	}
	return b.String()
}

func TestTheAgentIsToldEachItemCRNCounted(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, agent: func(a agentRun) {
		seen.add(a.spec)
		report(t, a, doneLabel(t, a.spec.WorkDir), answeredOwner)
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	specs := seen.all()
	if len(specs) != 1 {
		t.Fatalf("the agent ran %d times, want once", len(specs))
	}
	for _, want := range []string{"2 items", "[1] Rename the sidebar label", "[2] How do I see a deal's owner?",
		buildstep.ItemsFile, specs[0].JobID.String(), "done", "answered", "exists", "wont", "not_done"} {
		if !strings.Contains(specs[0].Prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, specs[0].Prompt)
		}
	}
	if got := git(t, run.remote, "show", run.head(t)+":"+buildstep.ItemsFile); !strings.Contains(got, `"answered"`) {
		t.Errorf("the pushed commit's %s = %s", buildstep.ItemsFile, got)
	}
	if !strings.Contains(traceText(run), "| 1 | Rename the sidebar label | ทำแล้ว |") {
		t.Errorf("the trace has no table of the items:\n%s", traceText(run))
	}
}

func TestAnItemLeftOutGoesBackToTheAgentOnce(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, agent: func(a agentRun) {
		if seen.add(a.spec) == 1 {
			report(t, a, doneLabel(t, a.spec.WorkDir))
			return
		}
		report(t, a, doneLabel(t, a.spec.WorkDir), answeredOwner)
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	specs := seen.all()
	if len(specs) != 2 {
		t.Fatalf("the agent ran %d times, want twice", len(specs))
	}
	if specs[1].ResumeSessionID != "s1" {
		t.Errorf("the second turn resumed %q, want the first turn's session s1", specs[1].ResumeSessionID)
	}
	if !strings.Contains(specs[1].Prompt, "item 2 has no entry") {
		t.Errorf("the second turn is not told what was wrong:\n%s", specs[1].Prompt)
	}
}

func TestItemsStillWrongAfterTheirSecondTurnFailTheBuild(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, agent: func(a agentRun) {
		seen.add(a.spec)
		change(t, a.spec.WorkDir)
	}})
	failedAndNeverReleased(t, run)
	if n := len(seen.all()); n != 2 {
		t.Errorf("the agent ran %d times, want twice", n)
	}
	if !strings.Contains(failedMessage(run), buildstep.ItemsFile+" is missing") {
		t.Errorf("the failure does not say why: %s", failedMessage(run))
	}
	if run.head(t) != run.seed {
		t.Errorf("it pushed a commit over items it never reported:\n%s", events(run))
	}
}

// One item is the whole request: the agent's closing list says what it did, as before.
func TestASingleItemNeedsNoItemReport(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: "Rename the sidebar label", agent: func(a agentRun) {
		seen.add(a.spec)
		change(t, a.spec.WorkDir)
	}})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if n := len(seen.all()); n != 1 || strings.Contains(seen.all()[0].Prompt, buildstep.ItemsFile) {
		t.Errorf("a one-item change ran the agent %d times or asked for %s", n, buildstep.ItemsFile)
	}
}

// reviewed answers as the builder until it is the reviewer (a run with tools named),
// which says what review says.
func reviewed(t *testing.T, seen *turns, review string, builder func(a agentRun, turn int)) func(agentRun) {
	return func(a agentRun) {
		turn := seen.add(a.spec)
		if len(a.spec.Tools) > 0 {
			if err := a.emit(domain.ClaudeEvent{Type: domain.ClaudeAssistant, Text: review}); err != nil {
				t.Fatal(err)
			}
			return
		}
		builder(a, turn)
	}
}

const confirmBoth = `Checked. {"items":[{"id":"1","verdict":"confirmed","reason":"อยู่ใน app.js"},{"id":"2","verdict":"confirmed","reason":"ถูกต้อง"}]}`

func TestABigRequestIsReviewedByASecondAgentThatOnlyReads(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, reviewMin: 2,
		agent: reviewed(t, &seen, confirmBoth, func(a agentRun, _ int) { report(t, a, doneLabel(t, a.spec.WorkDir), answeredOwner) })})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	specs := seen.all()
	if len(specs) != 2 {
		t.Fatalf("%d runs, want the builder and the reviewer", len(specs))
	}
	r := specs[1]
	if !slices.Equal(r.Tools, []string{"Read", "Grep", "Glob"}) || r.ResumeSessionID != "" {
		t.Errorf("the reviewer ran with tools %q and session %q, want Read, Grep, Glob and a fresh one", r.Tools, r.ResumeSessionID)
	}
	for _, want := range []string{"Rename the sidebar label", buildstep.ItemsFile, "Do not change", "confirmed", "not_confirmed"} {
		if !strings.Contains(r.Prompt, want) {
			t.Errorf("the reviewer's prompt lacks %q:\n%s", want, r.Prompt)
		}
	}
	if !strings.Contains(traceText(run), "| 1 | Rename the sidebar label | ทำแล้ว | เปลี่ยนป้ายแล้ว | ยืนยัน |") {
		t.Errorf("the table has no reviewer's column:\n%s", traceText(run))
	}
	if run.store.trace == nil || run.store.trace.SessionID == "review-1" {
		t.Errorf("the reviewer's session became the build's, which the next edit resumes")
	}
}

func TestWhatTheReviewerDoesNotConfirmGoesBackToTheBuilder(t *testing.T) {
	var seen turns
	notTwo := `{"items":[{"id":"1","verdict":"confirmed","reason":"ok"},{"id":"2","verdict":"not_confirmed","reason":"ไม่มีคอลัมน์ Sales ในโค้ด"}]}`
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, reviewMin: 2,
		agent: reviewed(t, &seen, notTwo, func(a agentRun, turn int) {
			fixed := answeredOwner
			if turn > 1 {
				fixed.Note = "แก้คำตอบ: ดูที่หน้ารายละเอียดดีล"
			}
			report(t, a, doneLabel(t, a.spec.WorkDir), fixed)
		})})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	specs := seen.all()
	if len(specs) != 3 {
		t.Fatalf("%d runs, want builder, reviewer, builder", len(specs))
	}
	if specs[2].ResumeSessionID != "s1" || !strings.Contains(specs[2].Prompt, "ไม่มีคอลัมน์ Sales ในโค้ด") {
		t.Errorf("the builder's last turn (session %q) is not given the reviewer's reason:\n%s", specs[2].ResumeSessionID, specs[2].Prompt)
	}
	if !strings.Contains(traceText(run), "ไม่ยืนยัน: ไม่มีคอลัมน์ Sales ในโค้ด") || !strings.Contains(traceText(run), "แก้คำตอบ") {
		t.Errorf("the table does not show the reviewer's word and what came of it:\n%s", traceText(run))
	}
}

// The reviewer's tools cannot write, and a tree that changed under it anyway is not shipped.
func TestAReviewerThatChangesTheTreeFailsTheBuild(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, reviewMin: 2,
		agent: func(a agentRun) {
			if seen.add(a.spec); len(a.spec.Tools) > 0 {
				if err := os.WriteFile(filepath.Join(a.spec.WorkDir, "app.js"), []byte("reviewer\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				_ = a.emit(domain.ClaudeEvent{Type: domain.ClaudeAssistant, Text: confirmBoth})
				return
			}
			report(t, a, doneLabel(t, a.spec.WorkDir), answeredOwner)
		}})
	failedAndNeverReleased(t, run)
	if !strings.Contains(failedMessage(run), "reviewer") {
		t.Errorf("the failure does not name the reviewer: %s", failedMessage(run))
	}
}

// A review CRN cannot read does not stop the build; the table says there was none.
func TestAnUnreadableReviewIsSaidSoAndTheBuildGoesOn(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, reviewMin: 2,
		agent: reviewed(t, &seen, "Everything looks fine to me.", func(a agentRun, _ int) { report(t, a, doneLabel(t, a.spec.WorkDir), answeredOwner) })})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if !strings.Contains(traceText(run), "ผู้ตรวจไม่ได้ส่งผลที่อ่านได้") {
		t.Errorf("the trace does not say the review was unreadable:\n%s", traceText(run))
	}
}

// Below the threshold there is no reviewer.
func TestASmallRequestIsNotReviewed(t *testing.T) {
	var seen turns
	run := runPipelineWith(t, pipelineOpts{helper: true, noList: true, delivery: localDelivery, change: twoItemChange, reviewMin: 3,
		agent: reviewed(t, &seen, confirmBoth, func(a agentRun, _ int) { report(t, a, doneLabel(t, a.spec.WorkDir), answeredOwner) })})
	if !hasStatus(run, "released") {
		t.Fatalf("no released:\n%s\n%s", events(run), run.logs)
	}
	if n := len(seen.all()); n != 1 {
		t.Errorf("%d runs for a request under the threshold, want the builder's only", n)
	}
}
