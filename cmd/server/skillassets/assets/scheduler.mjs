// FITT build scheduler — copy unchanged to lib/scheduler.mjs. Never edit it.
//
// The app declares its scheduled work in crn.schedule.json and handles each job at
// POST /api/cron/<name>. On a customer's server (CRN_SCHEDULER=internal, the image
// default) this module calls those handlers itself. On Cloud Run
// (CRN_SCHEDULER=external) the app may sleep between requests, so the platform's
// scheduler calls them instead and this module starts nothing.
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { readFileSync } from 'node:fs';
import path from 'node:path';

export const EVERY = [5, 10, 15, 20, 30, 60];
const NAME = /^[a-z0-9][a-z0-9-]{0,39}$/;
const DAILY = /^([01]\d|2[0-3]):([0-5]\d)$/;
const KEYS = new Set(['name', 'path', 'every_minutes', 'daily_at']);
const BANGKOK_OFFSET_MS = 7 * 3600_000; // Thailand keeps UTC+7 all year
const MINUTE = 60_000, DAY = 24 * 3600_000;

/** Throws on the first entry that is not a valid job; returns the entries. */
export function validateSchedule(entries) {
  if (!Array.isArray(entries)) throw new Error('crn.schedule.json must be a JSON array');
  const seen = new Set();
  for (const e of entries) {
    const label = JSON.stringify(e);
    if (!e || typeof e !== 'object' || Array.isArray(e)) throw new Error(`schedule entry is not an object: ${label}`);
    for (const k of Object.keys(e)) if (!KEYS.has(k)) throw new Error(`schedule entry has unknown key "${k}": ${label}`);
    if (typeof e.name !== 'string' || !NAME.test(e.name)) throw new Error(`schedule name must be lowercase letters, digits and dashes: ${label}`);
    if (seen.has(e.name)) throw new Error(`schedule name "${e.name}" is used twice`);
    seen.add(e.name);
    if (e.path !== `/api/cron/${e.name}`) throw new Error(`schedule path must be /api/cron/${e.name}: ${label}`);
    const every = e.every_minutes !== undefined, daily = e.daily_at !== undefined;
    if (every === daily) throw new Error(`schedule entry needs exactly one of every_minutes or daily_at: ${label}`);
    if (every && !EVERY.includes(e.every_minutes)) throw new Error(`every_minutes must be one of ${EVERY.join(', ')}: ${label}`);
    if (daily && (typeof e.daily_at !== 'string' || !DAILY.test(e.daily_at))) throw new Error(`daily_at must be HH:MM (Bangkok time): ${label}`);
  }
  return entries;
}

/** Milliseconds from nowMs to the job's next run; a run due exactly now is the next one. */
export function nextDelayMs(entry, nowMs) {
  if (entry.every_minutes !== undefined) {
    const step = entry.every_minutes * MINUTE;
    return step - (nowMs % step);
  }
  const [h, m] = entry.daily_at.split(':').map(Number);
  const local = nowMs + BANGKOK_OFFSET_MS;
  const target = h * 3600_000 + m * MINUTE;
  const delay = target - (local % DAY);
  return delay > 0 ? delay : delay + DAY;
}

function readEntries() {
  return validateSchedule(JSON.parse(readFileSync(path.join(process.cwd(), 'crn.schedule.json'), 'utf8')));
}

/**
 * Call it once at server start (instrumentation.ts register()). Returns null when
 * the platform schedules the jobs, otherwise { stop() }.
 */
export function startScheduler({ env = process.env, entries, fetchImpl = fetch, setTimeoutImpl = setTimeout, clearTimeoutImpl = clearTimeout, now = Date.now, log = console.error } = {}) {
  if ((env.CRN_SCHEDULER ?? 'internal') === 'external') return null;
  const jobs = entries ?? readEntries();
  // Locally CRON_SECRET is optional: the handlers run in this same process, so a
  // secret made here at start guards them as well as a configured one.
  if (!env.CRON_SECRET) env.CRON_SECRET = randomBytes(32).toString('hex');
  const port = env.PORT || '3000';
  const timers = new Map();
  let stopped = false;
  const schedule = job => {
    if (stopped) return;
    timers.set(job.name, setTimeoutImpl(async () => {
      try {
        const res = await fetchImpl(`http://127.0.0.1:${port}${job.path}`, { method: 'POST', headers: { 'x-cron-secret': env.CRON_SECRET } });
        if (!res.ok) log(`[scheduler] ${job.name} answered ${res.status}`);
      } catch (e) {
        log(`[scheduler] ${job.name} failed: ${e?.message ?? e}`);
      }
      schedule(job);
    }, nextDelayMs(job, now())));
  };
  for (const job of jobs) schedule(job);
  return { stop() { stopped = true; for (const t of timers.values()) clearTimeoutImpl(t); } };
}

/** Whether a request to a /api/cron/* handler carries the secret. False when none is set. */
export function cronAuthorized(request, env = process.env) {
  const want = Buffer.from(env.CRON_SECRET ?? '');
  const got = Buffer.from(request.headers.get('x-cron-secret') ?? '');
  if (want.length === 0 || got.length !== want.length) return false;
  return timingSafeEqual(got, want);
}
