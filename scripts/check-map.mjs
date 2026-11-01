import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdir, readFile, writeFile, rm, realpath, lstat, open } from 'node:fs/promises';
import { resolve, dirname, basename } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { runProcess } from './compose-process.mjs';
import { createWebServer } from '../web/server.mjs';

assert.equal(process.platform, 'linux', '地图完整验证使用 Linux Core');
assert.ok(process.env.MOERISE_PLAYWRIGHT_MODULE, '需要 MOERISE_PLAYWRIGHT_MODULE');
const scaleCount = Number(process.env.MOERISE_MAP_SCALE || 1000);
assert.ok(Number.isSafeInteger(scaleCount) && scaleCount >= 100 && scaleCount <= 10000, 'MOERISE_MAP_SCALE 为 100—10000 的验证规模，不是产品数量上限');
const { chromium } = await import(pathToFileURL(process.env.MOERISE_PLAYWRIGHT_MODULE).href);
const repo = fileURLToPath(new URL('../', import.meta.url));
const baselineBinary = process.env.MOERISE_BASELINE_BINARY ? resolve(process.env.MOERISE_BASELINE_BINARY) : '';
const id = randomBytes(8).toString('hex'), sourceName = `moerise-map-${id}`;
const root = resolve(repo, '.local', `map-acceptance-${id}`), binary = resolve(root, 'moerise');
let servingBinary = baselineBinary || binary;
const cancellation = new AbortController();
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => cancellation.abort(new Error(signal)));
const username = 'map-owner', password = randomBytes(24).toString('hex');
const modelKey = randomBytes(24).toString('hex'), machineKey = randomBytes(24).toString('hex');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=', 'base64');
const evidence = { checks: [], pageErrors: [], measurements: [], apiMeasurements: [], sourceRequests: { lists: 0, notes: 0 },
  sourceFailures: [], canceledSourceReads: 0, embeddingCalls: 0 };
let browser, page, web, model, proxy, processCore, coreExit, log, cookie = '', csrf = '', url, coreURL, sourceURL, pat;
let createdSource = false, success = false, step = '初始化', failName = '', holdName = '', held = [], total = 0, rateLimitLists = false;
const fixtures = [];

