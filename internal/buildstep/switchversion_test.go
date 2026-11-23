package buildstep

import (
	"os"
	"path/filepath"
	"testing"
)

// An app built before 23 Nov 2026 carries switch version 1, which knows no read-only; the
// release check now refuses it, so CRN tells the agent to put version 2 in. OKR keeps its
// copy at src/platform/service-status.mjs.
func TestAnOlderSwitchIsFound(t *testing.T) {
	write := func(dir, rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	old := "// FITT service-status switch — shipped by Code Runner.\nexport const EXEMPT_PATHS = [];\n"
	current := "// FITT service-status switch, version 2\nexport const SWITCH_VERSION = 2;\n"

	dir := t.TempDir()
	write(dir, "src/platform/service-status.mjs", old)
	write(dir, "node_modules/x/service-status.mjs", current)
	if !OlderServiceStatusSwitch(dir) {
		t.Error("version 1 at src/platform not found")
	}
	write(dir, "src/platform/service-status.mjs", current)
	if OlderServiceStatusSwitch(dir) {
		t.Error("version 2 taken for an older one")
	}
	write(dir, ".next/server/service-status.mjs", old)
	if OlderServiceStatusSwitch(dir) {
		t.Error("build output counted as the app's switch")
	}
	if OlderServiceStatusSwitch(t.TempDir()) {
		t.Error("an app without the switch has no older one")
	}
}
