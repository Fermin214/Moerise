import http from 'node:http';

export function createFixtureProxy(coreOrigin) {
  return http.createServer((req, res) => {
    const upstream = http.request(new URL(req.url, coreOrigin), {
      method: req.method, headers: req.headers,
    }, response => {
      res.writeHead(response.statusCode, response.headers);
      response.on('error', () => res.destroy());
      response.pipe(res);
    });
    upstream.on('error', () => {
      if (res.headersSent) res.destroy();
      else { res.writeHead(502); res.end(); }
    });
    req.on('aborted', () => upstream.destroy());
    res.on('close', () => { if (!res.writableFinished) upstream.destroy(); });
    req.pipe(upstream);
  });
}
