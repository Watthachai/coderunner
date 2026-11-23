package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/buildsettings"
	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// A change request can hold many items (OKR #6, 16 Nov: 16 from UAT; the agent did 2, and
// CRN shipped them because nothing held the agent to each item). CRN counts the items
// (buildstep.SplitChange), asks the agent for a report on each in buildstep.ItemsFile,
// and checks the report against the tree. What is wrong goes back to the agent once; if
// it is still wrong the build fails and nothing is pushed. A request of reviewMinItems
// items or more also goes to a second agent that only reads, and checks each item
// against the code; what it does not confirm goes back to the builder once.

// reviewTools are all the reviewer gets: it reads, and changes nothing.
var reviewTools = []string{"Read", "Grep", "Glob"}

// SetReviewMinItems sets the item count from which a second agent reviews a build's
// items; zero turns the review off.
func (m *manager) SetReviewMinItems(n int) { m.reviewMinItems = n }

// itemsPrompt is what the edit prompt adds for a request of several items.
func itemsPrompt(items []buildstep.Item, jobID uuid.UUID) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nCRN's instructions about this request's items (not part of the request): CRN counted %d items in it:\n", len(items))
	for _, it := range items {
		fmt.Fprintf(&b, "[%s] %s\n", it.ID, it.Title)
	}
	b.WriteString("Before you end your turn, write " + buildstep.ItemsFile + " at the project root, replacing any earlier one, as " +
		`{"job": "` + jobID.String() + `", "items": [{"id": "1", "status": "done", "summary": "…", "files": ["…"], "tests": [{"file": "…", "name": "…"}], "note": "…"}]}` +
		", with exactly one entry for each id above. status is one of: " +
		"done (changed in this build: files are the files you changed for it; tests are tests in the project, by file and by name exactly as written there, that show it works; if no test can, say why in note), " +
		"answered (a question: the answer, in note), exists (already so: where, in note), wont (you will not do it: why, in note), " +
		"not_done (you could not finish it: why, in note). Write summary and note in Thai, for the customer. " +
		"CRN checks this file against the tree before it commits: an item left out, a file this build did not change or a test that is not there " +
		"sends the build back to you once, and then fails it.")
	return b.String()
}

func itemsFollowUp(problems []string) string {
	return "CRN checked " + buildstep.ItemsFile + " against the tree and found:\n- " + strings.Join(problems, "\n- ") +
		"\nFix each one: do the work an item still needs, or correct its entry so it says what is true. Keep to every rule of this build. " +
		"End your turn when " + buildstep.ItemsFile + " is right: CRN checks it again, and fails the build if it is still wrong."
}

func reviewFollowUp(items []buildstep.Item, review map[string]buildstep.Verdict) string {
	var b strings.Builder
	b.WriteString("An independent reviewer read the code and did not confirm these items:\n")
	for _, it := range items {
		if v := review[it.ID]; v.Verdict == "not_confirmed" {
			fmt.Fprintf(&b, "- item %s (%s): %s\n", it.ID, it.Title, v.Reason)
		}
	}
	b.WriteString("For each one, either fix the code (and its test) so the item is really done, or correct its entry in " + buildstep.ItemsFile +
		" (status, summary, note) so it says what is true. Leave the other items' work as it is. " +
		"CRN checks " + buildstep.ItemsFile + " again, and the build's report shows both the reviewer's word and yours.")
	return b.String()
}

