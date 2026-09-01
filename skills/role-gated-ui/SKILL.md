---
name: role-gated-ui
description: Enforce the per-role rules of a ported FITT demo — who sees a menu item, a column, a button or a whole page, and who is allowed to perform a write — on the server, where hiding actually holds, and keep every role demonstrable even though the delivered app has ONE standardized login. Use when the PRD or prototype mentions บทบาท/สิทธิ์/ระดับผู้ใช้ (ผู้ดูแลระบบ, แอดมิน, ผู้จัดการ, หัวหน้างาน, พนักงาน, ผู้ใช้ทั่วไป, ผู้อนุมัติ), เฉพาะผู้ดูแลระบบ, ต้องได้รับการอนุมัติ, ไม่มีสิทธิ์เข้าถึง, a role column on a user, a menu that differs per user, an approve/reject step, or a screen the prototype showed only to some users. Skip when every user of the app sees exactly the same thing.
---

# role-gated-ui — permissions that survive the port

The prototype faked its roles. It picked one from a dropdown, or hard-coded `currentUser = users[0]`, and every "permission" was `{isAdmin && <Button/>}` in JSX. Porting that literally produces a demo where the button is invisible and **the action still works** — anyone who knows the URL, or re-enables the element in devtools, can do the thing the customer said only a manager may do. The customer's tester will write that up as a security bug, and they will be right.

**Hiding is presentation. Permission is a server-side check.** Do both, in that order of importance.

**Lane.** `data-tables` owns the list screen; this skill only decides which rows and columns it may show. `thai-formatting` owns the Thai strings for values. This skill owns the session, the role read, the gate and the denial message.

## The constraint you cannot design around: there is ONE login

`fitt-build` standardizes every login to a single email+password checked against `DEV_EMAIL` / `DEV_PASSWORD`, and the seed creates exactly one user — `role: "Admin"`. That is deliberate: the operator needs one credential that opens every UAT. It has a consequence this skill exists to handle: **if you gate purely on the logged-in user's stored role, the customer can only ever see the Admin view, and every other role in the PRD becomes untestable.** A demo that cannot show the พนักงาน screen has not delivered the พนักงาน requirement.

So separate the two ideas:

- **Authentication** stays exactly as `fitt-build` mandates. One env credential. Do not add users to the login, do not seed a second password, do not restore the prototype's user picker as a way in.
- **Effective role** is session state that starts at the seeded user's role and, *for an Admin*, can be switched to any role the PRD defines — so the tester can walk through every role's screens with the one credential they were given.

```ts
// lib/session.ts
import { cookies } from "next/headers";

export type Role = "Admin" | "Manager" | "Staff";      // from the PRD, not invented
const ROLES: Role[] = ["Admin", "Manager", "Staff"];

export async function currentUser() {
  const jar = await cookies();                          // Next 16: cookies() is async
  const id = jar.get("uid")?.value;
  const user = id ? await prisma.user.findUnique({ where: { id } }) : null;
  if (!user) return null;

  // Admin may view the app as another role. Anyone else is their own role, always.
  const viewAs = jar.get("viewAs")?.value as Role | undefined;
  const effective: Role =
    user.role === "Admin" && viewAs && ROLES.includes(viewAs) ? viewAs : (user.role as Role);

  return { ...user, role: effective, realRole: user.role as Role };
}
```

Reading `cookies()` opts the route out of static prerender, which is what you want — every gated page is per-request anyway. Keep `export const dynamic = "force-dynamic"`.

The switcher is a small control in the header, visible **only** when `realRole === "Admin"`, labelled plainly (`ดูในมุมมองของ:`) so nobody mistakes it for a real login. It is a declared addition: record it in `BUILD_NOTES.md` under **"Added to keep the app usable"**, exactly like the standardized login. Add it only when the PRD defines more than one role — a single-role app does not need it.

## Gate the action, then the page, then the pixel

Three layers. Skipping the first is the bug.

```ts
// 1. THE WRITE. Every server action re-checks. This is the only real gate.
"use server";
export async function approveOrder(id: string) {
  const me = await currentUser();
  if (!me) redirect("/login");
  if (!can(me.role, "order.approve")) {
    return { error: "คุณไม่มีสิทธิ์อนุมัติรายการนี้" };   // verbatim from the PRD
  }
  await prisma.order.update({ where: { id }, data: { status: "APPROVED" } });
  revalidatePath("/orders");
}
```

