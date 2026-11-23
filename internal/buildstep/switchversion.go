package buildstep

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
)

// switchV2 marks the service-status switch that knows read-only (FITT Billing, 23 Nov 2026).
var switchV2 = []byte("export const SWITCH_VERSION = 2;")

// OlderServiceStatusSwitch reports whether the app under dir carries a copy of the
// service-status switch older than version 2. Dependencies and build output are not the
// app's copy.
func OlderServiceStatusSwitch(dir string) bool {
	older := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || older {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".next", ".git", ".claude", "out", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "service-status.mjs" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err == nil && !bytes.Contains(raw, switchV2) {
			older = true
		}
		return nil
	})
	return older
}
