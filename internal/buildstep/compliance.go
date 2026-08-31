package buildstep

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// compliance.go checks the generated tree against the harness rules that a green
// `next build` cannot enforce.
//
// The rules live in the fitt-build skill as prose — seed the login account only,
// ship a TEST_CASES.md, tick every PORT_CHECKLIST line — and prose is advice: a
// model that ignores it produces a build that looks perfectly successful. Every
// violation found here has already shipped to a customer at least once, which is
// why the check exists at all.
//
// It only ever reports. Failing a build over a heuristic would trade a silent
// wrong answer for a loud one, and these signals are deliberately shallow: they
// read a couple of files and count things. The operator sees the finding in the
// build trace next to the "✓ pushed" line and decides.

// Finding is one violated rule, ready to show to an operator.
type Finding struct {
	// Rule is a stable slug for logs and future filtering ("seed_rows").
	Rule string
	// Text is the operator-facing line, already carrying its own evidence.
	Text string
}

// seedWrite matches a Prisma write in a seed file: `prisma.user.upsert(`,
// `db.task.createMany(`. The client identifier varies (the skill says `prisma`,
// generated code sometimes says `db`), so the model name is the SECOND segment.
var seedWrite = regexp.MustCompile(`([A-Za-z_]\w*)\.([A-Za-z_]\w*)\.(upsert|create|createMany|createManyAndReturn)\s*\(`)

// uncheckedItem matches an unticked markdown checkbox: "- [ ] port Foo.tsx".
var uncheckedItem = regexp.MustCompile(`^\s*[-*]\s*\[ \]`)

// seedFiles are the seed entry points the skill wires into package.json, in the
// order they are tried. Only the first one found is inspected.
var seedFiles = []string{
	filepath.Join("prisma", "seed.ts"),
	filepath.Join("prisma", "seed.mts"),
	filepath.Join("prisma", "seed.js"),
}

// CheckHarnessRules inspects a finished build's working directory and returns
// what looks wrong. An empty slice means nothing tripped — NOT that the build is
// correct. Missing files are skipped rather than reported when the app has no
// business having them (a static app has no prisma/seed.ts), so the checks are
// individually best-effort and never return an error.
func CheckHarnessRules(dir string) []Finding {
	var findings []Finding
	if f, ok := checkSeed(dir); ok {
		findings = append(findings, f)
	}
	if f, ok := checkTestCases(dir); ok {
		findings = append(findings, f)
	}
	if f, ok := checkPortChecklist(dir); ok {
		findings = append(findings, f)
	}
	return findings
}

// checkSeed flags a seed that looks like it carries demo rows.
//
// A compliant seed upserts ONE thing — the dev Admin account — so it writes a
// single model, once. Anything wider is worth a look: a second model, a bulk
// `createMany`, or a handful of write calls. Reference data the app cannot
// render without is a legitimate exception, which is exactly why this reports
// the model names instead of deciding: the reader checks them against the
// "Seeded reference data" section of BUILD_NOTES.md.
func checkSeed(dir string) (Finding, bool) {
	var body []byte
	for _, name := range seedFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			body = b
			break
		}
	}
	if len(body) == 0 {
		return Finding{}, false // no seed (or unreadable) — nothing to say
	}

	matches := seedWrite.FindAllStringSubmatch(string(body), -1)
	models := map[string]bool{}
	bulk := false
	for _, m := range matches {
		models[m[2]] = true
		if m[3] == "createMany" || m[3] == "createManyAndReturn" {
			bulk = true
		}
	}
	if len(models) == 0 {
		return Finding{}, false // a no-op seed is correct for an app with no login
	}
	if !bulk && len(models) == 1 && len(matches) <= 3 {
		return Finding{}, false // one model, a couple of writes: the login account
	}

	names := make([]string, 0, len(models))
	for m := range models {
		names = append(names, m)
	}
	sort.Strings(names)

	text := "⚠ seed writes " + plural(len(matches), "row", "rows") + " across " +
		plural(len(models), "model", "models") + " (" + strings.Join(names, ", ") + ")"
	if bulk {
		text += " using createMany"
	}
	text += " — the harness seeds the login account only. Check prisma/seed.ts for demo rows; " +
		"reference data must be listed in BUILD_NOTES.md under \"Seeded reference data\"."
	return Finding{Rule: "seed_rows", Text: text}, true
}

// checkTestCases flags a build that shipped without its test script.
func checkTestCases(dir string) (Finding, bool) {
	if _, err := os.Stat(filepath.Join(dir, "TEST_CASES.md")); err == nil {
		return Finding{}, false
	}
	return Finding{
		Rule: "test_cases_missing",
		Text: "⚠ no TEST_CASES.md — every build ships a test script (skill step 9). The customer got a demo with nothing to test it against.",
	}, true
}

// checkPortChecklist flags a build that finished over an unticked checklist.
//
// Lines marked [orphan] are excluded: the port rules keep an unreachable file on
// the list, unticked and with its evidence, precisely so it stays visible — that
// is a declared exclusion, not unfinished work.
func checkPortChecklist(dir string) (Finding, bool) {
	body, err := os.ReadFile(filepath.Join(dir, "PORT_CHECKLIST.md"))
	if err != nil {
		return Finding{}, false // no checklist here (edit builds, static apps)
	}
	open := 0
	for _, line := range strings.Split(string(body), "\n") {
		if uncheckedItem.MatchString(line) && !strings.Contains(strings.ToLower(line), "[orphan]") {
			open++
		}
	}
	if open == 0 {
		return Finding{}, false
	}
	return Finding{
		Rule: "checklist_open",
		Text: "⚠ PORT_CHECKLIST.md still has " + plural(open, "unticked item", "unticked items") +
			" — a green build over an unticked checklist is a failed port (skill step 10). Screens are probably missing.",
	}, true
}

// plural renders "1 row" / "4 rows" — the findings read as sentences, so the
// count and its noun have to agree.
func plural(n int, one, many string) string {
	word := many
	if n == 1 {
		word = one
	}
	return strconv.Itoa(n) + " " + word
}

// LogFindings writes each finding at Warn level with its rule slug, so a build
// that tripped something is greppable in the server log too.
func LogFindings(findings []Finding, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, f := range findings {
		logger.Warn("harness rule violated", "rule", f.Rule, "detail", f.Text)
	}
}
