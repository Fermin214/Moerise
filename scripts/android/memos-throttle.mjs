import http from 'node:http';
import { createModelGate } from './fixture-policy.mjs';

// 向量原文复查和同步共用请求余量；只转发 Memos 官方 API，不读取其数据库。
export function createMemosThrottle(sourceOrigin, signal) {
  const gate = createModelGate({ maxPending: 64, intervalMs: 650 });
  return http.createServer(async (req, res) => {
    const queuedAt = performance.now();
    const release = await gate(signal);
    const waitMs = Math.round(performance.now() - queuedAt);
    if (waitMs >= 5000) console.warn('Memos fixture queue', JSON.stringify({
      method: req.method, wait_ms: waitMs, cancelled: req.destroyed || res.destroyed,
    }));
    if (!release) { res.writeHead(503, { 'Retry-After': '2' }); res.end(); return; }
    if (req.destroyed || res.destroyed) { release(); return; }
    let released = false, upstream;
    const finish = () => {
      if (!released) { released = true; release(); }
    };
    res.once('finish', finish);
    res.once('close', () => { upstream?.destroy(); finish(); });
    try {
      upstream = http.request(sourceOrigin, {
        method: req.method, path: req.url, headers: { ...req.headers, host: new URL(sourceOrigin).host },
      }, response => {
        // 文件正文受下游背压影响；响应头到达后释放 API 队列，允许 Core 复查父笔记。
        if (req.method === 'GET' && req.url.startsWith('/file/')) finish();
        res.writeHead(response.statusCode, response.headers);
        response.once('error', () => res.destroy());
        response.pipe(res);
      });
      upstream.once('error', () => {
        if (!res.headersSent) res.writeHead(502);
        res.end();
      });
      req.once('aborted', () => upstream.destroy());
      req.pipe(upstream);
    } catch {
      if (!res.headersSent) res.writeHead(502);
      res.end();
    }
  });
}
