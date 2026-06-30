package main

// builtinSkillName is the name of the seeded built-in harness skill.
const builtinSkillName = "fitt-build"

// builtinSkillDescription is the seeded skill's one-line description (also the
// `description:` front-matter inside builtinSkillBody).
const builtinSkillDescription = "Turn a FITT Builder export (a web-prototype zip plus IDEA/BRD/PRD briefs) in the current directory into a clean, installable, buildable project that faithfully reproduces the prototype. Use at the start of every Code Runner build."

// builtinSkillBody is the verbatim SKILL.md for the built-in `fitt-build` skill.
// The code is the source of truth: EnsureBuiltinSkill re-applies this body (and
// description/files) on every startup via ON CONFLICT (name) DO UPDATE, while
// PRESERVING the operator's enabled flag. fitt-build ships with NO extra files
// (Files empty) — it is SKILL.md-only by design.
const builtinSkillBody = `---
name: fitt-build
description: Turn a FITT Builder export (a web-prototype zip plus IDEA/BRD/PRD briefs) in the current directory into a clean, installable, buildable project that faithfully reproduces the prototype. Use at the start of every Code Runner build.
---

# fitt-build — FITT Builder export → runnable project

You are in a working directory that contains ONE export zip from FITT Builder and possibly brief files. Produce a clean, self-contained project that **installs and builds**, faithfully reproducing the prototype. Do NOT redesign.

## Steps

1. **Extract.** Find the ` + "`*.zip`" + ` in this directory, extract it in place, then delete the zip.
2. **Understand.** Read ` + "`IDEA.md`" + ` / ` + "`BRD.md`" + ` / ` + "`PRD.md`" + ` (root or ` + "`docs/`" + `) if present — enough to write one or two honest sentences about the product later.
3. **Detect the stack (do not assume).** From the extracted files:
   - ` + "`package.json`" + ` present → Node. Look at dependencies + scripts to tell which: **Vite**, **Next.js**, **Create React App / react-scripts**, **Astro**, **SvelteKit**, **Vue**, or plain bundler.
   - ` + "`index.html`" + ` and no ` + "`package.json`" + ` → static site (serve as-is).
   - Otherwise infer from the files; pick the simplest correct setup.
4. **Make it install and build.**
   - Node: install with the project's lockfile if present (` + "`npm ci`" + ` when a ` + "`package-lock.json`" + ` exists, else ` + "`npm install`" + `). Then run the project's build script (usually ` + "`npm run build`" + `) and make it pass. Fix ONLY what blocks install or build — a missing dependency, a wrong import path, a bad entry point. Keep diffs minimal.
   - Static: confirm ` + "`index.html`" + ` and its asset paths resolve.
   - Never upgrade frameworks, restyle, rename components, or add features. **Reproduce faithfully.**
5. **Project hygiene.**
   - Ensure a ` + "`.gitignore`" + ` that ignores ` + "`node_modules/`" + `, ` + "`dist/`" + `, ` + "`build/`" + `, ` + "`.next/`" + `, ` + "`.env*`" + `.
   - If ` + "`package.json`" + ` "name" is a placeholder, set it to a kebab-case slug of the product name.
   - Never add secrets; never commit ` + "`node_modules`" + `.
6. **Write ` + "`BUILD_NOTES.md`" + `** at the root with: the product in 1–2 lines, the stack you detected, the exact commands to install / dev / build, and a bullet list of everything you changed and why.
7. **Finish** when the project installs and builds cleanly. If you genuinely cannot make the build pass, stop and write the exact failure + the blocking error in BUILD_NOTES.md rather than pretending success.

## Hard rules
- Faithful reproduction — no redesign, restyle, or feature additions.
- Minimal diffs; explain every change in BUILD_NOTES.md.
- Honest outcome — never fake a passing build.
`
