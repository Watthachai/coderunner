package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltinSkillShipsEveryAsset guards the gap that shipped references/test-cases.md
// into the repo but never into a build.
//
// builtinSkillFiles is hand-maintained: a new file under skillassets/ needs a
// //go:embed directive AND a map entry. Miss the second and nothing complains —
// the build compiles, the skill seeds, and SKILL.md cites a reference the build
// agent never receives. This test is the thing that complains.
func TestBuiltinSkillShipsEveryAsset(t *testing.T) {
	root := "skillassets"

	var onDisk []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// SKILL.md is builtinSkillBody, not a Files entry.
		if rel == "SKILL.md" {
			return nil
		}
		// macOS drops .DS_Store into any browsed directory. It is gitignored and
		// must not be shipped; named explicitly rather than skipping all dotfiles,
		// because assets/.dockerignore IS shipped.
		if d.Name() == ".DS_Store" {
			return nil
		}
		onDisk = append(onDisk, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(onDisk) == 0 {
		t.Fatalf("no asset files found under %s — is the test running in the package dir?", root)
	}

	for _, rel := range onDisk {
		body, ok := builtinSkillFiles[rel]
		if !ok {
			t.Errorf("skillassets/%s is not in builtinSkillFiles — embed it and add the map entry, "+
				"or every build will be missing it", rel)
			continue
		}
		if strings.TrimSpace(body) == "" {
			t.Errorf("builtinSkillFiles[%q] is empty — the //go:embed directive is probably wrong", rel)
		}
	}

	// The other direction: a map entry pointing at a file that no longer exists
	// would ship an empty file into every build.
	for rel := range builtinSkillFiles {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("builtinSkillFiles has %q but %s/%s does not exist", rel, root, rel)
		}
	}
}

// TestBuiltinSkillReferencesAreShipped checks the other half of the same failure:
// SKILL.md naming a reference file that is not among the shipped files. The body
// cites them as bare names ("see test-cases.md"), so match on the base name.
func TestBuiltinSkillReferencesAreShipped(t *testing.T) {
	shipped := map[string]bool{}
	for rel := range builtinSkillFiles {
		shipped[filepath.Base(rel)] = true
	}

	entries, err := os.ReadDir(filepath.Join("skillassets", "references"))
	if err != nil {
		t.Fatalf("read references dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.Contains(builtinSkillBody, name) {
			continue // not cited by SKILL.md; nothing to check
		}
		if !shipped[name] {
			t.Errorf("SKILL.md cites %q but it is not shipped with the skill", name)
		}
	}
}
