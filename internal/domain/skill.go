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

// SkillVersion is a point-in-time snapshot of a skill, recorded on every
// user-initiated change (an operator edit via PUT or a zip upload). It captures
// the resulting state so history can be browsed. Builtin re-seeds on startup do
// NOT record versions.
type SkillVersion struct {
	Version     int               `json:"version"`
	Description string            `json:"description"`
	Body        string            `json:"body"`
	Files       map[string]string `json:"files"`
	Note        string            `json:"note"`
	CreatedAt   time.Time         `json:"created_at"`
}
