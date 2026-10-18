import test from 'node:test';
import assert from 'node:assert/strict';
import { validateSchedule, nextDelayMs, startScheduler, cronAuthorized } from '../cmd/server/skillassets/assets/scheduler.mjs';
import { validateSchedule as checkerValidate } from '../internal/buildstep/verify-delivery.mjs';

// A delivered app declares its scheduled work in crn.schedule.json. On a
// customer's server the app calls its own handlers (CRN_SCHEDULER=internal); on
// Cloud Run the platform does (external), so the app must not run timers there.
const good = [{ name: 'reminders', path: '/api/cron/reminders', every_minutes: 15 }, { name: 'daily-report', path: '/api/cron/daily-report', daily_at: '08:00' }];
const bad = [
  [{ name: 'A', path: '/api/cron/A', every_minutes: 15 }],
  [{ name: 'a', path: '/api/other', every_minutes: 15 }],
  [{ name: 'a', path: '/api/cron/a', every_minutes: 7 }],
  [{ name: 'a', path: '/api/cron/a', daily_at: '24:00' }],
  [{ name: 'a', path: '/api/cron/a', every_minutes: 15, daily_at: '08:00' }],
  [{ name: 'a', path: '/api/cron/a' }],
  [{ name: 'a', path: '/api/cron/a', every_minutes: 5 }, { name: 'a', path: '/api/cron/a', every_minutes: 10 }],
  [{ name: 'a', path: '/api/cron/a', every_minutes: 15, extra: 1 }],
  {},
];

test('the app and the checker accept and refuse the same schedules', () => {
  for (const v of [validateSchedule, checkerValidate]) {
    assert.deepEqual(v(good), good);
    assert.deepEqual(v([]), []);
    for (const b of bad) assert.throws(() => v(b), undefined, JSON.stringify(b));
  }
});

test('next run: every N minutes on the clock, daily in Bangkok time', () => {
  assert.equal(nextDelayMs({ every_minutes: 15 }, Date.parse('2026-10-18T10:07:00Z')), 8 * 60_000);
  assert.equal(nextDelayMs({ every_minutes: 60 }, Date.parse('2026-10-18T10:00:00Z')), 60 * 60_000);
  assert.equal(nextDelayMs({ daily_at: '08:00' }, Date.parse('2026-10-18T00:30:00Z')), 30 * 60_000);
  assert.equal(nextDelayMs({ daily_at: '08:00' }, Date.parse('2026-10-18T01:00:00Z')), 24 * 3600_000, 'exactly on time runs tomorrow');
  assert.equal(nextDelayMs({ daily_at: '08:00' }, Date.parse('2026-10-18T02:00:00Z')), 23 * 3600_000);
});

test('internal mode calls its own handler with a secret it made; external mode does nothing', async () => {
  const calls = [], timers = [];
  const env = { CRN_SCHEDULER: 'internal', PORT: '3000' };
  const s = startScheduler({
    env, entries: good, now: () => Date.parse('2026-10-18T10:07:00Z'),
    setTimeoutImpl: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    fetchImpl: async (url, init) => { calls.push({ url, init }); return new Response(null, { status: 204 }); },
  });
  assert.ok(s && env.CRON_SECRET && env.CRON_SECRET.length >= 32);
  assert.deepEqual(timers.map(t => t.ms), [8 * 60_000, (14 * 60 + 53) * 60_000]); // 10:07Z is 17:07 in Bangkok
  await timers[0].fn();
  assert.equal(calls[0].url, 'http://127.0.0.1:3000/api/cron/reminders');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.headers['x-cron-secret'], env.CRON_SECRET);
  assert.equal(timers.length, 3, 'reschedules after running');
  const ext = { CRN_SCHEDULER: 'external' };
  assert.equal(startScheduler({ env: ext, entries: good, setTimeoutImpl: () => assert.fail('no timers') }), null);
  assert.equal(ext.CRON_SECRET, undefined);
});

test('a failing job is logged and still rescheduled', async () => {
  const timers = [];
  startScheduler({
    env: { CRN_SCHEDULER: 'internal' }, entries: [good[0]], now: () => Date.parse('2026-10-18T10:07:00Z'),
    setTimeoutImpl: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    fetchImpl: async () => { throw new Error('connection refused'); },
    log: () => {},
  });
  await timers[0].fn();
  assert.equal(timers.length, 2);
});

test('a cron handler accepts only the secret', () => {
  const req = s => new Request('http://x/api/cron/a', { method: 'POST', headers: s ? { 'x-cron-secret': s } : {} });
  assert.equal(cronAuthorized(req('k'.repeat(32)), { CRON_SECRET: 'k'.repeat(32) }), true);
  assert.equal(cronAuthorized(req('wrong'), { CRON_SECRET: 'k'.repeat(32) }), false);
  assert.equal(cronAuthorized(req(''), { CRON_SECRET: '' }), false);
  assert.equal(cronAuthorized(req(null), { CRON_SECRET: 'k'.repeat(32) }), false);
});
