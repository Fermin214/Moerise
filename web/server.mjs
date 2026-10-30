import http from 'node:http';
import https from 'node:https';
import { readFile } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { BlockList, isIP } from 'node:net';

const prefix = '/api/app/v1';
const pages = new Set(['/', '/archived', '/review', '/walk', '/insights', '/map', '/settings']);
const notePath = /^\/notes\/[A-Za-z0-9_-]+$/;
const assets = new Map([
  ['/', ['index.html', 'text/html; charset=utf-8']],
  ['/index.html', ['index.html', 'text/html; charset=utf-8']],
  ['/app.js', ['app.js', 'text/javascript; charset=utf-8']],
  ['/draft.js', ['draft.js', 'text/javascript; charset=utf-8']],
  ['/errors.js', ['errors.js', 'text/javascript; charset=utf-8']],
  ['/routes.js', ['routes.js', 'text/javascript; charset=utf-8']],
  ['/markdown.js', ['markdown.js', 'text/javascript; charset=utf-8']],
  ['/settings.js', ['settings.js', 'text/javascript; charset=utf-8']],
  ['/styles.css', ['styles.css', 'text/css; charset=utf-8']],
  ['/prototype.css', ['prototype.css', 'text/css; charset=utf-8']],
  ['/memos.css', ['memos.css', 'text/css; charset=utf-8']],
  ['/layout.css', ['layout.css', 'text/css; charset=utf-8']],
  ['/memos-logo.webp', ['memos-logo.webp', 'image/webp']],
  ['/favicon.svg', ['favicon.svg', 'image/svg+xml']],
]);
const hopHeaders = new Set([
  'connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization',
  'te', 'trailer', 'transfer-encoding', 'upgrade',
]);

function origin(value, label) {
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password ||
      url.pathname !== '/' || url.search || url.hash) {
    throw new Error(`${label} must be an HTTP(S) origin without a path or credentials`);
  }
  return url;
}

