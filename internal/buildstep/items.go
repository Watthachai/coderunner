package buildstep

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A change request can hold many items (OKR #6, 16 Nov: 16 from UAT; the agent did 2 and
// CRN shipped them, because nothing checked the items). CRN counts the items itself,
// asks the agent for a report on each in ItemsFile, and checks that report against the
// tree before anything is committed.

// ItemsFile is the agent's report on the request's items, at the project's root. It is
// committed with the build, so the repository says what each build did with each item.
const ItemsFile = "CRN_ITEMS.json"

// Item is one thing the request asks for, as CRN counts them.
type Item struct {
	ID    string
	Title string
}

// ItemReport is ItemsFile: which build wrote it, and one result per item.
type ItemReport struct {
	Job   string       `json:"job"`
	Items []ItemResult `json:"items"`
}

// ItemResult is the agent's word on one item. Status is one of ItemStatuses. A done item
// names the files it changed and the tests that show it (or, in Note, why none can); any
// other status explains itself in Note: the answer, where it already is, or why not.
type ItemResult struct {
	ID      string     `json:"id"`
	Status  string     `json:"status"`
	Summary string     `json:"summary"`
	Files   []string   `json:"files,omitempty"`
	Tests   []ItemTest `json:"tests,omitempty"`
	Note    string     `json:"note,omitempty"`
}

// ItemTest is a test in the project: its file and its name as written there.
type ItemTest struct {
	File string `json:"file"`
	Name string `json:"name"`
}

// ItemStatuses are the statuses a result may have, with how DV's table says each.
var ItemStatuses = map[string]string{
	"done":     "ทำแล้ว",
	"answered": "ตอบเป็นคำถาม",
	"exists":   "มีอยู่แล้ว",
	"wont":     "ไม่ทำ",
	"not_done": "ยังไม่ได้ทำ",
}

// noteSays is what Note must hold for a status other than done.
var noteSays = map[string]string{
	"answered": "the answer",
	"exists":   "where it already is",
	"wont":     "the reason",
	"not_done": "the reason",
}

// titleRunes is the longest title an item gets; the request itself holds the rest.
const titleRunes = 120

var (
	envelopeRe = regexp.MustCompile(`(?m)^FITTCORE-CHANGE-REQUEST v1 boundary=(\S+)`)
	numberedRe = regexp.MustCompile(`^(\d{1,3})[.)]\s+(\S.*)$`)
	feedbackRe = regexp.MustCompile(`(?s)\[Customer feedback\](.*?)\[/Customer feedback\]`)
)

// SplitChange counts the items of a change request. Each [Customer feedback] block is an
// item; failing that, a list numbered 1, 2, 3… at the start of lines is one item per
// number; failing that, the whole is one item. In a FITTCORE-CHANGE-REQUEST envelope each
// of its items is split so, numbered 2.1, 2.2… when the envelope holds more than one.
func SplitChange(change string) []Item {
	bodies := envelopeItems(change)
	var out []Item
	for i, body := range bodies {
		parts := splitBody(body)
		for j, title := range parts {
			id := strconv.Itoa(j + 1)
			if len(bodies) > 1 {
				id = strconv.Itoa(i + 1)
				if len(parts) > 1 {
					id += "." + strconv.Itoa(j+1)
				}
			}
			out = append(out, Item{ID: id, Title: shorten(title)})
		}
	}
	return out
}

// EnvelopeItemNumbers are the item=N numbers of the change's FITTCORE-CHANGE-REQUEST, in
// the order it writes them; nil when the change has no envelope. A picture sent with an
// edit names one of them (internal/attach).
func EnvelopeItemNumbers(change string) []int {
	m := envelopeRe.FindStringSubmatch(change)
	if m == nil {
		return nil
	}
	itemLine := regexp.MustCompile(`^--` + regexp.QuoteMeta(m[1]) + ` item=(\d{1,6})(?:\s|$)`)
	var out []int
	for _, line := range strings.Split(change, "\n") {
		if n := itemLine.FindStringSubmatch(strings.TrimRight(line, "\r")); n != nil {
			v, _ := strconv.Atoi(n[1])
			out = append(out, v)
		}
	}
	return out
}

// envelopeItems are the bodies of the envelope's items, or the change itself when it has
// no envelope.
func envelopeItems(change string) []string {
	m := envelopeRe.FindStringSubmatch(change)
	if m == nil {
		return []string{change}
	}
	mark := "--" + m[1] + " "
	var bodies []string
	var cur []string
	in := false
	for _, line := range strings.Split(change, "\n") {
		if strings.HasPrefix(line, mark) {
			if in {
				bodies = append(bodies, strings.Join(cur, "\n"))
			}
			cur, in = nil, strings.HasPrefix(line, mark+"item=")
			continue
		}
		if in {
			cur = append(cur, line)
		}
	}
	if in {
		bodies = append(bodies, strings.Join(cur, "\n"))
	}
	if len(bodies) == 0 {
		return []string{change}
	}
	return bodies
}

// splitBody returns the title of each item in one body.
func splitBody(body string) []string {
	if blocks := feedbackRe.FindAllStringSubmatch(body, -1); len(blocks) > 0 {
		var titles []string
		for _, b := range blocks {
			titles = append(titles, feedbackTitle(b[1]))
		}
		return titles
	}
	var titles []string
	for _, line := range strings.Split(body, "\n") {
		m := numberedRe.FindStringSubmatch(strings.TrimLeft(line, "\u200b\ufeff"))
		if m == nil {
			continue
		}
		if m[1] != strconv.Itoa(len(titles)+1) {
			titles = nil // not one list counted from 1: not a list of items
			break
		}
		titles = append(titles, strings.TrimSpace(m[2]))
	}
	if len(titles) > 1 {
		return titles
	}
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(strings.TrimLeft(line, "\u200b\ufeff")); t != "" {
			return []string{t}
		}
	}
	return []string{""}
}

