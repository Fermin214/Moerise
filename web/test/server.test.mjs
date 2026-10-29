import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { test } from 'node:test';
import { createWebServer } from '../server.mjs';

async function fixture(t, handler) {
  const core = http.createServer(handler);
  core.listen(0, '127.0.0.1');
  await once(core, 'listening');
  const web = createWebServer({
    coreOrigin: `http://127.0.0.1:${core.address().port}`,
    publicOrigin: 'https://notes.example.com',
  });
  web.listen(0, '127.0.0.1');
  await once(web, 'listening');
  t.after(async () => {
    await Promise.all([web, core].map(server => new Promise(resolve => {
      server.close(resolve);
      server.closeAllConnections();
    })));
  });
  return { core, web, url: `http://127.0.0.1:${web.address().port}` };
}

function raw(url, path) {
  return new Promise((resolve, reject) => {
    const req = http.get({ hostname: '127.0.0.1', port: new URL(url).port, path }, res => {
      res.resume();
      res.on('end', () => resolve(res.statusCode));
    });
    req.on('error', reject);
  });
}

test('build assets are served with no-store and a same-origin CSP', async t => {
  const { url } = await fixture(t, () => assert.fail('Static files must not reach Core'));
  for (const path of ['/', '/app.js', '/draft.js', '/errors.js', '/routes.js', '/markdown.js', '/styles.css', '/favicon.svg']) {
    const response = await fetch(url + path);
    assert.equal(response.status, 200);
    assert.equal(response.headers.get('cache-control'), 'no-store');
    assert.match(response.headers.get('content-security-policy'), /connect-src 'self'/);
    assert.equal(response.headers.get('x-content-type-options'), 'nosniff');
    assert.ok((await response.text()).length > 100);
  }
  const head = await fetch(url, { method: 'HEAD' });
  assert.equal(head.status, 200);
  assert.equal(await head.text(), '');
});

test('only the raw fixed client API prefix can reach Core', async t => {
  let count = 0;
  const { url } = await fixture(t, (req, res) => { count++; res.end('{}'); });
  for (const path of [
    '/api/ext/v1/notes', '/mcp', '/api/v1/memos', '/server.mjs', '/package.json',
    '/api/app/v10/notes', '//example.com/api/app/v1/notes',
    '/api/app/v1/../ext/v1/notes', '/api/app/v1/%2e%2e/notes',
    '/api/app/v1/%2f..%2fnotes', '/api/app/v1/notes\\..\\..\\ext',
  ]) {
    assert.ok([400, 404].includes(await raw(url, path)), path);
  }
  assert.equal(count, 0);
  assert.equal((await fetch(`${url}/api/app/v1/notes?state=NORMAL&order=created_desc&page_size=20`)).status, 200);
  assert.equal(count, 1);
});

test('forwards raw content, cookie, origin, csrf and idempotency without machine headers', async t => {
  let received;
  const { url } = await fixture(t, async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    received = { headers: req.headers, path: req.url, body };
    res.writeHead(201, {
      'Content-Type': 'application/json', 'Set-Cookie': ['session=opaque; HttpOnly; Path=/'],
      'Cache-Control': 'public, max-age=999',
      'Access-Control-Allow-Origin': '*',
    });
    res.end(JSON.stringify({ name: 'memos/a', content: JSON.parse(body).content }));
  });
  const content = '  原正文\n#标签 #另一个标签\n<script>private</script>  \n';
  const response = await fetch(`${url}/api/app/v1/notes`, {
    method: 'POST',
    headers: {
      Origin: 'https://notes.example.com', Cookie: 'session=opaque',
      'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf',
      'Idempotency-Key': 'same-key',
      'X-Forwarded-Host': 'attacker.example', 'X-Target-URL': 'http://attacker.example',
    },
    body: JSON.stringify({ content }),
  });
  assert.equal(response.status, 201);
  assert.equal((await response.json()).content, content);
  assert.equal(received.path, '/api/app/v1/notes');
  assert.equal(JSON.parse(received.body).content, content);
  assert.equal(received.headers.cookie, 'session=opaque');
  assert.equal(received.headers.origin, 'https://notes.example.com');
  assert.equal(received.headers['x-csrf-token'], 'csrf');
  assert.equal(received.headers['idempotency-key'], 'same-key');
  for (const header of ['authorization', 'x-forwarded-host', 'x-target-url']) assert.equal(received.headers[header], undefined);
  assert.match(response.headers.get('set-cookie'), /HttpOnly/);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  assert.equal(response.headers.get('access-control-allow-origin'), null);
});

test('rejects Authorization even with an existing cookie, including an empty header', async t => {
  const { url } = await fixture(t, () => assert.fail('Authorization requests must not reach Core'));
  for (const path of ['/api/app/v1', '/api/app/v1/session', '/api/app/v1/notes']) {
    for (const authorization of ['', 'Bearer machine-token']) {
      for (const method of ['GET', 'POST', 'PATCH', 'DELETE']) {
        const response = await fetch(url + path, {
          method, headers: {
            Cookie: 'session=existing-cookie', Authorization: authorization,
            Origin: 'https://notes.example.com', 'X-CSRF-Token': 'csrf',
          },
        });
        assert.equal(response.status, 403);
        assert.equal((await response.json()).error.code, 'AUTHORIZATION_REJECTED');
      }
    }
  }
});