func reviewPrompt(items []buildstep.Item, change, diffPath string, files []string) string {
	var b strings.Builder
	b.WriteString("You are reviewing a finished build for CRN. Do not change any file: your tools only read. " +
		"The change request is at the end. CRN counted these items in it:\n")
	for _, it := range items {
		fmt.Fprintf(&b, "[%s] %s\n", it.ID, it.Title)
	}
	b.WriteString("The builder reported on each item in " + buildstep.ItemsFile + " at the project root. " +
		"The whole diff of this build against the commit it started from is in " + diffPath + ". " +
		"For each item, check the builder's claim against the code itself, not against the builder's words: " +
		"done: the change is in the code, does what the item asks, and the named tests test it; answered: the answer is true of the code; " +
		"exists: it is really there; wont or not_done: the reason holds. " +
		`End your reply with one JSON object and nothing after it: {"items": [{"id": "1", "verdict": "confirmed", "reason": "…"}]}, ` +
		"one entry per item, verdict confirmed or not_confirmed, the reason short and in Thai.\n\nFiles this build changed:\n")
	for _, f := range files {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("\nThe change request:\n\n" + change)
	return b.String()
}

// agentTurns runs the build's agent, and runs it again when CRN hands it back what is
// wrong; it also runs the reviewer. A run that returns false has ended the job.
type agentTurns struct {
	m       *manager
	ctx     context.Context // the parent: a job is finished with it, so a cancel can be recorded
	runCtx  context.Context
	job     *domain.Job
	gate    *jobGate
	workDir string
	cfg     buildsettings.Resolved
	log     *slog.Logger
	meta    *traceMeta
	// costBase is the session the build resumed: its earlier runs are other builds' cost.
	costBase     string
	builderCost  float64
	reviewerCost float64
	// pictures is the prompt's section on the request's pictures; the reviewer is shown them too.
	pictures string
}

// run is one turn of the builder: the build's own session, resumed from resume.
func (a *agentTurns) run(prompt, resume string) (domain.RunResult, bool) {
	m, ctx, job, log := a.m, a.ctx, a.job, a.log
	// Inject every enabled skill into {workDir}/.claude/skills/{name}/SKILL.md so the
	// spawned claude discovers the fitt-build harness. The injected .claude dir is removed
	// after each turn, so it is never committed.
	if err := m.injectSkills(ctx, a.workDir); err != nil {
		log.Error("inject skills failed", "err", err)
		m.finishFailed(ctx, job, "inject skills failed: "+err.Error(), *a.meta)
		return domain.RunResult{}, false
	}
	runSpec := domain.RunSpec{
		JobID:           job.ID,
		ProjectID:       job.ProjectID,
		WorkDir:         a.workDir,
		Prompt:          prompt,
		ResumeSessionID: resume,
		Model:           a.cfg.Model,
		Effort:          a.cfg.Effort,
	}
	if !a.watch(&runSpec) {
		return domain.RunResult{}, false
	}

	var backgrounded []backgroundCall
	refused := map[string]bool{}
	emit := func(ev domain.ClaudeEvent) error {
		backgrounded = append(backgrounded, backgroundWork(ev)...)
		for _, id := range refusedCalls(ev) {
			refused[id] = true
		}
		// Persist the session id as soon as it appears so a future build can
		// --resume even if this run later fails.
		if ev.SessionID != "" && ev.SessionID != job.SessionID {
			job.SessionID = ev.SessionID
			if err := m.store.SetJobSession(ctx, job.ID, ev.SessionID); err != nil {
				log.Warn("persist session id failed", "err", err)
			}
		}
		msg := translate(job.ID, ev)
		// Skip empty assistant chatter (e.g. an assistant turn that only carried
		// a tool_use, already surfaced above as a tool call) so the live console
		// isn't full of blank "text" rows.
		if msg.Kind == domain.WSAssistantText && strings.TrimSpace(msg.Text) == "" {
			return nil
		}
		m.publish(job.ID, msg)
		return nil
	}

	result, runErr := m.runner.Run(a.runCtx, runSpec, emit)
	a.builderCost = result.CostUSD
	if a.costBase != "" {
		traces, err := m.store.ListBuildTraces(ctx, job.ProjectID)
		if err != nil {
			log.Warn("earlier runs' cost unknown; this run's cost includes them", "err", err)
		}
		a.builderCost = runCost(result.CostUSD, a.costBase, job.ID, traces)
	}
	a.meta.cost = a.builderCost + a.reviewerCost
	result.CostUSD = a.meta.cost

	// Remove the injected .claude dir (success OR fail) BEFORE the git phase so
	// the harness skills are never committed/pushed.
	if rmErr := os.RemoveAll(filepath.Join(a.workDir, ".claude")); rmErr != nil {
		log.Warn("remove injected .claude failed", "err", rmErr)
	}

	// Persist the terminal session id from the result line if not already stored.
	if result.SessionID != "" && result.SessionID != job.SessionID {
		job.SessionID = result.SessionID
		if err := m.store.SetJobSession(ctx, job.ID, result.SessionID); err != nil {
			log.Warn("persist terminal session id failed", "err", err)
		}
	}
	if !a.ended(runErr, result.Success) {
		return result, false
	}
	// Claude runs with background tasks off (internal/claude agentEnv), and refuses a
	// call that asks for one (OKR #7: the agent then ran it in the foreground). Should
	// a run still start one, the session may have ended with it unfinished: what it was
	// doing may not be in the tree, so nothing of this run is committed.
	if left := stillInBackground(backgrounded, refused); len(left) > 0 {
		errMsg := "the agent left work running in the background (" + strings.Join(left, "; ") +
			"): its session ends with its turn, so that work may be missing from this build; nothing was committed"
		log.Error("build failed", "err", errMsg, "cost_usd", a.meta.cost)
		m.finishFailed(ctx, job, errMsg, *a.meta)
		return result, false
	}
	return result, true
}

// watch readies a run for the gate: what the agent reads and prints is read too, and its
// process group dies with it. False when the prompt already holds a real value.
func (a *agentTurns) watch(spec *domain.RunSpec) bool {
	if a.gate.listed() {
		// A real value in the request stops here, before it reaches the model.
		a.gate.scan.Scan("the agent's input", []byte(spec.Prompt))
		if err := a.gate.check(); err != nil {
			a.m.finishFailed(a.ctx, a.job, "candidate acceptance: "+err.Error(), *a.meta)
			return false
		}
		spec.StdoutTrace = a.gate.scan.Source("the agent's output")
		spec.StderrTrace = a.gate.scan.Source("the agent's errors")
	}
	if a.gate != nil {
		spec.StopGroupOnExit = true
	}
	return true
}

// ended settles a finished run: false when it ended the job (cancelled, failed, or the
// server shutting down), or when what the agent left could not be stopped.
func (a *agentTurns) ended(runErr error, success bool) bool {
	m, ctx, job, log := a.m, a.ctx, a.job, a.log
	if runErr != nil || !success {
		// runCtx cancelled while the parent is still alive == an operator hit
		// Cancel. Finalize as cancelled (using the parent ctx, which is NOT
		// cancelled) instead of failed.
		if a.runCtx.Err() != nil && ctx.Err() == nil {
			log.Info("build cancelled by operator", "cost_usd", a.meta.cost)
			m.finishCancelled(ctx, job, *a.meta)
			return false
		}
		// Parent ctx cancelled == server shutdown mid-build: leave the job
		// 'building' for startup reconcile rather than recording a false failure.
		if ctx.Err() != nil {
			log.Info("build interrupted by shutdown; leaving for reconcile")
			return false
		}
		errMsg := "claude run reported failure"
		if runErr != nil {
			errMsg = runErr.Error()
		}
		log.Error("build failed", "err", errMsg, "cost_usd", a.meta.cost)
		m.finishFailed(ctx, job, errMsg, *a.meta)
		return false
	}
	// Nothing the agent started goes on running past here, when no other build ran beside it.
	if a.gate != nil {
		if err := m.stopAgent(a.runCtx, job, a.gate, log); err != nil {
			log.Error("the agent could not be stopped", "err", err)
			if a.runCtx.Err() != nil && ctx.Err() == nil {
				m.finishCancelled(ctx, job, *a.meta)
			} else {
				m.finishFailed(ctx, job, err.Error(), *a.meta)
			}
			return false
		}
	}
	return true
}

// review runs the reviewer on a fresh session with tools that only read, and returns
// what it wrote. ok is false when the run ended the job; text is "" when it ran but
// could not finish.
func (a *agentTurns) review(prompt string) (text string, ok bool) {
	spec := domain.RunSpec{
		JobID:     a.job.ID,
		ProjectID: a.job.ProjectID,
		WorkDir:   a.workDir,
		Prompt:    prompt,
		Model:     a.cfg.Model,
		Effort:    a.cfg.Effort,
		Tools:     reviewTools,
	}
	if !a.watch(&spec) {
		return "", false
	}
	var said []string
	// The reviewer's session is its own: it is never stored as the build's, which the
	// project's next edit resumes.
	emit := func(ev domain.ClaudeEvent) error {
		if ev.Type == domain.ClaudeAssistant && strings.TrimSpace(ev.Text) != "" {
			said = append(said, ev.Text)
		}
		msg := translate(a.job.ID, ev)
		if msg.Kind == domain.WSAssistantText {
			if strings.TrimSpace(msg.Text) == "" {
				return nil
			}
			msg.Text = "🔎 ผู้ตรวจ: " + msg.Text
		}
		a.m.publish(a.job.ID, msg)
		return nil
	}
	result, runErr := a.m.runner.Run(a.runCtx, spec, emit)
	a.reviewerCost += result.CostUSD
	a.meta.cost = a.builderCost + a.reviewerCost
	if runErr != nil || !result.Success {
		if a.runCtx.Err() != nil || a.ctx.Err() != nil {
			return "", a.ended(runErr, result.Success)
		}
		a.log.Warn("the reviewer did not finish", "err", runErr)
		a.m.publishText(a.job.ID, "ผู้ตรวจรันไม่จบ จึงไม่มีผลตรวจซ้ำในรอบนี้")
		return "", true
	}
	if !a.ended(nil, true) {
		return "", false
	}
	return strings.Join(said, "\n"), true
}

// holdToItems checks the builder's report on the items, hands it back what is wrong once,
// has a big request reviewed, and publishes the table of the items. It returns the
// builder's last result, and false when it ended the job.
func (a *agentTurns) holdToItems(items []buildstep.Item, base, change string, last domain.RunResult) (domain.RunResult, bool) {
	m, job := a.m, a.job
	r, problems := buildstep.CheckItems(a.workDir, base, job.ID.String(), items)
	if len(problems) > 0 {
		m.publishText(job.ID, fmt.Sprintf("CRN ตรวจ %s แล้วพบ %d จุดที่ยังไม่ครบ ส่งกลับให้ agent แก้อีกรอบ:\n- %s",
			buildstep.ItemsFile, len(problems), strings.Join(problems, "\n- ")))
		var ok bool
		if last, ok = a.again(itemsFollowUp(problems), last); !ok {
			return last, false
		}
		if r, problems = buildstep.CheckItems(a.workDir, base, job.ID.String(), items); len(problems) > 0 {
			return last, a.itemsFailed(problems)
		}
	}

	var review map[string]buildstep.Verdict
	if m.reviewMinItems > 0 && len(items) >= m.reviewMinItems {
		verdicts, ok := a.reviewItems(items, base, change)
		if !ok {
			return last, false
		}
		review = verdicts
		if unconfirmed(review) > 0 {
			m.publishText(job.ID, fmt.Sprintf("ผู้ตรวจไม่ยืนยัน %d ข้อ ส่งกลับให้ agent แก้หรือชี้แจง", unconfirmed(review)))
			if last, ok = a.again(reviewFollowUp(items, review), last); !ok {
				return last, false
			}
			if r, problems = buildstep.CheckItems(a.workDir, base, job.ID.String(), items); len(problems) > 0 {
				return last, a.itemsFailed(problems)
			}
		}
	}
	m.publishText(job.ID, "ผลรายข้อ (CRN ตรวจกับโค้ดแล้ว):\n"+buildstep.ItemsTable(items, r, review))
	return last, true
}

// again is one more turn of the builder, in its own session.
func (a *agentTurns) again(prompt string, last domain.RunResult) (domain.RunResult, bool) {
	resume := last.SessionID
	if resume == "" {
		resume = a.job.SessionID
	}
	next, ok := a.run(prompt, resume)
	if next.SessionID == "" {
		next.SessionID = last.SessionID
	}
	return next, ok
}

func (a *agentTurns) itemsFailed(problems []string) bool {
	errMsg := buildstep.ItemsFile + " is still wrong after the agent's second turn: " + strings.Join(problems, "; ") + "; nothing was committed"
	a.log.Error("build failed", "err", errMsg)
	a.m.finishFailed(a.ctx, a.job, errMsg, *a.meta)
	return false
}

// reviewItems has the reviewer check each item, and fails the build if the tree changed
// under it. A review CRN cannot read is said so, and the build goes on without it.
func (a *agentTurns) reviewItems(items []buildstep.Item, base, change string) (map[string]buildstep.Verdict, bool) {
	m, job := a.m, a.job
	dir, err := os.MkdirTemp("", "crn-review-")
	if err != nil {
		m.finishFailed(a.ctx, job, "prepare the review: "+err.Error(), *a.meta)
		return nil, false
	}
	defer os.RemoveAll(dir)
	diff := filepath.Join(dir, "build.diff")
	files, err := buildstep.WriteReviewDiff(a.workDir, base, diff)
	if err != nil {
		m.finishFailed(a.ctx, job, "prepare the review: "+err.Error(), *a.meta)
		return nil, false
	}
	before, err := buildstep.TreeFingerprint(a.workDir)
	if err != nil {
		m.finishFailed(a.ctx, job, "prepare the review: "+err.Error(), *a.meta)
		return nil, false
	}
	m.publishText(job.ID, fmt.Sprintf("ส่ง %d ข้อให้ผู้ตรวจอีกตัวตรวจกับโค้ด (อ่านอย่างเดียว)", len(items)))
	started := time.Now()
	text, ok := a.review(reviewPrompt(items, change, diff, files) + a.pictures)
	if !ok {
		return nil, false
	}
	after, err := buildstep.TreeFingerprint(a.workDir)
	if err != nil || after != before {
		errMsg := "the reviewer changed the project's files, which it may only read; nothing was committed"
		if err != nil {
			errMsg = "read the tree after the review: " + err.Error()
		}
		a.log.Error("build failed", "err", errMsg)
		m.finishFailed(a.ctx, job, errMsg, *a.meta)
		return nil, false
	}
	a.log.Info("items reviewed", "took", time.Since(started).Round(time.Second), "cost_usd", a.reviewerCost)
	if text == "" {
		return nil, true
	}
	review, err := buildstep.ParseVerdicts(text, items)
	if err != nil {
		m.publishText(job.ID, "ผู้ตรวจไม่ได้ส่งผลที่อ่านได้ ("+err.Error()+") ตารางจึงไม่มีคอลัมน์ผู้ตรวจ")
		return nil, true
	}
	return review, true
}

func unconfirmed(review map[string]buildstep.Verdict) int {
	n := 0
	for _, v := range review {
		if v.Verdict == "not_confirmed" {
			n++
		}
	}
	return n
}
