# Customer delivery skill validation

Validated locally on 7 September 2026. This is source validation; no live Runner restart, skill-database update, customer deployment or paid Claude conversion was performed.

## Implemented behavior

- The core harness removes demo authentication/data shortcuts, preserves the product scope, pins compatible direct dependencies and requires a lockfile, real server permissions and versioned migrations.
- First-install bootstrap uses a PostgreSQL transaction lock and a durable completion marker. Existing users are adopted unchanged. Repeated, concurrent or later ticket starts do not reset credentials; deleted users are not recreated. Failure rolls back cleanly.
- Customer startup applies committed migrations and once-only bootstrap with failure stopping startup. No schema push, destructive-reset fallback or shared login password is emitted.
- The release checker actually installs, typechecks, runs tests, compiles, builds the customer Docker image, runs its migration/seed tooling and executes Playwright against it. Empty, skipped or flaky required test results fail. The Runner executes its own embedded checker before Git export and image publication. Existing migration files cannot be rewritten by issue edits.
- The job records enabled skill content hashes. Source-only cache reuse cannot bypass a customer verification run. Issue closure follows successful image delivery when image mode is enabled. Cancellation is propagated to verification, Git and image commands.

## Evidence

- All Go package tests, go vet and go build passed.
- Four release-contract regression tests passed, including rejection of stale/fabricated success receipts, skipped tests, inconsistent dependency locks and inherited host secrets.
- PostgreSQL/Prisma integration: all six behavioral subtests passed (seven reported tests including the parent). Twelve concurrent installers produced one administrator and one marker. Further cases covered unchanged user hash/business records, deletion, adoption, rollback/retry and missing/weak initial inputs.
- `TestDeliveryCustomerRuntime` passed through the Go wrapper using an isolated Next.js/Prisma fixture, Docker PostgreSQL, the actual runtime Dockerfile and Chromium. Its two unit tests and two browser tests passed. Browser assertions verified account preservation and real create/reload/delete persistence. See customer-runtime.json for the command receipt.
- Core plus all nine companion skill frontmatters validated. Fixed the charts description's invalid angle brackets.
- The portable fixture generator was executed and verified. Test-owned Docker containers/networks/images were cleaned up. Existing Runner database containers were left running.

Fixture versions: host Node 24.19.0; Docker Node 22; Next 16.2.9; React 19.2.7; Prisma 6.19.0; Vitest 3.2.4; Playwright 1.55.1. The fixture is test scaffolding, not a production authentication example. Prisma 7 and external identity/provider configurations must pass the gate in their own generated application; this fixture does not establish their behavior.

## Activation and limits

The host's Node 26 browser installer stalled while extracting Chromium. The same fixture passed with Node 24 LTS. The verifier now explicitly accepts Node 22/24 and the Runner supports `CRN_VERIFY_NODE` to select that runtime consistently for npm and Playwright.

Build/restart the backend after active jobs finish to refresh the embedded fitt-build harness. Its enabled flag is preserved. Upload the changed companion skill files separately and verify their enabled state. Source edits do not update already uploaded DB records automatically. Set CRN_BUILD_IMAGE=true when a published customer image is the required deliverable. Materialize-only mode with both AI and image creation disabled remains a development path.

These checks establish the tested bootstrap/delivery behavior, not that every possible customer application will succeed without further work. The agent is instructed to repair within the same job up to twice; unresolved failures block delivery. No actual customer export or paid agent session was run in this validation. Earlier Runner control-API/terminal authentication and WebSocket-origin review findings remain outside this skill change, so this does not certify the entire Runner as production-secure.

Reproduction instructions and runtime requirements are in the repository README. The generated fixture can be recreated with tests/create-runtime-fixture.py.
