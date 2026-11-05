import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import zlib from 'node:zlib';
import { acceptCandidate, createScanner, forbiddenEntry, parseForbidden, scanBlob, scanTree, widgetTagProblem, PLACEHOLDERS } from '../internal/buildstep/accept-candidate.mjs';

const entries = (...values) => values.map((v, i) => ({ label: `value #${i + 1}`, ...parseForbidden(forbiddenEntry(v))[0] }));

function scanAll(values, chunks) {
  const hits = [];
  const source = createScanner(entries(...values), hit => hits.push(hit)).source('here');
  for (const c of chunks) source.push(Buffer.from(c));
  return hits;
}

test('a value is found wherever its bytes are, as a substring, and reported without the value', () => {
  assert.deepEqual(scanAll(['172.0.2.7'], ['172.0.2.7']), [{ label: 'value #1', where: 'here', offset: 0 }]);
  assert.deepEqual(scanAll(['172.0.2.7'], ['host=172.0.2.7:5432']), [{ label: 'value #1', where: 'here', offset: 5 }]);
  assert.deepEqual(scanAll(['172.0.2.7'], ['x%2F172.0.2.7.']), [{ label: 'value #1', where: 'here', offset: 4 }]);
  assert.equal(scanAll(['172.0.2.7'], ['172.0.2.70'])[0].offset, 0, 'a longer token still holds the bytes');
  assert.deepEqual(scanAll(['172.0.2.7'], ['172.0.2.8', '172.0.2', '']), []);
  assert.deepEqual(scanAll(['a'], ['bbb']), []);
});

test('a value split across two chunks is found once, at its true offset', () => {
  assert.deepEqual(scanAll(['db_role_x'], ['12345db_r', 'ole_x678']), [{ label: 'value #1', where: 'here', offset: 5 }]);
  assert.equal(scanAll(['db_role_x'], ['d', 'b', '_', 'r', 'o', 'l', 'e', '_', 'x']).length, 1);
  assert.equal(scanAll(['db_role_x'], ['db_role_x', 'tail']).length, 1, 'not reported again from the kept tail');
});

test('several values of different lengths are all found', () => {
  const hits = scanAll(['aaa', 'bbbbb'], ['xxaaayybbbbbzz', 'aaa']);
  assert.deepEqual(hits.map(h => [h.label, h.offset]).sort(), [['value #1', 14], ['value #1', 2], ['value #2', 7]].sort());
});

test('the forbidden list holds no value, refuses a malformed line and an empty list', () => {
  const line = forbiddenEntry('secret-host');
  assert.ok(!line.includes('secret-host'));
  assert.match(line, /^[0-9a-f]{64} 11 \d+$/);
  assert.equal(parseForbidden(`# comment\n\n${line}\n`).length, 1);
  assert.throws(() => parseForbidden('secret-host'), /forbidden/);
  assert.throws(() => parseForbidden('# nothing\n'), /no values/);
});

test('the scan reads the commit tree by path and a gzip layer through its compression', async t => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'crn-accept-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  await fs.mkdir(path.join(dir, 'a/b'), { recursive: true });
  await fs.writeFile(path.join(dir, 'a/b/notes.md'), 'see db_role_x here');
  await fs.writeFile(path.join(dir, 'clean.txt'), 'nothing');
  await fs.symlink('clean.txt', path.join(dir, 'link'));
  const hits = [];
  const scanner = createScanner(entries('db_role_x'), hit => hits.push(hit));
  const scope = await scanTree(dir, scanner);
  assert.deepEqual(scope, { files: 2, bytes: 25 });
  assert.deepEqual(hits, [{ label: 'value #1', where: 'source a/b/notes.md', offset: 4 }]);

  hits.length = 0;
  const layer = path.join(dir, 'layer');
  await fs.writeFile(layer, zlib.gzipSync(Buffer.from('tar bytes db_role_x end')));
  assert.equal((await scanBlob(layer, 'layer x', scanner)).encoding, 'gzip');
  assert.deepEqual(hits, [{ label: 'value #1', where: 'layer x', offset: 10 }]);

  hits.length = 0;
  const config = path.join(dir, 'config');
  await fs.writeFile(config, '{"Env":["A=db_role_x"]}');
  assert.equal((await scanBlob(config, 'config', scanner)).encoding, 'raw');
  assert.equal(hits.length, 1);

  const zstd = path.join(dir, 'zstd');
  await fs.writeFile(zstd, Buffer.from([0x28, 0xb5, 0x2f, 0xfd, 1, 2, 3]));
  await assert.rejects(scanBlob(zstd, 'layer z', scanner), /cannot read/, 'a layer it cannot read fails closed');
});

const project = '11111111-1111-4111-8111-111111111111';
const tag = (p = project, ingest = '/api/fitt-feedback') => `<script src="/fitt-feedback.js" data-fitt-feedback="true" data-project="${p}" data-ingest="${ingest}"></script>`;

