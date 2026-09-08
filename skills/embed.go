// Package skills embeds the companion skills kept in this directory so a running
// server can tell whether the copies in its database still match the reviewed
// source.
//
// These are deliberately NOT seeded the way fitt-build is. fitt-build lives in
// cmd/server/skillassets and the code owns it: every restart re-applies it
// (EnsureBuiltinSkill), so binary and database cannot disagree for long. A
// companion skill is uploaded by an operator, who may then legitimately edit it
// in the dashboard — overwriting that on boot would throw their work away. So
// the server reports the difference and lets a human decide.
//
// Editing a SKILL.md here therefore changes nothing about a running CRN until
// someone uploads it. That gap is exactly what this package makes visible.
package skills

import (
	"embed"
	"io/fs"
	"strings"
)

// Only the two shapes a companion skill can have: its SKILL.md, and an optional
// references/ directory. Anything else in a skill directory is not shipped to a
// build and would report as spurious drift, so it is not embedded either.
//
//go:embed */SKILL.md */references
var sources embed.FS

// Source is one companion skill as it exists in this repository: the raw
// SKILL.md, and every extra file keyed by its path relative to the skill
// directory ("references/table-recipes.md"). The keys match what an upload
// stores in domain.Skill.Files, so the two are directly comparable.
type Source struct {
	Body  string
	Files map[string]string
}

// Sources returns every companion skill keyed by its directory name. The
// directory name is not automatically the skill name — an upload takes that
// from the frontmatter — so a caller matching these against database rows must
// resolve the name the same way the upload handler does.
func Sources() map[string]Source {
	out := map[string]Source{}
	// The walk cannot fail: the FS is compiled in, so every path it yields is
	// one embed already validated at build time.
	_ = fs.WalkDir(sources, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dir, rel, ok := strings.Cut(p, "/")
		if !ok {
			return nil
		}
		raw, err := sources.ReadFile(p)
		if err != nil {
			return err
		}
		src, seen := out[dir]
		if !seen {
			src.Files = map[string]string{}
		}
		if rel == "SKILL.md" {
			src.Body = string(raw)
		} else {
			src.Files[rel] = string(raw)
		}
		out[dir] = src
		return nil
	})
	return out
}
