import assert from 'node:assert/strict';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import { readFile } from 'node:fs/promises';
import { parseArgs } from 'node:util';

const { values } = parseArgs({ options: {
  origin: { type: 'string' }, upstream: { type: 'string' },
  cert: { type: 'string' }, key: { type: 'string' },
  'proxy-port': { type: 'string', default: '28080' },
} });
const origin = new URL(values.origin);
const upstream = new URL(values.upstream);
assert.equal(origin.protocol, 'https:');
assert.equal(origin.origin, values.origin);
assert.equal(upstream.protocol, 'http:');
assert.equal(upstream.origin, values.upstream);
assert.ok(['127.0.0.1', 'localhost', '[::1]'].includes(upstream.hostname));
const proxyPort = Number(values['proxy-port']);
assert.ok(Number.isInteger(proxyPort) && proxyPort > 0 && proxyPort <= 65535);
const tlsPort = Number(origin.port || 443);
const authority = `${origin.hostname}:${tlsPort}`;
const sockets = new Set();
function track(socket) {
  sockets.add(socket);
  socket.once('close', () => sockets.delete(socket));
}
const tls = https.createServer({
  cert: await readFile(values.cert), key: await readFile(values.key),
}, (req, res) => {
  const request = http.request(upstream, {
    method: req.method, path: req.url, headers: req.headers,
  }, response => {
    res.writeHead(response.statusCode, response.headers);
    response.pipe(res);
    response.once('error', () => res.destroy());
  });
  request.once('error', () => {
    if (!res.headersSent) res.writeHead(502);
    res.end();
  });
  req.once('aborted', () => request.destroy());
  res.once('close', () => { if (!res.writableEnded) request.destroy(); });
  req.pipe(request);
});
const proxy = http.createServer((_req, res) => { res.writeHead(403); res.end(); });
proxy.on('connect', (req, client, head) => {
  if (req.url !== authority) {
    client.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
    return;
  }
  const target = net.connect(tlsPort, '127.0.0.1', () => {
    client.write('HTTP/1.1 200 Connection Established\r\n\r\n');
    if (head.length) target.write(head);
    target.pipe(client);
    client.pipe(target);
  });
  track(target);
  client.once('error', () => target.destroy());
  client.once('close', () => target.destroy());
  target.once('error', () => client.destroy());
  target.once('close', () => client.destroy());
});
for (const server of [tls, proxy]) server.on('connection', track);
await Promise.all([
  new Promise((resolve, reject) => tls.once('error', reject).listen(tlsPort, '127.0.0.1', resolve)),
  new Promise((resolve, reject) => proxy.once('error', reject).listen(proxyPort, '127.0.0.1', resolve)),
]);
console.log(`HTTPS USB fixture: ${origin.origin}`);
console.log(`adb reverse tcp:${proxyPort} tcp:${proxyPort}`);
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => {
  for (const socket of sockets) socket.destroy();
  tls.close();
  proxy.close();
});
