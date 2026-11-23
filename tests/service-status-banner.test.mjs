import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

// public/fitt-service-status.js: every page shows the read-only message as a banner, so
// people know why nothing saves. It asks the app's own GET /api/service-status.
const script = readFileSync(new URL('../cmd/server/skillassets/assets/fitt-service-status.js', import.meta.url), 'utf8');

// The root layout renders the placeholder; withPlaceholder false is a layout without it.
function page(answers, withPlaceholder = true) {
  const placeholder = { id: 'fitt-service-status', hidden: true, textContent: '', style: {}, attrs: {}, setAttribute(k, v) { this.attrs[k] = v; } };
  const body = { children: withPlaceholder ? [placeholder] : [] };
  const document = {
    readyState: 'complete', body,
    getElementById: id => body.children.find(c => c.id === id) ?? null,
    createElement: () => { throw new Error('the banner must not add anything to the page'); },
    addEventListener() {},
  };
  const asked = [];
  let tick = null;
  const ctx = {
    document,
    fetch: async (url, init) => { asked.push({ url, init }); const a = answers.shift(); if (a instanceof Error) throw a; return { ok: true, json: async () => a }; },
    setInterval: fn => { tick = fn; return 1; },
  };
  vm.runInNewContext(script, ctx);
  return { body, placeholder, asked, tick: async () => { tick(); await new Promise(r => setTimeout(r, 0)); }, settle: () => new Promise(r => setTimeout(r, 0)) };
}

test('read-only shows the message as text in the layout placeholder', async () => {
  const p = page([{ status: 'read_only', message: 'อ่านอย่างเดียว <b>x</b>' }]);
  await p.settle();
  assert.equal(p.asked[0].url, '/api/service-status');
  assert.equal(p.placeholder.hidden, false);
  assert.equal(p.placeholder.textContent, 'อ่านอย่างเดียว <b>x</b>');
  assert.equal(p.placeholder.attrs.role, 'status');
  assert.equal(p.placeholder.style.position, 'sticky');
});

test('active shows nothing, and the banner goes when the app is back to active', async () => {
  const p = page([{ status: 'read_only', message: 'm' }, { status: 'active', message: '' }]);
  await p.settle();
  assert.equal(p.placeholder.hidden, false);
  await p.tick();
  assert.equal(p.placeholder.hidden, true);
  assert.equal(p.placeholder.textContent, '');
});

test('an unreachable status leaves the page as it is', async () => {
  const p = page([new Error('offline')]);
  await p.settle();
  assert.equal(p.placeholder.hidden, true);
});

test('a layout without the placeholder is left alone, not added to', async () => {
  const p = page([{ status: 'read_only', message: 'm' }], false);
  await p.settle();
  assert.equal(p.body.children.length, 0);
});
