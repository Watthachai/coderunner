package main

import _ "embed"

// builtinSkillName is the name of the seeded built-in harness skill.
const builtinSkillName = "fitt-build"

// builtinSkillDescription is the seeded skill's one-line description (it also
// appears verbatim as the `description:` front-matter inside builtinSkillBody /
// skillassets/SKILL.md — keep the two in sync). Third person; says what + when.
// Must stay <= 1024 chars.
const builtinSkillDescription = "Convert or update a FITT Builder prototype as a customer-deliverable Next.js application with persistent data, authenticated permissions, versioned migrations and executable release checks. Use for initial CRN builds and issue-driven edits; preserve customer data and the existing product scope."

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
	//go:embed skillassets/references/test-cases.md
	refTestCases string
	//go:embed skillassets/assets/Dockerfile
	assetDockerfile string
	//go:embed skillassets/assets/.dockerignore
	assetDockerignore string
)

// builtinSkillFiles is the set of extra files shipped with the built-in skill,
// keyed by path relative to the skill dir (SKILL.md is NOT here — it is
// builtinSkillBody). It is wired into the seed as domain.Skill.Files.
//
// Adding a file under skillassets/ is NOT enough: it must be embedded above and
// listed here, or SKILL.md ends up citing a reference the build never receives.
// TestBuiltinSkillShipsEveryAsset fails when the two drift apart.
//
//go:embed skillassets/references/auth-and-bootstrap.md
var refAuthBootstrap string

//go:embed skillassets/references/delivery-checks.md
var refDeliveryChecks string

//go:embed skillassets/assets/bootstrap-once.mjs
var assetBootstrap string

//go:embed skillassets/scripts/verify-delivery.mjs
var deliveryVerifier string

var builtinSkillFiles = map[string]string{
	"references/auth-and-bootstrap.md": refAuthBootstrap,
	"references/delivery-checks.md":    refDeliveryChecks,
	"assets/bootstrap-once.mjs":        assetBootstrap,
	"scripts/verify-delivery.mjs":      deliveryVerifier,
	"references/nextjs-conversion.md":  refNextjsConversion,
	"references/prisma-setup.md":       refPrismaSetup,
	"references/test-cases.md":         refTestCases,
	"assets/Dockerfile":                assetDockerfile,
	"assets/.dockerignore":             assetDockerignore,
}