async function docker(args, cleanup = false) {
  return runProcess('docker', args, { cwd: repo, timeout: 180_000, signal: cleanup ? undefined : cancellation.signal });
}
async function port() {
  const server = http.createServer(); server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const value = server.address().port; await new Promise(resolve => server.close(resolve)); return value;
}
async function wait(label, predicate, timeout = 120_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    cancellation.signal.throwIfAborted();
    if (await predicate()) return;
    await delay(300, undefined, { signal: cancellation.signal });
  }
  throw new Error(`等待超时：${label}`);
}
async function request(base, path, { method = 'GET', body, headers = {} } = {}, expected = 200) {
  const start = performance.now();
  const response = await fetch(base + path, { method,
    headers: { ...headers, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body), redirect: 'error',
    signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(60_000)]),
  });
  const raw = await response.text(), value = raw ? JSON.parse(raw) : null;
  assert.equal(response.status, expected, `${method} ${path}: ${value?.error?.code || 'HTTP'}`);
  return { response, value, durationMs: performance.now() - start, bytes: Buffer.byteLength(raw) };
}
async function api(method, path, body, expected = 200) {
  const result = await request(url, `/api/app/v1${path}`, { method, body,
    headers: { Cookie: cookie, Origin: url, 'X-CSRF-Token': csrf, 'Idempotency-Key': randomUUID() },
  }, expected);
  for (const item of result.response.headers.getSetCookie()) if (item.startsWith('moerise_local_session=')) cookie = item.split(';')[0];
  if (result.value?.csrf_token) csrf = result.value.csrf_token;
  assert.equal(result.response.headers.get('cache-control'), 'no-store');
  for (const secret of [pat, modelKey, password]) assert.equal(JSON.stringify(result.value)?.includes(secret), false);
  if (path === '/map' && result.value?.status === 'ready') {
    const status = await readFile(`/proc/${processCore.pid}/status`, 'utf8');
    evidence.measurements.push({ notes: result.value.points.length, durationMs: result.durationMs, responseBytes: result.bytes,
      coreRSSKiB: Number(/^VmRSS:\s+(\d+)/m.exec(status)?.[1]), corePeakRSSKiB: Number(/^VmHWM:\s+(\d+)/m.exec(status)?.[1]) });
  }
  if (processCore && method === 'GET') {
    const status = await readFile(`/proc/${processCore.pid}/status`, 'utf8');
    evidence.apiMeasurements.push({ path, notes: total, status: result.response.status, durationMs: result.durationMs,
      responseBytes: result.bytes, coreRSSKiB: Number(/^VmRSS:\s+(\d+)/m.exec(status)?.[1]) });
    assert.ok(result.durationMs < 60_000, '客户端读取须在代理预算内返回');
  }
  return result.value;
}
async function source(method, path, body) {
  if (method === 'POST' && path === '/memos') await delay(550, undefined, { signal: cancellation.signal });
  return (await request(sourceURL, `/api/v1${path}`, { method, body, headers: { Authorization: `Bearer ${pat}` } })).value;
}
async function check(label, action) {
  step = label; console.log(label); const start = Date.now();
  await action(); evidence.checks.push({ check: label, passed: true, durationMs: Date.now() - start });
}
async function close(server) {
  if (server) await new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });
}
async function stopCore() {
  if (!processCore) return;
  const child = processCore;
  const timer = setTimeout(() => child.kill('SIGKILL'), 3000).unref();
  child.kill('SIGTERM'); await coreExit; clearTimeout(timer); processCore = null;
}
async function startCore() {
  processCore = spawn(servingBinary, ['serve', '--config', resolve(root, 'config.json'), '--host', '127.0.0.1', '--port', new URL(coreURL).port],
    { cwd: repo, stdio: ['ignore', log.fd, log.fd] });
  coreExit = new Promise((resolve, reject) => { processCore.once('error', reject); processCore.once('close', resolve); });
  await wait('Core 就绪', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } });
}
async function command(...args) {
  return runProcess(binary, [...args, '--config', resolve(root, 'config.json')], { cwd: repo, signal: cancellation.signal });
}
async function cleanFiles() {
  assert.equal(dirname(await realpath(root)), await realpath(resolve(repo, '.local')));
  assert.match(basename(root), /^map-acceptance-[0-9a-f]{16}$/);
  for (const entry of ['config.json', 'moerise', 'state', 'index', 'backup', 'before-restore']) {
    const target = resolve(root, entry);
    let info;
    try { info = await lstat(target); } catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    assert.equal(info.isSymbolicLink(), false); assert.equal(dirname(await realpath(target)), await realpath(root));
    await rm(target, { recursive: info.isDirectory(), force: true });
  }
}
const marker = note => page.locator(`.map-dot[data-name="${note.name}"]`);
const reader = () => page.locator('.map-note');
const view = () => page.evaluate(() => history.state.mapView);
async function browse(target) {
  for (let attempt = 0; ; attempt++) {
    try { return target ? await page.goto(target) : await page.reload(); }
    catch (error) {
      if (attempt || !error.message.includes('net::ERR_NETWORK_CHANGED')) throw error;
    }
  }
}
async function ready() {
  await page.waitForFunction(count => document.querySelectorAll('.map-dot').length === count && document.querySelector('.map-page')?.getAttribute('aria-busy') === 'false', total, { timeout: 65_000 });
}
async function readPoint(note) {
  await marker(note).focus(); await marker(note).press('Enter');
  await page.locator(`.map-note[data-name="${note.name}"]`).waitFor();
}
async function vectorsReady() {
  await wait('完整向量索引', async () => {
    const models = await api('GET', '/models');
    return models.index_status === 'ready' && models.vector_index.active.notes === total;
  }, Math.max(120_000, total * 750));
}
function assertClusters(map, notes) {
  const points = new Map(map.points.map(point => [point.name, point]));
  const centers = Array.from({ length: 3 }, () => ({ x: 0, y: 0, count: 0 }));
  for (const note of notes) {
    const point = points.get(note.name), center = centers[note.group];
    assert.ok(point?.semantic); center.x += point.x; center.y += point.y; center.count++;
  }
  for (const center of centers) { center.x /= center.count; center.y /= center.count; }
  for (const note of notes) {
    const point = points.get(note.name), center = centers[note.group], own = Math.hypot(point.x - center.x, point.y - center.y);
    for (let group = 0; group < 3; group++) if (group !== note.group) assert.ok(own * 2 < Math.hypot(point.x - centers[group].x, point.y - centers[group].y), '密集避让不能覆盖语义簇关系');
  }
}

