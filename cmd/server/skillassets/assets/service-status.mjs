// FITT service-status switch, version 2 — shipped by Code Runner. Copy it unchanged; do not edit.
//
// FITT Billing can suspend a customer's app (unpaid subscription, lapsed MA) or put it in
// read-only mode. The app asks FITT Builder for its status, with the project's license key:
//
//   FITT_STATUS_URL   e.g. https://fitt-builder.fittbsa.com/v1/service-status
//   FITT_LICENSE_KEY  the project's key (fbl_…), issued by FITT Billing
//
// Both unset (or either one) = switch off: the app runs normally and asks nobody.
// It asks when the first request arrives and then at most every 5 minutes (30 seconds
// after a failed check), waiting at most 2 seconds for the very first answer. When it
// can't get an answer it keeps the last status it knew — it never makes the app
// slower or brings it down. It sends its version (x-fitt-switch-version), so FITT
// Builder can tell Billing which apps know read_only.
//
//   suspended  every route answers a 503 suspension page, except the exempt paths.
//   read_only  every read (GET, HEAD, OPTIONS) is served; every other request is refused
//              with 423 and the message, except the paths the app lists in allowWrites
//              (its sign-in and sign-out, and any search or export it sends by POST).
//              public/fitt-service-status.js shows the message as a banner on every page.
//
// Customer data is never touched. Uses only fetch, so it runs on any Next.js runtime.

export const SWITCH_VERSION = 2;
export const EXEMPT_PATHS = ['/api/health', '/healthz', '/api/service-status'];
const STATUSES = ['active', 'suspended', 'read_only'];
const READS = ['GET', 'HEAD', 'OPTIONS'];
const DEFAULT_MESSAGE = 'บริการถูกระงับชั่วคราว กรุณาติดต่อผู้ให้บริการ';
const READ_ONLY_MESSAGE = 'ระบบอยู่ในโหมดอ่านอย่างเดียว ดูข้อมูลได้ แต่บันทึกหรือแก้ไขไม่ได้ กรุณาติดต่อผู้ให้บริการ';

export function createServiceStatus({ url, key, intervalMs = 300_000, retryMs = 30_000, firstWaitMs = 2_000, timeoutMs = 5_000, fetchImpl = globalThis.fetch, now = Date.now }) {
  let status = null;
  let message = '';
  let dueAt = -Infinity;
  let inflight = null;
  async function check() {
    let answered = false;
    try {
      const res = await fetchImpl(url, { headers: { authorization: `Bearer ${key}`, 'x-fitt-switch-version': String(SWITCH_VERSION) }, cache: 'no-store', signal: AbortSignal.timeout(timeoutMs) });
      if (!res.ok) return;
      const body = await res.json();
      if (!STATUSES.includes(body?.status)) return;
      status = body.status;
      message = typeof body.message === 'string' ? body.message : '';
      answered = true;
    } catch {
      // Unreachable, timed out or not JSON: keep the last known status.
    } finally {
      // A failed check is retried sooner, so an app that started while FITT
      // Builder was out of reach doesn't wait a whole interval to learn its status.
      dueAt = now() + (answered ? intervalMs : Math.min(retryMs, intervalMs));
      inflight = null;
    }
  }
  return {
    async current() {
      if (!inflight && now() >= dueAt) inflight = check();
      if (status === null && inflight) {
        let timer;
        await Promise.race([inflight, new Promise(r => { timer = setTimeout(r, firstWaitMs); })]);
        clearTimeout(timer);
      }
      return { status: status ?? 'active', message };
    },
  };
}

const escapeHtml = s => s.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

function page(title, message, status) {
  const html = `<!doctype html><html lang="th"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${title}</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;font-family:system-ui,sans-serif;background:#f6f7f9;color:#1f2933}main{max-width:32rem;padding:2rem;text-align:center}h1{font-size:1.4rem;margin:0 0 .75rem}p{margin:0;line-height:1.6}</style></head><body><main><h1>${title}</h1><p>${escapeHtml(message)}</p></main></body></html>`;
  return new Response(html, { status, headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' } });
}

export function suspendedResponse(message) {
  return page('ระงับบริการชั่วคราว', message || DEFAULT_MESSAGE, 503);
}

// A form a browser sends from a page gets a page back; fetch calls and Next.js server
// actions get JSON they can show.
export function readOnlyResponse(message, request) {
  const text = message || READ_ONLY_MESSAGE;
  const accept = request?.headers.get('accept') ?? '';
  if (accept.includes('text/html') && !request.headers.get('next-action')) return page('โหมดอ่านอย่างเดียว', text, 423);
  return new Response(JSON.stringify({ error: 'read_only', message: text }), { status: 423, headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' } });
}

const switches = new Map();

function switchFor({ env = globalThis.process?.env ?? {}, ...deps } = {}) {
  const url = env.FITT_STATUS_URL, key = env.FITT_LICENSE_KEY;
  if (!url || !key) return null;
  const id = `${url}\n${key}`;
  if (!switches.has(id)) switches.set(id, createServiceStatus({ url, key, ...deps }));
  return switches.get(id);
}

// "/api/auth/" lists everything under it; any other entry is that path only.
function listed(path, allowWrites) {
  return allowWrites.some(p => (p.endsWith('/') ? path.startsWith(p) : path === p));
}

/**
 * Call first in the app's request proxy. A Response means "answer with this" (the app is
 * suspended, or read-only and this request would write); null means "carry on as usual".
 * options.allowWrites: the paths that may still be written while read-only.
 */
export async function serviceStatusResponse(request, options = {}) {
  const { allowWrites = [], ...rest } = options;
  const s = switchFor(rest);
  if (!s) return null;
  const path = new URL(request.url).pathname;
  if (EXEMPT_PATHS.includes(path)) return null;
  const { status, message } = await s.current();
  if (status === 'suspended') return suspendedResponse(message);
  if (status === 'read_only' && !READS.includes(request.method) && !listed(path, allowWrites)) return readOnlyResponse(message, request);
  return null;
}

/** The status for GET /api/service-status, which the banner script asks. Off = active. */
export async function serviceStatusNow(options = {}) {
  const s = switchFor(options);
  if (!s) return { status: 'active', message: '' };
  const { status, message } = await s.current();
  return { status, message: status === 'read_only' ? message || READ_ONLY_MESSAGE : message };
}
