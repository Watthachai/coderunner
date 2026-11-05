import fs from 'node:fs';
import http from 'node:http';
// The suspension switch the checker exercises: suspended -> 503 on /, health stays up.
async function suspendedMessage() {
  if (!process.env.FITT_STATUS_URL) return null;
  try {
    const r = await fetch(process.env.FITT_STATUS_URL, { headers: { authorization: `Bearer ${process.env.FITT_LICENSE_KEY ?? ''}` } });
    const s = await r.json();
    return s.status === 'suspended' ? s.message : null;
  } catch { return null; }
}
// The feedback widget, wired as CRN wires it into a Next app. FIXTURE_WEAK_WIDGET: "twice"
// loads it twice, "outside" makes the page ask another origin, so the release gate's tests
// can show that it catches them.
const PROJECT = '11111111-1111-4111-8111-111111111111';
const weakWidget = process.env.FIXTURE_WEAK_WIDGET;
const tag = `<script src="/fitt-feedback.js" data-fitt-feedback="true" data-project="${PROJECT}" data-ingest="/api/fitt-feedback"></script>`;
const login = `<!doctype html><html><head><title>login</title></head><body><p>fixture login</p>${weakWidget === 'outside' ? '<img src="http://192.0.2.1/x.png" alt="">' : ''}${tag}${weakWidget === 'twice' ? tag : ''}</body></html>`;
http.createServer(async (req, res) => {
  if (req.url === '/api/health') { res.writeHead(200); return res.end('ok'); }
  const message = await suspendedMessage();
  if (message) { res.writeHead(503, { 'content-type': 'text/html' }); return res.end(`<p>${message}</p>`); }
  if (req.url === '/fitt-feedback.js') { res.writeHead(200, { 'content-type': 'application/javascript' }); return res.end(fs.readFileSync(new URL('./public/fitt-feedback.js', import.meta.url))); }
  // Nothing listens behind the route in a test, as with an app whose receiver is not set.
  if (req.url === '/api/fitt-feedback' && req.method === 'POST') { res.writeHead(502); return res.end('feedback receiver unreachable'); }
  if (req.url === '/login') { res.writeHead(200, { 'content-type': 'text/html' }); return res.end(login); }
  res.writeHead(200, { 'content-type': 'text/html' }); res.end('<p>fixture</p>');
}).listen(3000, '0.0.0.0');
