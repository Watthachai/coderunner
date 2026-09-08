# Authentication and once-only first-install administrator

The customer needs access on the first installation. That is not permission to ship a permanent demo bypass. Preserve an existing production identity provider and account model. For a new password-based application use a maintained auth library; implement hashed passwords, expiring authenticated sessions, logout/revocation, login rate limits, CSRF/origin protection and server-side authorization. Never trust a raw user-id or role cookie.

## Bootstrap state belongs to the customer database

Use the shipped `assets/bootstrap-once.mjs` helper from the seed, copied into prisma/. Add a `SystemBootstrap` model with string primary key `id` and `completedAt` timestamp in a committed migration. Adapt the Prisma client import to the installed major.

The helper takes an existing Prisma client and two callbacks using the provided transaction:
- `hasUsers(tx)` checks for any existing application users.
- `createAdmin(tx, {email, password})` hashes the password with the app's password hasher, creates the first administrator and marks password change required. It must not log/return the password or perform external effects.

The helper serializes installers with a PostgreSQL advisory transaction lock. If completion is already recorded, it returns before reading bootstrap credentials. If users already exist (adopting an older customer DB), it records completion without adding an account. Otherwise it validates BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD, creates the administrator and records completion in one transaction. Rollback on failure permits a clean retry. Concurrent starts create one administrator. Existing customer users are never overwritten.

No fallback password. The operator supplies a unique initial secret through deployment secrets; never put it in source, image layers, callbacks, traces or BUILD_NOTES. After the first installation the operator can remove the bootstrap variables. An issue build or ordinary restart does not read them once the DB marker exists. The administrator remains an ordinary account after changing its initial password; the bootstrap credential must not be checked at normal login. Deleting an account must not resurrect it on restart: the marker remains.

For external SSO, provision the initial authorized administrator through the real provider's supported first-install process instead; do not silently replace SSO with a local-password demo. If provider configuration is missing, report the missing deployment requirement.

## Permissions and tests

Use real persisted roles and server-side ownership checks. Remove unrestricted role-switch controls; a support impersonation feature, if explicitly required, needs authorization, audit and visible state. Test every role that is in scope using fixtures in a disposable test DB, never seeded customer demo users.

Test: wrong password, malformed/expired session, logged-out protected read/write, forbidden role write, cross-tenant read/write, logout, password change, two simultaneous bootstraps, rerun after deletion, and an upgrade preserving the original password hash. Tests may create disposable fixtures; deployment seeds may not.
