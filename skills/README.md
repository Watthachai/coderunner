# Skills — capability modules injected into every build

`fitt-build` (in `cmd/server/skillassets/`) is the built-in harness: it owns the
port itself — inventory, conversion, Prisma, Docker, the finish gate. It is
seeded by the server and lives in Go.

Everything here is different: **uploadable skills** that teach the build agent
how to do one recurring job well. They are kept in this repo so they are
reviewable and versioned, then uploaded to CRN, which stores them in the DB and
writes every ENABLED one into `{build}/.claude/skills/<name>/` before spawning
Claude Code.

| skill | owns |
|---|---|
| `thai-formatting` | the **values** — money, dates, VAT, Thai text, `lib/format.ts` |
| `data-tables` | list / search / filter / sort / pagination screens |
| `charts` | chart screens and the server→client aggregation seam |
| `printable-documents` | documents meant for paper — print CSS, บาทถ้วน, running numbers |
| `mail-service` | outbound email through the DMailService API |
| `calendar-scheduling` | month/week grids, slot availability, double-booking checks |
| `explainable-scoring` | a derived score and the breakdown that must add up to it |
| `role-gated-ui` | who may see and who may do — enforced on the server |
| `barcode-scanning` | scan input, code lookup, and the not-found/duplicate paths |

> `mail-service` was authored directly in the CRN dashboard and existed only as a
> row in the database. It is mirrored here so a lost volume or a new machine
> cannot take it with them; the copy came from `GET /internal/skills/mail-service`.

## Uploading

```bash
cd skills/<name> && zip -r /tmp/<name>.zip . && cd -
curl -sS -F "file=@/tmp/<name>.zip" http://<crn-host>:8080/internal/skills/upload
```

The zip needs a `SKILL.md` (at the root or under one common top directory);
everything else is kept at its relative path. Uploading the same name again
records a new version — the dashboard shows the diff.

## What a skill here must be

**Progressive disclosure decides everything.** Only the frontmatter `name` and
`description` sit in a build's context; the body loads only if the model decides
the description matches what it is doing. So the description is the product:
name the concrete triggers (in Thai, since that is what the screens say) and the
symptoms, and end with when to skip. A vague description either never fires or
fires on every build. Keep it under 1024 characters.

**Explain HOW, never mandate WHAT.** The build is a port. A skill says how to
render the chart the prototype already has; it must never cause a chart, a
column, or a document to appear that the prototype did not have.

**Assume an empty database.** Delivered demos seed the login account and nothing
else, so every screen's first render has zero rows. Guards for that belong
inside the snippet, not in a closing note.

**Never ask a question.** Builds run unattended (`claude -p`). A skill that
stops to ask has stopped the build. Tell it how to decide instead.

**Never fake data.** No placeholder rows, no sample line items, no invented
chart points. An empty screen says it is empty, in Thai.

**Ground every rule in a snippet** that can be pasted as-is, and verify claims
before writing them down — `th-TH` already formats in พ.ศ., a Prisma `Decimal`
in a `reduce(…, 0)` concatenates into `฿NaN`, and headless-browser PDF does not
exist inside `.next/standalone`. All three were checked by running them.

**Stay in your lane.** These skills can co-load on one screen: a document shows
a table of amounts. Overlapping helpers are how an axis reads `2026` while the
table beside it reads `2569`. Cross-reference the owner instead of redefining
its helper.
