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
const dockerUser = process.platform === 'linux' ? ['--user', `${process.getuid()}:${process.getgid()}`] : [];
const id = randomBytes(8).toString('hex'), name = `moerise-chat-${id}`;
const root = resolve(repo, '.local', `chat-acceptance-${id}`);
const source = `${name}-source`, core = `${name}-core`, containers = new Set();
const cancellation = new AbortController(), sourceStop = new AbortController();
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => cancellation.abort(new Error(signal)));
const username = 'chat-owner', password = randomBytes(24).toString('hex');
const modelKey = randomBytes(24).toString('hex'), machineKey = randomBytes(24).toString('hex');
const evidence = { scope: '3.7 自然聊天完整流程', checks: [], pageErrors: [], sourceFailures: [], browserRequests: [], llmCalls: 0, pending: [] };
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
  assert.match(basename(root), /^chat-acceptance-[0-9a-f]{16}$/);
  for (const entry of ['config.json', 'moerise', 'state', 'index']) {
    const target = resolve(root, entry);
    let info;
    try { info = await lstat(target); } catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    assert.equal(info.isSymbolicLink(), false);
    assert.equal(dirname(await realpath(target)), await realpath(root));
    await rm(target, { recursive: info.isDirectory(), force: true });
  }
}
const model = { mode: 'complete', requests: [], aborted: 0 };
const reply = '  ## 新的联想\n\n保留 **完整 Markdown** 与 #原生标签。 😀\n\n原文之外，这是一种可能的解释。  \n';
let config, selectedChat, savedNote, beforeRestart, secondaryChat, mainBeforeControls;

const readyChat = () => page.locator('.insights-page[aria-busy="false"]').waitFor();
const ask = () => page.locator('[data-chat="ask"]');
const send = () => page.getByRole('button', { name: '发送', exact: true });
async function menuAction(label) {
  await page.getByRole('button', { name: '会话', exact: true }).click();
  await page.getByRole('menuitem', { name: label, exact: true }).click();
}
async function sourceAPI(method, path, body, expected = 200) {
  return (await request(sourceURL, `/api/v1${path}`, {
    method, body, headers: { Authorization: `Bearer ${pat}` },
  }, expected)).value;
}
async function loginBrowser(path) {
  await page.goto(url + path);
  await page.locator('#username').fill(username); await page.locator('#password').fill(password);
  await page.locator('#login').click(); await readyChat();
}
async function restoreBrowser() {
  await page.goto(`${url}/insights?${new URLSearchParams({ chat: selectedChat.id })}`);
  await readyChat();
}
async function pickAll() {
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: '原始标签', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: '共同标签', exact: true }).click();
  await page.getByRole('button', { name: `全选这 ${fixtures.length} 条`, exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
}
async function waitTurn(id, status) {
  let value;
  await wait(`聊天终态 ${status}`, async () => {
    value = await api('GET', `/chats/${id}`);
    return value.turns.at(-1)?.assistant.status === status;
  });
  return value;
}
async function restartCore() {
  await docker(['restart', core]);
  await wait('重启 Core', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } });
}

