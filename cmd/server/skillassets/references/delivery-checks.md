# Executable customer-delivery contract

Ship `crn-delivery.json` with `{ "schema_version": 1, "database": true }` (use false only for a genuinely static app). The verifier runs locally with Node 22 or 24 LTS and Docker. The same Node must be first on PATH for npm and Playwright. Runner operators can select it with CRN_VERIFY_NODE. It refuses missing documents, unticked required checklist items, missing lockfile, non-exact direct versions, missing test scripts, or a `crn.runtime.json` that does not declare every variable the app reads (references/runs-anywhere.md). It then runs clean installation, database-client generation when applicable, typecheck, unit tests, production build, the actual customer Docker image (including runtime migration/seed tools), and browser smoke tests against that image. A failed command stops the release. It uses a disposable PostgreSQL container bound to loopback; it never uses the customer's database URL.

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

## External PostgreSQL over TLS only

Use this mode only when the request asks for it. The image then only talks to a database outside the container over TLS, and never runs without TLS. Declare it in `crn-delivery.json`:

```json
{ "schema_version": 1, "database": true, "database_mode": "external-tls-only", "database_ca_file": "/run/<dir>/<name>.pem" }
```

`database_ca_file` is where the installer mounts the CA certificate read-only. The image gets the label `fitt.db.mode=external-tls-only`. The app reads three variables, all declared in `crn.runtime.json`:

- `DATABASE_URL`: `...?schema=<schema>&sslmode=verify-full`, used by node-postgres at runtime and in the seed.
- `MIGRATION_DATABASE_URL`: `...?schema=<schema>&sslmode=require&sslaccept=strict&sslcert=<database_ca_file>`, used by `prisma migrate deploy` (Prisma's schema engine). `prisma.config.ts` reads only this variable.
- `DATABASE_CA_FILE`: the CA path. node-postgres trusts only this CA, given as `ssl.ca` with `rejectUnauthorized: true`. Never use `NODE_EXTRA_CA_CERTS`, `rejectUnauthorized: false`, a lenient `checkServerIdentity` or `NODE_TLS_REJECT_UNAUTHORIZED`.

One shared helper validates both URLs and the CA file before anything opens a connection, on every path including migrate: Prisma's schema engine reads the file itself, so check the file first. It refuses any other TLS parameter, a missing variable, a schema that is malformed or `public`, and two URLs that point at different host, port, database, user or schema. The CA file must be a regular file (check with `lstat`: a symlink is refused), readable, and hold exactly one PEM certificate. Errors leave out the URL, credentials and query string.

Add `db:check`: through the runtime's own pool configuration, run `select current_user, current_schema()`. Print one JSON line `{"current_user":"…","current_schema":"…"}`, close the pool and exit 0. On any failure, exit non-zero with a sanitized message. `db:deploy` runs the helper's pre-migrate check and then `prisma migrate deploy`. `db:seed` uses the helper and prints `already-complete` once bootstrapped. These three scripts are the image's migrate-only, seed-only and connection-check commands:

```
docker run --rm -e ... -v <ca>:<database_ca_file>:ro --entrypoint npm <image> --silent run db:deploy
```

In this mode the verifier never offers a plaintext database. It creates PostgreSQL 17.11 stand-ins, from one image pinned by digest, on a private network and generates a throwaway CA whose server certificate carries the stand-in's IP. The app connects as a non-superuser role with a connection limit, to schema `crn_verify`; tests get the host-side URLs in `DATABASE_URL`, `MIGRATION_DATABASE_URL` and `DATABASE_CA_FILE`.

The verifier first runs migrate → seed (three times) → `db:check` → the app and the browser tests. It then checks that nothing of the app sits in `public`, that the role owns every object, and that `_prisma_migrations` matches the image's migration files.

Every case below must FAIL, through runtime (`db:check`), seed and migrate:

- a wrong CA
- the IP not in the SAN
- an expired certificate
- a server without TLS

These configuration errors must also fail, and must not open any connection (a tripwire counts them):

- a missing `MIGRATION_DATABASE_URL`
- `sslmode` missing, or set to `prefer`, `disable` or `no-verify`
- `sslaccept=accept_invalid_certs`, or a missing `sslaccept` or `sslcert`
- a schema of `Film-Job`, `a;drop` or `public`
- a CA file that is missing, a symlink, empty, unreadable, or holds more than one certificate (through runtime, seed and migrate)
- two URLs that differ in any one of host, port, database, user or schema (stops `db:deploy`)

No output may contain a secret, a connection URL, a query string or `user:password@host`. The budget in this mode is 30 minutes.
