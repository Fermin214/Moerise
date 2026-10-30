import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdir, readFile, writeFile, rm, realpath, lstat } from 'node:fs/promises';
import { resolve, dirname, basename } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { runProcess } from './compose-process.mjs';
import { createWebServer } from '../web/server.mjs';

assert.ok(process.env.MOERISE_PLAYWRIGHT_MODULE, '需要 MOERISE_PLAYWRIGHT_MODULE');
const { chromium } = await import(pathToFileURL(process.env.MOERISE_PLAYWRIGHT_MODULE).href);
const repo = fileURLToPath(new URL('../', import.meta.url));
const id = randomBytes(8).toString('hex'), name = `moerise-discovery-${id}`;
const root = resolve(repo, '.local', `discovery-acceptance-${id}`);
const source = `${name}-source`, core = `${name}-core`, containers = new Set();
const cancellation = new AbortController(), sourceStop = new AbortController();
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => cancellation.abort(new Error(signal)));
const username = 'discovery-owner', password = randomBytes(24).toString('hex');
const modelKey = randomBytes(24).toString('hex'), machineKey = randomBytes(24).toString('hex');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=', 'base64');
const evidence = { checks: [], pageErrors: [], sourceFailures: [], browserRequests: [], embeddingCalls: 0 };
const fixtures = [];
let browser, page, web, mock, sourceProxy, cookie = '', csrf = '', url, coreURL, sourceURL, pat;
let network = false, step = '初始化', success = false;

async function docker(args, cleanup = false) {
  try {
    return await runProcess('docker', args, {
      cwd: repo, timeout: 180_000, signal: cleanup ? undefined : cancellation.signal,
    });
  } catch { throw new Error(`Docker ${args[0]} 失败`); }
}
async function port() {
  const server = http.createServer(); server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const value = server.address().port; await new Promise(resolve => server.close(resolve)); return value;
}
async function wait(label, predicate) {
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    cancellation.signal.throwIfAborted();
    if (await predicate()) return;
    await delay(1000, undefined, { signal: cancellation.signal });
  }
  throw new Error(`等待超时：${label}`);
}
async function request(base, path, { method = 'GET', body, headers = {} } = {}, expected = 200) {
  const response = await fetch(base + path, {
    method, headers: { ...headers, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), redirect: 'error',
    signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(60_000)]),
  });
  const value = response.status === 204 ? null : await response.json();
  assert.equal(response.status, expected, `${method} ${path}: ${value?.error?.code || 'HTTP'}`);
  return { response, value };
}
async function api(method, path, body, expected = 200) {
  const { response, value } = await request(url, `/api/app/v1${path}`, {
    method, body, headers: { Cookie: cookie, Origin: url, 'X-CSRF-Token': csrf, 'Idempotency-Key': randomUUID() },
  }, expected);
  for (const item of response.headers.getSetCookie()) if (item.startsWith('moerise_local_session=')) cookie = item.split(';')[0];
  if (value?.csrf_token) csrf = value.csrf_token;
  assert.equal(response.headers.get('cache-control'), 'no-store');
  const encoded = JSON.stringify(value);
  for (const secret of [pat, modelKey, password]) assert.equal(encoded?.includes(secret), false);
  return value;
}
async function check(label, action) {
  step = label; const started = Date.now(); console.log(label);
  await action(); evidence.checks.push({ check: label, passed: true, durationMs: Date.now() - started });
}
async function close(server) {
  if (server) await new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });
}
async function cleanFiles() {
  assert.equal(dirname(await realpath(root)), await realpath(resolve(repo, '.local')));
  assert.match(basename(root), /^discovery-acceptance-[0-9a-f]{16}$/);
  for (const entry of ['config.json', 'moerise', 'state', 'index']) {
    const target = resolve(root, entry);
    let info;
    try { info = await lstat(target); } catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    assert.equal(info.isSymbolicLink(), false);
    assert.equal(dirname(await realpath(target)), await realpath(root));
    await rm(target, { recursive: info.isDirectory(), force: true });
  }
}
const card = () => page.locator('.discovery-note:visible');
const next = () => page.getByRole('button', { name: '继续漫步', exact: true });
const noteID = note => note.name.split('/')[1];
const planNames = batch => batch.items.map(item => item.name);
let batch, image, file, specified;

