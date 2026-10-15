// FITT service-status switch — shipped by Code Runner. Copy it unchanged; do not edit.
//
// FITT Billing can suspend a customer's app (unpaid subscription, lapsed MA). The app
// asks FITT Builder whether it is suspended, with the project's license key:
//
//   FITT_STATUS_URL   e.g. https://fitt-builder.fittbsa.com/v1/service-status
//   FITT_LICENSE_KEY  the project's key (fbl_…), issued by FITT Billing
//
// Both unset (or either one) = switch off: the app runs normally and asks nobody.
// It asks when the first request arrives and then at most every 5 minutes, waiting
// at most 2 seconds for the very first answer. When it can't get an answer it keeps
// the last status it knew — it never makes the app slower or brings it down. While
// suspended, every route answers a 503 suspension page except the health paths;
// customer data is never touched. Uses only fetch, so it runs on any Next.js runtime.

export const EXEMPT_PATHS = ['/api/health', '/healthz'];
const DEFAULT_MESSAGE = 'บริการถูกระงับชั่วคราว กรุณาติดต่อผู้ให้บริการ';

export function createServiceStatus({ url, key, intervalMs = 300_000, firstWaitMs = 2_000, timeoutMs = 5_000, fetchImpl = globalThis.fetch, now = Date.now }) {
  let status = null;
  let message = '';
  let checkedAt = -Infinity;
  let inflight = null;
  async function check() {
    try {
      const res = await fetchImpl(url, { headers: { authorization: `Bearer ${key}` }, cache: 'no-store', signal: AbortSignal.timeout(timeoutMs) });
      if (!res.ok) return;
      const body = await res.json();
      if (body?.status !== 'active' && body?.status !== 'suspended') return;
      status = body.status;
      message = typeof body.message === 'string' ? body.message : '';
    } catch {
      // Unreachable, timed out or not JSON: keep the last known status.
    } finally {
      checkedAt = now();
      inflight = null;
    }
  }
  return {
    async current() {
      if (!inflight && now() - checkedAt >= intervalMs) inflight = check();
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

export function suspendedResponse(message) {
  const text = escapeHtml(message || DEFAULT_MESSAGE);
  const html = `<!doctype html><html lang="th"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>ระงับบริการชั่วคราว</title><style>body{margin:0;min-height:100vh;display:grid;place-items:center;font-family:system-ui,sans-serif;background:#f6f7f9;color:#1f2933}main{max-width:32rem;padding:2rem;text-align:center}h1{font-size:1.4rem;margin:0 0 .75rem}p{margin:0;line-height:1.6}</style></head><body><main><h1>ระงับบริการชั่วคราว</h1><p>${text}</p></main></body></html>`;
  return new Response(html, { status: 503, headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' } });
}

const switches = new Map();

/**
 * Call first in the app's request proxy. A Response means "answer with this"
 * (the app is suspended); null means "carry on as usual".
 */
export async function serviceStatusResponse(request, env = globalThis.process?.env ?? {}, deps = {}) {
  const url = env.FITT_STATUS_URL, key = env.FITT_LICENSE_KEY;
  if (!url || !key) return null;
  if (EXEMPT_PATHS.includes(new URL(request.url).pathname)) return null;
  const id = `${url}\n${key}`;
  if (!switches.has(id)) switches.set(id, createServiceStatus({ url, key, ...deps }));
  const { status, message } = await switches.get(id).current();
  return status === 'suspended' ? suspendedResponse(message) : null;
}
