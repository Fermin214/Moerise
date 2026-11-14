import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { readFile, writeFile, mkdir, open } from 'node:fs/promises';
import { resolve, basename } from 'node:path';
import { parseArgs } from 'node:util';
import { pathToFileURL } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { createWebServer } from '../../web/server.mjs';
import { runProcess } from '../lib/process.mjs';

const { values } = parseArgs({ options: {
  notes: { type: 'string' }, vectors: { type: 'string' }, dimensions: { type: 'string', default: '1024' },
  keep: { type: 'boolean', default: false },
  resume: { type: 'string' },
} });
assert.ok(values.notes && values.vectors, '需要 --notes、--vectors，使用已有笔记与向量');
const repo = resolve(import.meta.dirname, '../..');
const corpus = JSON.parse(await readFile(resolve(values.notes), 'utf8'));
const dimensions = Number(values.dimensions), vectors = await readFile(resolve(values.vectors));
assert.equal(vectors.length, corpus.length * dimensions * 4);
const vectorByText = new Map(corpus.map((note, row) => [note.content,
  Array.from({ length: dimensions }, (_, column) => vectors.readFloatLE((row * dimensions + column) * 4))]));
const resumed = values.resume ? JSON.parse(await readFile(resolve(values.resume, 'fixture.json'), 'utf8')) : null;
const resumedConfig = values.resume ? await readFile(resolve(values.resume, 'config.json'), 'utf8').then(JSON.parse).catch(() => null) : null;
const id = values.resume ? basename(resolve(values.resume)).slice('stars-acceptance-'.length) : randomBytes(6).toString('hex');
assert.match(id, /^[a-f0-9]{12}$/);
const name = `moerise-stars-${id}`;
const root = resolve(repo, '.local', `stars-acceptance-${id}`);
const runtimeRoot = resolve(root, `runtime-${randomBytes(4).toString('hex')}`);
const report = { notes: corpus.length, dimensions, checks: [], errors: [], mapRequests: [], frames: [] };
const username = 'stars-owner', password = resumed?.password || randomBytes(24).toString('hex');
const modelKey = resumed?.modelKey || randomBytes(24).toString('hex');
let sourceURL, coreURL, url, core, coreExit, web, model, browser, page, log, pat, owner, cookie = '', csrf = '', success = false;
let created = false;

