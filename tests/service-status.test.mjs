import test from 'node:test';
import assert from 'node:assert/strict';
import { createServiceStatus, serviceStatusResponse, EXEMPT_PATHS } from '../cmd/server/skillassets/assets/service-status.mjs';

// The suspension switch every delivered app carries (FITT Billing phase 2). It must
// never make an app slower or fall over: an unreachable status server leaves the
// app on its last known status, and an unset switch does nothing at all.

const URL_ = 'https://builder.test/v1/service-status';
const KEY = 'fbl_test-key';
const answer = (status, message = '') => ({ ok: true, status: 200, json: async () => ({ status, message, checkedAt: '2026-10-15T00:00:00.000Z' }) });

function fakeFetch(responses) {
  const calls = [];
  const fetchImpl = async (url, init) => {
    calls.push({ url, authorization: init?.headers?.authorization });
    const next = responses.length > 1 ? responses.shift() : responses[0];
    if (next instanceof Error) throw next;
    return typeof next === 'function' ? next() : next;
  };
  return { fetchImpl, calls };
}

function clock(start = 0) {
  let t = start;
  return { now: () => t, advance: ms => { t += ms; } };
}

test('the first request waits for the first answer, so a suspended app never serves a page', async () => {
  const { fetchImpl, calls } = fakeFetch([answer('suspended', 'บริการถูกระงับชั่วคราว')]);
  const s = createServiceStatus({ url: URL_, key: KEY, fetchImpl });
  assert.deepEqual(await s.current(), { status: 'suspended', message: 'บริการถูกระงับชั่วคราว' });
  assert.deepEqual(calls, [{ url: URL_, authorization: `Bearer ${KEY}` }]);
});

test('asks at most once per interval, however many requests arrive', async () => {
  const c = clock();
  const { fetchImpl, calls } = fakeFetch([answer('active')]);
  const s = createServiceStatus({ url: URL_, key: KEY, fetchImpl, now: c.now, intervalMs: 300_000 });
  await Promise.all([s.current(), s.current(), s.current()]);
  c.advance(299_000);
  await s.current();
  assert.equal(calls.length, 1);
  c.advance(1_000);
  await s.current();
  assert.equal(calls.length, 2);
});

test('suspension and reactivation take effect at the next check', async () => {
  const c = clock();
  const { fetchImpl } = fakeFetch([answer('active'), answer('suspended', 'ค้างชำระ'), answer('active')]);
  const s = createServiceStatus({ url: URL_, key: KEY, fetchImpl, now: c.now, intervalMs: 1000 });
  assert.equal((await s.current()).status, 'active');
  c.advance(1000);
  await s.current(); // starts the check; a known status is served meanwhile
  await new Promise(r => setTimeout(r, 0));
  assert.equal((await s.current()).status, 'suspended');
  c.advance(1000);
  await s.current();
  await new Promise(r => setTimeout(r, 0));
  assert.equal((await s.current()).status, 'active');
});

test('keeps the last known status when the server is down, refuses the key or answers garbage', async () => {
  const c = clock();
  const unauthorized = { ok: false, status: 401, json: async () => ({ code: 'unauthorized' }) };
  const garbage = { ok: true, status: 200, json: async () => ({ status: 'maybe' }) };
  const { fetchImpl } = fakeFetch([answer('suspended', 'ค้างชำระ'), new Error('ECONNREFUSED'), unauthorized, garbage]);
  const s = createServiceStatus({ url: URL_, key: KEY, fetchImpl, now: c.now, intervalMs: 1000 });
  assert.equal((await s.current()).status, 'suspended');
  for (let i = 0; i < 3; i++) {
    c.advance(1000);
    await s.current();
    await new Promise(r => setTimeout(r, 0));
    assert.deepEqual(await s.current(), { status: 'suspended', message: 'ค้างชำระ' });
  }
});

test('with no answer yet, an unreachable server leaves the app running', async () => {
  const { fetchImpl } = fakeFetch([new Error('ENOTFOUND')]);
  const s = createServiceStatus({ url: URL_, key: KEY, fetchImpl });
  assert.equal((await s.current()).status, 'active');
});

test('a slow server delays the first request by at most firstWaitMs', async () => {
  const never = () => new Promise(() => {});
  const { fetchImpl } = fakeFetch([never]);
  const s = createServiceStatus({ url: URL_, key: KEY, fetchImpl, firstWaitMs: 50 });
  const started = Date.now();
  assert.equal((await s.current()).status, 'active');
  assert.ok(Date.now() - started < 1000);
});

const env = { FITT_STATUS_URL: URL_, FITT_LICENSE_KEY: KEY };
const request = path => new Request(`http://app.test${path}`);

test('unset or half-set switch does nothing and asks nobody', async () => {
  let asked = 0;
  const fetchImpl = async () => { asked++; return answer('suspended'); };
  for (const e of [{}, { FITT_STATUS_URL: URL_ }, { FITT_LICENSE_KEY: KEY }, { FITT_STATUS_URL: '', FITT_LICENSE_KEY: '' }]) {
    assert.equal(await serviceStatusResponse(request('/'), e, { fetchImpl }), null);
  }
  assert.equal(asked, 0);
});

test('a suspended app answers every page with a 503 suspension page, uncached, except health', async () => {
  const { fetchImpl } = fakeFetch([answer('suspended', 'บริการถูกระงับชั่วคราว กรุณาติดต่อผู้ให้บริการ')]);
  const res = await serviceStatusResponse(request('/orders/42?x=1'), env, { fetchImpl });
  assert.equal(res.status, 503);
  assert.equal(res.headers.get('cache-control'), 'no-store');
  assert.match(res.headers.get('content-type'), /^text\/html/);
  const html = await res.text();
  assert.match(html, /บริการถูกระงับชั่วคราว กรุณาติดต่อผู้ให้บริการ/);
  assert.equal(await serviceStatusResponse(request('/api/orders'), env, { fetchImpl }).then(r => r?.status), 503);
  for (const path of EXEMPT_PATHS) assert.equal(await serviceStatusResponse(request(path), env, { fetchImpl }), null);
});

test('the message is shown as text, never as markup', async () => {
  const { fetchImpl } = fakeFetch([answer('suspended', '<script>alert(1)</script>')]);
  const html = await (await serviceStatusResponse(request('/'), { ...env, FITT_LICENSE_KEY: 'fbl_other' }, { fetchImpl })).text();
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /&lt;script&gt;/);
});

test('an active app passes every request through', async () => {
  const { fetchImpl } = fakeFetch([answer('active')]);
  assert.equal(await serviceStatusResponse(request('/'), { ...env, FITT_LICENSE_KEY: 'fbl_active' }, { fetchImpl }), null);
});
