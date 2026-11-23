import fs from 'node:fs';
import http from 'node:http';
// The suspension switch the checker exercises: suspended -> 503 on /, health stays up;
// read_only -> pages served, writes refused with 423, the status route and the banner.
async function serviceStatus() {
  if (!process.env.FITT_STATUS_URL) return { status: 'active', message: '' };
  try {
    const r = await fetch(process.env.FITT_STATUS_URL, { headers: { authorization: `Bearer ${process.env.FITT_LICENSE_KEY ?? ''}`, 'x-fitt-switch-version': '2' } });
    return await r.json();
  } catch { return { status: 'active', message: '' }; }
}
// The feedback widget, wired as CRN wires it into a Next app. FIXTURE_WEAK_WIDGET: "twice"
// loads it twice, "outside" makes the page ask another origin, so the release gate's tests
// can show that it catches them. "absent" is a build without the widget, and "absent-outside"
// one whose page asks another origin all the same.
const PROJECT = '11111111-1111-4111-8111-111111111111';
const weakWidget = process.env.FIXTURE_WEAK_WIDGET;
const tag = `<script src="/fitt-feedback.js" data-fitt-feedback="true" data-project="${PROJECT}" data-ingest="/api/fitt-feedback"></script>`;
const noWidget = weakWidget === 'absent' || weakWidget === 'absent-outside';
const login = `<!doctype html><html><head><title>login</title></head><body><p>fixture login</p>${weakWidget === 'outside' || weakWidget === 'absent-outside' ? '<img src="http://192.0.2.1/x.png" alt="">' : ''}${noWidget ? '' : tag}${weakWidget === 'twice' ? tag : ''}</body></html>`;
http.createServer(async (req, res) => {
  if (req.url === '/api/health') { res.writeHead(200); return res.end('ok'); }
  const sw = await serviceStatus();
  if (req.url === '/api/service-status') { res.writeHead(200, { 'content-type': 'application/json' }); return res.end(JSON.stringify({ status: sw.status, message: sw.message })); }
  if (sw.status === 'suspended') { res.writeHead(503, { 'content-type': 'text/html' }); return res.end(`<p>${sw.message}</p>`); }
  if (sw.status === 'read_only' && !['GET', 'HEAD', 'OPTIONS'].includes(req.method)) { res.writeHead(423, { 'content-type': 'application/json' }); return res.end(JSON.stringify({ error: 'read_only', message: sw.message })); }
  if (noWidget && (req.url === '/fitt-feedback.js' || req.url === '/api/fitt-feedback')) { res.writeHead(404); return res.end('not found'); }
  if (req.url === '/fitt-feedback.js') { res.writeHead(200, { 'content-type': 'application/javascript' }); return res.end(fs.readFileSync(new URL('./public/fitt-feedback.js', import.meta.url))); }
  // Nothing listens behind the route in a test, as with an app whose receiver is not set.
  if (req.url === '/api/fitt-feedback' && req.method === 'POST') { res.writeHead(502); return res.end('feedback receiver unreachable'); }
  if (req.url === '/login') { res.writeHead(200, { 'content-type': 'text/html' }); return res.end(login); }
  res.writeHead(200, { 'content-type': 'text/html' }); res.end('<script src="/fitt-service-status.js" defer></script><div id="fitt-service-status" hidden></div><p>fixture</p>');
}).listen(3000, '0.0.0.0');
