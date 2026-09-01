---
name: mail-service
description: 'Send transactional or bulk emails through the DMailService API and inspect delivery status. Use when integrating outbound email, building notification flows, debugging delivery, querying event logs, recipient-level status (to/cc/bcc), webhook handling for Mailjet/SendGrid/Mailgun, async delivery jobs, fallback provider behavior, or templating with {{variable}} / {{#each}} / {{#if}} syntax.'
---

# DMailService — Mail Sending Skill

A production-grade guide for calling the **DMailService** REST API to send templated transactional emails, run bulk campaigns (≤100 recipients/request), and observe delivery lifecycle (sent → delivered → opened/clicked, or bounced/failed/dropped).

The service multiplexes across **Mailjet, SendGrid, and Mailgun** with priority-based selection and automatic fallback, persists every send as an `event_log` (Firestore), tracks per-recipient status in `recipient_events`, and exposes both webhook ingestion and provider polling for status reconciliation.

## When to Use This Skill

- Sending a single transactional email (welcome, OTP, receipt, password reset).
- Sending up to 100 personalized emails in one request (bulk newsletter, batch notification).
- Querying delivery status, opens, clicks, bounces for an organization.
- Inspecting per-recipient status when an email had `cc` / `bcc`.
- Triggering a manual sync from the provider, or polling provider status directly.
- Diagnosing why a job did or did not fall back to the secondary provider.
- Configuring webhook URLs in Mailjet/SendGrid/Mailgun consoles.

Do **not** use this skill for: organization/template/user CRUD (different endpoints, JWT-only).

## Prerequisites

1. **API key** with at least `mail:send` (and `mail:read` for status endpoints), bound to a single organization. Created via `POST /api/v1/api-keys` (JWT, admin). The plaintext key is shown **once** at creation.
2. **Template ID** of an active template in the same organization. Created via `POST /api/v1/templates`.
3. **Organization** must have at least one enabled `mailProvider` with valid encrypted credentials; if `enableFallback: true`, configure a second provider with `priority: 2`.
4. **Default From address** set on the organization (`defaultFromEmail` / `defaultFromName`) — request-level `fromEmail` overrides it but the domain must be verified at the provider.

## Authentication

All `/mail/*` endpoints accept **either** auth scheme. Pick one per request:

| Scheme       | Header                                  | Use case                              |
| ------------ | --------------------------------------- | ------------------------------------- |
| API Key      | `X-API-Key: <plaintext-key>`            | Service-to-service, server-side jobs  |
| JWT (Bearer) | `Authorization: Bearer <access_token>`  | User session calling on behalf of org |

When using API Key, `organizationId` may be omitted from the request body — it is resolved from the key's binding. Webhook endpoints (`/webhooks/*`) are public and verify provider signatures instead.

## Base URL

```
{BASE_URL}/api/v1            # e.g. http://localhost:8080/api/v1
```

Production deployments must be behind HTTPS. Never put an API key in a query string, browser, or client-side code.

---

## Core Procedure: Send a Single Email

`POST /mail/send` — JSON body, idempotent **only** at the provider level (no client idempotency key yet; dedupe upstream if needed).

### Minimal request

```http
POST /api/v1/mail/send
X-API-Key: <key>
Content-Type: application/json

{
  "templateId": "tmpl_abc123",
  "toEmail": "user@example.com",
  "variables": { "firstName": "John" }
}
```

### Full request (all supported fields)

```json
{
  "organizationId": "org_uuid",
  "templateId": "tmpl_abc123",
  "to":   [{ "email": "user@example.com", "name": "John Doe" }],
  "cc":   [{ "email": "manager@example.com", "name": "Manager" }],
  "bcc":  [{ "email": "audit@example.com" }],
  "fromEmail": "noreply@yourdomain.com",
  "fromName":  "Your Company",
  "subject":   "Welcome, {{firstName}}!",
  "variables": {
    "firstName": "John",
    "items": [{ "product": "Item A" }, { "product": "Item B" }],
    "isPremium": true
  },
  "metadata":   { "campaign": "welcome", "userId": "u_42" },
  "attachments": [{
    "filename":    "invoice.pdf",
    "contentType": "application/pdf",
    "content":     "<base64-bytes>"
  }]
}
```

### Field rules

- `templateId` — **required**.
- Recipients — **required**, supply *either* `to[]` (preferred) *or* legacy `toEmail`/`toName`. `to[]` takes precedence if both present.
- `cc` / `bcc` — optional arrays of `{email, name?}`; each `email` must pass RFC validation.
- `fromEmail` / `fromName` — optional; falls back to organization defaults. Domain must be verified at the active provider or the send fails.
- `subject` — optional; falls back to the template's subject. Variable substitution applies.
- `variables` — `map[string]any`. Used both in subject and body. Missing required template variables → 400.
- `metadata` — opaque map persisted on the event log; useful for downstream correlation.
- `attachments[].content` — **base64-encoded bytes**, not a URL. Keep total request body under the provider's limit (SendGrid 30 MB, Mailjet 15 MB).