test('deep links serve the application only for whitelisted pages', async t => {
  const { url } = await fixture(t, () => assert.fail('Page requests must not reach Core'));
  for (const path of ['/notes/abc-123?from=%2Farchived', '/archived?tag=a', '/review', '/walk', '/insights', '/map', '/settings']) {
    const response = await fetch(url + path);
    assert.equal(response.status, 200, path);
    assert.match(response.headers.get('content-type'), /text\/html/);
    assert.match(await response.text(), /id="app"/);
  }
  assert.equal((await fetch(`${url}/notes/abc`, { method: 'HEAD' })).status, 200);
  for (const path of ['/notes/', '/notes/abc/extra', '/unknown', '/src/routes.js', '/internal/config']) {
    assert.equal((await fetch(url + path)).status, 404, path);
  }
});

test('PATCH forwards full JSON, version, CSRF and retry key and rejects other origins', async t => {
  let received;
  const { url } = await fixture(t, async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    received = { method: req.method, body: JSON.parse(body), headers: req.headers };
    res.end('{}');
  });
  const body = { content: '#原文\n', source_hash: 'source-v1', attachments: [{ filename: 'a', type: 'text/plain', content: 'YQ==' }], remove_attachments: ['attachments/b'] };
  const response = await fetch(`${url}/api/app/v1/notes/a`, {
    method: 'PATCH', headers: { Origin: 'https://notes.example.com', 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf', 'Idempotency-Key': 'fixed-key' },
    body: JSON.stringify(body),
  });
  assert.equal(response.status, 200);
  assert.equal(received.method, 'PATCH');
  assert.deepEqual(received.body, body);
  assert.equal(received.headers['idempotency-key'], 'fixed-key');
  assert.equal(received.headers['x-csrf-token'], 'csrf');
  assert.equal((await fetch(`${url}/api/app/v1/notes/a`, { method: 'PATCH' })).status, 403);
});

test('protected attachment GET and HEAD stream Range metadata and cookie through Core', async t => {
  const { url } = await fixture(t, (req, res) => {
    assert.equal(req.headers.cookie, 'session=opaque');
    assert.equal(req.headers.range, 'bytes=1-3');
    assert.equal(req.headers['if-range'], '"revision"');
    res.writeHead(206, {
      'Content-Type': 'application/octet-stream', 'Content-Length': '3',
      'Content-Range': 'bytes 1-3/5', 'Accept-Ranges': 'bytes', 'Content-Disposition': 'attachment; filename="a.bin"',
    });
    res.end(req.method === 'HEAD' ? undefined : Buffer.from([1, 2, 3]));
  });
  for (const method of ['GET', 'HEAD']) {
    const response = await fetch(`${url}/api/app/v1/notes/a/attachments/b/content`, {
      method, headers: { Cookie: 'session=opaque', Range: 'bytes=1-3', 'If-Range': '"revision"' },
    });
    assert.equal(response.status, 206);
    assert.equal(response.headers.get('content-range'), 'bytes 1-3/5');
    assert.equal(response.headers.get('cache-control'), 'no-store');
    assert.match(response.headers.get('content-disposition'), /attachment/);
    assert.deepEqual([...new Uint8Array(await response.arrayBuffer())], method === 'HEAD' ? [] : [1, 2, 3]);
  }
});

test('rejects invalid write origins and methods, passes session deletion', async t => {
  let count = 0;
  const { url } = await fixture(t, (req, res) => {
    count++;
    assert.equal(req.method, 'DELETE');
    assert.equal(req.headers['x-csrf-token'], 'csrf');
    res.writeHead(204, { 'Set-Cookie': 'session=; Max-Age=0; Path=/' });
    res.end();
  });
  for (const value of [undefined, 'null', 'https://attacker.example', 'https://notes.example.com.attacker.example']) {
    const headers = value === undefined ? {} : { Origin: value };
    assert.equal((await fetch(`${url}/api/app/v1/session`, { method: 'POST', headers })).status, 403);
  }
  assert.equal((await fetch(`${url}/api/app/v1/session`, { method: 'PUT' })).status, 405);
  assert.equal(count, 0);
  const response = await fetch(`${url}/api/app/v1/session`, {
    method: 'DELETE', headers: { Origin: 'https://notes.example.com', 'X-CSRF-Token': 'csrf' },
  });
  assert.equal(response.status, 204);
  assert.equal(count, 1);
});

test('returns a structured no-store error when Core disconnects', async t => {
  const { url } = await fixture(t, req => req.socket.destroy());
  const response = await fetch(`${url}/api/app/v1/session`);
  assert.equal(response.status, 502);
  assert.equal(response.headers.get('cache-control'), 'no-store');
  const { error } = await response.json();
  assert.equal(error.code, 'CORE_UNAVAILABLE');
  assert.equal(error.retryable, true);
  assert.ok(error.request_id);
});

test('does not follow upstream redirects or send credentials to a redirect target', async t => {
  let count = 0;
  const { url } = await fixture(t, (req, res) => {
    count++;
    res.writeHead(302, { Location: 'https://unrelated.invalid/' });
    res.end();
  });
  const response = await fetch(`${url}/api/app/v1/session`, { redirect: 'manual' });
  assert.equal(response.status, 302);
  assert.equal(count, 1);
});

test('rejects configured origins that could add credentials or paths', () => {
  for (const coreOrigin of [
    'file:///etc/passwd', 'https://user:secret@example.com', 'http://example.com/api',
    'http://example.com?target=http://attacker.example', 'http://example.com/#fragment',
  ]) assert.throws(() => createWebServer({ coreOrigin }));
  assert.throws(() => createWebServer({ publicOrigin: 'https://notes.example.com/path' }));
});