// feedbackTitle is a feedback block's words on one line, and its page.
func feedbackTitle(block string) string {
	var words []string
	page := ""
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "Page:"):
			page = strings.TrimSpace(strings.TrimPrefix(line, "Page:"))
		case strings.HasPrefix(line, "Pins:"):
		default:
			words = append(words, line)
		}
	}
	title := strings.Join(words, " ")
	if page != "" {
		title += " (" + page + ")"
	}
	return title
}

func shorten(s string) string {
	r := []rune(s)
	if len(r) <= titleRunes {
		return s
	}
	return string(r[:titleRunes-1]) + "…"
}

// CheckItems reads ItemsFile in dir and returns it with what is wrong with it: an item of
// want it leaves out or repeats, one want does not have, a status it does not know, a
// done item without a file this build changed since base or with a test the project does
// not hold, any other status without its note. job is this build's id, which the report
// must carry, so an earlier build's report is never taken for this one's.
func CheckItems(dir, base, job string, want []Item) (ItemReport, []string) {
	var r ItemReport
	raw, err := os.ReadFile(filepath.Join(dir, ItemsFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, []string{ItemsFile + " is missing"}
	}
	if err != nil {
		return r, []string{"read " + ItemsFile + ": " + err.Error()}
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, []string{ItemsFile + " is not valid JSON: " + err.Error()}
	}
	var problems []string
	if r.Job != job {
		problems = append(problems, fmt.Sprintf("%s says job %q: it is not this build's (job %s)", ItemsFile, r.Job, job))
	}
	changed, err := changedSince(dir, base)
	if err != nil {
		return r, append(problems, "list the files this build changed: "+err.Error())
	}
	known := map[string]bool{}
	for _, it := range want {
		known[it.ID] = true
	}
	seen := map[string]int{}
	for _, res := range r.Items {
		seen[res.ID]++
		if !known[res.ID] {
			problems = append(problems, fmt.Sprintf("item %s is not one of the request's", res.ID))
			continue
		}
		problems = append(problems, checkResult(dir, res, changed)...)
	}
	for _, it := range want {
		switch n := seen[it.ID]; {
		case n == 0:
			problems = append(problems, fmt.Sprintf("item %s has no entry", it.ID))
		case n > 1:
			problems = append(problems, fmt.Sprintf("item %s has more than one entry", it.ID))
		}
	}
	return r, problems
}

func checkResult(dir string, res ItemResult, changed map[string]bool) []string {
	var problems []string
	say := func(format string, a ...any) {
		problems = append(problems, fmt.Sprintf("item %s: ", res.ID)+fmt.Sprintf(format, a...))
	}
	if _, ok := ItemStatuses[res.Status]; !ok {
		say("status %q is not one of done, answered, exists, wont, not_done", res.Status)
		return problems
	}
	if strings.TrimSpace(res.Summary) == "" {
		say("no summary")
	}
	if res.Status != "done" {
		if strings.TrimSpace(res.Note) == "" {
			say("%s needs %s in note", res.Status, noteSays[res.Status])
		}
		return problems
	}
	if len(res.Files) == 0 {
		say("done but names no changed file")
	}
	for _, f := range res.Files {
		if !filepath.IsLocal(f) {
			say("%s is not a path in the project", f)
		} else if !changed[filepath.ToSlash(filepath.Clean(f))] {
			say("%s did not change in this build", f)
		}
	}
	if len(res.Tests) == 0 && strings.TrimSpace(res.Note) == "" {
		say("done with no test and no note why")
	}
	for _, tc := range res.Tests {
		if !filepath.IsLocal(tc.File) {
			say("test file %s is not a path in the project", tc.File)
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, tc.File))
		switch {
		case err != nil:
			say("test file %s does not exist", tc.File)
		case strings.TrimSpace(tc.Name) == "" || !strings.Contains(string(body), tc.Name):
			say("%s has no test %q", tc.File, tc.Name)
		}
	}
	return problems
}

