package domain

import "time"

// Skill is one Claude Agent Skill (a SKILL.md body plus metadata). Enabled
// skills are injected into every build's working dir as
// {workdir}/.claude/skills/{name}/SKILL.md before Claude is spawned. The
// built-in `fitt-build` skill is seeded on startup and is editable but not
// deletable (is_builtin = true).
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	// Files is the OPTIONAL set of extra files shipped alongside SKILL.md, keyed
	// by path relative to the skill dir (e.g. "scripts/setup.sh",
	// "references/stack.md"). SKILL.md is NOT in here — it stays in Body. On
	// injection each entry is written to {workdir}/.claude/skills/{name}/{path}.
	// Always non-nil after a store read (defaults to an empty map).
	Files     map[string]string `json:"files"`
	Enabled   bool              `json:"enabled"`
	IsBuiltin bool              `json:"is_builtin"`
	UpdatedAt time.Time         `json:"updated_at"`
}
