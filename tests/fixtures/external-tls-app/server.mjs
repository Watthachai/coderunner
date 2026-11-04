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
http.createServer(async (req, res) => {
  if (req.url === '/api/health') { res.writeHead(200); return res.end('ok'); }
  const message = await suspendedMessage();
  if (message) { res.writeHead(503, { 'content-type': 'text/html' }); return res.end(`<p>${message}</p>`); }
  res.writeHead(200, { 'content-type': 'text/html' }); res.end('<p>fixture</p>');
}).listen(3000, '0.0.0.0');
