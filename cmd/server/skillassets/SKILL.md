---
name: fitt-build
description: Convert or update a FITT Builder prototype as a customer-deliverable Next.js application with persistent data, authenticated permissions, versioned migrations and executable release checks. Use for initial CRN builds and issue-driven edits; preserve customer data and the existing product scope.
---

# FITT build — customer delivery

A build is one unattended job with a bounded implement → verify → repair loop. Produce a release candidate, not a demo. A successful compile is not evidence that business behavior, permissions or upgrades work. Do not invent test results or remove requirements to get a green build.

## Read what applies

- `references/nextjs-conversion.md`: preserve the actual wired screens, styles, assets and interactions when converting Vite to Next.js App Router.
- `references/prisma-setup.md`: database constraints, migrations and first-install bootstrap. Required for persistent apps.
- `references/auth-and-bootstrap.md`: authenticated sessions, permissions and the once-only initial administrator. Required when the app has login or private data.
- `references/delivery-checks.md`: executable validation contract and bounded repair. Required on every build and edit.
- `references/test-cases.md`: requirement-linked customer acceptance instructions. Keep results not actually executed blank.

## Establish scope before writing

Read IDEA/BRD/PRD, the actual import graph/router and the existing repository. Write `PORT_CHECKLIST.md` mapping each reachable screen, data entity and must-level requirement to its implementation and executable test. Identify unreachable source with evidence; do not silently omit PRD requirements. Preserve UI and business rules. Remove demo rows, mocked successful requests, simulated authentication, fake role switching and production placeholders. Reference data required by the business is allowed and must be identified.

Initial build: extract the supplied zip. Existing project or issue edit: inspect and preserve its repository, migration history, user accounts and customer data. Never reset a database, replay an initial migration under a new name, or replace a working authentication provider to simplify the build. If a fresh export replaces an existing app, retrieve its committed migrations before authoring new ones; if that history is unavailable, report a blocked upgrade rather than inventing a new baseline.

## Implement a real application

- Read the installed Next.js docs before relying on framework conventions. Resolve compatible dependency versions once; pin direct dependencies to exact versions, commit the npm lockfile, and use `npm ci` thereafter. Do not run unbounded `@latest` upgrades during issue edits. Record Node/Next/React/Prisma versions in BUILD_NOTES.
- Preserve each wired screen and interaction. Use App Router, standalone output and server-side reads/writes. No database access during compilation. No success toast before a write commits.
- Derive required, unique, foreign-key and money precision constraints from the brief. Enforce validation, authorization and tenant ownership on every server write. Preserve required relationships; never make them nullable merely to make schema deployment easier.
- Use transactions and database constraints for stock, payments, document numbers and booking invariants. Handle duplicate/concurrent writes deliberately. Service integrations need timeouts and truthful errors; use test doubles during verification, never send real customer mail/payments from tests.
- Every entity required for operation must have a real input/import or derived write path. Document necessary administration flows. Render empty states on a fresh database; no invented records.
- Keep existing production authentication. For a new local-password app, use a maintained auth/session library and hashed passwords. The initial administrator is created once per customer database, never once per build. See auth-and-bootstrap.md.
- Add error boundaries and useful health checks without exposing secrets. Do not ship developer role switches, bypasses, default shared passwords or automatic feedback submission containing customer data.

## Verify within this build

Implement meaningful unit and browser tests, including the actual requested change. Run the packaged delivery checker described in delivery-checks.md. Repair a failing check at most twice within this job, rerunning affected tests and the full release gate. If it still fails or needs unavailable external credentials, stop with a concrete failure report; never mark the result released or delete/skip the failing test. Do not trigger a separate customer ticket to conceal incomplete work.

For initial installs test an empty disposable database. For issue edits also test the upgrade path using an isolated fixture at the previous migration state; preserve a sentinel user, its password hash and a business record. Repeated starts and repeated bootstrap with changed/absent bootstrap inputs must not create accounts or reset credentials.

## Deliver evidence and instructions

Ship `BUILD_NOTES.md`, `TEST_CASES.md`, `PORT_CHECKLIST.md`, `crn-delivery.json`, the lockfile, committed migrations, automated tests and the checker receipt. Document required environment variables, first-install account setup, backup, upgrade and recovery. Separate automated results from customer acceptance steps. Never include live secrets, bootstrap passwords or production database dumps in artifacts.

CRN writes the Docker delivery bundle. It must apply committed migrations and run the once-only bootstrap before serving; it must fail visibly on migration/bootstrap error. Both source verification and image creation must succeed before an image can be called deliverable. Passing checks covers the tested scope, not every possible production scenario.
