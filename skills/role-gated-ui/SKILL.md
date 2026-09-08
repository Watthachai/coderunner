---
name: role-gated-ui
description: Implement authenticated server-side permissions and tenant ownership for FITT applications with roles, restricted screens, approval flows or private data. Preserve real account roles and verify denied actions; skip public apps without identity or restricted operations.
---

# Real permissions

Preserve the product's role matrix. Authenticate sessions with the application's maintained auth library, load the current account and role from trusted server state, and authorize each server action/route independently. A hidden button is presentation, never authorization. Keep resource ownership and tenant scope in database reads and writes.

Remove demo role switches and raw user-id/role cookies. Do not replace an existing auth provider or collapse all users into an administrator. Test roles with disposable fixture users, not customer seed accounts. Centralize the capability matrix; deny unknown roles/capabilities by default. A legitimate empty queue renders its empty state; it is different from denied access.

For first-install account provisioning follow fitt-build references/auth-and-bootstrap.md: bootstrap once per customer database, preserve accounts and password hashes on every issue build, and never resurrect deleted users. Use real role management only where required by the brief. Do not log credentials or session tokens.

Required tests: unauthenticated read/write, insufficient-role write, cross-tenant access, tampered/expired session, authorized success and logout. Assert server responses and unchanged data after rejection. UI tests verify matching menus/buttons but never substitute for server tests.