try {
  await mkdir(root, { recursive: true });
  await check('编译 Linux Core 与正式 Web', async () => {
    containers.add(`${name}-build`);
    await docker(['run', ...dockerUser, '--name', `${name}-build`, '--mount', `type=bind,source=${repo},target=/src`, '-w', '/src',
      '-e', 'GOMODCACHE=/src/.local/gomodcache', '-e', 'GOCACHE=/src/.local/linux-gocache',
      'golang:1.27.3-bookworm', 'go', 'build', '-o', `/src/.local/${basename(root)}/moerise`, '.']);
    await runProcess(process.execPath, ['web/build.mjs'], { cwd: repo, signal: cancellation.signal });
  });
  const sourcePort = await port(), corePort = await port(), webPort = await port();
  sourceURL = `http://127.0.0.1:${sourcePort}`; coreURL = `http://127.0.0.1:${corePort}`; url = `http://127.0.0.1:${webPort}`;
  await check('独立 Memos 正常与归档材料、可控 HTTP LLM、关闭 Embedding', async () => {
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
      body: { description: 'Chat acceptance', expiresInDays: 0 },
    })).value.token;
    for (let index = 0; index < 3; index++) {
      const note = await sourceAPI('POST', '/memos', {
        content: `研究材料 ${index}\n\n${'完整原文 😀 '.repeat(40)}\n\n末尾 ${index} #原始标签 #共同标签`,
        visibility: 'PRIVATE',
      });
      const patch = { createTime: '2026-10-20T08:00:00Z' };
      if (index === 1) patch.state = 'ARCHIVED';
      const updated = await sourceAPI('PATCH', `/${note.name}?${new URLSearchParams({
        updateMask: index === 1 ? 'create_time,state' : 'create_time',
      })}`, patch);
      fixtures.push(updated);
    }
    let nextSourceRequest = 0;
    sourceProxy = http.createServer(async (req, res) => {
      const start = Math.max(Date.now(), nextSourceRequest); nextSourceRequest = start + 150;
      try { await delay(Math.max(0, start - Date.now()), undefined, { signal: sourceStop.signal }); }
      catch { res.destroy(); return; }
      if (res.destroyed) return;
      const upstream = http.request(sourceURL + req.url, { method: req.method,
        headers: { ...req.headers, host: new URL(sourceURL).host } }, response => {
        const creationProbe = response.statusCode === 404 && req.method === 'GET' &&
          /^\/api\/v1\/memos\/[A-Za-z0-9_-]+$/.test(req.url);
        if (response.statusCode >= 400 && !creationProbe) evidence.sourceFailures.push({ method: req.method, status: response.statusCode });
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
        assert.equal(req.url, '/v1/chat/completions'); assert.equal(req.headers.authorization, `Bearer ${modelKey}`);
        assert.equal(body.model, 'chat-fixture'); assert.equal(body.stream, true); assert.ok(body.messages.length >= 3);
        evidence.llmCalls++; model.requests.push(structuredClone(body.messages));
        res.writeHead(200, { 'Content-Type': 'text/event-stream' });
        res.write(`data: ${JSON.stringify({ model: 'chat-fixture', choices: [{
          index: 0, delta: { content: reply }, finish_reason: null,
        }] })}\n\n`);
        if (model.mode === 'hold') {
          res.on('close', () => { if (!res.writableEnded) model.aborted++; }); return;
        }
        if (model.mode === 'eof') { res.end(); return; }
        res.end(`data: ${JSON.stringify({ model: 'chat-fixture', choices: [{
          index: 0, delta: {}, finish_reason: 'stop',
        }] })}\n\ndata: [DONE]\n\n`);
      } catch {
        evidence.providerFailure = true;
        if (!res.headersSent) res.writeHead(400);
        res.end();
      }
    });
    mock.listen(0, '0.0.0.0'); await once(mock, 'listening');
    config = JSON.parse(await readFile(resolve(repo, 'config.example.json'), 'utf8'));
    config.memos = { ...config.memos, instance_id: name, page_size: 2,
      base_url: `http://host.docker.internal:${sourceProxy.address().port}`, public_url: sourceURL, user: login.user.name, token: pat };
    config.web = { enabled: true, public_origin: url, state_encryption_key: randomBytes(32).toString('base64') };
    config.state_dir = '/data/state'; config.index_dir = '/data/index'; config.cursor_secret = randomBytes(32).toString('hex');
    config.access_tokens = [{ name: 'acceptance', token: machineKey,
      scopes: ['notes:read', 'notes:write', 'ai:generate', 'reviews:write'] }];
    config.sync = { ...config.sync, poll_seconds: 60, full_seconds: 300, retry_seconds: 3 };
    config.notifications.enabled = false; config.embedding.enabled = false;
    config.llm = { ...config.llm, enabled: true, base_url: `http://host.docker.internal:${mock.address().port}/v1`,
      api_key: modelKey, model: 'chat-fixture', max_retries: 0, context_bytes: 128000 };
    config.insights.default_prompt = '请完整阅读所有已选笔记，区分原意与新的联想。';
    await writeFile(resolve(root, 'config.json'), JSON.stringify(config));
  });
  await check('Linux Core、正式 Web、Chromium 与本人会话', async () => {
    containers.add(core);
    await docker(['run', ...dockerUser, '-dt', '--name', core, '--network', name, '--mount', `type=bind,source=${root},target=/data`,
      '--add-host', 'host.docker.internal:host-gateway', '-p', `127.0.0.1:${corePort}:8787`,
      'golang:1.27.3-bookworm', '/data/moerise', 'serve', '--config', '/data/config.json', '--host', '0.0.0.0', '--port', '8787']);
    web = createWebServer({ coreOrigin: coreURL, publicOrigin: url, dist: resolve(repo, 'web/dist') });
    web.listen(webPort, '127.0.0.1'); await once(web, 'listening');
    await wait('Core', async () => { try { await request(coreURL, '/health/ready'); return true; } catch { return false; } });
    await api('POST', '/session', { username, password });
    assert.equal((await api('GET', '/models')).embedding.enabled, false);
    await request(url, '/api/app/v1/chats', {}, 401);
    await request(url, '/api/app/v1/chats', { headers: { Authorization: `Bearer ${machineKey}` } }, 403);
    browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
    page = await browser.newPage({ viewport: { width: 1360, height: 960 } });
    await page.clock.setFixedTime('2026-10-31T00:00:00Z');
    page.on('pageerror', error => evidence.pageErrors.push(error.message));
    page.on('request', req => {
      if (new URL(req.url()).pathname.startsWith('/api/app/v1/')) {
        evidence.browserRequests.push({ method: req.method(), path: new URL(req.url()).pathname });
      }
    });
    await loginBrowser('/insights');
  });
  await check('日期与标签组合选材、归档完整原文、一键洞察与自由追问', async () => {
    await page.getByRole('button', { name: '添加笔记', exact: true }).click();
    await page.getByRole('button', { name: '2026-10-20', exact: true }).click();
    await page.getByRole('button', { name: '2026-10-21', exact: true }).click();
    await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '原始标签', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '共同标签', exact: true }).click();
    await page.getByRole('button', { name: '全选这 3 条', exact: true }).click();
    await page.getByRole('button', { name: '完成', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'detached' });
    selectedChat = await api('GET', `/chats/${new URL(page.url()).searchParams.get('chat')}`);
    assert.deepEqual(selectedChat.materials.map(item => item.name).sort(), fixtures.map(item => item.name).sort());
    const count = fixtures.reduce((sum, note) => sum + [...note.content].length, 0);
    assert.match(await page.locator('[data-chat="material-toggle"]').textContent(), new RegExp(`${count} / 20000`));
    await page.locator('[data-chat="one"]').click(); await readyChat();
    const firstMessages = model.requests[0];
    const materials = JSON.parse(firstMessages[1].content.replace('本轮明确选定的全部笔记材料：\n', ''));
    assert.equal(materials.length, 3);
    for (const item of materials) assert.equal(item.content, fixtures.find(note => note.name === item.name).content);
    assert.equal(firstMessages[2].content, config.insights.default_prompt);
    await ask().fill('还有哪些不同解释？'); await send().click(); await readyChat();
    assert.equal(model.requests[1].length, 5);
    assert.equal(model.requests[1][3].content, reply);
    assert.equal(model.requests[1][4].content, '还有哪些不同解释？');
    assert.equal(await page.locator('.insight-answer strong').first().textContent(), '完整 Markdown');
  });
  await check('原回复主动保存、固定版本重试与 Memos 原文读回', async () => {
    const response = page.waitForResponse(response => response.url().endsWith('/save') && response.request().method() === 'POST');
    await page.getByRole('button', { name: '保存为笔记', exact: true }).first().click();
    savedNote = await (await response).json();
    await page.getByRole('button', { name: '已保存为笔记', exact: true }).waitFor();
    const current = await api('GET', `/chats/${selectedChat.id}`), turn = current.turns[0];
    const replay = await api('POST', `/chats/${selectedChat.id}/messages/${turn.request_id}/save`, { version: turn.assistant.version });
    assert.equal(replay.name, savedNote.name);
    assert.equal((await sourceAPI('GET', `/${savedNote.name}`)).content, reply);
    assert.equal(savedNote.content, reply);
    beforeRestart = await api('GET', `/chats/${selectedChat.id}`);
    await page.screenshot({ path: resolve(root, 'chat-desktop.png'), fullPage: true });
  });
  await check('刷新、详情返回、侧栏重进与 Core 重启恢复', async () => {
    const calls = evidence.llmCalls;
    await page.reload(); await readyChat();
    assert.equal(await page.locator('.insight-assistant').count(), 2);
    await page.locator('.insight-material .chip').first().click();
    await page.getByRole('button', { name: '‹ 返回AI 洞察', exact: true }).click(); await readyChat();
    assert.equal(new URL(page.url()).searchParams.get('chat'), selectedChat.id);
    await page.locator('.side a[href="/"]').click();
    await page.locator('.side a[href="/insights"]').click(); await readyChat();
    assert.equal(new URL(page.url()).searchParams.get('chat'), selectedChat.id);
    await restartCore();
    assert.deepEqual(await api('GET', `/chats/${selectedChat.id}`), beforeRestart);
    await page.reload(); await readyChat();
    assert.equal(evidence.llmCalls, calls);
    assert.equal(await page.locator('.insight-assistant').count(), 2);
  });
  await check('共享设置修改与恢复默认 Prompt，原历史保持', async () => {
    await page.locator('.side a[href="/settings"]').click();
    await page.locator('#settings-page[aria-busy="false"]').waitFor();
    await page.locator('[name="insights.default_prompt"]').fill('新的真实聊天 Prompt');
    await page.getByRole('button', { name: '保存设置', exact: true }).click();
    await page.getByRole('status').filter({ hasText: '设置已保存' }).waitFor();
    await restoreBrowser();
    await page.locator('[data-chat="one"]').click(); await readyChat();
    assert.equal(model.requests.at(-1).at(-1).content, '新的真实聊天 Prompt');
    let current = await api('GET', `/chats/${selectedChat.id}`);
    assert.equal(current.turns[0].prompt, config.insights.default_prompt);
    assert.equal(current.turns.at(-1).prompt, '新的真实聊天 Prompt');
    await page.locator('.side a[href="/settings"]').click();
    await page.locator('#settings-page[aria-busy="false"]').waitFor();
    const settings = await api('GET', '/settings');
    await page.getByRole('button', { name: '恢复默认', exact: true }).click();
    assert.equal(await page.locator('[name="insights.default_prompt"]').inputValue(), settings.default_insight_prompt);
    await page.getByRole('button', { name: '保存设置', exact: true }).click();
    await page.getByRole('status').filter({ hasText: '设置已保存' }).waitFor();
    await restoreBrowser();
    current = await api('GET', `/chats/${selectedChat.id}`);
    assert.equal(current.turns.length, 3);
  });
  await check('真实上下文与材料超限保留原输入、历史和完整选材', async () => {
    const calls = evidence.llmCalls, initial = await api('GET', `/chats/${selectedChat.id}`);
    let settings = await api('GET', '/settings');
    await api('PATCH', '/settings', { version: settings.version, settings: { llm: { context_bytes: 1024 } } });
    const question = '上下文预算检查。' + '长'.repeat(1000);
    await ask().fill(question); await send().click(); await readyChat();
    await page.locator('[data-chat="warning"]').filter({ hasText: '上下文上限' }).waitFor();
    assert.equal(await ask().inputValue(), question);
    assert.equal(evidence.llmCalls, calls);
    assert.deepEqual((await api('GET', `/chats/${selectedChat.id}`)).turns, initial.turns);
    settings = await api('GET', '/settings');
    await api('PATCH', '/settings', { version: settings.version, settings: { llm: { context_bytes: 128000 } } });
    const budgetNotes = [];
    for (let i = 0; i < 3; i++) budgetNotes.push(await sourceAPI('POST', '/memos', {
      content: `${'m'.repeat(7000)} #预算边界`, visibility: 'PRIVATE',
    }));
    await page.getByRole('button', { name: '添加笔记', exact: true }).click();
    await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '预算边界', exact: true }).click();
    await page.getByRole('button', { name: '全选这 3 条', exact: true }).click();
    await page.getByRole('button', { name: '完成', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'detached' });
    const current = await api('GET', `/chats/${selectedChat.id}`);
    assert.equal(current.materials.length, 6);
    const resolved = await api('POST', '/chat-materials/resolve', { names: current.materials.map(item => item.name) });
    assert.ok(resolved.characters > 20000);
    for (const note of budgetNotes) assert.equal(resolved.notes.find(item => item.name === note.name).content, note.content);
    await page.getByText('超过字数上限，请减少所选笔记。', { exact: true }).waitFor();
    assert.ok(await send().isDisabled());
    assert.equal(evidence.llmCalls, calls);
    for (let i = 0; i < 3; i++) {
      await page.locator('.insight-material').filter({ hasText: 'mmmmmmmmmmmm' }).first()
        .getByRole('button', { name: '移除这条笔记', exact: true }).click();
      await readyChat();
    }
    assert.equal((await api('GET', `/chats/${selectedChat.id}`)).materials.length, 3);
    assert.deepEqual((await api('GET', `/chats/${selectedChat.id}`)).turns, initial.turns);
    await ask().fill('');
  });
  await check('导航断流、供应商异常结束与 Core 重启中断', async () => {
    model.mode = 'hold';
    const initialAborts = model.aborted;
    await ask().fill('导航断流检查'); await send().click();
    await page.locator('.insight-answer strong').nth(3).waitFor();
    await page.locator('.side a[href="/"]').click();
    let current = await waitTurn(selectedChat.id, 'interrupted');
    assert.equal(current.turns.at(-1).assistant.content, reply);
    await wait('上游请求取消', async () => model.aborted > initialAborts);
    model.mode = 'eof'; await restoreBrowser();
    await ask().fill('异常结束检查'); await send().click(); await readyChat();
    current = await waitTurn(selectedChat.id, 'failed');
    assert.equal(current.turns.at(-1).assistant.error_code, 'LLM_PROTOCOL');
    assert.equal(current.turns.at(-1).assistant.content, reply);
    model.mode = 'hold';
    await ask().fill('重启中断检查'); await send().click();
    await page.locator('.insight-answer strong').nth(5).waitFor();
    const calls = evidence.llmCalls;
    await restartCore();
    current = await waitTurn(selectedChat.id, 'interrupted');
    await page.reload(); await readyChat();
    assert.equal(evidence.llmCalls, calls);
    assert.equal(current.turns.at(-1).assistant.content, reply);
    model.mode = 'complete';
  });
  await check('来源变更反馈、历史保留与明确重新选材', async () => {
    const calls = evidence.llmCalls;
    fixtures[0] = await sourceAPI('PATCH', `/${fixtures[0].name}?updateMask=content`,
      { content: '研究材料：明确变更的完整来源。 #原始标签 #共同标签' });
    await page.reload(); await readyChat();
    await page.locator('[data-chat="warning"]').filter({ hasText: '所选材料已修改' }).waitFor();
    await ask().fill('来源变更检查'); await send().click(); await readyChat();
    assert.equal(evidence.llmCalls, calls);
    assert.equal(await page.locator('.insight-assistant').count(), 6);
    await pickAll();
    await ask().fill('使用重新选择的来源继续'); await send().click(); await readyChat();
    assert.equal(evidence.llmCalls, calls + 1);
    const material = JSON.parse(model.requests.at(-1)[1].content.replace('本轮明确选定的全部笔记材料：\n', ''));
    assert.equal(material.find(item => item.name === fixtures[0].name).content, fixtures[0].content);
    await sourceAPI('DELETE', `/${fixtures[1].name}`);
    await page.reload(); await readyChat();
    await page.locator('[data-chat="warning"]').filter({ hasText: '删除或无法访问' }).waitFor();
    assert.equal(await page.locator('.insight-assistant').count(), 7);
    const current = await api('GET', `/chats/${selectedChat.id}`);
    const unavailable = current.materials.findIndex(item => item.name === fixtures[1].name);
    await page.locator('.insight-material').nth(unavailable).getByRole('button', { name: '移除这条笔记', exact: true }).click();
    await readyChat();
    assert.equal((await api('GET', `/chats/${selectedChat.id}`)).materials.length, 2);
    assert.equal(await page.locator('.insight-assistant').count(), 7);
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: resolve(root, 'chat-mobile.png'), fullPage: true });
  });
  await check('新建空草稿、历史分页及指定会话恢复', async () => {
    const calls = evidence.llmCalls;
    mainBeforeControls = await api('GET', `/chats/${selectedChat.id}`);
    await page.setViewportSize({ width: 1360, height: 960 });
    await menuAction('新建会话'); await readyChat();
    await page.reload(); await readyChat();
    assert.equal(new URL(page.url()).searchParams.get('new'), '1');
    assert.equal(await page.locator('.insight-assistant').count(), 0);
    assert.match(await page.locator('[data-chat="material-toggle"]').textContent(), /0 条/);
    assert.equal(evidence.llmCalls, calls);
    assert.equal((await api('GET', '/chats')).chats.length, 1);
    await page.getByRole('button', { name: '添加笔记', exact: true }).click();
    await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '原始标签', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '共同标签', exact: true }).click();
    await page.getByRole('button', { name: '全选这 2 条', exact: true }).click();
    await page.getByRole('button', { name: '完成', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'detached' });
    secondaryChat = await api('GET', `/chats/${new URL(page.url()).searchParams.get('chat')}`);
    assert.notEqual(secondaryChat.id, selectedChat.id);
    await ask().fill('独立会话的首轮问题'); await send().click(); await readyChat();
    secondaryChat = await api('GET', `/chats/${secondaryChat.id}`);
    assert.equal(secondaryChat.preview, '独立会话的首轮问题');
    const seeds = [];
    for (let i = 0; i < 20; i++) seeds.push(await api('POST', '/chats', {
      request_id: randomUUID(), title: `分页会话 ${i}`, names: [fixtures[0].name],
    }, 201));
    await menuAction('历史会话');
    const history = page.getByRole('dialog', { name: '历史会话', exact: true });
    await history.locator('.insight-history-row').nth(19).waitFor();
    assert.equal(await history.locator('.insight-history-row').count(), 20);
    await history.getByRole('button', { name: '加载更多', exact: true }).click();
    await history.locator('.insight-history-row').nth(21).waitFor();
    await page.screenshot({ path: resolve(root, 'chat-history.png'), fullPage: true });
    await history.getByRole('button', { name: /一键洞察/ }).click(); await readyChat();
    assert.equal(new URL(page.url()).searchParams.get('chat'), selectedChat.id);
    assert.equal(await page.locator('.insight-assistant').count(), mainBeforeControls.turns.length);
    assert.deepEqual(await api('GET', `/chats/${selectedChat.id}`), mainBeforeControls);
    await menuAction('历史会话');
    await history.locator('.insight-history-row').nth(19).waitFor();
    await history.getByRole('button', { name: '加载更多', exact: true }).click();
    await history.getByRole('button', { name: /独立会话的首轮问题/ }).click(); await readyChat();
    assert.equal(new URL(page.url()).searchParams.get('chat'), secondaryChat.id);
    assert.equal(await page.locator('.insight-assistant').count(), 1);
    assert.equal(evidence.llmCalls, calls + 1);
    for (const seed of seeds) await api('DELETE', `/chats/${seed.id}`, { version: seed.version }, 204);
  });
  await check('主动停止、取消终态与删除前生成及保存排空', async () => {
    const calls = evidence.llmCalls;
    let releaseSave, saved;
    const holdSave = async route => {
      const response = await route.fetch(); saved = await response.json();
      await new Promise(resolve => { releaseSave = resolve; });
      await route.fulfill({ response });
    };
    await page.route('**/messages/*/save', holdSave);
    await page.getByRole('button', { name: '保存为笔记', exact: true }).click();
    await wait('保存返回已到达浏览器代理', async () => !!releaseSave);
    model.mode = 'hold';
    await ask().fill('主动停止检查'); await send().click();
    await page.locator('.insight-answer strong').nth(1).waitFor();
    await page.getByRole('button', { name: '停止', exact: true }).click(); await readyChat();
    let current = await waitTurn(secondaryChat.id, 'canceled');
    assert.equal(current.turns.at(-1).assistant.content, reply);
    assert.equal(current.turns.at(-1).assistant.error_code, 'CHAT_CANCELED');
    assert.ok(await page.getByRole('button', { name: '保存为笔记', exact: true }).last().isDisabled());
    const deletes = evidence.browserRequests.filter(item => item.method === 'DELETE' && item.path.includes('/chats/')).length;
    await ask().fill('删除前停止检查'); await send().click();
    await page.locator('.insight-answer strong').nth(2).waitFor();
    await menuAction('删除当前会话');
    await page.locator('.cfm').getByRole('button', { name: '取消', exact: true }).click();
    assert.equal(evidence.browserRequests.filter(item => item.method === 'DELETE' && item.path.includes('/chats/')).length, deletes);
    await menuAction('删除当前会话');
    await page.screenshot({ path: resolve(root, 'chat-delete.png'), fullPage: true });
    await page.locator('.cfm').getByRole('button', { name: '删除', exact: true }).click();
    current = await waitTurn(secondaryChat.id, 'canceled');
    assert.equal(current.turns.at(-1).assistant.content, reply);
    assert.equal(evidence.browserRequests.filter(item => item.method === 'DELETE' && item.path.includes('/chats/')).length, deletes);
    releaseSave();
    await page.waitForURL('**/insights?new=1'); await readyChat();
    await page.unroute('**/messages/*/save', holdSave);
    await api('GET', `/chats/${secondaryChat.id}`, undefined, 404);
    assert.equal((await sourceAPI('GET', `/${saved.name}`)).content, reply);
    assert.equal((await sourceAPI('GET', `/${savedNote.name}`)).content, reply);
    assert.equal(await page.locator('.insight-assistant').count(), 0);
    assert.equal(evidence.llmCalls, calls + 2);
    model.mode = 'complete';
    await menuAction('历史会话');
    await page.getByRole('dialog', { name: '历史会话', exact: true }).getByRole('button', { name: /一键洞察/ }).click();
    await readyChat();
    assert.equal(new URL(page.url()).searchParams.get('chat'), selectedChat.id);
    assert.deepEqual(await api('GET', `/chats/${selectedChat.id}`), mainBeforeControls);
  });
  await check('Cookie/Origin/CSRF、机器 REST/MCP 隔离及退出清理', async () => {
    const calls = evidence.llmCalls;
    await request(url, '/api/app/v1/chats', { method: 'POST', body: {}, headers: { Cookie: cookie, Origin: url } }, 403);
    for (const path of ['/chats', `/chats/${selectedChat.id}/messages`]) {
      await request(coreURL, `/api/ext/v1${path}`, { method: 'POST', body: {},
        headers: { Authorization: `Bearer ${machineKey}` } }, 403);
    }
    const mcp = (await request(coreURL, '/mcp', { method: 'POST', headers: {
      Authorization: `Bearer ${machineKey}`, Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': '2025-11-25',
    }, body: { jsonrpc: '2.0', id: 'chat-list', method: 'tools/list', params: {} } })).value;
    assert.ok(mcp.result.tools.every(tool => tool.annotations.readOnlyHint === true && !/chat|generate|save/.test(tool.name)));
    assert.deepEqual(await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length })), { local: 0, session: 0 });
    await page.locator('#user-menu').click(); await page.locator('#logout').click();
    await page.locator('#login-form').waitFor({ state: 'visible' });
    assert.equal(await page.locator('.insights-page').count(), 0);
    await api('DELETE', '/session', undefined, 204);
    await request(url, '/api/app/v1/chats', { headers: { Cookie: cookie } }, 401);
    assert.equal((await sourceAPI('GET', `/${savedNote.name}`)).content, reply);
    assert.equal(evidence.llmCalls, calls);
    assert.equal(evidence.providerFailure, undefined); assert.deepEqual(evidence.pageErrors, []);
    assert.deepEqual(evidence.sourceFailures, []);
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