// changedSince is every file that differs from base in dir: changed, added (tracked or
// not, unless ignored), removed, or committed since.
func changedSince(dir, base string) (map[string]bool, error) {
	changed := map[string]bool{}
	for _, args := range [][]string{
		{"-c", "core.quotepath=false", "diff", "--name-only", "-z", base},
		{"-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard", "-z"},
	} {
		out, err := gitCommand(context.Background(), dir, args...).Output()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", args[2], err)
		}
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				changed[f] = true
			}
		}
	}
	return changed, nil
}

// Verdict is the reviewer's word on one item: confirmed or not_confirmed, and why.
type Verdict struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// ParseVerdicts reads the last {"items": [...]} the reviewer wrote in text, which must
// give every item of want a verdict of confirmed or not_confirmed.
func ParseVerdicts(text string, want []Item) (map[string]Verdict, error) {
	for at := strings.LastIndex(text, "{"); at >= 0; at = strings.LastIndex(text[:at], "{") {
		var v struct {
			Items []Verdict `json:"items"`
		}
		if err := json.NewDecoder(strings.NewReader(text[at:])).Decode(&v); err != nil || v.Items == nil {
			continue
		}
		got := map[string]Verdict{}
		for _, x := range v.Items {
			got[x.ID] = x
		}
		for _, it := range want {
			x, ok := got[it.ID]
			if !ok {
				return nil, fmt.Errorf("the review gives item %s no verdict", it.ID)
			}
			if x.Verdict != "confirmed" && x.Verdict != "not_confirmed" {
				return nil, fmt.Errorf("the review's verdict on item %s is %q, not confirmed or not_confirmed", it.ID, x.Verdict)
			}
		}
		return got, nil
	}
	return nil, errors.New(`the review holds no {"items": [...]}`)
}

