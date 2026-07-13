// Package feedback mirrors in-demo feedback requests to GitHub issues: it turns
// a feedback row into issue content and runs the background watcher that creates
// the issues (see docs/superpowers/specs/2026-07-13-feedback-to-github-issues-design.md).
package feedback

import (
	"fmt"
	"strings"
	"time"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// IssueContent renders a feedback request into a GitHub issue title, body, and
// label set. Deterministic — no network, no LLM.
func IssueContent(f *domain.FeedbackRequest) (title, body string, labels []string) {
	return issueTitle(f), issueBody(f), []string{
		"fitt:feedback", "type:" + f.Category, "prio:" + f.Priority,
	}
}

func issueTitle(f *domain.FeedbackRequest) string {
	summary := firstLine(f.Note)
	if summary == "" && len(f.Payload.Pins) > 0 {
		summary = f.Payload.Pins[0].Label
	}
	if summary == "" {
		summary = "no description"
	}
	if len(summary) > 60 {
		summary = strings.TrimSpace(summary[:60]) + "…"
	}
	return "[feedback] " + f.Category + ": " + summary
}

func issueBody(f *domain.FeedbackRequest) string {
	reporter := f.Reporter
	if reporter == "" {
		reporter = "anonymous"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Category:** %s · **Priority:** %s\n", f.Category, f.Priority)
	fmt.Fprintf(&b, "**Reported:** %s by %s\n", f.CreatedAt.Format(time.RFC3339), reporter)
	if f.PageURL != "" {
		fmt.Fprintf(&b, "**Page:** %s\n", f.PageURL)
	}
	if note := strings.TrimSpace(f.Note); note != "" {
		b.WriteString("\n" + note + "\n")
	}
	if len(f.Payload.Pins) > 0 {
		b.WriteString("\n### Pins\n")
		for _, p := range f.Payload.Pins {
			label := p.Label
			if label == "" {
				label = "element"
			}
			fmt.Fprintf(&b, "- 📍 **%s**", label)
			if p.Selector != "" {
				fmt.Fprintf(&b, " — `%s`", p.Selector)
			}
			b.WriteString("\n")
			if n := strings.TrimSpace(p.Note); n != "" {
				fmt.Fprintf(&b, "  %s\n", n)
			}
		}
	}
	fmt.Fprintf(&b, "\n<sub>Mirrored from CRN feedback %s. Screenshots viewable in the CRN Edit Request Panel.</sub>\n", f.ID)
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
