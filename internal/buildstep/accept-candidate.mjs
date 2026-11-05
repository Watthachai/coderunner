// The last gate before CRN reports "released" for an external-tls-only app. It runs on
// the amd64 image the registry holds, named by digest, not on a build of the same source:
//   1. the image is linux/amd64 and its revision label names this commit
//   2. no forbidden value or placeholder in the commit's tree (names, symlink targets and
//      content) or anywhere in the image
//   3. the whole release checker (verify-delivery.mjs) against that image
//   4. the feedback widget, while the checker has the image running
// Anything else, including a crash, leaves the receipt at status "failed".
import fs from 'node:fs/promises';
import { createReadStream, realpathSync, readFileSync } from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import zlib from 'node:zlib';
import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { pathToFileURL } from 'node:url';
import { verify, requireDigestRef } from './verify-delivery.mjs';

export const REQUIRED_PLATFORM = 'linux/amd64';
export const PLACEHOLDERS = ['WITHHELD_DB_HOST', 'WITHHELD_DB_NAME', 'WITHHELD_DB_ROLE'];

// --- forbidden values, held only as hashes ---------------------------------------------
// The real values never reach CRN: the list holds a SHA-256 of each, to confirm a match,
// its length, and a weak rolling hash that lets the scan slide over every byte cheaply.
const BASE = 257, PRIME = 2147483647;
function rollingHash(bytes) {
  let h = 0;
  for (const b of bytes) h = (h * BASE + b) % PRIME;
  return h;
}
export function forbiddenEntry(value) {
  const bytes = Buffer.from(value);
  return `${createHash('sha256').update(bytes).digest('hex')} ${bytes.length} ${rollingHash(bytes)}`;
}
export function parseForbidden(text) {
  const entries = [];
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const m = line.match(/^([0-9a-f]{64}) (\d+) (\d+)$/);
    if (!m) throw new Error('The forbidden list has a malformed line (want: <sha256> <length> <rolling>)');
    entries.push({ sha256: m[1], length: Number(m[2]), rolling: Number(m[3]) });
  }
  if (!entries.length) throw new Error('The forbidden list has no values');
  return entries;
}

// --- the scanner: every occurrence of the bytes, as a substring --------------------------
export function createScanner(entries, onHit) {
  const groups = new Map();
  for (const e of entries) {
    let g = groups.get(e.length);
    if (!g) {
      let pow = 1;
      for (let i = 0; i < e.length; i++) pow = (pow * BASE) % PRIME;
      g = { length: e.length, pow, targets: new Map() };
      groups.set(e.length, g);
    }
    if (!g.targets.has(e.rolling)) g.targets.set(e.rolling, []);
    g.targets.get(e.rolling).push(e);
  }
  const list = [...groups.values()];
  const keep = Math.max(...list.map(g => g.length)) - 1;
  return {
    source(where) {
      let tail = Buffer.alloc(0), before = 0;
      return {
        // How many hits these bytes held, so a caller can tell before it names them.
        push(chunk) {
          let found = 0;
          if (!chunk.length) return found;
          const buf = tail.length ? Buffer.concat([tail, chunk]) : chunk;
          const fresh = tail.length; // a window must end in the new bytes, or it was seen
          for (const g of list) {
            const { length, pow, targets } = g;
            let h = 0;
            for (let i = 0; i < buf.length; i++) {
              h = (h * BASE + buf[i]) % PRIME;
              if (i >= length) h = (h + PRIME - (buf[i - length] * pow) % PRIME) % PRIME;
              if (i >= length - 1 && i >= fresh && targets.has(h)) {
                const sha = createHash('sha256').update(buf.subarray(i - length + 1, i + 1)).digest('hex');
                for (const e of targets.get(h)) if (e.sha256 === sha) { found++; onHit({ label: e.label, where, offset: before + i - length + 1 }); }
              }
            }
          }
          const cut = Math.max(0, buf.length - keep);
          before += cut;
          tail = Buffer.from(buf.subarray(cut));
          return found;
        },
      };
    },
  };
}

