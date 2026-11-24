package jobs

import (
	"strings"
	"testing"

	"github.com/Watthachai/fitt-coderunner/internal/buildstep"
)

// From 1 Dec 2026 CRN makes every change customers ask for in OKR, Billing's part
// included. The Billing team leaves its rules for CRN in docs/billing/CRN-HANDOFF.md
// (money and accounting rules, the tests to pass before a build, what not to do) and
// asked CRN to read it before changing src/modules/billing. Every edit build is told to
// read and follow the repository's own instructions for the part it changes.
func TestAnEditReadsTheTeamsHandoffBeforeChangingItsPart(t *testing.T) {
	for name, prompt := range map[string]string{
		"edit":       editPrompt("แก้ยอดรวมในใบแจ้งหนี้"),
		"repo image": repoImagePrompt("แก้ยอดรวมในใบแจ้งหนี้", &buildstep.RepoImage{SchemaVersion: 1, Dockerfile: "Dockerfile"}),
	} {
		for _, want := range []string{"AGENTS.md", "CLAUDE.md", "CRN.md", "CRN-HANDOFF.md", "docs/billing/CRN-HANDOFF.md", "src/modules/billing", "tests"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt does not mention %s:\n%s", name, want, prompt)
			}
		}
	}
}
