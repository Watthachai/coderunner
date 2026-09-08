# Persistent data and customer upgrades

Preserve the PRD's constraints: required fields stay required, unique identifiers stay unique, foreign keys retain ownership, amounts use explicit decimal precision. A default is a business decision, not a workaround. For populated databases use expand → backfill → constrain migrations. Do not blanket-fill unknown values with zero/empty strings or change required relations to optional.

## Version and client contract

Pin matching exact `prisma` and `@prisma/client` versions and compatible driver adapters. Read that installed major's documentation. Prisma 7 uses a root prisma.config.ts and driver adapter; do not combine it with a Prisma 6 configuration snippet. The build image includes the locked dependencies, root Prisma config and prisma/ tooling. Keep config self-contained in those shipped paths. Put runtime DB tooling in production dependencies; do not install a fresh Prisma version inside the runtime image.

Use a server-only client singleton. Convert Decimal/Date values at the server→client boundary. Validate input before writes; check tenant and authenticated user on every query that crosses an ownership boundary. Keep DB pages dynamic so compilation does not connect to a database.

## Migration lifecycle

- Commit `prisma/migrations/` and preserve old migrations verbatim on edits.
- Author migrations against disposable development databases. Use the version-appropriate Prisma migration commands and inspect the SQL.
- `db:deploy` is an npm script that applies committed migrations (`prisma migrate deploy` for supported Prisma 6/7). It is not `db push`, reset or schema recreation.
- `db:seed` runs the once-only bootstrap described in auth-and-bootstrap.md. Keep tooling under prisma/; any root prisma.config.ts imports must exist in the runtime image.
- Applying a migration twice must be a no-op. An upgrade fixture must retain user IDs, password hashes and a representative business record. Destructive changes need an explicit migration/backup/recovery plan; do not accept data loss automatically.
- No `db push` on customer startup. A failed migration stops startup. Keep production DB URLs out of build and test environments.

Sources: https://www.prisma.io/docs/orm/v6/prisma-migrate/workflows/prototyping-your-schema ; https://www.prisma.io/docs/guides/upgrade-prisma-orm/v7

## First-install data

Seed only required reference data and the initial administrator, once per customer database. Never seed mock transactions or a set of fake users. Runtime customer records are created through real application flows. For an application with no local accounts, use a no-op bootstrap rather than inventing login.

Use explicit `db:generate`, `db:deploy` and `db:seed` npm scripts so tooling follows the committed lockfile. `db:generate` needs no live DB; `db:deploy` and `db:seed` run only against the isolated test DB or the operator-supplied customer DB at startup.
