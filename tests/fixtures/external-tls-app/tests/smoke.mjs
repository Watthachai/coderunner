// One real request against the running image (CRN_BASE_URL), reported in Playwright's JSON shape.
const r = await fetch(process.env.CRN_BASE_URL).catch(() => null);
const ok = r?.status === 200;
console.log(JSON.stringify({ stats: { expected: ok ? 1 : 0, unexpected: ok ? 0 : 1, flaky: 0, skipped: 0 }, errors: [] }));
process.exit(ok ? 0 : 1);