function webOrigin(value) {
  const url = origin(value, 'PUBLIC_ORIGIN');
  const parts = /^https?:\/\/(\[[\da-f:.]+\]|[\da-z.-]+)(?::(\d+))?\/?$/i.exec(value);
  if (!parts || (parts[2] !== undefined && Number(parts[2]) === 0)) {
    throw new Error('Invalid PUBLIC_ORIGIN');
  }
  const host = parts[1].replace(/^\[|\]$/g, '').replace(/\.$/, '').toLowerCase();
  const family = isIP(host);
  if (!family && (host.length > 253 ||
      !host.split('.').every(label => /^[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?$/.test(label)) ||
      /^(?:\d+|0x[\da-f]*)$/.test(host.split('.').at(-1)))) {
    throw new Error('Invalid PUBLIC_ORIGIN');
  }
  const loopback = new BlockList();
  loopback.addSubnet('127.0.0.0', 8, 'ipv4');
  loopback.addAddress('::1', 'ipv6');
  if (url.protocol === 'http:' && host !== 'localhost' &&
      !(family && loopback.check(host, family === 6 ? 'ipv6' : 'ipv4'))) {
    throw new Error('PUBLIC_ORIGIN requires HTTPS or a loopback HTTP address');
  }
  return url;
}

function coreReady(core, transport, timeout) {
  return new Promise(resolve => {
    // Health probes have a fixed target and never inherit browser headers.
    const probe = transport.get({
      protocol: core.protocol, hostname: core.hostname.replace(/^\[|\]$/g, ''),
      port: core.port || undefined, path: '/health/ready',
      headers: { Accept: 'application/json' }, signal: AbortSignal.timeout(timeout),
    }, response => {
      response.on('error', () => resolve(false));
      response.on('end', () => resolve(response.statusCode === 200));
      response.resume();
    });
    probe.on('error', () => resolve(false));
  });
}

function error(res, status, code, message) {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(JSON.stringify({ error: { code, message, retryable: status >= 500, request_id: randomUUID() } }));
}

export function createWebServer({
  coreOrigin = process.env.CORE_ORIGIN || 'http://127.0.0.1:8787',
  publicOrigin = process.env.PUBLIC_ORIGIN || 'http://localhost:3000',
  dist = fileURLToPath(new URL('./dist/', import.meta.url)),
  proxyTimeout = 60_000,
  healthTimeout = 2_000,
} = {}) {
  const core = origin(coreOrigin, 'CORE_ORIGIN');
  const publicURL = webOrigin(publicOrigin);
  const transport = core.protocol === 'https:' ? https : http;
  if (!Number.isInteger(healthTimeout) || healthTimeout < 1 || healthTimeout > 2_147_483_647) {
    throw new Error('Invalid health timeout');
  }

  return http.createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    res.setHeader('Referrer-Policy', 'no-referrer');
    res.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'");
    res.setHeader('Permissions-Policy', 'camera=(), microphone=(), geolocation=()');

    // Match the raw request target before any URL normalization can remove dot segments.
    const target = req.url || '/';
    const path = target.split('?')[0];
    if (!target.startsWith('/') || target.startsWith('//') ||
        /[%\\#]/.test(path) || path.split('/').some(part => part === '.' || part === '..')) {
      error(res, 400, 'INVALID_PATH', '请求路径无效。');
      return;
    }
    if (path === '/health/live' || path === '/health/ready') {
      if (!['GET', 'HEAD'].includes(req.method)) {
        res.setHeader('Allow', 'GET, HEAD');
        error(res, 405, 'METHOD_NOT_ALLOWED', '此请求方法不可用。');
        return;
      }
      let healthy = false;
      try {
        await readFile(resolve(dist, 'index.html'));
        healthy = path === '/health/live' || await coreReady(core, transport, healthTimeout);
      } catch {
        // Health responses expose neither build contents nor upstream failures.
      }
      const body = JSON.stringify({ status: healthy ? (path === '/health/live' ? 'ok' : 'ready') : 'degraded' });
      res.writeHead(healthy ? 200 : 503, {
        'Content-Type': 'application/json; charset=utf-8', 'Content-Length': Buffer.byteLength(body),
      });
      res.end(req.method === 'HEAD' ? undefined : body);
      return;
    }
    if (path === prefix || path.startsWith(`${prefix}/`)) {
      if (req.headers.authorization !== undefined) {
        error(res, 403, 'AUTHORIZATION_REJECTED', '客户端 API 仅使用登录会话，不接受 Authorization 请求头。');
        return;
      }
      if (!['GET', 'HEAD', 'POST', 'PATCH', 'DELETE'].includes(req.method)) {
        res.setHeader('Allow', 'GET, HEAD, POST, PATCH, DELETE');
        error(res, 405, 'METHOD_NOT_ALLOWED', '此请求方法不可用。');
        return;
      }
      if (!['GET', 'HEAD'].includes(req.method) && req.headers.origin !== publicURL.origin) {
        error(res, 403, 'ORIGIN_REJECTED', '请求来源不匹配。');
        return;
      }
      const headers = {};
      for (const name of ['accept', 'content-type', 'content-length', 'cookie', 'origin', 'x-csrf-token', 'idempotency-key',
        ...(['GET', 'HEAD'].includes(req.method) ? ['range', 'if-range'] : [])]) {
        if (req.headers[name] !== undefined) headers[name] = req.headers[name];
      }
      headers.host = core.host;
      const upstream = transport.request({
        protocol: core.protocol, hostname: core.hostname.replace(/^\[|\]$/g, ''),
        port: core.port || undefined, method: req.method, path: target, headers,
      }, response => {
        res.statusCode = response.statusCode;
        const connectionHeaders = new Set(String(response.headers.connection || '').split(',').map(name => name.trim().toLowerCase()));
        for (const [name, value] of Object.entries(response.headers)) {
          if (value !== undefined && !hopHeaders.has(name) && !connectionHeaders.has(name) &&
              !['cache-control', 'content-security-policy', 'www-authenticate'].includes(name) &&
              !name.startsWith('access-control-')) {
            res.setHeader(name, value);
          }
        }
        response.on('error', () => res.destroy());
        response.pipe(res);
      });
      upstream.setTimeout(proxyTimeout, () => upstream.destroy(new Error('timeout')));
      upstream.on('error', () => {
        if (res.destroyed) return;
        if (res.headersSent) res.destroy();
        else error(res, 502, 'CORE_UNAVAILABLE', '服务暂时无法连接，请重试；创建请求请继续使用原提交。');
      });
      req.on('aborted', () => upstream.destroy());
      res.on('close', () => { if (!res.writableFinished) upstream.destroy(); });
      req.pipe(upstream);
      return;
    }
    const asset = assets.get(path) || (pages.has(path) || notePath.test(path) ? assets.get('/') : null);
    if (!asset || !['GET', 'HEAD'].includes(req.method)) {
      error(res, 404, 'NOT_FOUND', '页面不存在。');
      return;
    }
    try {
      const body = await readFile(resolve(dist, asset[0]));
      res.writeHead(200, { 'Content-Type': asset[1], 'Content-Length': body.length });
      res.end(req.method === 'HEAD' ? undefined : body);
    } catch {
      error(res, 503, 'WEB_BUILD_MISSING', '页面文件缺失，请先运行 npm run build。');
    }
  });
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const port = Number(process.env.PORT || 3000);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Invalid PORT');
  const server = createWebServer();
  server.listen(port, process.env.HOST || '0.0.0.0', () => console.log(`Moerise Web listening on ${port}`));
  for (const signal of ['SIGTERM', 'SIGINT']) {
    process.on(signal, () => {
      server.close(() => process.exit(0));
      setTimeout(() => { server.closeAllConnections(); process.exit(0); }, 5000).unref();
    });
  }
}