async function freePort() {
  const server = http.createServer(); server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const port = server.address().port; await new Promise(resolve => server.close(resolve)); return port;
}
async function request(base, path, options = {}, expected = 200) {
  const started = performance.now();
  const response = await fetch(base + path, {
    ...options, headers: { ...(options.body === undefined ? {} : { 'Content-Type': 'application/json' }), ...options.headers },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: AbortSignal.timeout(65_000), redirect: 'error',
  });
  const text = await response.text();
  let value;
  try { value = text ? JSON.parse(text) : null; } catch { value = { invalid: true }; }
  assert.equal(response.status, expected, `${path}: ${JSON.stringify(value).slice(0, 300)}`);
  return { value, response, ms: performance.now() - started, bytes: Buffer.byteLength(text) };
}
async function source(method, path, body) {
  if (method === 'POST') await delay(550);
  for (let attempt = 0; ; attempt++) {
    try {
      return (await request(sourceURL, '/api/v1' + path, { method, body, headers: { Authorization: `Bearer ${pat}` } })).value;
    } catch (error) {
      if (!error.message.includes('429') || attempt >= 8) throw error;
      await delay(1100 * (attempt + 1));
    }
  }
}
async function api(method, path, body, expected = 200) {
  const result = await request(url, '/api/app/v1' + path, {
    method, body, headers: { Cookie: cookie, Origin: url, 'X-CSRF-Token': csrf, 'Idempotency-Key': randomUUID() },
  }, expected);
  for (const item of result.response.headers.getSetCookie()) if (item.startsWith('moerise_local_session=')) cookie = item.split(';')[0];
  if (result.value?.csrf_token) csrf = result.value.csrf_token;
  if (path === '/map' && result.value.status === 'ready') report.mapRequests.push({ ms: result.ms, bytes: result.bytes });
  return result.value;
}
async function wait(label, predicate, timeout = 300_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await delay(1500);
  }
  throw new Error('等待超时：' + label);
}
async function check(label, action) {
  console.log(label); const start = performance.now();
  await action(); report.checks.push({ label, ms: performance.now() - start });
  await writeFile(resolve(root, 'report.json'), JSON.stringify(report, null, 2));
}
async function startCore() {
  core = spawn(resolve(root, 'moerise'), ['serve', '--config', resolve(root, 'config.json'),
    '--host', '127.0.0.1', '--port', new URL(coreURL).port], {
    cwd: repo, stdio: ['ignore', log.fd, log.fd],
    env: { ...process.env, MOERISE_LAYOUT_URL: process.env.MOERISE_LAYOUT_URL || 'http://127.0.0.1:8766' },
  });
  coreExit = once(core, 'exit');
  await wait('Core', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } }, 30_000);
}
async function stopCore() {
  if (!core) return;
  const child = core; core = null; child.kill('SIGTERM'); await coreExit;
}
async function readyMap(count, previousVersion = '') {
  let result;
  await wait('观星快照', async () => {
    try { result = await api('GET', '/map'); }
    catch (error) { if (error.actual === 503 || error.actual === 429) return false; throw error; }
    return result.status === 'ready' && result.points.length === count &&
      result.algorithm === 'umap-balanced-v1' && result.layout_version !== previousVersion && !result.update_state;
  }, 900_000);
  return result;
}
async function rss(pid) {
  const status = await readFile(`/proc/${pid}/status`, 'utf8');
  return { rssMiB: Number(/^VmRSS:\s+(\d+)/m.exec(status)?.[1]) / 1024,
    peakMiB: Number(/^VmHWM:\s+(\d+)/m.exec(status)?.[1]) / 1024 };
}