### Success response (HTTP 200)

```json
{
  "success": true,
  "code": 200,
  "message": "Email sent successfully",
  "data": {
    "eventLogId":   "evt_uuid",
    "status":       "sent",
    "provider":     "sendgrid",
    "usedFallback": false
  }
}
```

`status: "sent"` means the provider accepted the message. Final delivery is asynchronous — see [Status Lifecycle](#status-lifecycle) below.

### Error envelope (any 4xx/5xx)

```json
{ "success": false, "code": 400, "message": "templateId is required", "requestId": "..." }
```

Never parse the message string for control flow; branch on `code` plus HTTP status.

---

## Bulk Send

`POST /mail/send-bulk` — same envelope, max **100 recipients per request**. Each recipient gets its own `variables`, `cc`, `bcc`, `metadata`, and its own event log.

```json
{
  "templateId": "tmpl_abc123",
  "fromEmail":  "noreply@yourdomain.com",
  "subject":    "Newsletter",
  "recipients": [
    { "toEmail": "a@x.com", "toName": "A", "variables": { "firstName": "A" } },
    { "toEmail": "b@x.com",                "variables": { "firstName": "B" } }
  ]
}
```

Response always returns 200 with per-recipient outcome — partial failures are reported in `errors[]`, **not** as a top-level error:

```json
{
  "data": {
    "totalRequested": 2,
    "totalSuccess":   2,
    "totalFailed":    0,
    "results": [ { "eventLogId": "...", "status": "sent", "provider": "sendgrid" } ],
    "errors":  []
  }
}
```

For >100 recipients, chunk client-side; respect the rate limiter (default `100 req / 60s` per IP+key).

---

## Template Syntax

Templates live in GCS as HTML; subject lines also accept the same syntax.

| Construct         | Example                                          |
| ----------------- | ------------------------------------------------ |
| Variable          | `Hello {{firstName}}`                            |
| Loop              | `{{#each items}}<li>{{product}}</li>{{/each}}`   |
| Conditional       | `{{#if isPremium}}Premium content{{/if}}`        |

Variable definitions on the template (`isRequired`, `isLoop`) are enforced server-side. Use `POST /templates/{id}/preview` (JWT) to render before sending.

---

## Status Lifecycle

After acceptance, the **AsyncMailDeliveryService** polls/syncs status (5s interval, max 20 checks ≈ 105s window) and reconciles webhook events.

| Status         | Terminal? | Fallback? | Meaning                                |
| -------------- | --------- | --------- | -------------------------------------- |
| `sent` / `processing` | No        | —         | Provider accepted; awaiting delivery  |
| `delivered`    | Yes       | No        | Reached recipient inbox                |
| `opened`       | Yes       | No        | Recipient opened (tracking pixel)      |
| `clicked`      | Yes       | No        | Recipient clicked a tracked link       |
| `bounced`      | Yes       | **No**    | Hard bounce — address invalid          |
| `rejected`     | Yes       | **No**    | Provider refused (recipient policy)    |
| `dropped`      | Yes       | **No**    | Provider policy block                  |
| `complained` / `spamreport` | Yes | No  | Recipient marked as spam               |
| `unsubscribed` | Yes       | No        | Recipient unsubscribed                 |
| `failed`       | Yes       | **Yes**   | Provider error → fallback if enabled   |

**Fallback only triggers on `failed`** (provider-side issue). Recipient-side issues (`bounced`, `rejected`, `dropped`) do not retry on the secondary provider — the address would fail there too.

Provider-specific event mapping:

- **SendGrid** `processed→sent`, `bounce→bounced`, `dropped→dropped`, `deferred→keep polling`.
- **Mailgun** `accepted→sent`, `failed{severity:permanent}→bounced`, `failed{severity:temporary}→failed` (triggers fallback).
- **Mailjet** maps directly to the canonical names above.

---

## Observing Delivery

| Endpoint                                     | Purpose                                   |
| -------------------------------------------- | ----------------------------------------- |
| `GET /mail/events/{id}`                      | Single event log with status history      |
| `GET /mail/events?organizationId=&status=&pageSize=&pageIndex=` | Paginated list (filter by status) |
| `GET /mail/events/summary?organizationId=`   | Aggregate counts (sent/delivered/…)       |
| `GET /mail/events/{id}/recipients`           | Per-recipient (to/cc/bcc) status timeline |
| `GET /mail/events/{id}/recipients/summary`   | Per-recipient roll-up                     |
| `GET /mail/events/{id}/provider-status`      | Live fetch from provider API              |
| `POST /mail/events/{id}/sync`                | Force re-sync from provider now           |
| `GET /mail/events/{id}/job-status`           | Async delivery job state + activity log   |

Pagination params: `pageSize` (1–100, default 10), `pageIndex` (1-based, default 1), `sortBy`, `sortOrder` (`asc`|`desc`, default `desc`). Response uses the `PagedResponse` envelope (`dataPage` block + `data` array).

**Rule of thumb for clients**: subscribe to webhooks for push updates; only call `/sync` or `/provider-status` when reconciling a stuck job. Polling `/events/{id}` is cheap (Firestore read) and safe.

---

## Webhooks (Inbound)

Configure in each provider's console. Use the **organization-scoped** URLs in production so signatures verify against that org's signing key:

```
POST /api/v1/webhooks/mailjet/{orgId}
POST /api/v1/webhooks/sendgrid/{orgId}
POST /api/v1/webhooks/mailgun/{orgId}
```

Legacy global routes (`/webhooks/{provider}`, no `orgId`) exist for backward compatibility and use the global signing key from server config; prefer the scoped form. The handler verifies the provider's HMAC/signature header, persists `recipient_events`, and updates the parent `event_log`.

---

## Operational Guardrails

- **Rate limit** — default `100 req / 60s` per credential. On 429, back off exponentially (start 1s, cap 30s, jitter ±20%).
- **Retry policy** — only retry on 5xx and network errors. Never retry 4xx (validation/auth/forbidden).
- **Idempotency** — there is no server-side idempotency key today. Persist `eventLogId` from the response; on transient client failure, query `/events?metadata.requestId=` (if you store `requestId` in `metadata`) before re-sending.
- **Secrets** — `X-API-Key` is plaintext on the wire; rely on TLS. Never log it. Rotate via `POST /api-keys/{id}/regenerate`; old key keeps working until you `deactivate`.
- **PII** — recipient email + variables are persisted in event logs and sent to the provider. Don't put passwords or tokens in `variables`/`metadata`.
- **Attachments** — base64 inflates payload by ~33%; total request must fit provider limit. Prefer linking to a presigned URL for large files.
- **Bulk fan-out** — split into chunks of ≤100 and enqueue server-side; do not parallelize beyond the rate limit.

## Quick Recipes

**cURL — send one:**
```bash
curl -fsS -X POST "$BASE_URL/api/v1/mail/send" \
  -H "X-API-Key: $DMAIL_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"templateId":"tmpl_abc","toEmail":"u@x.com","variables":{"firstName":"U"}}'
```

**Node (fetch) — send + poll until terminal:**
```js
const send = await fetch(`${BASE}/api/v1/mail/send`, {
  method: 'POST',
  headers: { 'X-API-Key': KEY, 'Content-Type': 'application/json' },
  body: JSON.stringify({ templateId, toEmail, variables }),
}).then(r => r.json());
if (!send.success) throw new Error(send.message);

const TERMINAL = new Set(['delivered','opened','clicked','bounced','rejected','dropped','complained','spamreport','unsubscribed','failed']);
const id = send.data.eventLogId;
for (let i = 0; i < 24; i++) {
  await new Promise(r => setTimeout(r, 5000));
  const ev = await fetch(`${BASE}/api/v1/mail/events/${id}`, { headers: { 'X-API-Key': KEY }}).then(r => r.json());
  if (TERMINAL.has(ev.data.status)) return ev.data;
}
```

**Python (requests) — bulk:**
```python
r = requests.post(f"{BASE}/api/v1/mail/send-bulk",
    headers={"X-API-Key": KEY},
    json={"templateId": tmpl, "recipients": batch[:100]},
    timeout=30)
r.raise_for_status()
body = r.json()
assert body["success"], body["message"]
for err in body["data"]["errors"]:
    log.warning("bulk failure %s: %s", err["toEmail"], err["error"])
```

## Troubleshooting

| Symptom                                  | Likely cause / fix                                                       |
| ---------------------------------------- | ------------------------------------------------------------------------ |
| `401 Unauthorized`                       | Wrong/missing `X-API-Key` or the key was deactivated/expired.            |
| `403 Forbidden` on `/events/{id}`        | Event log belongs to a different organization than the API key.          |
| `400 organizationId is required`         | Using JWT auth without `organizationId` in body, or expired API key.     |
| Send returns 200 but never `delivered`   | Check `/job-status` then `/provider-status`; verify domain auth (SPF/DKIM). |
| Status stuck at `sent` >2 min            | Webhook URL not configured — call `POST /events/{id}/sync` to reconcile. |
| `usedFallback:true` repeatedly           | Primary provider credential or domain misconfigured at provider side.    |
| `bounced` immediately                    | Recipient address invalid; do not retry on a different provider.         |
| 429                                      | Rate limit hit — back off; consider a dedicated key per workload.        |
| Attachment send fails with 413/payload error | Total base64 body exceeded provider limit; switch to hosted link.    |

## Reference

- API Key endpoints — `docs/API_KEY_ENDPOINTS.md` (this repo, `../../../docs/`)
- Status mapping & fallback flow — `docs/email_status_reference.md`
- Swagger UI — `GET /swagger/index.html` on a running instance
- Source of truth: handler `handlers/mail_handler.go`, service `services/mail_service.go`, async lifecycle `services/async_delivery_service.go`
