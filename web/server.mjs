import http from 'node:http';
import https from 'node:https';
import { readFile } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { BlockList, isIP } from 'node:net';
import { createGzip } from 'node:zlib';

const prefix = '/api/app/v1';
const pages = new Set(['/', '/archived', '/review', '/walk', '/insights', '/map', '/settings']);
const notePath = /^\/notes\/[A-Za-z0-9_-]+$/;
const assets = new Map([
  ['/', ['index.html', 'text/html; charset=utf-8']],
  ['/index.html', ['index.html', 'text/html; charset=utf-8']],
  ['/i18n.js', ['i18n.js', 'text/javascript; charset=utf-8']],
  ['/en.js', ['en.js', 'text/javascript; charset=utf-8']],
  ['/app.js', ['app.js', 'text/javascript; charset=utf-8']],
  ['/timeline.js', ['timeline.js', 'text/javascript; charset=utf-8']],
  ['/client.js', ['client.js', 'text/javascript; charset=utf-8']],
  ['/discovery.js', ['discovery.js', 'text/javascript; charset=utf-8']],
  ['/dom.js', ['dom.js', 'text/javascript; charset=utf-8']],
  ['/forms.js', ['forms.js', 'text/javascript; charset=utf-8']],
  ['/leave-guard.js', ['leave-guard.js', 'text/javascript; charset=utf-8']],
  ['/menus.js', ['menus.js', 'text/javascript; charset=utf-8']],
  ['/modal.js', ['modal.js', 'text/javascript; charset=utf-8']],
  ['/notes.js', ['notes.js', 'text/javascript; charset=utf-8']],
  ['/router.js', ['router.js', 'text/javascript; charset=utf-8']],
  ['/search-input.js', ['search-input.js', 'text/javascript; charset=utf-8']],
  ['/material-picker.js', ['material-picker.js', 'text/javascript; charset=utf-8']],
  ['/stream-markdown.js', ['stream-markdown.js', 'text/javascript; charset=utf-8']],
  ['/shell.js', ['shell.js', 'text/javascript; charset=utf-8']],
  ['/draft.js', ['draft.js', 'text/javascript; charset=utf-8']],
  ['/errors.js', ['errors.js', 'text/javascript; charset=utf-8']],
  ['/routes.js', ['routes.js', 'text/javascript; charset=utf-8']],
  ['/markdown.js', ['markdown.js', 'text/javascript; charset=utf-8']],
  ['/media.js', ['media.js', 'text/javascript; charset=utf-8']],
  ['/settings.js', ['settings.js', 'text/javascript; charset=utf-8']],
  ['/insights.js', ['insights.js', 'text/javascript; charset=utf-8']],
  ['/map.js', ['map.js', 'text/javascript; charset=utf-8']],
  ['/map-stars-scene.js', ['map-stars-scene.js', 'text/javascript; charset=utf-8']],
  ['/map-star-paint.js', ['map-star-paint.js', 'text/javascript; charset=utf-8']],
  ['/map-star-cloud.js', ['map-star-cloud.js', 'text/javascript; charset=utf-8']],
  ['/map-star-density.js', ['map-star-density.js', 'text/javascript; charset=utf-8']],
  ['/map-density-worker.js', ['map-density-worker.js', 'text/javascript; charset=utf-8']],
  ['/map-star-renderer.js', ['map-star-renderer.js', 'text/javascript; charset=utf-8']],
  ['/map-render-worker.js', ['map-render-worker.js', 'text/javascript; charset=utf-8']],
  ['/map-scene.js', ['map-scene.js', 'text/javascript; charset=utf-8']],
  ['/map-regions.js', ['map-regions.js', 'text/javascript; charset=utf-8']],
  ['/map-paint.js', ['map-paint.js', 'text/javascript; charset=utf-8']],
  ['/map-worker.js', ['map-worker.js', 'text/javascript; charset=utf-8']],
  ['/chat-stream.js', ['chat-stream.js', 'text/javascript; charset=utf-8']],
  ['/styles.css', ['styles.css', 'text/css; charset=utf-8']],
  ['/shell.css', ['shell.css', 'text/css; charset=utf-8']],
  ['/memos.css', ['memos.css', 'text/css; charset=utf-8']],
  ['/tokens.css', ['tokens.css', 'text/css; charset=utf-8']],
  ['/interaction.css', ['interaction.css', 'text/css; charset=utf-8']],
  ['/layout.css', ['layout.css', 'text/css; charset=utf-8']],
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

function acceptedEncodings(header = '') {
  const qualities = new Map();
  for (const item of String(header).toLowerCase().split(',')) {
    const [name, ...parameters] = item.trim().split(';').map(part => part.trim());
    const q = parameters.find(part => part.startsWith('q='));
    const value = q === undefined ? 1 : /^q=(?:0(?:\.\d{0,3})?|1(?:\.0{0,3})?)$/.test(q) ? Number(q.slice(2)) : 0;
    qualities.set(name, value);
  }
  const quality = name => qualities.get(name) ?? (name === 'identity' ? (qualities.get('*') === 0 ? 0 : 1) : qualities.get('*') ?? 0);
  return ['br', 'gzip', 'identity'].filter(name => quality(name) > 0).sort((a, b) => quality(b) - quality(a));
}

export function traceResponse(req, res, log, { interval = 5000 } = {}) {
  const started = performance.now(), socket = res.socket;
  const initialSocketBytes = socket?.bytesWritten || 0;
  const requestId = randomUUID();
  const clientId = /^[a-f0-9-]{36}$/.test(req.headers['x-moerise-trace'] || '') ? req.headers['x-moerise-trace'] : undefined;
  let bodyBytes = 0, upstreamBytes = 0, firstBody = null, lastBody = null;
  let blockedAt = null, blockedMs = 0, blockedWrites = 0, finished = false, upstreamEnded = false;
  const elapsed = () => Math.round((performance.now() - started) * 100) / 100;
  function event(event, details = {}) {
    try {
      log({ time: new Date().toISOString(), source: 'web', event, request_id: requestId, client_id: clientId,
        path: req.url.split('?')[0], method: req.method, elapsed_ms: elapsed(),
        status: res.headersSent ? res.statusCode : null, body_bytes: bodyBytes, upstream_bytes: upstreamBytes,
        upstream_ended: upstreamEnded, first_body_ms: firstBody, last_body_ms: lastBody,
        socket_bytes: Math.max(0, (socket?.bytesWritten || initialSocketBytes) - initialSocketBytes),
        queued_bytes: res.writableLength, need_drain: res.writableNeedDrain,
        blocked_writes: blockedWrites, blocked_ms: Math.round(blockedMs + (blockedAt === null ? 0 : performance.now() - blockedAt)),
        ...details });
    } catch { /* Diagnostic output must not interrupt the response. */ }
  }
  function count(chunk, encoding) {
    if (chunk === undefined || chunk === null || typeof chunk === 'function') return;
    bodyBytes += Buffer.byteLength(chunk, typeof encoding === 'string' ? encoding : undefined);
    lastBody = elapsed();
    if (firstBody === null) { firstBody = lastBody; event('body_first_chunk'); }
  }
  const write = res.write, end = res.end;
  res.write = function (chunk, ...args) {
    count(chunk, args[0]);
    const ready = write.call(this, chunk, ...args);
    if (!ready && blockedAt === null) { blockedAt = performance.now(); blockedWrites++; }
    return ready;
  };
  res.end = function (chunk, ...args) { count(chunk, args[0]); return end.call(this, chunk, ...args); };
  const timer = setInterval(() => event('progress'), interval);
  timer.unref();
  function drain() {
    if (blockedAt !== null) { blockedMs += performance.now() - blockedAt; blockedAt = null; }
  }
  res.on('drain', drain);
  res.on('finish', () => {
    finished = true; drain(); clearInterval(timer); event('response_finish');
  });
  res.on('close', () => {
    drain(); clearInterval(timer); event('response_close', { write_completed: finished });
  });
  res.on('error', err => event('response_error', { error: err.code || err.name }));
  req.on('aborted', () => event('request_aborted'));
  res.setHeader('X-Request-ID', requestId);
  event('request_start', {
    remote_address: req.socket.remoteAddress,
    forwarded_for: String(req.headers['x-forwarded-for'] || '').slice(0, 256),
    device: /mobile|android|iphone|ipad/i.test(req.headers['user-agent'] || '') ? 'mobile' : 'desktop',
    accept_encoding: String(req.headers['accept-encoding'] || '').slice(0, 128),
  });
  return {
    event, requestId, elapsed,
    upstream(response) {
      event('upstream_headers', { upstream_status: response.statusCode, content_length: response.headers['content-length'] || null });
      response.on('data', chunk => { upstreamBytes += chunk.length; });
      response.on('end', () => { upstreamEnded = true; event('upstream_end'); });
      response.on('aborted', () => event('upstream_aborted'));
      response.on('close', () => event('upstream_close', { upstream_complete: response.complete }));
    },
  };
}

export function createWebServer({
  coreOrigin = process.env.CORE_ORIGIN || 'http://127.0.0.1:8787',
  publicOrigin = process.env.PUBLIC_ORIGIN || 'http://localhost:3000',
  dist = fileURLToPath(new URL('./dist/', import.meta.url)),
  proxyTimeout = 60_000,
  chatProxyTimeout = 125_000,
  healthTimeout = 2_000,
  requestLogger,
} = {}) {
  const core = origin(coreOrigin, 'CORE_ORIGIN');
  const publicURL = webOrigin(publicOrigin);
  const transport = core.protocol === 'https:' ? https : http;
  if (!Number.isInteger(healthTimeout) || healthTimeout < 1 || healthTimeout > 2_147_483_647) {
    throw new Error('Invalid health timeout');
  }
  if (!Number.isInteger(chatProxyTimeout) || chatProxyTimeout < 1 || chatProxyTimeout > 2_147_483_647) {
    throw new Error('Invalid chat proxy timeout');
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
      const trace = ['/api/app/v1/map', '/api/app/v1/session'].includes(path) && typeof requestLogger === 'function'
        ? traceResponse(req, res, requestLogger) : null;
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
        trace?.upstream(response);
        res.statusCode = response.statusCode;
        const connectionHeaders = new Set(String(response.headers.connection || '').split(',').map(name => name.trim().toLowerCase()));
        for (const [name, value] of Object.entries(response.headers)) {
          if (value !== undefined && !hopHeaders.has(name) && !connectionHeaders.has(name) &&
              !['cache-control', 'content-security-policy', 'www-authenticate'].includes(name) &&
              !name.startsWith('access-control-')) {
            res.setHeader(name, value);
          }
        }
        if (trace) {
          res.setHeader('X-Request-ID', trace.requestId);
          res.setHeader('Server-Timing', `upstream;dur=${trace.elapsed()}`);
        }
        response.on('error', err => {
          trace?.event('upstream_response_error', { error: err.code || err.name }); res.destroy();
        });
        if (req.method === 'GET' && path === '/api/app/v1/map' && response.statusCode === 200 &&
            !response.headers['content-encoding'] && acceptedEncodings(req.headers['accept-encoding']).includes('gzip')) {
          const compression = createGzip({ level: 1 });
          res.removeHeader('Content-Length');
          res.setHeader('Content-Encoding', 'gzip'); res.setHeader('Vary', 'Accept-Encoding');
          compression.on('end', () => trace?.event('compression_end'));
          compression.on('error', err => {
            trace?.event('compression_error', { error: err.code || err.name }); res.destroy();
          });
          response.pipe(compression).pipe(res);
        } else response.pipe(res);
      });
      const chatStream = req.method === 'POST' && /^\/api\/app\/v1\/chats\/[a-zA-Z0-9-]+\/messages$/.test(path);
      upstream.setTimeout(chatStream ? chatProxyTimeout : proxyTimeout, () => {
        trace?.event('upstream_timeout'); upstream.destroy(new Error('timeout'));
      });
      upstream.on('error', err => {
        trace?.event('upstream_request_error', { error: err.code || err.message });
        if (res.destroyed) return;
        if (res.headersSent) res.destroy();
        else error(res, 502, 'CORE_UNAVAILABLE', '服务暂时无法连接，请重试；创建请求请继续使用原提交。');
      });
      req.on('aborted', () => upstream.destroy());
      res.on('close', () => { if (!res.writableFinished) upstream.destroy(); });
      req.pipe(upstream);
      return;
    }
    const versioned = /^\/([a-z][a-z0-9-]*)\.([a-f0-9]{20})\.(js|css|svg)$/.exec(path);
    const original = versioned && assets.get(`/${versioned[1]}.${versioned[3]}`);
    const asset = assets.get(path) || (original ? [path.slice(1), original[1]] : null) ||
      (pages.has(path) || notePath.test(path) ? assets.get('/') : null);
    if (!asset || !['GET', 'HEAD'].includes(req.method)) {
      error(res, 404, 'NOT_FOUND', '页面不存在。');
      return;
    }
    try {
      let body, encoding;
      for (const accepted of acceptedEncodings(req.headers['accept-encoding'])) {
        try {
          body = await readFile(resolve(dist, asset[0] + (accepted === 'identity' ? '' : `.${accepted === 'gzip' ? 'gz' : 'br'}`)));
          encoding = accepted;
          break;
        } catch (err) {
          if (accepted === 'identity' || err.code !== 'ENOENT') throw err;
        }
      }
      if (!body) {
        error(res, 406, 'ENCODING_NOT_ACCEPTABLE', '没有可接受的响应编码。');
        return;
      }
      res.setHeader('Vary', 'Accept-Encoding');
      if (encoding !== 'identity') res.setHeader('Content-Encoding', encoding);
      if (versioned) res.setHeader('Cache-Control', 'public, max-age=31536000, immutable');
      res.writeHead(200, { 'Content-Type': asset[1], 'Content-Length': body.length });
      res.end(req.method === 'HEAD' ? undefined : body);
    } catch {
      if (versioned) error(res, 404, 'NOT_FOUND', '页面不存在。');
      else error(res, 503, 'WEB_BUILD_MISSING', '页面文件缺失，请先运行 npm run build。');
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