// ItemsTable is the report as DV asked for it (18 Nov): one row per item in the request's
// order, its status in DV's words, and, when there was a review, the reviewer's verdict.
func ItemsTable(want []Item, r ItemReport, review map[string]Verdict) string {
	byID := map[string]ItemResult{}
	for _, res := range r.Items {
		byID[res.ID] = res
	}
	var b strings.Builder
	b.WriteString("| # | รายการ | สถานะ | รายละเอียด |")
	sep := "|---|---|---|---|"
	if review != nil {
		b.WriteString(" ผู้ตรวจ |")
		sep += "---|"
	}
	b.WriteString("\n" + sep + "\n")
	for _, it := range want {
		res, ok := byID[it.ID]
		status, detail := "ไม่มีรายงาน", ""
		if ok {
			status = ItemStatuses[res.Status]
			if status == "" {
				status = res.Status
			}
			detail = res.Summary
			if strings.TrimSpace(res.Note) != "" {
				detail += ": " + res.Note
			}
		}
		row := []string{it.ID, it.Title, status, detail}
		if review != nil {
			v, ok := review[it.ID]
			switch {
			case !ok:
				row = append(row, "—")
			case v.Verdict == "confirmed":
				row = append(row, "ยืนยัน")
			default:
				row = append(row, strings.TrimSuffix("ไม่ยืนยัน: "+v.Reason, ": "))
			}
		}
		b.WriteString("|")
		for _, cell := range row {
			b.WriteString(" " + cellText(cell) + " |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// cellText keeps a value on one line of one table cell.
func cellText(s string) string {
	s = strings.NewReplacer("|", "/", "\r", " ", "\n", " ").Replace(strings.TrimSpace(s))
	return strings.Join(strings.Fields(s), " ")
}

// ItemIDs are the ids of items, in order.
func ItemIDs(items []Item) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return slices.Clip(ids)
}

// reviewShownBytes is the largest new file the review diff shows in full.
const reviewShownBytes = 256 * 1024

// lockfile is a file the review diff names but does not show: generated, and long.
func lockfile(path string) bool {
	switch filepath.Base(path) {
	case "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "go.sum":
		return true
	}
	return false
}

// WriteReviewDiff writes to out what the build changed in dir since base, for the
// reviewer: the diff of tracked files, then each new file in full (lockfiles and large or
// binary files by name only). It returns every changed file, sorted.
func WriteReviewDiff(dir, base, out string) ([]string, error) {
	changed, err := changedSince(dir, base)
	if err != nil {
		return nil, err
	}
	diff, err := gitCommand(context.Background(), dir, "-c", "core.quotepath=false", "diff", base, "--",
		".", ":(exclude,glob)**/package-lock.json", ":(exclude,glob)**/pnpm-lock.yaml", ":(exclude,glob)**/yarn.lock").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	untracked, err := gitCommand(context.Background(), dir, "-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var b strings.Builder
	b.Write(diff)
	for _, f := range strings.Split(string(untracked), "\x00") {
		if f == "" {
			continue
		}
		b.WriteString("\n=== new file " + f + " ===\n")
		body, err := os.ReadFile(filepath.Join(dir, f))
		switch {
		case err != nil:
			b.WriteString("(unreadable: " + err.Error() + ")\n")
		case lockfile(f) || len(body) > reviewShownBytes || !utf8Valid(body):
			b.WriteString(fmt.Sprintf("(%d bytes, not shown)\n", len(body)))
		default:
			b.Write(body)
		}
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	files := make([]string, 0, len(changed))
	for f := range changed {
		files = append(files, f)
	}
	slices.Sort(files)
	return files, nil
}

func utf8Valid(b []byte) bool { return utf8.Valid(b) && !bytes.Contains(b, []byte{0}) }

// TreeFingerprint is a hash of everything in dir git sees: what differs from HEAD, and
// the bytes of every untracked file. Two equal fingerprints mean nothing was changed.
func TreeFingerprint(dir string) (string, error) {
	h := sha256.New()
	for _, args := range [][]string{
		{"status", "--porcelain=v1", "-uall", "-z"},
		{"diff", "HEAD", "--binary"},
	} {
		out, err := gitCommand(context.Background(), dir, args...).Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		h.Write(out)
		h.Write([]byte{0})
	}
	untracked, err := gitCommand(context.Background(), dir, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}
	for _, f := range strings.Split(string(untracked), "\x00") {
		if f == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return "", err
		}
		h.Write([]byte(f))
		h.Write([]byte{0})
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