// The commit's tree: every name, every symlink's target as bytes (never followed) and every
// file's content. A name holding a value must not reach the receipt or an error, so such an
// entry, and anything below it, is named by its number.
export async function scanTree(root, scanner) {
  const scope = { directories: 0, files: 0, symlinks: 0, bytes: 0 };
  let entries = 0;
  const reading = async (label, work) => {
    try { return await work(); } catch { throw new Error(`The scan could not read ${label}`); }
  };
  async function walk(dir, label) {
    for (const entry of await reading(label, () => fs.readdir(dir, { withFileTypes: true }))) {
      const file = path.join(dir, entry.name), rel = path.relative(root, file), number = ++entries;
      const held = scanner.source(`name of source entry #${number}`).push(Buffer.from(rel)) > 0;
      const name = held ? `source entry #${number}` : `source ${rel}`;
      if (entry.isDirectory()) {
        scope.directories++;
        await walk(file, name);
      } else if (entry.isSymbolicLink()) {
        scanner.source(`symlink target of ${name}`).push(await reading(name, () => fs.readlink(file, { encoding: 'buffer' })));
        scope.symlinks++;
      } else if (entry.isFile()) {
        const source = scanner.source(name);
        await reading(name, async () => {
          for await (const chunk of createReadStream(file, { highWaterMark: 1 << 20 })) { source.push(chunk); scope.bytes += chunk.length; }
        });
        scope.files++;
      } else throw new Error(`The commit's tree cannot hold ${name}: it is not a file, a directory or a symlink`);
    }
  }
  await walk(root, 'the commit\'s tree');
  return scope;
}

// One blob of a saved image: a gzip layer through its compression, anything else as it is.
// A compression it cannot read fails closed: an unread layer would be a false "clean".
export async function scanBlob(file, where, scanner) {
  const handle = await fs.open(file);
  const { buffer } = await handle.read(Buffer.alloc(4), 0, 4, 0);
  await handle.close();
  if (buffer[0] === 0x28 && buffer[1] === 0xb5 && buffer[2] === 0x2f && buffer[3] === 0xfd) throw new Error(`The scan cannot read ${where}: zstd compression`);
  const gzip = buffer[0] === 0x1f && buffer[1] === 0x8b;
  const raw = createReadStream(file, { highWaterMark: 1 << 20 });
  const stream = gzip ? raw.pipe(zlib.createGunzip()) : raw;
  if (gzip) raw.on('error', e => stream.destroy(e));
  const source = scanner.source(where);
  let bytes = 0;
  for await (const chunk of stream) { source.push(chunk); bytes += chunk.length; }
  return { where, encoding: gzip ? 'gzip' : 'raw', bytes };
}

function command(cmd, args, { limitMs = 0 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '', err = '', timer;
    if (limitMs) timer = setTimeout(() => child.kill('SIGKILL'), limitMs);
    child.stdout.on('data', b => { out += b; });
    child.stderr.on('data', b => { err = (err + b).slice(-2000); });
    child.once('error', reject);
    child.once('close', code => { clearTimeout(timer); code === 0 ? resolve(out) : reject(new Error(`${cmd} ${args[0]} failed (exit ${code}): ${err.slice(-500)}`)); });
  });
}

// Every byte of the image as the registry holds it: docker save gives the manifest, the
// config (Env, Labels, Cmd, history) and every layer, including files a later layer deleted.
export async function scanImage(image, scanner) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-accept-image-'));
  try {
    const tar = path.join(dir, 'image.tar'), out = path.join(dir, 'x');
    await fs.mkdir(out);
    await command('docker', ['save', '-o', tar, image], { limitMs: 15 * 60_000 });
    await command('tar', ['-xf', tar, '-C', out], { limitMs: 15 * 60_000 });
    await fs.rm(tar);
    const blobs = [];
    async function walk(d) {
      for (const entry of await fs.readdir(d, { withFileTypes: true })) {
        const file = path.join(d, entry.name);
        if (entry.isDirectory()) await walk(file);
        else if (entry.isFile()) blobs.push(await scanBlob(file, `image ${path.relative(out, file)}`, scanner));
      }
    }
    await walk(out);
    // What the config held, so the receipt says it was read.
    const read = async digest => JSON.parse(await fs.readFile(path.join(out, 'blobs', ...digest.split(':')), 'utf8'));
    const index = JSON.parse(await fs.readFile(path.join(out, 'index.json'), 'utf8'));
    const top = await read(index.manifests[0].digest);
    const manifest = top.manifests ? await read(top.manifests[0].digest) : top;
    const config = await read(manifest.config.digest);
    return {
      blobs,
      config: { digest: manifest.config.digest, env: (config.config?.Env ?? []).length, labels: Object.keys(config.config?.Labels ?? {}).length, history: (config.history ?? []).length },
      layers: manifest.layers.length,
    };
  } finally { await fs.rm(dir, { recursive: true, force: true }); }
}

