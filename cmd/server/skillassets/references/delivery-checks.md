# Executable customer-delivery contract

Ship `crn-delivery.json` with `{ "schema_version": 1, "database": true }` (use false only for a genuinely static app). The verifier runs locally with Node 22 or 24 LTS and Docker. The same Node must be first on PATH for npm and Playwright. Runner operators can select it with CRN_VERIFY_NODE. It refuses missing documents, unticked required checklist items, missing lockfile, non-exact direct versions or missing test scripts. It then runs clean installation, database-client generation when applicable, typecheck, unit tests, production build, the actual customer Docker image (including runtime migration/seed tools), and browser smoke tests against that image. A failed command stops the release. It uses a disposable PostgreSQL container bound to loopback; it never uses the customer's database URL.

Required npm scripts:
- `typecheck`: `tsc --noEmit` (perform a real typecheck).
- `test:unit`: `vitest run`; accept reporter/outputFile flags. Do not enable passWithNoTests.
- `test:smoke`: `playwright test`; accept `--reporter=json`. Config reads CRN_BASE_URL and does not start another server; use one worker and no retry so flaky tests fail visibly.
- `build`: `next build`; `start`: `next start`.
- DB apps: `db:generate`, `db:deploy`, `db:seed` as described in prisma-setup.md.

Run `node .claude/skills/fitt-build/scripts/verify-delivery.mjs` from the app directory. CRN reruns its own embedded copy before Git export. Receipts are written to `CRN_VERIFICATION.json`; do not fabricate or prepopulate them. Keep raw test artifacts out of the customer source. Test suites must contain actual cases; all required cases must pass with none skipped/flaky. Pin Vitest and Playwright as dev dependencies. Install the matching Playwright Chromium for testing via its local CLI (never upgrade the package).

The disposable database URL is available as DATABASE_URL to tests. CRN_TEST_ADMIN_EMAIL and CRN_TEST_ADMIN_PASSWORD contain the original first-install test credentials even after the verifier removes bootstrap inputs. Use them to assert the original account still authenticates, and never log them.

Browser cases must cover wired routes/empty states, a create→read→update→delete flow, reload persistence, validation and authorization. DB apps must cover bootstrap rerun and upgrade preservation. Use test-only fixture creation guarded by the disposable database, never a production bypass route. Include request/response and visible behavior assertions; screenshots and clicking alone do not assert correctness. Treat optional external integrations with explicit mocks and list the untested live boundary.

The verifier creates temporary images, containers and a private Docker network, then removes its own resources. Host application secrets are not inherited by child commands. The verifier has a 15-minute budget and kills its child process group on cancel. It records command outcomes and test counts, not a claim of comprehensive semantic correctness. On failure inspect the cause, repair at most twice and rerun. Never weaken or skip a test merely to pass. If Docker/browser tooling is missing, fail with that dependency clearly identified.