test('the login page must carry exactly one widget tag, for this project, posting to its own origin', () => {
  assert.equal(widgetTagProblem(`<html><body>${tag()}</body></html>`, project), null);
  assert.match(widgetTagProblem('<html></html>', project), /0 widget/);
  assert.match(widgetTagProblem(tag() + tag(), project), /2 widget/);
  assert.match(widgetTagProblem(tag('22222222-2222-4222-8222-222222222222'), project), /project/);
  assert.match(widgetTagProblem(tag(project, 'https://elsewhere.example/feedback'), project), /same origin/);
  assert.equal(widgetTagProblem('<script>self.__next_f.push("\\"src\\":\\"/fitt-feedback.js\\"")</script>' + tag(), project), null, 'a streamed copy is not a tag');
});

// A fake world, recording the order of the steps.
function world(overrides = {}) {
  const steps = [];
  const deps = {
    platform: async () => { steps.push('platform'); return 'linux/amd64'; },
    scanTree: async () => { steps.push('scan source'); return { files: 1, bytes: 1 }; },
    scanImage: async () => { steps.push('scan image'); return { blobs: [] }; },
    verify: async (_cwd, _df, options) => {
      steps.push(`checker ${options.image}`);
      await options.whileAppRuns({ baseUrl: 'http://app', cwd: '.', check() {} });
      return { status: 'passed', external_tls: { must_fail: [] } };
    },
    checkWidget: async () => { steps.push('widget'); return { checks: [] }; },
    ...overrides,
  };
  return { deps, steps };
}
const digest = 'sha256:' + 'a'.repeat(64);
const request = deps => ({ image: `reg/app@${digest}`, commit: 'c1', project, widget: true, forbidden: forbiddenEntry('real-value'), cwd: '.', deps });

test('a candidate passes only when platform, scans, checker and widget all pass, in that order', async () => {
  const { deps, steps } = world();
  const r = await acceptCandidate(request(deps));
  assert.equal(r.status, 'passed', r.error);
  assert.deepEqual(steps, ['platform', 'scan source', 'scan image', `checker reg/app@${digest}`, 'widget']);
  assert.equal(r.image, `reg/app@${digest}`);
  assert.equal(r.platform, 'linux/amd64');
  assert.equal(r.artifact_scan.values.real, 1);
  assert.equal(r.artifact_scan.values.placeholders, PLACEHOLDERS.length);
  assert.deepEqual(r.artifact_scan.hits, []);
  assert.ok(r.finished_at);
});

test('a candidate named by a tag, or for another platform, never reaches the checker', async () => {
  const bad = world();
  const tagged = await acceptCandidate({ ...request(bad.deps), image: 'reg/app:v3' });
  assert.equal(tagged.status, 'failed');
  assert.match(tagged.error, /digest/);
  assert.deepEqual(bad.steps, []);

  const arm = world({ platform: async () => 'linux/arm64' });
  const r = await acceptCandidate(request(arm.deps));
  assert.equal(r.status, 'failed');
  assert.match(r.error, /linux\/arm64/);
  assert.deepEqual(arm.steps, []);
});

test('a forbidden value or placeholder fails the candidate, names where, and never prints it', async () => {
  for (const [value, label] of [['real-value', 'real value #1'], [PLACEHOLDERS[0], `placeholder ${PLACEHOLDERS[0]}`]]) {
    const { deps, steps } = world({
      scanImage: async (_image, scanner) => { scanner.source('layer sha256:abc').push(Buffer.from(`x ${value} y`)); return { blobs: [] }; },
    });
    const r = await acceptCandidate(request(deps));
    assert.equal(r.status, 'failed');
    assert.deepEqual(r.artifact_scan.hits, [{ label, where: 'layer sha256:abc', offset: 2 }]);
    assert.ok(!steps.some(s => s.startsWith('checker')), 'stops before the long checks');
    assert.match(r.error, /layer sha256:abc/);
    if (label.startsWith('real')) assert.ok(!JSON.stringify(r).includes(value), 'the receipt must not hold the value');
  }
});

test('the checker failing, crashing or a widget problem each fail the candidate', async () => {
  const failed = world({ verify: async () => ({ status: 'failed', error: 'CA file is a symlink (migrate): opened 1 connection(s)' }) });
  const a = await acceptCandidate(request(failed.deps));
  assert.equal(a.status, 'failed');
  assert.match(a.error, /symlink/);

  const crashed = world({ verify: async () => { throw new Error('boom'); } });
  const b = await acceptCandidate(request(crashed.deps));
  assert.equal(b.status, 'failed');
  assert.match(b.error, /boom/);

  const widget = world({ checkWidget: async () => { throw new Error('2 widget tags on /login'); }, verify: async (_c, _d, o) => { try { await o.whileAppRuns({}); } catch (e) { return { status: 'failed', error: e.message }; } return { status: 'passed' }; } });
  const c = await acceptCandidate(request(widget.deps));
  assert.equal(c.status, 'failed');
  assert.match(c.error, /2 widget tags/);
});

test('a checker that passed without running the widget check does not pass the candidate', async () => {
  const { deps } = world({ verify: async () => ({ status: 'passed' }) });
  const r = await acceptCandidate(request(deps));
  assert.equal(r.status, 'failed');
  assert.match(r.error, /widget/);
});

test('without a forbidden list the candidate fails closed', async () => {
  const { deps, steps } = world();
  const r = await acceptCandidate({ ...request(deps), forbidden: '' });
  assert.equal(r.status, 'failed');
  assert.match(r.error, /no values/);
  assert.deepEqual(steps, ['platform']);
});