// --- the feedback widget, on the running image ------------------------------------------
export function widgetTagProblem(html, project) {
  const tags = (html.match(/<script\b[^>]*>/gi) ?? []).filter(t => /\bsrc=["']?\/fitt-feedback\.js/.test(t));
  if (tags.length !== 1) return `/login holds ${tags.length} widget tags, want 1`;
  const attr = name => tags[0].match(new RegExp(`\\b${name}=["']?([^"'\\s>]*)`))?.[1];
  if (attr('data-project') !== project) return 'The widget tag names another project';
  if (attr('data-ingest') !== '/api/fitt-feedback') return 'The widget must post to its own route on the same origin';
  return null;
}
export async function checkWidget({ baseUrl, cwd, project, expected, check }) {
  const get = (p, init) => fetch(`${baseUrl}${p}`, { redirect: 'follow', signal: AbortSignal.timeout(60_000), ...init });
  const page = await get('/login');
  const html = await page.text();
  const script = await get('/fitt-feedback.js');
  const route = await get('/api/fitt-feedback', { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' });
  const found = { login_status: page.status, script_status: script.status, route_status: route.status };
  if (expected) {
    check('login page answers', page.status === 200, `/login answered ${page.status}`);
    const problem = widgetTagProblem(html, project);
    check('one widget tag for this project, posting to the same origin', !problem, problem);
    check('widget script loads', script.status === 200 && /javascript/.test(script.headers.get('content-type') ?? ''), `/fitt-feedback.js answered ${script.status} ${script.headers.get('content-type')}`);
    // Nothing is listening behind the route here, so reaching it answers 502 from the route itself.
    check('widget route answers on the same origin', route.ok || route.status === 502, `/api/fitt-feedback answered ${route.status}`);
  } else {
    check('widget absent: no tag on /login', !/\/fitt-feedback\.js/.test(html.replace(/self\.__next_f[^<]*/g, '')), 'This build must not carry the widget, but /login loads it');
    check('widget absent: no script', script.status === 404, `This build must not carry the widget, but /fitt-feedback.js answered ${script.status}`);
    check('widget absent: no route', route.status === 404 || route.status === 405, `This build must not carry the widget, but its route answered ${route.status}`);
  }

  // In a real browser, with or without the widget: no page may ask another origin.
  const { chromium } = createRequire(path.join(cwd, 'package.json'))('@playwright/test');
  const browser = await chromium.launch();
  try {
    const tab = await browser.newPage();
    // The widget's shadow root is closed so an app's tests cannot see it; open it for this check only.
    await tab.addInitScript(() => { const attach = Element.prototype.attachShadow; Element.prototype.attachShadow = function (o) { return attach.call(this, { ...o, mode: 'open' }); }; });
    const origin = new URL(baseUrl).origin, outside = new Set();
    tab.on('request', r => { const u = new URL(r.url()); if (!['data:', 'blob:'].includes(u.protocol) && u.origin !== origin) outside.add(u.origin); });
    await tab.goto(`${baseUrl}/login`, { waitUntil: 'load', timeout: 180_000 });
    const hosts = await tab.locator('#fitt-feedback-host').count();
    if (expected) {
      check('one widget on the page', hosts === 1, `The page holds ${hosts} widgets`);
      const openPanels = tab.locator('#fitt-feedback-host .panel:not([hidden])');
      check('widget starts closed', await openPanels.count() === 0, 'The widget panel is open before anyone asked for it');
      await tab.locator('#fitt-feedback-host .fab').click({ timeout: 60_000 });
      await openPanels.first().waitFor({ state: 'visible', timeout: 30_000 });
      check('widget opens', true);
    } else {
      check('widget absent: none on the page', hosts === 0, `This build must not carry the widget, but the page holds ${hosts}`);
    }
    await tab.waitForLoadState('networkidle', { timeout: 60_000 }).catch(() => {});
    found.outside_requests = [...outside].sort();
    check('no request leaves the app\'s origin', outside.size === 0, `The page asked ${found.outside_requests.join(', ')}`);
  } finally { await browser.close(); }
  return found;
}

// --- the gate ----------------------------------------------------------------------------
const REVISION_LABEL = 'org.opencontainers.image.revision';
const REAL_DEPS = {
  platform: async image => (await command('docker', ['image', 'inspect', '--format', '{{.Os}}/{{.Architecture}}', image])).trim(),
  revision: async image => (await command('docker', ['image', 'inspect', '--format', `{{index .Config.Labels "${REVISION_LABEL}"}}`, image])).trim(),
  scanTree, scanImage, verify, checkWidget,
};
// forbidden: the text of the hash list, as CRN pinned it before the agent ran.
// widget: whether this build must carry the widget.
export async function acceptCandidate({ image, commit, project, widget, forbidden, cwd = process.cwd(), deps = REAL_DEPS }) {
  const receipt = { schema_version: 1, status: 'failed', image, commit, project, started_at: new Date().toISOString() };
  try {
    requireDigestRef(image);
    receipt.platform = await deps.platform(image);
    if (receipt.platform !== REQUIRED_PLATFORM) throw new Error(`The candidate is ${receipt.platform}, want ${REQUIRED_PLATFORM}`);
    receipt.revision = await deps.revision(image);
    if (receipt.revision !== commit) throw new Error(`The candidate's revision label is "${receipt.revision ?? ''}", want ${commit}`);

    const real = parseForbidden(forbidden).map((e, i) => ({ ...e, label: `real value #${i + 1}` }));
    const planted = PLACEHOLDERS.map(p => ({ ...parseForbidden(forbiddenEntry(p))[0], label: `placeholder ${p}` }));
    const hits = [];
    const scanner = createScanner([...real, ...planted], hit => hits.push(hit));
    receipt.artifact_scan = { scope: ['the commit\'s tree: names, symlink targets and content', 'image manifest', 'image config: Env, Labels, Cmd, history', 'every image layer, decompressed'], list_sha256: createHash('sha256').update(forbidden).digest('hex'), values: { real: real.length, placeholders: planted.length }, hits };
    receipt.artifact_scan.source = await deps.scanTree(cwd, scanner);
    receipt.artifact_scan.image = await deps.scanImage(image, scanner);
    if (hits.length) throw new Error(`Forbidden content: ${hits.slice(0, 8).map(h => `${h.label} in ${h.where}`).join('; ')}`);

    let widgetDone = false;
    const checker = await deps.verify(cwd, undefined, {
      image,
      whileAppRuns: async ctx => { receipt.widget = { expected: widget, ran: true, ...await deps.checkWidget({ ...ctx, project, expected: widget }) }; widgetDone = true; },
    });
    receipt.checker = checker;
    if (checker.status !== 'passed') throw new Error(`Release checker: ${checker.error ?? 'did not pass'}`);
    if (!widgetDone) throw new Error('The widget check did not run');
    receipt.status = 'passed';
  } catch (error) { receipt.error = String(error.message ?? error).slice(-3000); }
  receipt.finished_at = new Date().toISOString();
  return receipt;
}

// node accept-candidate.mjs --image <repo>@sha256:… --commit <sha> --project <id> --widget on|off --receipt <file>
//   (the pinned hash list on stdin: this process never opens the list's file)
// node accept-candidate.mjs --hash-values   (values on stdin, one per line; prints the list)
if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  const args = process.argv.slice(2);
  if (args[0] === '--hash-values') {
    for (const value of readFileSync(0, 'utf8').split('\n')) if (value) console.log(forbiddenEntry(value));
  } else {
    const opt = name => { const at = args.indexOf(`--${name}`); if (at < 0 || !args[at + 1]) throw new Error(`--${name} is required`); return args[at + 1]; };
    const receipt = await acceptCandidate({ image: opt('image'), commit: opt('commit'), project: opt('project'), widget: opt('widget') === 'on', forbidden: readFileSync(0, 'utf8') });
    await fs.writeFile(opt('receipt'), JSON.stringify(receipt, null, 2) + '\n');
    process.exitCode = receipt.status === 'passed' ? 0 : 1;
  }
}