try {
  await mkdir(root, { recursive: true });
  await check('编译正式 Linux Core 与 Web', async () => {
    await runProcess('go', ['build', '-o', binary, '.'], { cwd: repo, signal: cancellation.signal });
    await runProcess(process.execPath, ['web/build.mjs'], { cwd: repo, signal: cancellation.signal });
    log = await open(resolve(root, 'core.log'), 'w', 0o600);
  });
  sourceURL = `http://127.0.0.1:${await port()}`; coreURL = `http://127.0.0.1:${await port()}`; url = `http://127.0.0.1:${await port()}`;
  await check('独立真实 Memos：正常、归档、同文、长文、原生标签和纯附件', async () => {
    createdSource = true;
    await docker(['run', '-d', '--name', sourceName, '--mount', 'type=volume,target=/var/opt/memos', '-p', `127.0.0.1:${new URL(sourceURL).port}:5230`,
      'neosmemo/memos:0.31.0@sha256:24c2707ddd8fbd2ceaa7a61ecab86a53cdcde640b24ce572b73da14b00b5785f']);
    await wait('Memos', async () => { try { await request(sourceURL, '/api/v1/instance/profile'); return true; } catch { return false; } });
    await request(sourceURL, '/api/v1/users', { method: 'POST', body: { username, password, role: 'ADMIN' } });
    const login = (await request(sourceURL, '/api/v1/auth/signin', { method: 'POST', body: { passwordCredentials: { username, password } } })).value;
    pat = (await request(sourceURL, `/api/v1/${login.user.name}/personalAccessTokens`, { method: 'POST', headers: { Authorization: `Bearer ${login.accessToken}` },
      body: { description: 'Map acceptance', expiresInDays: 0 } })).value.token;
    const bodies = ['balcony herbs #想法\n\n' + 'balcony herbs 完整原文\n'.repeat(160) + '\n原文末尾', 'balcony herbs light #灵感',
      'bicycle brakes #摘录', 'bicycle tyres #教训', 'reading fiction', 'reading history'];
    bodies.push(bodies[0]);
    for (const content of bodies) fixtures.push(await source('POST', '/memos', { content, visibility: 'PRIVATE' }));
    await source('PATCH', `/${fixtures[1].name}?updateMask=state`, { state: 'ARCHIVED' });
    for (const [filename, type, data] of [['private.png', 'image/png', png], ['original.txt', 'text/plain', Buffer.from('原文件完整内容\n')]]) {
      const note = await source('POST', '/memos', { content: '', visibility: 'PRIVATE' });
      await source('POST', '/attachments', { memo: note.name, filename, type, content: data.toString('base64') }); fixtures.push(note);
    }
    total = fixtures.length;
    let nextSourceRequest = 0;
    proxy = http.createServer(async (req, res) => {
      const path = req.url.split('?')[0];
      if (req.method === 'GET' && path === '/api/v1/memos') {
        evidence.sourceRequests.lists++;
        if (rateLimitLists) {
          res.writeHead(429, { 'Content-Type': 'application/json' });
          res.end('{"message":"controlled rate limit"}'); return;
        }
      } else if (req.method === 'GET' && /^\/api\/v1\/memos\/[^/]+$/.test(path)) evidence.sourceRequests.notes++;
      // Leave room in Memos' shared per-user budget for fixture writes and parallel sidebar reads.
      if (req.method === 'GET') {
        const start = Math.max(Date.now(), nextSourceRequest); nextSourceRequest = start + 150;
        try { await delay(Math.max(0, start - Date.now()), undefined, { signal: cancellation.signal }); }
        catch { res.destroy(); return; }
        if (res.destroyed) return;
      }
      if (req.method === 'GET' && req.url.split('?')[0] === `/api/v1/${holdName}`) {
        await new Promise(resolve => { held.push(resolve); res.once('close', () => { if (!res.writableEnded) evidence.canceledSourceReads++; resolve(); }); });
        if (res.destroyed) return;
      }
      if (failName && req.method === 'GET' && req.url.split('?')[0] === '/api/v1/memos' && new URL(req.url, sourceURL).searchParams.get('state') === 'ARCHIVED') {
        res.writeHead(503, { 'Content-Type': 'application/json' }); res.end('{"message":"controlled source outage"}'); return;
      }
      const upstream = http.request(sourceURL + req.url, { method: req.method, headers: { ...req.headers, host: new URL(sourceURL).host } }, response => {
        if (response.statusCode >= 400) evidence.sourceFailures.push({ method: req.method, path: req.url.split('?')[0], status: response.statusCode });
        res.writeHead(response.statusCode, response.headers); response.pipe(res);
      });
      res.on('close', () => { if (!res.writableEnded) upstream.destroy(); });
      upstream.on('error', () => { if (!res.headersSent) res.writeHead(503); res.end(); }); req.pipe(upstream);
    });
    proxy.listen(0, '127.0.0.1'); await once(proxy, 'listening');
    model = http.createServer(async (req, res) => {
      let raw = ''; for await (const chunk of req) raw += chunk;
      try {
        const body = JSON.parse(raw); assert.equal(req.headers.authorization, `Bearer ${modelKey}`);
        if (baselineBinary && req.url === '/v1/chat/completions') {
          assert.equal(body.model, 'upgrade-fixture'); assert.equal(body.stream, true);
          res.writeHead(200, { 'Content-Type': 'text/event-stream' });
          res.end(`data: ${JSON.stringify({ model: 'upgrade-fixture', choices: [{ index: 0, delta: { content: '升级前保存的完整回复\n\n保留原始正文与材料引用。' }, finish_reason: null }] })}\n\n` +
            `data: ${JSON.stringify({ model: 'upgrade-fixture', choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`);
          return;
        }
        assert.equal(req.url, '/v1/embeddings');
        assert.match(body.model, /^map-fixture/); assert.ok(Array.isArray(body.input)); evidence.embeddingCalls++;
        const data = body.input.map((text, index) => {
          let angle = text.includes('bicycle') ? 2 * Math.PI / 3 : text.includes('reading') ? 4 * Math.PI / 3 : 0;
          angle += text.length % 5 * 0.003;
          return { index, embedding: [Math.cos(angle), Math.sin(angle), 0.01] };
        });
        res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ data }));
      } catch { evidence.providerFailure = true; res.writeHead(400); res.end('{}'); }
    });
    model.listen(0, '127.0.0.1'); await once(model, 'listening');
    const config = JSON.parse(await readFile(resolve(repo, 'config.example.json'), 'utf8'));
    config.memos = { ...config.memos, instance_id: sourceName, base_url: `http://127.0.0.1:${proxy.address().port}`, public_url: sourceURL,
      user: login.user.name, token: pat, page_size: 100 };
    config.web = { enabled: true, public_origin: url, state_encryption_key: randomBytes(32).toString('base64') };
    config.state_dir = resolve(root, 'state'); config.index_dir = resolve(root, 'index'); config.cursor_secret = randomBytes(32).toString('hex');
    config.access_tokens = [{ name: 'machine', token: machineKey, scopes: ['notes:read', 'notes:write', 'reviews:write', 'ai:generate'] }];
    config.sync = { ...config.sync, poll_seconds: 5, full_seconds: 60, retry_seconds: 3 };
    config.timezone = 'UTC';
    config.review = { ...config.review, min_age_hours: 0, recent_days: 0 }; config.notifications.enabled = false; config.llm.enabled = false;
    config.embedding = { ...config.embedding, enabled: true, provider: 'openai-compatible', protocol: 'openai-compatible',
      base_url: `http://127.0.0.1:${model.address().port}/v1`, api_key: modelKey, model: 'map-fixture', dimensions: 3, max_retries: 0 };
    if (baselineBinary) config.llm = { ...config.llm, enabled: true, protocol: 'openai-compatible',
      base_url: `http://127.0.0.1:${model.address().port}/v1`, api_key: modelKey, model: 'upgrade-fixture', max_retries: 0 };
    await writeFile(resolve(root, 'config.json'), JSON.stringify(config), { mode: 0o600 });
  });
  await check('本人 Cookie、机器禁止和正式浏览器真实点集', async () => {
    await startCore(); web = createWebServer({ coreOrigin: coreURL, publicOrigin: url, dist: resolve(repo, 'web/dist') });
    web.listen(Number(new URL(url).port), '127.0.0.1'); await once(web, 'listening');
    await request(url, '/api/app/v1/map', {}, 401);
    await request(url, '/api/app/v1/map', { headers: { Authorization: `Bearer ${machineKey}` } }, 403);
    await request(coreURL, '/api/ext/v1/map', { headers: { Authorization: `Bearer ${machineKey}` } }, 403);
    await api('POST', '/session', { username, password }); await vectorsReady();
    const map = await api('GET', '/map'); assert.equal(map.points.length, total); assert.equal(new Set(map.points.map(point => point.name)).size, total);
    assert.deepEqual(map.points.filter(point => !point.semantic).map(point => point.name).sort(), fixtures.slice(7).map(note => note.name).sort());
    browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
    page = await browser.newPage({ viewport: { width: 1360, height: 960 } }); page.on('pageerror', error => evidence.pageErrors.push(error.message));
    await browse(`${url}/map`); await page.locator('#username').fill(username); await page.locator('#password').fill(password); await page.locator('#login').click(); await ready();
    await readPoint(fixtures[0]); assert.match(await reader().innerText(), /原文末尾/); assert.match(await reader().innerText(), new RegExp('完整原文'));
    await readPoint(fixtures[7]); await page.waitForFunction(() => document.querySelector('.map-note img')?.naturalWidth > 0);
    const imageURL = await reader().locator('img').getAttribute('src'); assert.match(imageURL, /^\/api\/app\/v1\/notes\/.+\/attachments\/.+\/content$/);
    const image = await page.request.get(url + imageURL); assert.deepEqual(await image.body(), png); await request(url, imageURL, {}, 401);
    await readPoint(fixtures[8]); assert.match(await reader().locator('a.attachment').innerText(), /^original\.txt\b/);
    await readPoint(fixtures[1]); assert.ok((await reader().innerText()).includes('balcony herbs light'));
    await page.screenshot({ path: resolve(root, 'map-desktop.png'), fullPage: true });
    const saved = await view(); await reader().getByRole('button', { name: '打开笔记', exact: true }).click(); await page.locator('article.note').waitFor();
    await browse(); await page.locator('article.note').waitFor(); await page.locator('#back').click(); await ready(); await reader().waitFor(); assert.deepEqual(await view(), saved);
  });
  if (baselineBinary) await check('旧二进制原目录升级保留会话、设置、分时回顾、聊天、向量与地图', async () => {
    let settings = await api('GET', '/settings');
    settings = await api('PATCH', '/settings', { version: settings.version,
      settings: { review: { count: 3 }, notifications: { times: ['09:00', '18:00'] } } });
    const plan = await api('GET', '/reviews/daily');
    assert.ok(plan.items.length > 0);
    let chat = await api('POST', '/chats', { request_id: randomUUID(), title: '升级保留', names: [fixtures[0].name] }, 201);
    const response = await fetch(url + `/api/app/v1/chats/${chat.id}/messages`, {
      method: 'POST', headers: { Cookie: cookie, Origin: url, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' },
      body: JSON.stringify({ request_id: randomUUID(), version: chat.version, kind: 'question', content: '升级前的问题' }),
      signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(60_000)]),
    });
    assert.equal(response.status, 200);
    assert.match(await response.text(), /event: complete/);
    chat = await api('GET', `/chats/${chat.id}`);
    assert.equal(chat.turns.length, 1); assert.equal(chat.turns[0].assistant.status, 'completed');
    assert.equal(chat.turns[0].assistant.content, '升级前保存的完整回复\n\n保留原始正文与材料引用。');
    const map = await api('GET', '/map'), calls = evidence.embeddingCalls, saved = await view();
    await stopCore(); servingBinary = binary; await startCore();
    await api('GET', '/session');
    assert.deepEqual(await api('GET', '/settings'), settings);
    assert.deepEqual(await api('GET', '/reviews/daily'), plan);
    assert.deepEqual(await api('GET', `/chats/${chat.id}`), chat);
    await vectorsReady(); assert.deepEqual(await api('GET', '/map'), map);
    assert.equal(evidence.embeddingCalls, calls, '升级复用已有向量，不重跑模型');
    await browse(); await ready(); assert.deepEqual(await view(), saved);
  });
  await check('鼠标平移、滚轮锚定、键盘、刷新与 Core 重启不跳变', async () => {
    const field = page.locator('#map-field'), box = await field.boundingBox(), before = await view();
    await page.mouse.move(box.x + 30, box.y + 30); await page.mouse.down(); await page.mouse.move(box.x + 120, box.y + 70, { steps: 8 }); await page.mouse.up();
    const pan = await view(); assert.ok(Math.abs(pan.x - before.x + 90 / before.scale) < 1e-6);
    await page.mouse.move(box.x + box.width / 2 + 40, box.y + box.height / 2); await page.mouse.wheel(0, -200);
    await page.waitForFunction(scale => history.state.mapView.scale > scale, pan.scale);
    const zoom = await view(); assert.ok(Math.abs(pan.x + 40 / pan.scale - zoom.x - 40 / zoom.scale) < 1e-5);
    await field.press('ArrowLeft'); await field.press('+'); const saved = await view();
    const map = await api('GET', '/map'); await browse(); await ready(); assert.deepEqual(await view(), saved);
    await stopCore(); await startCore(); assert.deepEqual(await api('GET', '/map'), map);
    await browse(); await ready(); assert.deepEqual(await view(), saved); await field.press('Home');
    // Zoom into coincident originals before pointer selection; keyboard focus also keeps each resource independently reachable.
    await marker(fixtures[6]).focus(); await field.press('+'); await field.press('+'); await marker(fixtures[6]).click();
    await page.locator(`.map-note[data-name="${fixtures[6].name}"]`).waitFor();
  });
  await check('真实来源错误清空旧点集，导航取消完整阅读并隔离迟到响应', async () => {
    failName = fixtures[0].name; await page.locator('#map-field').press('r');
    await page.locator('.map-page .error').waitFor(); assert.equal(await page.locator('.map-dot').count(), 0); assert.equal(await reader().count(), 0);
    failName = ''; await page.locator('#map-field').press('r'); await ready();
    holdName = fixtures[0].name; await marker(fixtures[0]).focus(); await marker(fixtures[0]).press('Enter'); await wait('来源读取已挂起', () => held.length > 0);
    await page.locator('nav a[href="/"]').click(); await page.waitForURL(url + '/');
    await wait('来源取消已传递', () => evidence.canceledSourceReads > 0); holdName = ''; for (const release of held.splice(0)) release();
    assert.equal(await page.locator('.map-page').count(), 0); await browse(url + '/map'); await ready();
  });
  await check('真实窄屏单指、双指缩放、取消与完整阅读', async () => {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
    await context.addCookies(await page.context().cookies()); const desktop = page; page = await context.newPage();
    page.on('pageerror', error => evidence.pageErrors.push(error.message)); await browse(url + '/map'); await ready();
    const field = page.locator('#map-field'); await field.scrollIntoViewIfNeeded(); const box = await field.boundingBox(), before = await view();
    const cdp = await context.newCDPSession(page), touch = (type, touchPoints) => cdp.send('Input.dispatchTouchEvent', { type, touchPoints });
    await touch('touchStart', [{ x: box.x + 30, y: box.y + 60, id: 1 }]); await touch('touchMove', [{ x: box.x + 90, y: box.y + 80, id: 1 }]); await touch('touchEnd', []);
    const pan = await view(); assert.ok(Math.abs(pan.x - before.x + 60 / before.scale) < 1e-5);
    const x = box.x + box.width / 2, y = box.y + 140;
    await touch('touchStart', [{ x: x - 30, y, id: 1 }, { x: x + 30, y, id: 2 }]);
    await touch('touchMove', [{ x: x - 60, y: y + 10, id: 1 }, { x: x + 60, y: y + 10, id: 2 }]); await touch('touchEnd', []);
    assert.ok((await view()).scale > pan.scale * 1.8);
    await touch('touchStart', [{ x: box.x + 20, y: box.y + 30, id: 1 }]); await touch('touchCancel', []);
    await field.evaluate(element => element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }))); await readPoint(fixtures[7]);
    await page.waitForFunction(() => document.querySelector('.map-note img')?.naturalWidth > 0);
    assert.equal(await field.evaluate(element => element.getBoundingClientRect().height), 440); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    await page.screenshot({ path: resolve(root, 'map-mobile.png'), fullPage: true }); await context.close(); page = desktop;
  });
  await check('真实 100/目标规模语义簇、传输/内存预算与密集点缩放阅读', async () => {
    const dense = [];
    for (const target of [...new Set([100, scaleCount])]) {
      for (; total < target; total++) {
        const group = total % 3, subject = ['balcony herbs', 'bicycle brakes', 'reading fiction'][group];
        const note = await source('POST', '/memos', { content: `${subject} observation ${total} #原生`, visibility: 'PRIVATE' }); dense.push({ ...note, group });
      }
      await vectorsReady(); const map = await api('GET', '/map'); assert.equal(map.points.length, target); assertClusters(map, dense);
      const stable = await api('GET', '/map'); assert.deepEqual(stable, map);
      await browse(); const renderStart = performance.now(); await ready();
      evidence.measurements.push({ browserNotes: target, browserLoadMs: performance.now() - renderStart });
      const note = dense.at(-1); await marker(note).focus(); const field = page.locator('#map-field');
      for (let count = 0; count < 18; count++) await field.press('+');
      await marker(note).focus();
      await marker(note).click(); await page.locator(`.map-note[data-name="${note.name}"]`).waitFor(); assert.match(await reader().innerText(), /observation/);
      await page.screenshot({ path: resolve(root, `map-${target}-zoom.png`), fullPage: true });
      const listed = await api('GET', '/notes?page_size=20');
      assert.equal(listed.notes.length, 20); assert.ok(listed.next_page_token);
      assert.ok(listed.notes.every(note => note.state === 'NORMAL'));
      const search = await api('GET', '/notes?query=observation&page_size=20');
      assert.equal(search.notes.length, 20); assert.ok(search.notes.every(note => note.content.includes('observation')));
      const beforeNotes = evidence.sourceRequests.notes;
      const tags = await api('GET', '/tags'), month = new Date(dense.at(-1).createTime).toISOString().slice(0, 7);
      assert.equal(tags.tags.find(item => item.tag === '原生')?.count, dense.length);
      const calendar = await api('GET', `/calendar?month=${month}`);
      const monthNotes = dense.filter(note => note.createTime.startsWith(month)).length;
      assert.ok(calendar.days.reduce((sum, day) => sum + day.count, 0) >= monthNotes);
      assert.equal(evidence.sourceRequests.notes, beforeNotes, '全量侧栏只使用官方分页，不逐条 GET');
      const materials = await api('GET', '/chat-materials?page_size=20');
      assert.equal(materials.notes.length, 20);
      const plan = await api('GET', '/reviews/daily');
      assert.ok(plan.items.length > 0);
      const walk = await api('POST', '/walks/start', {});
      assert.ok(walk.item?.note?.name || walk.item?.name);
      const measured = evidence.apiMeasurements.filter(item => item.notes === target);
      assert.ok(measured.every(item => item.responseBytes < 8 * 1024 * 1024), '实验响应预算为 8 MiB');
      assert.ok(measured.every(item => item.coreRSSKiB < 768 * 1024), '实验 Core RSS 预算为 768 MiB');
      await browse(url + '/'); await page.locator('#notes article.card').first().waitFor({ timeout: 65_000 });
      await page.waitForFunction(() => document.querySelectorAll('#tags .tag-row').length > 0 &&
        !document.querySelector('#sidebar-error').textContent &&
        document.querySelector('#calendar button:not(:disabled)'), undefined, { timeout: 65_000 });
      assert.equal(await page.locator('#notes article.card').count(), 20);
      await page.locator('#load-more').click();
      await page.waitForFunction(() => document.querySelectorAll('#notes article.card').length === 40, undefined, { timeout: 65_000 });
      await page.screenshot({ path: resolve(root, `timeline-${target}.png`), fullPage: true });
      await browse(url + '/map'); await ready();
    }
    const first = await api('GET', '/map'), unchanged = new Map(first.points.map(point => [point.name, point]));
    await source('PATCH', `/${fixtures[2].name}?updateMask=content`, { content: 'reading edited subject #原生' });
    await source('DELETE', `/${fixtures[4].name}`); await source('POST', '/memos', { content: 'balcony herbs added', visibility: 'PRIVATE' });
    await wait('少量增改删发布', async () => {
      const models = await api('GET', '/models'); if (models.index_status !== 'ready') return false;
      let map;
      try { map = await api('GET', '/map'); }
      catch (error) { if (/MAP_(SOURCE|INDEX)_CHANGED/.test(error.message)) return false; throw error; }
      return map.points.length === total && !map.points.some(point => point.name === fixtures[4].name) &&
        map.points.find(point => point.name === fixtures[2].name)?.source_hash !== unchanged.get(fixtures[2].name).source_hash;
    });
    const changed = await api('GET', '/map'); assert.equal(changed.layout_frame, first.layout_frame);
    for (const point of changed.points) {
      const old = unchanged.get(point.name); if (old && point.name !== fixtures[2].name) assert.deepEqual([point.x, point.y], [old.x, old.y]);
    }
  });
  await check('来源限流不返回部分侧栏、列表或聊天材料，恢复后可重读', async () => {
    rateLimitLists = true;
    try {
      for (const path of ['/tags', '/calendar?month=' + new Date().toISOString().slice(0, 7), '/notes', '/chat-materials']) {
        const result = await api('GET', path, undefined, 503);
        assert.equal(result.error.code, 'UPSTREAM_RATE_LIMITED');
        assert.equal(result.error.retryable, true);
        for (const field of ['notes', 'tags', 'days']) assert.equal(result[field], undefined);
      }
    } finally { rateLimitLists = false; }
    assert.ok((await api('GET', '/tags')).tags.length > 0);
  });
  await check('模型空间切换、索引重建/备份恢复与模型关闭基础能力', async () => {
    let settings = await api('GET', '/settings'); const before = await api('GET', '/map');
    settings = await api('PATCH', '/settings', { version: settings.version, settings: { embedding: { model: 'map-fixture-next' } } });
    await vectorsReady(); const switched = await api('GET', '/map'); assert.notEqual(switched.layout_frame, before.layout_frame);
    const plan = await api('GET', '/reviews/daily'); await command('backup', '--output', resolve(root, 'backup'));
    for (const operation of ['rebuild-index', 'restore']) {
      await stopCore(); await command(operation, ...(operation === 'restore' ? ['--source', resolve(root, 'backup'), '--safety-backup', resolve(root, 'before-restore')] : []));
      await startCore();
      if (operation === 'restore') {
        await api('GET', '/session', undefined, 401); await api('POST', '/session', { username, password });
        await vectorsReady();
        await browse(); await page.locator('#login-form').waitFor({ state: 'visible' });
        await page.locator('#username').fill(username); await page.locator('#password').fill(password); await page.locator('#login').click(); await ready();
      }
      await vectorsReady(); const rebuilt = await api('GET', '/map');
      const sources = map => map.points.map(({ x, y, ...source }) => source);
      assert.deepEqual(sources(rebuilt), sources(switched));
      assert.ok(rebuilt.points.every(point => Number.isFinite(point.x) && Number.isFinite(point.y)));
      assert.deepEqual(await api('GET', '/map'), rebuilt); assert.notEqual(rebuilt.vector_generation, switched.vector_generation);
      assert.deepEqual(await api('GET', '/settings'), settings); assert.deepEqual(await api('GET', '/reviews/daily'), plan);
    }
    await api('PATCH', '/settings', { version: settings.version, settings: { embedding: { enabled: false }, llm: { enabled: false } } });
    assert.equal((await api('GET', '/map')).status, 'disabled'); await browse(); await page.getByRole('status').filter({ hasText: '请在设置中启用 Embedding' }).waitFor();
    await api('GET', `/notes/${fixtures[0].name.slice(6)}`); await api('GET', '/notes?query=balcony'); assert.equal(await page.locator('.map-dot').count(), 0);
    await page.locator('#user-menu').click(); await page.locator('#logout').click(); await page.locator('#login-form').waitFor({ state: 'visible' });
    assert.equal(await page.locator('.map-page').count(), 0); assert.equal(evidence.providerFailure, undefined); assert.deepEqual(evidence.pageErrors, []);
  });
  success = true;
} catch (error) {
  evidence.failure = { check: step, message: error.message }; console.error(`${step}: ${error.message}`); process.exitCode = 1;
  if (page && !page.isClosed()) { try { await page.screenshot({ path: resolve(root, 'failure.png'), fullPage: true }); } catch {} }
} finally {
  holdName = ''; for (const release of held.splice(0)) release();
  const errors = [];
  for (const action of [() => browser?.close(), () => close(web), stopCore, () => close(proxy), () => close(model), () => log?.close(),
    async () => { if (createdSource) await docker(['rm', '-f', '-v', sourceName], true); }, cleanFiles]) {
    try { await action(); } catch (error) { errors.push(error.message); }
  }
  evidence.cleanupErrors = errors; if (errors.length) { success = false; process.exitCode = 1; }
  await writeFile(resolve(root, 'checks.json'), JSON.stringify({ ...evidence, success }, null, 2) + '\n'); console.log(`证据：${root}`);
}