```tsx
// 2. THE PAGE. A gated route redirects before it renders anything.
const me = await currentUser();
if (!me) redirect("/login");
if (!can(me.role, "report.view")) redirect("/?denied=report");

// 3. THE PIXEL. Cosmetic only — it stops confusion, never an attacker.
{can(me.role, "order.approve") && <ApproveButton id={o.id} />}
```

A client component cannot call `currentUser()`. Pass the role down as a prop from the server component that already loaded it — never re-fetch it in the browser, and never store it in `localStorage`, where the user edits it.

## One permission table, not scattered booleans

`{user.role === "Admin" || user.role === "Manager"}` sprinkled across twenty components is how a screen ends up disagreeing with the menu that links to it. Put the matrix in one file, keyed by the capability the PRD names:

```ts
// lib/permissions.ts
const MATRIX: Record<Role, string[]> = {
  Admin:   ["*"],
  Manager: ["order.view", "order.approve", "report.view"],
  Staff:   ["order.view", "order.create"],
};
export const can = (role: Role, cap: string) =>
  MATRIX[role]?.some((c) => c === "*" || c === cap) ?? false;
```

Derive the capability list from the PRD's role rules and the BRD's Functional Requirements — **match those sections by keyword** (`บทบาท`, `สิทธิ์`, `ผู้ใช้งานและสิทธิ์`), never by section number, which drifts between generated documents. Every capability you invent that the documents do not name is a redesign.

## Roles are reference data — seed them, seed nothing else

If the schema has a `Role` lookup table the UI reads to draw itself (a dropdown of roles, a settings screen), that is exactly the reference data `fitt-build` permits the seed to create. Seed the role rows with `upsert`, list them in `BUILD_NOTES.md`, and stop there. **Do not seed example users** — no `staff@example.com`, no "สมชาย (พนักงาน)". The user table starts with the one dev Admin and grows from the app's own create form.

An enum on `User.role` needs no seeding at all, and is the better choice when the PRD's roles are a closed list.

## Empty database, and the role screens on it

The user list has exactly one row on first render — the dev Admin — and that is correct, not an empty state. What needs writing is everything gated *below* an empty table: a Manager's approval queue with nothing to approve says **"ไม่มีรายการรอการอนุมัติ"**, not "คุณไม่มีสิทธิ์". Do not confuse *nothing to show* with *not allowed to see* — they are different screens with different fixes, and telling a Manager they lack permission when the queue is simply empty is a bug the customer will report.

Denial itself is a designed screen too: a short Thai explanation and a link back to somewhere the role *can* go. Never a blank page, never a raw 403, never an infinite redirect between two pages neither role may see.

## Rules

- **Every server action and route handler re-checks the role.** No exceptions, including the ones whose button is already hidden.
- **Never trust anything from the client** — not a role in a form field, a header, `localStorage`, or a `viewAs` cookie belonging to a non-Admin. The check above re-reads the user from the database every request.
- **`redirect()` for a denied page, a returned `{ error }` for a denied action.** Throwing inside a server action surfaces as an unhandled boundary error and gets reported to the 🐞 widget as a crash.
- **Copy the denial text verbatim from the PRD's `การควบคุมความถูกต้องของข้อมูล (Validation & Edge Cases)`** — it states the real Thai message for an unauthorized attempt. Do not invent wording.
- **Do not add roles, capabilities or an approval step the documents do not describe.** Port the rules; do not design a permission system.
- **Do not weaken the standardized login** to demonstrate roles. The switcher rides on top of it; it never replaces it.
- **Never ask.** Builds run unattended. If the PRD names a role but no rule for it, give it the narrowest capability set that still lets its screens render, and say so in `BUILD_NOTES.md`.

## Verify before you finish

Signed in with the one env credential, on an **empty** database:

- as Admin every gated screen opens, and the `ดูในมุมมองของ` control is visible;
- switching to each PRD role changes the menu, the visible columns and the buttons;
- while viewing as a lower role, calling a privileged server action — submit the form, or hit the route directly — is **refused by the server**, not merely hidden;
- switching back to Admin restores everything, and a non-Admin cannot set `viewAs` at all;
- a denied page redirects somewhere useful instead of blanking;
- an empty approval queue says it is empty, not that you lack permission;
- `TEST_CASES.md` has a negative case per role rule, quoting the `AC`/`US` id it came from.