try {
  await mkdir(runtimeRoot, { recursive: true }); log = await open(resolve(root, 'core.log'), 'w', 0o600);
  await check('构建 Core 与 Web', async () => {
    const results = await Promise.allSettled([
      runProcess('go', ['build', '-o', resolve(root, 'moerise'), '.'], { cwd: repo }),
      runProcess(process.execPath, ['web/build.mjs'], { cwd: repo }),
    ]);
    for (const result of results) if (result.status === 'rejected') throw result.reason;
  });
  sourceURL = resumed?.sourceURL || `http://127.0.0.1:${await freePort()}`; coreURL = `http://127.0.0.1:${await freePort()}`;
  url = resumedConfig?.web.public_origin || `http://127.0.0.1:${await freePort()}`;
  await check('独立 Memos 与既有笔记、向量语料', async () => {
    if (resumed) {
      pat = resumed.pat; owner = resumed.owner; created = true;
      await request(sourceURL, '/api/v1/instance/profile'); return;
    }
    await runProcess('docker', ['run', '-d', '--name', name, '--mount', 'type=volume,target=/var/opt/memos',
      '-p', `127.0.0.1:${new URL(sourceURL).port}:5230`,
      'neosmemo/memos:0.31.0@sha256:24c2707ddd8fbd2ceaa7a61ecab86a53cdcde640b24ce572b73da14b00b5785f'], { cwd: repo });
    created = true;
    await wait('Memos', async () => { try { await request(sourceURL, '/api/v1/instance/profile'); return true; } catch { return false; } }, 60_000);
    await request(sourceURL, '/api/v1/users', { method: 'POST', body: { username, password, role: 'ADMIN' } });
    const login = (await request(sourceURL, '/api/v1/auth/signin', { method: 'POST',
      body: { passwordCredentials: { username, password } } })).value;
    owner = login.user.name;
    pat = (await request(sourceURL, `/api/v1/${owner}/personalAccessTokens`, { method: 'POST',
      headers: { Authorization: `Bearer ${login.accessToken}` }, body: { description: 'Stars acceptance', expiresInDays: 0 } })).value.token;
    for (let i = 0; i < corpus.length; i++) {
      await source('POST', '/memos', { content: corpus[i].content, visibility: 'PRIVATE' });
      if ((i + 1) % 100 === 0) console.log(`已导入 ${i + 1} / ${corpus.length}`);
    }
    await writeFile(resolve(root, 'fixture.json'), JSON.stringify({ sourceURL, password, modelKey, pat, owner }), { mode: 0o600 });
  });
  await check('正式向量索引与后台 UMAP 快照', async () => {
    model = http.createServer(async (req, res) => {
      let raw = ''; for await (const chunk of req) raw += chunk;
      try {
        const body = JSON.parse(raw);
        assert.equal(req.headers.authorization, `Bearer ${modelKey}`);
        const data = body.input.map((text, index) => {
          const vector = vectorByText.get(text);
          assert.ok(vector, '每条向量请求必须匹配既有笔记正文');
          return { index, embedding: vector };
        });
        res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ data }));
      } catch (error) { report.errors.push(error.message); res.writeHead(400); res.end('{}'); }
    });
    model.listen(resumedConfig ? Number(new URL(resumedConfig.embedding.base_url).port) : 0, '127.0.0.1');
    await once(model, 'listening');
    const config = JSON.parse(await readFile(resolve(repo, 'config.example.json'), 'utf8'));
    config.memos = { ...config.memos, instance_id: name, base_url: sourceURL, public_url: sourceURL, user: owner, token: pat, page_size: 100 };
    config.web = { enabled: true, public_origin: url,
      state_encryption_key: resumedConfig?.web.state_encryption_key || randomBytes(32).toString('base64') };
    config.state_dir = resumedConfig?.state_dir || resolve(runtimeRoot, 'state');
    config.index_dir = resumedConfig?.index_dir || resolve(runtimeRoot, 'index');
    config.cursor_secret = resumedConfig?.cursor_secret || randomBytes(32).toString('hex');
    config.access_tokens = [{ name: 'read', token: randomBytes(32).toString('hex'), scopes: ['notes:read'] }];
    config.sync = { ...config.sync, poll_seconds: 15, full_seconds: 600, retry_seconds: 5 };
    config.notifications.enabled = false; config.llm.enabled = false;
    config.embedding = { ...config.embedding, enabled: true, provider: 'openai-compatible', protocol: 'openai-compatible',
      base_url: `http://127.0.0.1:${model.address().port}/v1`, api_key: modelKey, model: 'qwen3-local-fixture',
      dimensions, max_retries: 0, batch_size: 20 };
    await writeFile(resolve(root, 'config.json'), JSON.stringify(config), { mode: 0o600 });
    await startCore();
    web = createWebServer({ coreOrigin: coreURL, publicOrigin: url, dist: resolve(repo, 'web/dist') });
    web.listen(Number(new URL(url).port), '127.0.0.1'); await once(web, 'listening');
    await api('POST', '/session', { username, password });
    const first = await readyMap(corpus.length);
    assert.equal(new Set(first.points.map(point => point.name)).size, corpus.length);
    assert.ok(first.points.every(point => point.semantic && point.stellar));
    await writeFile(resolve(root, 'snapshot.json'), JSON.stringify(first));
    report.core = await rss(core.pid);
  });
  await check('快照读取延迟与重启稳定性', async () => {
    const first = await api('GET', '/map');
    for (let i = 0; i < 30; i++) await api('GET', '/map');
    await stopCore(); await startCore();
    const coldStart = performance.now(), cold = await api('GET', '/map');
    report.coldReadMs = performance.now() - coldStart;
    assert.deepEqual(cold, first);
    report.coreRestart = await rss(core.pid);
  });
  await check('桌面、窄屏、点星阅读与连续缩放', async () => {
    const { chromium } = await import(pathToFileURL(process.env.MOERISE_PLAYWRIGHT_MODULE || resolve(repo, 'web/node_modules/playwright/index.mjs')).href);
    browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
    page = await browser.newPage({ locale: 'zh-CN', viewport: { width: 1440, height: 1000 } });
    page.on('pageerror', error => report.errors.push(error.message));
    await page.goto(url + '/map'); await page.locator('#username').fill(username); await page.locator('#password').fill(password);
    const start = performance.now(); await page.locator('#login').click();
    await page.waitForSelector(`#map-field[data-stars="${corpus.length}"][data-rendered="true"]`);
    report.loginToStarsMs = performance.now() - start;
    await page.screenshot({ path: resolve(root, 'stars-desktop.png') });
    for (let i = 0; i < 80; i++) {
      const field = page.locator('#map-field'), box = await field.boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.mouse.wheel(0, i < 40 ? -25 : 25);
      await page.evaluate(() => new Promise(requestAnimationFrame));
      report.frames.push(Number(await field.getAttribute('data-draw-ms')));
    }
    await page.getByRole('button', { name: '显示全图', exact: true }).click();
    const point = page.locator('.map-dot').first(); const target = await point.getAttribute('data-name');
    await point.focus(); await point.press('Enter'); await page.locator(`.map-note[data-name="${target}"]`).waitFor();
    const expected = (await api('GET', `/notes/${target.slice(6)}`)).content;
    assert.ok((await page.locator('.map-note').innerText()).includes(expected.slice(0, 12)));
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: resolve(root, 'stars-mobile.png') });
    await page.getByRole('button', { name: '关闭笔记详情', exact: true }).click();
    await page.getByRole('button', { name: '切换星图氛围', exact: true }).click();
    assert.equal(await page.getByRole('button', { name: '切换星图氛围', exact: true }).getAttribute('aria-pressed'), 'false');
  });
  await check('新增真实笔记后旧坐标与星体属性保持', async () => {
    const before = await api('GET', '/map');
    // Reuse an existing body and its actual vector; the source resource is new.
    const added = await source('POST', '/memos', { content: corpus[0].content, visibility: 'PRIVATE' });
    const after = await readyMap(corpus.length + 1, before.layout_version);
    assert.equal(after.layout_frame, before.layout_frame);
    const old = new Map(before.points.map(point => [point.name, point]));
    for (const point of after.points) {
      if (point.name === added.name) continue;
      const prior = old.get(point.name);
      assert.deepEqual([point.x, point.y, point.stellar], [prior.x, prior.y, prior.stellar]);
    }
  });
  await check('主动重建建立新坐标框架', async () => {
    const before = await api('GET', '/map');
    await api('POST', '/map/rebuild', {}, 202);
    const after = await readyMap(corpus.length + 1, before.layout_version);
    assert.notEqual(after.layout_frame, before.layout_frame);
    report.coreFinal = await rss(core.pid);
    report.worker = (await request(process.env.MOERISE_LAYOUT_URL || 'http://127.0.0.1:8766', '/health')).value;
  });
  assert.deepEqual(report.errors, []);
  success = true;
  console.log(JSON.stringify({ report: root, core: report.core, coreFinal: report.coreFinal,
    coldReadMs: report.coldReadMs, loginToStarsMs: report.loginToStarsMs }));
} finally {
  await mkdir(root, { recursive: true }); report.success = success;
  await writeFile(resolve(root, 'report.json'), JSON.stringify(report, null, 2));
  await browser?.close(); await stopCore();
  for (const server of [web, model]) if (server) await new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });
  await log?.close();
  if (created && !values.keep && success) await runProcess('docker', ['rm', '-f', '-v', name], { cwd: repo });
}
