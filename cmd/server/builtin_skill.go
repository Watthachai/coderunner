package main

import _ "embed"

// builtinSkillName is the name of the seeded built-in harness skill.
const builtinSkillName = "fitt-build"

// builtinSkillDescription is the seeded skill's one-line description (it also
// appears verbatim as the `description:` front-matter inside builtinSkillBody /
// skillassets/SKILL.md — keep the two in sync). Third person; says what + when.
// Must stay <= 1024 chars.
const builtinSkillDescription = "Convert a FITT Builder export (a Vite + React SPA prototype zip plus IDEA/BRD/PRD briefs) in the current directory into a runnable Next.js (App Router, TypeScript) app backed by Prisma + PostgreSQL with a Docker build, faithfully preserving the prototype UI/UX. Use at the start of every Code Runner build."

// builtinSkillBody is the SKILL.md for the built-in `fitt-build` skill. It and
// the reference/asset files below are embedded from cmd/server/skillassets/ so
// the backtick-heavy markdown stays a real, lintable file (no Go raw-string
// escaping). The code is the source of truth: EnsureBuiltinSkill re-applies this
// body (plus description and files) on every startup via ON CONFLICT (name) DO
// UPDATE, while PRESERVING the operator's enabled flag.
//
//go:embed skillassets/SKILL.md
var builtinSkillBody string

// The reference guides and Docker templates shipped alongside SKILL.md. Claude
// reads references/*.md during the conversion and copies assets/* into the
// project as the Docker starting point. Each is written by InjectSkills to
// {workdir}/.claude/skills/fitt-build/<key>.
var (
	//go:embed skillassets/references/nextjs-conversion.md
	refNextjsConversion string
	//go:embed skillassets/references/prisma-setup.md
	refPrismaSetup string
	//go:embed skillassets/assets/Dockerfile
	assetDockerfile string
	//go:embed skillassets/assets/.dockerignore
	assetDockerignore string
)

// builtinSkillFiles is the OPTIONAL set of extra files shipped with the built-in
// skill, keyed by path relative to the skill dir (SKILL.md is NOT here — it is
// builtinSkillBody). It is wired into the seed as domain.Skill.Files.
var builtinSkillFiles = map[string]string{
	"references/nextjs-conversion.md": refNextjsConversion,
	"references/prisma-setup.md":      refPrismaSetup,
	"assets/Dockerfile":               assetDockerfile,
	"assets/.dockerignore":            assetDockerignore,
}