try {
  await mkdir(root, { recursive: true });
  await check('编译 Linux Core 与正式 Web', async () => {
    containers.add(`${name}-build`);
    await docker(['run', '--name', `${name}-build`, '--mount', `type=bind,source=${repo},target=/src`, '-w', '/src',
      '-e', 'GOMODCACHE=/src/.local/gomodcache', '-e', 'GOCACHE=/src/.local/linux-gocache',
      'golang:1.27.3-bookworm', 'go', 'build', '-o', `/src/.local/${basename(root)}/moerise`, '.']);
    await runProcess(process.execPath, ['web/build.mjs'], { cwd: repo, signal: cancellation.signal });
  });
  const sourcePort = await port(), corePort = await port(), webPort = await port();
  sourceURL = `http://127.0.0.1:${sourcePort}`; coreURL = `http://127.0.0.1:${corePort}`; url = `http://127.0.0.1:${webPort}`;
  await check('真实 Memos 原文、纯图和纯文件来源', async () => {
    network = true; await docker(['network', 'create', name]);
    containers.add(source);
    await docker(['run', '-d', '--name', source, '--network', name, '--mount', 'type=volume,target=/var/opt/memos',
      '-p', `127.0.0.1:${sourcePort}:5230`,
      'neosmemo/memos:0.31.0@sha256:24c2707ddd8fbd2ceaa7a61ecab86a53cdcde640b24ce572b73da14b00b5785f']);
    await wait('Memos', async () => { try { await request(sourceURL, '/api/v1/instance/profile'); return true; } catch { return false; } });
    await request(sourceURL, '/api/v1/users', { method: 'POST', body: { username, password, role: 'ADMIN' } });
    const login = (await request(sourceURL, '/api/v1/auth/signin', {
      method: 'POST', body: { passwordCredentials: { username, password } },
    })).value;
    pat = (await request(sourceURL, `/api/v1/${login.user.name}/personalAccessTokens`, {
      method: 'POST', headers: { Authorization: `Bearer ${login.accessToken}` },
      body: { description: 'Discovery acceptance', expiresInDays: 0 },
    })).value.token;
    const sourceAPI = async (path, body) => (await request(sourceURL, `/api/v1${path}`, {
      method: 'POST', headers: { Authorization: `Bearer ${pat}` }, body,
    })).value;
    for (let index = 0; index < 5; index++) fixtures.push(await sourceAPI('/memos', {
      content: `discovery note ${index}\n\n${index === 0 ? '完整正文'.repeat(120) : '第二段原文'}\n\n末尾文字 ${index} #原始标签`,
      visibility: 'PRIVATE',
    }));
    image = await sourceAPI('/memos', { content: '', visibility: 'PRIVATE' });
    await sourceAPI('/attachments', { memo: image.name, filename: 'private.png', type: 'image/png', content: png.toString('base64') });
    file = await sourceAPI('/memos', { content: '', visibility: 'PRIVATE' });
    await sourceAPI('/attachments', { memo: file.name, filename: 'private.txt', type: 'text/plain',
      content: Buffer.from('file without a body').toString('base64') });
    let nextSourceRequest = 0;
    sourceProxy = http.createServer(async (req, res) => {
      const start = Math.max(Date.now(), nextSourceRequest); nextSourceRequest = start + 150;
      try { await delay(Math.max(0, start - Date.now()), undefined, { signal: sourceStop.signal }); }
      catch { res.destroy(); return; }
      if (res.destroyed) return;
      const upstream = http.request(sourceURL + req.url, { method: req.method,
        headers: { ...req.headers, host: new URL(sourceURL).host } }, response => {
        if (response.statusCode >= 400) evidence.sourceFailures.push({ path: req.url.split('?')[0], status: response.statusCode });
        res.writeHead(response.statusCode, response.headers); response.pipe(res);
      });
      upstream.on('error', () => { if (!res.headersSent) res.writeHead(503); res.end(); });
      req.pipe(upstream);
    });
    sourceProxy.listen(0, '0.0.0.0'); await once(sourceProxy, 'listening');
    mock = http.createServer(async (req, res) => {
      let raw = ''; for await (const chunk of req) raw += chunk;
      try {
        const body = JSON.parse(raw);
        assert.equal(req.url, '/v1/embeddings'); assert.equal(req.headers.authorization, `Bearer ${modelKey}`);
        assert.equal(body.model, 'discovery-fixture'); assert.ok(Array.isArray(body.input));
        evidence.embeddingCalls++;
        const data = body.input.map((text, index) => {
          const angle = 0.1 + (text.length % 7) / 20;
          return { index, embedding: [Math.cos(angle), Math.sin(angle), 0] };
        });
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ data, usage: { total_tokens: data.length } }));
      } catch {
        evidence.providerFailure = true; res.writeHead(400); res.end('{}');
      }
    });
    mock.listen(0, '0.0.0.0'); await once(mock, 'listening');
    const config = JSON.parse(await readFile(resolve(repo, 'config.example.json'), 'utf8'));
    config.memos = { ...config.memos, instance_id: name, base_url: `http://host.docker.internal:${sourceProxy.address().port}`,
      public_url: sourceURL, user: login.user.name, token: pat };
    config.web = { enabled: true, public_origin: url, state_encryption_key: randomBytes(32).toString('base64') };
    config.state_dir = '/data/state'; config.index_dir = '/data/index'; config.cursor_secret = randomBytes(32).toString('hex');
    config.access_tokens = [{ name: 'acceptance', token: machineKey, scopes: ['notes:read', 'reviews:read', 'reviews:write', 'discovery:write'] }];
    config.sync = { ...config.sync, poll_seconds: 5, full_seconds: 60, retry_seconds: 3 };
    config.review = { ...config.review, count: 3, min_age_hours: 0, recent_days: 0 };
    config.notifications = { enabled: false, time: '08:00', times: ['08:00', '20:00'] };
    config.llm.enabled = false;
    config.embedding = { ...config.embedding, enabled: true, provider: 'openai-compatible', protocol: 'openai-compatible',
      base_url: `http://host.docker.internal:${mock.address().port}/v1`, api_key: modelKey,
      model: 'discovery-fixture', dimensions: 3, max_retries: 0 };
    await writeFile(resolve(root, 'config.json'), JSON.stringify(config));
  });
  await check('真实 Linux Core、Cookie/CSRF 与语义就绪', async () => {
    containers.add(core);
    await docker(['run', '-dt', '--name', core, '--network', name, '--mount', `type=bind,source=${root},target=/data`,
      '--add-host', 'host.docker.internal:host-gateway', '-p', `127.0.0.1:${corePort}:8787`,
      'golang:1.27.3-bookworm', '/data/moerise', 'serve', '--config', '/data/config.json', '--host', '0.0.0.0', '--port', '8787']);
    web = createWebServer({ coreOrigin: coreURL, publicOrigin: url, dist: resolve(repo, 'web/dist') });
    web.listen(webPort, '127.0.0.1'); await once(web, 'listening');
    await wait('Core', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } });
    await request(url, '/api/app/v1/reviews/daily', {}, 401);
    await request(url, '/api/app/v1/reviews/daily', { headers: { Authorization: `Bearer ${machineKey}` } }, 403);
    await api('POST', '/session', { username, password });
    await request(url, '/api/app/v1/walks/start', { method: 'POST', body: {}, headers: { Origin: url, Cookie: cookie } }, 403);
    await wait('完整文档向量', async () => {
      const result = await api('GET', '/models');
      return result.index_status === 'ready' && result.vector_index.active.notes === 7 &&
        result.vector_index.active.completed_chunks === result.vector_index.active.chunks &&
        result.vector_index.active.chunks >= 5;
    });
    batch = await api('GET', '/reviews/daily');
    assert.equal(batch.items.length, 6);
    assert.equal(new Set(planNames(batch)).size, 6);
    assert.deepEqual([...planNames(batch)].sort(), [...fixtures.map(note => note.name), image.name].sort());
    assert.equal(planNames(batch).includes(file.name), false);
    evidence.plan = { id: batch.id, names: planNames(batch) };
    browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
    page = await browser.newPage({ viewport: { width: 1360, height: 960 } });
    page.on('pageerror', error => evidence.pageErrors.push(error.message));
    page.on('request', req => {
      if (!new URL(req.url()).pathname.startsWith('/api/app/v1/')) return;
      evidence.browserRequests.push({ method: req.method(), path: new URL(req.url()).pathname });
    });
    await page.goto(`${url}/review`);
    await page.locator('#username').fill(username); await page.locator('#password').fill(password);
    await page.locator('#login').click();
    await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).waitFor();
  });
  await check('全天固定回顾、完整原文、纯图及受保护详情', async () => {
    assert.equal(await page.locator('[data-review-index]').count(), 6);
    const textPosition = planNames(batch).indexOf(fixtures[0].name);
    await page.getByRole('button', { name: `第 ${textPosition + 1} / 6 条`, exact: true }).click();
    assert.match(await card().innerText(), new RegExp('完整正文'.repeat(120)));
    assert.match(await card().innerText(), /末尾文字 0/);
    await page.screenshot({ path: resolve(root, 'review-desktop.png'), fullPage: true });
    const imagePosition = planNames(batch).indexOf(image.name);
    await page.getByRole('button', { name: `第 ${imagePosition + 1} / 6 条`, exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.discovery-note img')?.naturalWidth > 0);
    const imageURL = await card().locator('img').getAttribute('src');
    assert.match(imageURL, /^\/api\/app\/v1\/notes\/.+\/attachments\/.+\/content$/);
    await request(url, imageURL, {}, 401);
    await card().getByRole('button', { name: '打开笔记', exact: true }).click();
    await page.locator(`article.note[data-name="${image.name}"]`).waitFor();
    await page.waitForFunction(() => document.querySelector('article.note img')?.naturalWidth > 0);
    assert.equal(new URL(page.url()).searchParams.get('from'), '/review');
    await page.getByRole('button', { name: '‹ 返回每日回顾', exact: true }).click();
    await page.locator(`.discovery-note[data-name="${image.name}"]:visible`).waitFor();
    assert.equal(await page.getByRole('button', { name: `第 ${imagePosition + 1} / 6 条`, exact: true }).getAttribute('aria-pressed'), 'true');
    const reads = evidence.browserRequests.filter(req => req.path.endsWith('/reviews/daily')).length;
    await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).click();
    await page.getByRole('button', { name: '再看一遍', exact: true }).click();
    assert.equal(evidence.browserRequests.filter(req => req.path.endsWith('/reviews/daily')).length, reads);
    assert.equal(await page.getByRole('button', { name: /完成|略过|换一组/ }).count(), 0);
  });
  await check('Core 重启与浏览器刷新保持同一全天计划', async () => {
    await docker(['restart', core]);
    await wait('重启 Core', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } });
    assert.deepEqual(await api('GET', '/reviews/daily'), batch);
    await page.reload();
    await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).waitFor();
    assert.deepEqual(await page.locator('[data-review-index]').evaluateAll(nodes => nodes.map(node => node.dataset.name)),
      planNames(batch));
    await wait('重启后语义就绪', async () => (await api('GET', '/models')).index_status === 'ready');
  });
  await check('详情指定起点、next 排除已走路径及详情返回', async () => {
    specified = fixtures[0];
    await page.goto(`${url}/notes/${noteID(specified)}`);
    await page.locator('article.note').getByRole('button', { name: '从这条漫步', exact: true }).click();
    await page.locator(`.discovery-note[data-name="${specified.name}"]:visible`).waitFor();
    const started = evidence.browserRequests.filter(req => req.path.endsWith('/walks/start')).length;
    await card().getByRole('button', { name: '打开笔记', exact: true }).click();
    await page.getByRole('button', { name: '‹ 返回随机漫步', exact: true }).click();
    await page.locator(`.discovery-note[data-name="${specified.name}"]:visible`).waitFor();
    assert.equal(evidence.browserRequests.filter(req => req.path.endsWith('/walks/start')).length, started);
    const seen = [specified.name];
    for (let index = 1; index < 5; index++) {
      const response = page.waitForResponse(response => response.url().endsWith('/api/app/v1/walks/next'));
      await next().click();
      const result = await (await response).json();
      assert.equal(result.exhausted, false); assert.deepEqual(result.warnings, []);
      const name = result.item.note.name;
      assert.equal(seen.includes(name), false); seen.push(name); assert.deepEqual(result.path, seen);
      await page.locator(`.discovery-note[data-name="${name}"]:visible`).waitFor();
    }
    assert.deepEqual([...seen].sort(), fixtures.map(note => note.name).sort());
    const response = page.waitForResponse(response => response.url().endsWith('/api/app/v1/walks/next'));
    await next().click();
    const result = await (await response).json();
    assert.equal(result.exhausted, true); assert.deepEqual(result.path, seen);
    await page.getByText('能走的路都走完了。', { exact: true }).waitFor();
    await page.locator(`.discovery-note[data-name="${seen.at(-1)}"]:visible`).waitFor();
    await page.screenshot({ path: resolve(root, 'walk-desktop.png'), fullPage: true });
  });
  await check('随机起点、窄屏与退出后凭据失效', async () => {
    await page.getByRole('button', { name: '停止', exact: true }).click();
    const response = page.waitForResponse(response => response.url().endsWith('/api/app/v1/walks/start'));
    await page.getByRole('button', { name: '随机开始', exact: true }).click();
    const result = await (await response).json();
    assert.equal(result.exhausted, false); assert.deepEqual(result.path, [result.item.note.name]);
    await card().waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: resolve(root, 'walk-mobile.png'), fullPage: true });
    assert.deepEqual(evidence.browserRequests.filter(req => req.method !== 'GET' && !/\/walks\/(start|next)$/.test(req.path)),
      [{ method: 'POST', path: '/api/app/v1/session' }]);
  });
  await check('关闭模型仍可回顾、漫步提示与机器接口写入隔离', async () => {
    const settings = await api('GET', '/settings');
    await api('PATCH', '/settings', { version: settings.version,
      settings: { embedding: { enabled: false }, llm: { enabled: false } } });
    assert.deepEqual(await api('GET', '/reviews/daily'), batch);
    await page.goto(`${url}/review`);
    await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).waitFor();
    await page.setViewportSize({ width: 320, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: resolve(root, 'review-mobile.png'), fullPage: true });
    await page.goto(`${url}/walk`);
    await page.getByRole('status').filter({ hasText: '请在设置中启用 Embedding' }).waitFor();
    assert.ok(await page.getByRole('button', { name: '随机开始', exact: true }).isDisabled());
    for (const path of ['/reviews/daily', '/reviews/daily/replace', '/discovery/open']) {
      await request(coreURL, `/api/ext/v1${path}`, { method: 'POST',
        headers: { Authorization: `Bearer ${machineKey}` }, body: {} }, 403);
    }
    assert.equal(evidence.providerFailure, undefined); assert.ok(evidence.embeddingCalls > 0);
    assert.deepEqual(evidence.pageErrors, []); assert.deepEqual(evidence.sourceFailures, []);
    const oldCookie = cookie;
    await api('DELETE', '/session', undefined, 204);
    await request(url, '/api/app/v1/reviews/daily', { headers: { Cookie: oldCookie } }, 401);
    await page.locator('#user-menu').click(); await page.locator('#logout').click();
    await page.locator('#login-form').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#discovery-page').count(), 0);
  });
  success = true;
} catch (error) {
  evidence.failure = { check: step, message: error.message };
  if (page && !page.isClosed()) {
    try { await page.screenshot({ path: resolve(root, 'failure.png'), fullPage: true }); } catch {}
  }
  if (containers.has(core)) {
    try { await writeFile(resolve(root, 'core.log'), await docker(['logs', core], true)); } catch {}
  }
  console.error(`${step}: ${error.message}`); process.exitCode = 1;
} finally {
  const cleanupErrors = [];
  for (const action of [() => browser?.close(), () => close(web), () => close(mock),
    async () => { sourceStop.abort(); await close(sourceProxy); }]) {
    try { await action(); } catch (error) { cleanupErrors.push(error.message); }
  }
  for (const container of [...containers].reverse()) {
    try { await docker(['rm', '-f', '-v', container], true); } catch (error) { cleanupErrors.push(error.message); }
  }
  if (network) { try { await docker(['network', 'rm', name], true); } catch (error) { cleanupErrors.push(error.message); } }
  try { await cleanFiles(); } catch (error) { cleanupErrors.push(error.message); }
  evidence.cleanupErrors = cleanupErrors;
  if (cleanupErrors.length) { success = false; process.exitCode = 1; console.error('验收资源清理失败'); }
  await writeFile(resolve(root, 'checks.json'), JSON.stringify({ ...evidence, success }, null, 2) + '\n');
  console.log(`证据：${root}`);
}
