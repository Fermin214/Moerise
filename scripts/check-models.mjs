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
const id = randomBytes(8).toString('hex'), name = `moerise-models-${id}`;
const root = resolve(repo, '.local', `models-acceptance-${id}`);
const source = `${name}-source`, core = `${name}-core`;
const cancellation = new AbortController();
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => cancellation.abort(new Error(signal)));
const evidence = { checks: [], pageErrors: [], modelChecks: [], sourceFailures: [] };
const sourceStop = new AbortController();
let browser, page, web, mock, sourceProxy, cookie = '', csrf = '', url, coreURL, sourceURL, pat;
const username = 'models-owner', password = randomBytes(24).toString('hex');
const embeddingKey = randomBytes(24).toString('hex'), llmKey = randomBytes(24).toString('hex');
const machineKey = randomBytes(24).toString('hex');
const fixture = { failure: 0, remaining: 1, successes: new Map(), llmModels: [], violations: [] };
const containers = new Set();
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
  const result = await request(url, `/api/app/v1${path}`, {
    method, body, headers: { Cookie: cookie, Origin: url, 'X-CSRF-Token': csrf, 'Idempotency-Key': randomUUID() },
  }, expected);
  for (const item of result.response.headers.getSetCookie()) if (item.startsWith('moerise_local_session=')) cookie = item.split(';')[0];
  if (path === '/session' && result.value?.csrf_token) csrf = result.value.csrf_token;
  return result.value;
}
const models = () => api('GET', '/models');
async function readyIndex() {
  let result;
  await wait('语义索引', async () => { result = await models(); return result.index_status === 'ready'; });
  return result.vector_index.active;
}
async function check(label, action) {
  step = label; const started = Date.now(); console.log(label);
  await action(); evidence.checks.push({ check: label, passed: true, durationMs: Date.now() - started });
}
async function saveEvidence() {
  await writeFile(resolve(root, 'checks.json'), JSON.stringify({ ...evidence, success }, null, 2) + '\n');
}
async function close(server) {
  if (!server) return;
  await new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });
}
async function cleanFiles() {
  const parent = await realpath(resolve(repo, '.local'));
  assert.equal(dirname(await realpath(root)), parent);
  assert.match(basename(root), /^models-acceptance-[0-9a-f]{16}$/);
  for (const entry of ['config.json', 'moerise', 'state', 'index']) {
    const target = resolve(root, entry);
    let info;
    try { info = await lstat(target); } catch (err) { if (err.code === 'ENOENT') continue; throw err; }
    assert.equal(info.isSymbolicLink(), false);
    assert.equal(dirname(await realpath(target)), await realpath(root));
    await rm(target, { recursive: info.isDirectory(), force: true });
  }
}
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
  await check('独立 Memos、官方账户与本人绑定', async () => {
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
      body: { description: 'Models acceptance', expiresInDays: 0 },
    })).value.token;
    let nextSourceRequest = 0;
    sourceProxy = http.createServer(async (req, res) => {
      // 为验收的并行轮询与来源复查统一限速，Memos 继续执行自己的限流规则。
      const start = Math.max(Date.now(), nextSourceRequest); nextSourceRequest = start + 150;
      try { await delay(Math.max(0, start - Date.now()), undefined, { signal: sourceStop.signal }); }
      catch { res.destroy(); return; }
      if (res.destroyed) return;
      const upstream = http.request(sourceURL + req.url, { method: req.method,
        headers: { ...req.headers, host: new URL(sourceURL).host } }, response => {
        const creationProbe = response.statusCode === 404 && req.method === 'GET' &&
          /^\/api\/v1\/memos\/[A-Za-z0-9_-]+$/.test(req.url);
        if (response.statusCode >= 400 && !creationProbe) {
          evidence.sourceFailures.push({ path: req.url.split('?')[0], status: response.statusCode });
        }
        res.writeHead(response.statusCode, response.headers); response.pipe(res);
      });
      upstream.on('error', () => { if (!res.headersSent) res.writeHead(503); res.end(); });
      req.pipe(upstream);
    });
    sourceProxy.listen(0, '0.0.0.0'); await once(sourceProxy, 'listening');
    const config = JSON.parse(await readFile(resolve(repo, 'config.example.json'), 'utf8'));
    config.memos = { ...config.memos, instance_id: name, base_url: `http://host.docker.internal:${sourceProxy.address().port}`,
      public_url: sourceURL, user: login.user.name, token: pat };
    config.web = { enabled: true, public_origin: url, state_encryption_key: randomBytes(32).toString('base64') };
    config.state_dir = '/data/state'; config.index_dir = '/data/index'; config.cursor_secret = randomBytes(32).toString('hex');
    config.access_tokens = [{ name: 'acceptance', token: machineKey, scopes: ['notes:read', 'sync:read'] }];
    config.sync = { ...config.sync, poll_seconds: 5, full_seconds: 60, retry_seconds: 3 };
    config.embedding.enabled = config.llm.enabled = config.notifications.enabled = false;
    await writeFile(resolve(root, 'config.json'), JSON.stringify(config));
    containers.add(core);
    await docker(['run', '-dt', '--name', core, '--network', name, '--mount', `type=bind,source=${root},target=/data`,
      '--add-host', 'host.docker.internal:host-gateway',
      '-p', `127.0.0.1:${corePort}:8787`, 'golang:1.27.3-bookworm',
      '/data/moerise', 'serve', '--config', '/data/config.json', '--host', '0.0.0.0', '--port', '8787']);
    web = createWebServer({ coreOrigin: coreURL, publicOrigin: url, dist: resolve(repo, 'web/dist') });
    web.listen(webPort, '127.0.0.1'); await once(web, 'listening');
    await wait('Core', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } });
  });
  mock = http.createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk;
    const body = JSON.parse(raw), json = value => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(value)); };
    if (req.url === '/v1/chat/completions') {
      if (req.headers.authorization !== `Bearer ${llmKey}`) fixture.violations.push('LLM credential');
      fixture.llmModels.push(body.model);
      res.setHeader('Content-Type', 'text/event-stream');
      const frame = (delta, finish_reason) => `data: ${JSON.stringify({ model: body.model, choices: [{ index: 0, delta, finish_reason }] })}\n\n`;
      res.end(frame({ content: 'OK' }, null) + frame({}, 'stop') + 'data: [DONE]\n\n'); return;
    }
    if (req.url !== '/v1/embeddings' || req.headers.authorization !== `Bearer ${embeddingKey}` ||
        body.encoding_format !== 'float' || !Array.isArray(body.input)) fixture.violations.push('Embedding request');
    const dimensions = body.model === 'embedding-b' ? 4 : 3;
    if (body.dimensions !== undefined && body.dimensions !== dimensions) fixture.violations.push('Embedding dimensions');
    const document = body.input.some(text => text.startsWith('note '));
    if (fixture.failure || (document && fixture.remaining === 0)) {
      res.statusCode = fixture.failure || 503;
      json({ error: { message: `private provider error ${embeddingKey}` } }); return;
    }
    if (document && fixture.remaining > 0) fixture.remaining--;
    const data = body.input.map((text, index) => {
      if (document) {
        const key = `${body.model}:${text}`; fixture.successes.set(key, (fixture.successes.get(key) || 0) + 1);
      }
      const embedding = Array(dimensions).fill(0); embedding[0] = text.length; embedding[1] = 1;
      return { index, embedding };
    }).reverse();
    json({ data, usage: { total_tokens: body.input.length } });
  });
  mock.listen(0, '0.0.0.0'); await once(mock, 'listening');
  const modelURL = `http://host.docker.internal:${mock.address().port}/v1`;
  browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  page = await browser.newPage({ viewport: { width: 1360, height: 960 } });
  page.on('pageerror', error => evidence.pageErrors.push(error.message));
  page.on('response', async response => {
    if (!/\/models\/(embedding|llm)\/check$/.test(response.url())) return;
    try {
      const body = await response.json();
      evidence.modelChecks.push({ model: response.url().includes('/embedding/') ? 'embedding' : 'llm',
        status: response.status(), code: body.error?.code || body.status, version: body.version });
    } catch {}
  });
  const field = path => page.locator(`[name="${path}"]`);
  const settingPage = async () => {
    await page.goto(`${url}/settings`);
    await page.getByRole('button', { name: '保存设置', exact: true }).waitFor();
  };
  const save = async () => {
    await page.getByRole('button', { name: '保存设置', exact: true }).click();
    await page.getByRole('status').filter({ hasText: '设置已保存' }).waitFor();
  };
  const fillModel = async (group, model, key) => {
    await field(`${group}.enabled`).check(); await field(`${group}.base_url`).fill(modelURL); await field(`${group}.model`).fill(model);
    const label = group === 'llm' ? 'LLM API Key' : 'Embedding API Key';
    await page.getByRole('combobox', { name: `${label}操作`, exact: true }).selectOption('replace');
    await page.getByLabel(`${label}新值`, { exact: true }).fill(key);
  };
  await check('模型关闭时的真实笔记、搜索和浏览器登录', async () => {
    await api('POST', '/session', { username, password });
    for (const content of ['note balcony herbs', 'note bicycle brakes', 'note reading list',
      'note first paragraph ' + '虚'.repeat(1100) + '\n\nnote second paragraph ' + '构'.repeat(1100)]) {
      await api('POST', '/notes', { content }, 201);
    }
    const list = await api('GET', '/notes?query=balcony'); assert.equal(list.notes.length, 1);
    await page.goto(`${url}/settings`); await page.locator('#username').fill(username); await page.locator('#password').fill(password);
    await page.locator('#login').click(); await page.locator('#settings-index-status').filter({ hasText: '已关闭' }).waitFor();
    assert.equal((await models()).llm.enabled, false);
    await request(url, '/api/app/v1/models', {}, 401);
    await request(url, '/api/app/v1/models', { headers: { Authorization: `Bearer ${machineKey}` } }, 403);
  });
  await check('仅 LLM 启用与真实流式连接检查', async () => {
    await fillModel('llm', 'llm-a', llmKey); await save();
    await page.getByRole('button', { name: '检查 LLM 连接', exact: true }).click();
    await page.locator('#settings-llm-check-result').filter({ hasText: '连接正常' }).waitFor();
    assert.equal((await models()).index_status, 'disabled'); assert.deepEqual(fixture.llmModels, ['llm-a']);
  });
  let initialID, firstChunk;
  await check('通用 Embedding 保存、部分构建与连接错误', async () => {
    await field('embedding.provider').selectOption('openai-compatible');
    await fillModel('embedding', 'embedding-a', embeddingKey);
    await page.locator('.settings-model').filter({ has: field('embedding.enabled') }).locator('summary').click();
    await field('embedding.dimensions').fill('3'); await field('embedding.batch_size').fill('1');
    await field('embedding.max_retries').fill('0'); await field('embedding.timeout_seconds').fill('5');
    await save();
    await wait('持久构建进度', async () => {
      const state = await models(), generation = state.vector_index.building;
      if (state.index_status !== 'unavailable' || generation.completed_chunks !== 1) return false;
      initialID = generation.id; return true;
    });
    firstChunk = [...fixture.successes.keys()][0];
    fixture.failure = 401;
    await page.getByRole('button', { name: '检查 Embedding 连接', exact: true }).click();
    await page.locator('#settings-embedding-check-result').filter({ hasText: '密钥被拒绝' }).waitFor();
    assert.equal((await page.locator('#settings-page').textContent()).includes(embeddingKey), false);
    const status = await models();
    const error = await api('POST', '/models/embedding/check', { version: status.version }, 503);
    assert.equal(error.error.code, 'EMBEDDING_AUTH'); assert.equal(JSON.stringify(error).includes(embeddingKey), false);
    await api('POST', '/models/embedding/check', { version: status.version - 1 }, 409);
  });
  await check('Core 重启续建、会话保持与已提交片段复用', async () => {
    await docker(['restart', core]);
    await wait('重启后状态', async () => { try { return (await models()).vector_index.building?.id === initialID; } catch { return false; } });
    fixture.failure = 0; fixture.remaining = -1;
    const active = await readyIndex(); assert.equal(active.id, initialID); assert.equal(active.chunks, 5);
    assert.equal(fixture.successes.get(firstChunk), 1);
    await settingPage();
    await page.getByRole('button', { name: '检查 Embedding 连接', exact: true }).click();
    await page.locator('#settings-embedding-check-result').filter({ hasText: '连接正常' }).waitFor();
  });
  await check('增量编辑复用未变片段与通用语义查询', async () => {
    const notes = (await api('GET', '/notes')).notes, note = notes.find(note => note.content === 'note balcony herbs');
    await api('PATCH', `/notes/${note.name.split('/')[1]}`, { content: 'note balcony herbs edited', source_hash: note.source_hash });
    await wait('增量代次', async () => {
      const state = await models(); return state.index_status === 'ready' && state.vector_index.active.id !== initialID;
    });
    initialID = (await readyIndex()).id; assert.equal(fixture.successes.size, 6);
    const result = (await request(coreURL, '/api/ext/v1/search', { method: 'POST',
      headers: { Authorization: `Bearer ${machineKey}` }, body: { query: 'herbs', mode: 'hybrid' } })).value;
    assert.equal(result.semantic.used, true);
  });
  await check('Embedding 空间切换期间保留旧代次并阻止混用', async () => {
    fixture.failure = 503;
    await field('embedding.model').fill('embedding-b');
    await page.locator('.settings-model').filter({ has: field('embedding.enabled') }).locator('summary').click();
    await field('embedding.dimensions').fill('4'); await field('embedding.send_dimensions').check(); await save();
    await wait('新空间失败', async () => {
      const state = await models();
      return state.index_status === 'unavailable' && state.vector_index.building?.dimensions === 4;
    });
    const state = await models(); assert.equal(state.vector_index.active.id, initialID); assert.equal(state.vector_index.ready, false);
    const result = (await request(coreURL, '/api/ext/v1/search', { method: 'POST',
      headers: { Authorization: `Bearer ${machineKey}` }, body: { query: 'herbs', mode: 'hybrid' } })).value;
    assert.equal(result.semantic.used, false); assert.equal(result.semantic.reason, 'INDEX_NOT_READY');
    await page.locator('#settings-index-status').filter({ hasText: '构建失败' }).waitFor();
    fixture.failure = 0;
    const active = await readyIndex(); assert.notEqual(active.id, initialID); assert.equal(active.dimensions, 4); initialID = active.id;
  });
  await check('更换 LLM 不重建向量，Embedding 可独立于 LLM', async () => {
    const before = fixture.successes.size;
    await field('llm.model').fill('llm-b'); await save();
    await page.getByRole('button', { name: '检查 LLM 连接', exact: true }).click();
    await page.locator('#settings-llm-check-result').filter({ hasText: '连接正常' }).waitFor();
    assert.equal(fixture.llmModels.at(-1), 'llm-b');
    assert.equal((await readyIndex()).id, initialID); assert.equal(fixture.successes.size, before);
    await field('llm.enabled').uncheck(); await save();
    assert.equal((await readyIndex()).id, initialID);
    await page.getByRole('button', { name: '检查 Embedding 连接', exact: true }).click();
    await page.locator('#settings-embedding-check-result').filter({ hasText: '连接正常' }).waitFor();
  });
  await check('索引重建保留设置、密钥与真实浏览器会话', async () => {
    const before = await api('GET', '/settings');
    await page.locator('a[data-route][href="/"]').click();
    await docker(['stop', core]);
    containers.add(`${name}-rebuild`);
    await docker(['run', '--name', `${name}-rebuild`, '--network', name, '--mount', `type=bind,source=${root},target=/data`,
      'golang:1.27.3-bookworm', '/data/moerise', 'rebuild-index', '--config', '/data/config.json']);
    await docker(['start', core]);
    await wait('重建后 Core', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } });
    const active = await readyIndex(); assert.notEqual(active.id, initialID);
    const after = await api('GET', '/settings'); assert.equal(after.version, before.version); assert.deepEqual(after.secrets, before.secrets);
    await settingPage(); await page.locator('#settings-index-status').filter({ hasText: '已就绪' }).waitFor();
    await page.screenshot({ path: resolve(root, 'models-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: resolve(root, 'models-mobile.png'), fullPage: true });
    await page.setViewportSize({ width: 320, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  });
  await check('关闭模型后基础操作与退出，模型请求保持独立密钥', async () => {
    await field('embedding.enabled').uncheck(); await save();
    assert.equal((await models()).index_status, 'disabled');
    await api('POST', '/notes', { content: 'note after disabling models' }, 201);
    assert.equal((await api('GET', '/notes?query=disabling')).notes.length, 1);
    assert.deepEqual(fixture.violations, []); assert.deepEqual(evidence.pageErrors, []); assert.deepEqual(evidence.sourceFailures, []);
    await api('DELETE', '/session', undefined, 204); await api('GET', '/models', undefined, 401);
    await page.locator('#user-menu').click(); await page.locator('#logout').click();
    await page.locator('#login-form').waitFor({ state: 'visible' });
  });
  success = true;
} catch (error) {
  evidence.failure = { check: step, message: error.message };
  if (page && !page.isClosed()) {
    evidence.failure.modelResults = await page.locator('[id$="-check-result"]').allTextContents();
    await page.screenshot({ path: resolve(root, 'failure.png'), fullPage: true });
  }
  if (containers.has(core)) {
    try { await writeFile(resolve(root, 'core.log'), await docker(['logs', core], true)); } catch {}
  }
  console.error(`${step}: ${error.message}`); process.exitCode = 1;
} finally {
  await browser?.close();
  await close(web); await close(mock); sourceStop.abort(); await close(sourceProxy);
  const cleanupErrors = [];
  for (const container of [...containers].reverse()) {
    try { await docker(['rm', '-f', '-v', container], true); } catch (error) { cleanupErrors.push(error.message); }
  }
  if (network) { try { await docker(['network', 'rm', name], true); } catch (error) { cleanupErrors.push(error.message); } }
  try { await cleanFiles(); } catch (error) { cleanupErrors.push(error.message); }
  evidence.cleanupErrors = cleanupErrors;
  if (cleanupErrors.length) { success = false; process.exitCode = 1; console.error('验收资源清理失败'); }
  await saveEvidence();
}
