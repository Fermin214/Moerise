import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { readFile, mkdtemp, cp, writeFile, rm, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { createWebServer } from '../server.mjs';

const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
if (!runtime) throw new Error('Set MOERISE_PLAYWRIGHT_MODULE to the installed playwright/index.mjs');
const { chromium } = await import(pathToFileURL(runtime).href);
function settings() {
  return {
    version: 1, default_insight_prompt: '内置默认洞察 Prompt',
    settings: {
      timezone: 'Asia/Hong_Kong',
      review: { count: 3, min_age_hours: 72, recent_days: 7, topic_limit: 0, include_archived: false },
      notifications: { enabled: false, time: '09:00' },
      llm: { enabled: false, protocol: 'openai-compatible', base_url: null, model: null, timeout_seconds: 90,
        max_concurrency: 2, max_retries: 2, max_output_tokens: 2048, context_bytes: 24000 },
      embedding: { enabled: false, provider: 'bailian', protocol: 'dashscope', base_url: null,
        model: 'qwen3.7-text-embedding', dimensions: 1024, send_dimensions: false, batch_size: 20, timeout_seconds: 30,
        max_concurrency: 2, max_retries: 2, query_instruction: null },
      channel: { kind: 'feishu-app', app_id: null, user_open_id: null, destination: null },
      insights: { default_prompt: '用户自定义 Prompt' },
    },
    secrets: Object.fromEntries(['llm_api_key', 'embedding_api_key', 'app_secret', 'verification_token',
      'encrypt_key', 'webhook_url', 'webhook_secret'].map(name => [name, { configured: name === 'llm_api_key' }])),
    channel_capabilities: { text: false, images: false, record: false },
  };
}
async function fixture(t) {
  const state = { value: settings(), session: false, gets: 0, patches: [], fail: null, hold: false, pending: [], csrf: false,
    checks: [], checkFail: null, holdCheck: false, modelPending: [], index: 'disabled' };
  const core = http.createServer(async (req, res) => {
    const path = new URL(req.url, 'http://localhost').pathname;
    let raw = ''; for await (const chunk of req) raw += chunk;
    const json = (data, status = 200) => {
      if (res.destroyed) return;
      res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(data));
    };
    const error = (code, status) => json({ error: { code } }, status);
    const identity = { user: { name: 'users/1', display_name: '设置测试' }, timezone: state.value.settings.timezone, csrf_token: 'csrf-settings' };
    if (path === '/api/app/v1/session') {
      if (req.method === 'POST') state.session = true;
      if (req.method === 'DELETE') { state.session = false; res.writeHead(204); res.end(); return; }
      if (!state.session) { error('SESSION_UNAUTHORIZED', 401); return; }
      json(identity); return;
    }
    if (!state.session) { error('SESSION_UNAUTHORIZED', 401); return; }
    if (path === '/api/app/v1/models') {
      const model = group => ({ enabled: state.value.settings[group].enabled,
        configured: state.value.secrets[`${group}_api_key`].configured,
        protocol: state.value.settings[group].protocol, model: state.value.settings[group].model });
      json({ version: state.value.version, llm: model('llm'), embedding: model('embedding'), index_status: state.index,
        vector_index: { ready: state.index === 'ready', active: null, building: state.index === 'building'
          ? { chunks: 12, completed_chunks: 4, last_error: null } : null } });
      return;
    }
    if (/^\/api\/app\/v1\/models\/(embedding|llm)\/check$/.test(path)) {
      assert.equal(req.headers['x-csrf-token'], 'csrf-settings');
      const body = JSON.parse(raw); state.checks.push({ path, ...body });
      if (body.version !== state.value.version) { error('SETTINGS_CONFLICT', 409); return; }
      if (state.checkFail) { error(state.checkFail, 503); return; }
      if (state.holdCheck) { state.modelPending.push(() => json({ version: body.version, status: 'ok', duration_ms: 17 })); return; }
      json({ version: body.version, status: 'ok', duration_ms: 17 }); return;
    }
    if (path === '/api/app/v1/notes') { json({ notes: [], next_page_token: '' }); return; }
    if (path === '/api/app/v1/tags') { json({ tags: [] }); return; }
    if (path === '/api/app/v1/calendar') { json({ month: new URL(req.url, 'http://localhost').searchParams.get('month'), days: [] }); return; }
    if (path !== '/api/app/v1/settings') { error('SOURCE_NOT_FOUND', 404); return; }
    if (req.method === 'GET') {
      state.gets++;
      const reply = structuredClone(state.value);
      if (state.hold) { state.pending.push(() => json(reply)); return; }
      json(reply); return;
    }
    assert.equal(req.method, 'PATCH');
    assert.equal(req.headers['x-csrf-token'], 'csrf-settings');
    if (state.csrf) { state.csrf = false; error('CSRF_REJECTED', 403); return; }
    const body = JSON.parse(raw); state.patches.push(body);
    if (state.fail) {
      const code = state.fail; state.fail = null;
      error(code, code === 'SETTINGS_CONFLICT' ? 409 : code === 'SETTINGS_BUSY' ? 503 : 422); return;
    }
    if (body.version !== state.value.version) { error('SETTINGS_CONFLICT', 409); return; }
    for (const [key, value] of Object.entries(body.settings)) {
      if (typeof value === 'object' && !Array.isArray(value)) Object.assign(state.value.settings[key], value);
      else state.value.settings[key] = value;
    }
    for (const [key, operation] of Object.entries(body.secrets)) {
      if (operation.action !== 'keep') state.value.secrets[key].configured = operation.action === 'replace';
    }
    state.value.version++;
    const webhook = state.value.secrets.webhook_url.configured;
    state.value.channel_capabilities = { text: webhook, images: false, record: false };
    const reply = structuredClone(state.value);
    if (state.hold) { state.pending.push(() => json(reply)); return; }
    json(reply);
  });
  core.listen(0, '127.0.0.1'); await once(core, 'listening');
  const dist = await mkdtemp(join(tmpdir(), 'moerise-settings-'));
  await cp(fileURLToPath(new URL('../src/', import.meta.url)), dist, { recursive: true });
  // 沿用生产构建的内联样式转换规则。
  const rules = [];
  const html = (await readFile(join(dist, 'index.html'), 'utf8')).replace(/<[^>]+\sstyle="[^"]*"[^>]*>/g, tag => {
    const style = /\sstyle="([^"]*)"/.exec(tag)[1], name = `layout-${rules.length}`;
    rules.push(`#app .${name}{${style}}`);
    const cleaned = tag.replace(/\sstyle="[^"]*"/, '');
    return /\sclass="/.test(cleaned)
      ? cleaned.replace(/\sclass="([^"]*)"/, ` class="$1 ${name}"`) : cleaned.replace(/^<([^\s>]+)/, `<$1 class="${name}"`);
  });
  await writeFile(join(dist, 'layout.css'), rules.join('\n'));
  await writeFile(join(dist, 'index.html'), html.replace('</head>', '<link rel="stylesheet" href="/layout.css"></head>'));
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
  const url = `http://127.0.0.1:${port}`;
  const web = createWebServer({ coreOrigin: `http://127.0.0.1:${core.address().port}`, publicOrigin: url, dist });
  web.listen(port, '127.0.0.1'); await once(web, 'listening');
  let browser;
  t.after(async () => {
    for (const release of [...state.pending.splice(0), ...state.modelPending.splice(0)]) release();
    await browser?.close();
    await Promise.all([web, core].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
    await rm(dist, { recursive: true, force: true });
  });
  browser = await chromium.launch({
    headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined,
    args: [`--log-file=${join(dist, 'chromium.log')}`],
    env: { ...process.env, CHROME_LOG_FILE: join(dist, 'chromium.log') },
  });
  const page = await browser.newPage({ viewport: { width: 1360, height: 960 } }), errors = [];
  page.on('pageerror', err => errors.push(err.message));
  return { page, state, url, errors };
}
async function login(page) {
  await page.locator('#login-form').waitFor({ state: 'visible' });
  await page.locator('#username').fill('test'); await page.locator('#password').fill('test');
  await page.locator('#login').click(); await page.locator('#workspace').waitFor({ state: 'visible' });
}
const pageReady = page => page.getByRole('button', { name: '保存设置', exact: true }).waitFor();
const setting = (page, path) => page.locator(`[name="${path}"]`);
const operation = (page, label) => page.getByRole('combobox', { name: `${label}操作`, exact: true });
const secret = (page, label) => page.getByLabel(`${label}新值`, { exact: true });

test('settings browser saves public fields, explicit secrets, prompt and multi-slot times with session CSRF', { timeout: 60_000 }, async t => {
  const { page, state, url, errors } = await fixture(t);
  await page.clock.setFixedTime(new Date('2026-10-31T18:00:00Z'));
  await page.goto(url); await login(page);
  await page.locator('#calendar-title').filter({ hasText: '2026年11月' }).waitFor();
  assert.equal(state.gets, 0);
  await page.locator('a[data-route][href="/settings"]').click(); await pageReady(page);
  assert.equal(state.gets, 1);
  assert.equal(await page.locator('#settings-page input[type="password"]:visible').count(), 0);
  await setting(page, 'review.count').fill('5');
  await setting(page, 'review.min_age_hours').fill('48');
  await setting(page, 'review.recent_days').fill('14');
  await setting(page, 'review.include_archived').check();
  await setting(page, 'timezone').fill('UTC');
  await setting(page, 'notifications.enabled').check();
  await page.getByRole('button', { name: '添加时段' }).click();
  await page.getByLabel('推送时段 2', { exact: true }).fill('18:30');
  await setting(page, 'llm.enabled').check();
  await setting(page, 'llm.base_url').fill('https://llm.example.test/v1');
  await setting(page, 'llm.model').fill('test-llm');
  await operation(page, 'LLM API Key').selectOption('replace');
  await secret(page, 'LLM API Key').fill('••••••••');
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.getByRole('alert').filter({ hasText: '真实的新值' }).waitFor();
  assert.equal(state.patches.length, 0);
  await secret(page, 'LLM API Key').fill('new-llm-key');
  await operation(page, 'Embedding API Key').selectOption('clear');
  await setting(page, 'channel.kind').selectOption('feishu-webhook');
  await operation(page, 'Webhook URL').selectOption('replace');
  await secret(page, 'Webhook URL').fill('https://hook.example.test/token');
  await page.getByRole('button', { name: '恢复默认', exact: true }).click();
  assert.equal(await setting(page, 'insights.default_prompt').inputValue(), state.value.default_insight_prompt);
  state.csrf = true;
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '设置已保存' }).waitFor();
  await page.locator('#calendar-title').filter({ hasText: '2026年10月' }).waitFor();
  await page.locator('#calendar button.today[aria-label^="2026-10-31"]').waitFor();
  const patch = state.patches[0];
  assert.equal(patch.version, 1);
  assert.deepEqual(patch.settings.review, { count: 5, min_age_hours: 48, recent_days: 14, include_archived: true });
  assert.deepEqual(patch.settings.notifications, { enabled: true, times: ['09:00', '18:30'] });
  assert.deepEqual(patch.secrets.llm_api_key, { action: 'replace', value: 'new-llm-key' });
  assert.deepEqual(patch.secrets.embedding_api_key, { action: 'clear' });
  assert.equal(patch.secrets.webhook_url.action, 'replace');
  assert.deepEqual(patch.secrets.app_secret, { action: 'keep' });
  assert.equal(patch.settings.llm.api_key, undefined);
  assert.equal(await page.locator('#settings-page input[type="password"]:visible').count(), 0);
  assert.equal(await operation(page, 'LLM API Key').inputValue(), 'keep');
  await page.getByText('文字推送：可用', { exact: true }).waitFor();
  await page.getByText('图文推送：不可用', { exact: true }).waitFor();
  await setting(page, 'review.count').fill('6');
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '设置已保存' }).waitFor();
  assert.equal(state.patches.at(-1).version, 2);
  assert.deepEqual(state.patches.at(-1).settings, { review: { count: 6 } });
  assert.ok(Object.values(state.patches.at(-1).secrets).every(value => value.action === 'keep' && !('value' in value)));
  if (process.env.MOERISE_SCREENSHOT_DIR) {
    await mkdir(process.env.MOERISE_SCREENSHOT_DIR, { recursive: true });
    await page.screenshot({ path: join(process.env.MOERISE_SCREENSHOT_DIR, 'settings-desktop.png'), fullPage: true });
  }
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  if (process.env.MOERISE_SCREENSHOT_DIR) {
    await mkdir(process.env.MOERISE_SCREENSHOT_DIR, { recursive: true });
    await page.screenshot({ path: join(process.env.MOERISE_SCREENSHOT_DIR, 'settings-mobile.png'), fullPage: true });
  }
  assert.deepEqual(errors, []);
});

test('injected requests cannot publish late success or expiry after settings disposal', { timeout: 60_000 }, async t => {
  const { page, url } = await fixture(t);
  await page.goto(url);
  const result = await page.evaluate(async initial => {
    const { createSettingsPage } = await import('/settings.js');
    const host = document.createElement('div'); document.body.append(host);
    let saved = 0, failed = 0;
    const requests = [];
    const view = createSettingsPage({
      sessionRequest(path, options) {
        if (path === '/models') return new Promise(() => {});
        return new Promise((resolve, reject) => requests.push({ path, options, resolve, reject }));
      },
      onSaved() { saved++; },
      handleFailure() { failed++; },
    });
    const first = view.mount(host), lateGET = requests.shift();
    view.destroy(); lateGET.resolve(initial); await first;
    const lateGETIgnored = host.childElementCount === 0 && lateGET.options.controller.signal.aborted;
    const second = view.mount(host); requests.shift().resolve(initial); await second;
    const action = host.querySelector('#settings-secret-llm_api_key');
    action.value = 'replace'; action.dispatchEvent(new Event('change'));
    const input = host.querySelector('[aria-label="LLM API Key新值"]');
    input.value = 'transient-secret';
    host.querySelector('form').requestSubmit();
    const latePATCH = requests.shift();
    const submittedRealSecret = latePATCH.options.body.secrets.llm_api_key.value === 'transient-secret';
    view.destroy();
    const removedSecret = input.value === '' && latePATCH.options.body.secrets.llm_api_key.value === undefined;
    latePATCH.resolve({ ...initial, version: 2 });
    await new Promise(resolve => setTimeout(resolve, 0));
    const third = view.mount(host), lateExpiry = requests.shift();
    view.destroy();
    lateExpiry.reject(Object.assign(new Error('Expired'), { status: 401, code: 'SESSION_UNAUTHORIZED' }));
    await third;
    const result = { lateGETIgnored, submittedRealSecret, removedSecret, saved, failed, nodes: host.childElementCount };
    host.remove(); return result;
  }, settings());
  assert.deepEqual(result, { lateGETIgnored: true, submittedRealSecret: true, removedSecret: true, saved: 0, failed: 0, nodes: 0 });
});

test('model settings pair protocols, check saved versions and discard obsolete results', { timeout: 60_000 }, async t => {
  const { page, state, url, errors } = await fixture(t);
  await page.goto(`${url}/settings`); await login(page); await pageReady(page);
  await page.locator('#settings-index-status').filter({ hasText: '已关闭' }).waitFor();
  await page.locator('a[data-route][href="/insights"]').click();
  await page.locator('#capability').filter({ hasText: '请在设置中启用 LLM' }).waitFor();
  await page.locator('a[data-route][href="/settings"]').click(); await pageReady(page);
  const check = page.getByRole('button', { name: '检查 Embedding 连接', exact: true });
  assert.ok(await check.isDisabled());
  await setting(page, 'embedding.enabled').check();
  await setting(page, 'embedding.provider').selectOption('openai-compatible');
  assert.equal(await setting(page, 'embedding.protocol').inputValue(), 'openai-compatible');
  await setting(page, 'embedding.base_url').fill('https://vectors.example.test/v1');
  await setting(page, 'embedding.model').fill('custom-embedding');
  await operation(page, 'Embedding API Key').selectOption('replace');
  await secret(page, 'Embedding API Key').fill('generic-key');
  await page.locator('.settings-model').filter({ has: check }).locator('summary').click();
  await setting(page, 'embedding.dimensions').fill('768');
  await setting(page, 'embedding.send_dimensions').check();
  assert.ok(await setting(page, 'embedding.query_instruction').isDisabled());
  assert.ok(await check.isDisabled());
  state.index = 'building';
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '设置已保存' }).waitFor();
  await page.locator('#settings-index-status').filter({ hasText: '4/12' }).waitFor();
  assert.ok(await check.isEnabled());
  assert.equal(state.patches[0].settings.embedding.send_dimensions, true);
  await check.click();
  await page.locator('#settings-embedding-check-result').filter({ hasText: '连接正常' }).waitFor();
  assert.equal(state.checks[0].version, 2);
  state.checkFail = 'EMBEDDING_AUTH';
  await check.click();
  await page.locator('#settings-embedding-check-result').filter({ hasText: '密钥被拒绝' }).waitFor();
  state.checkFail = null; state.holdCheck = true;
  await check.click();
  await page.locator('#settings-embedding-check-result').filter({ hasText: '正在检查' }).waitFor();
  await setting(page, 'embedding.model').fill('new-model');
  for (const release of state.modelPending.splice(0)) release();
  await page.locator('#settings-embedding-check-result').filter({ hasText: '保存后检查' }).waitFor();
  assert.ok(await check.isDisabled());
  await setting(page, 'embedding.protocol').selectOption('dashscope');
  assert.equal(await setting(page, 'embedding.provider').inputValue(), 'bailian');
  assert.equal(await setting(page, 'embedding.send_dimensions').isChecked(), false);
  await page.locator('a[data-route][href="/review"]').click();
  assert.equal(await page.locator('#settings-page').count(), 0);
  await page.locator('a[data-route][href="/map"]').click();
  await page.locator('#capability').filter({ hasText: '语义索引构建中' }).waitFor();
  assert.deepEqual(errors, []);
});

test('conflict requires reload, busy can retry, and leaving or expiring removes sensitive inputs', { timeout: 60_000 }, async t => {
  const { page, state, url, errors } = await fixture(t);
  await page.goto(`${url}/settings`); await login(page); await pageReady(page);
  await operation(page, 'LLM API Key').selectOption('replace'); await secret(page, 'LLM API Key').fill('temporary-key');
  await operation(page, 'LLM API Key').selectOption('keep');
  assert.equal(await secret(page, 'LLM API Key').inputValue(), '');
  await operation(page, 'LLM API Key').selectOption('replace'); await secret(page, 'LLM API Key').fill('conflict-key');
  await setting(page, 'review.count').fill('4'); state.fail = 'SETTINGS_CONFLICT';
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.getByRole('alert').filter({ hasText: '其他页面更新' }).waitFor();
  assert.ok(await page.getByRole('button', { name: '保存设置', exact: true }).isDisabled());
  assert.equal(await setting(page, 'review.count').inputValue(), '4');
  assert.equal(await secret(page, 'LLM API Key').inputValue(), '');
  await page.getByRole('button', { name: '重新加载设置' }).click(); await pageReady(page);
  assert.equal(await setting(page, 'review.count').inputValue(), '3');
  await setting(page, 'review.count').fill('4'); state.fail = 'SETTINGS_BUSY';
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.getByRole('alert').filter({ hasText: '服务繁忙' }).waitFor();
  assert.ok(await page.getByRole('button', { name: '保存设置', exact: true }).isEnabled());
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '设置已保存' }).waitFor();
  await operation(page, 'LLM API Key').selectOption('replace'); await secret(page, 'LLM API Key').fill('route-key');
  await page.locator('a[data-route][href="/review"]').click();
  assert.equal(await page.locator('#settings-page').count(), 0);
  assert.equal(await page.locator('input[type="password"]').count(), 1);
  state.hold = true;
  await page.locator('a[data-route][href="/settings"]').click();
  await page.waitForFunction(() => document.querySelector('#settings-page')?.textContent.includes('正在加载'));
  await page.locator('#q').fill('筛选');
  await page.waitForURL(url => url.searchParams.get('query') === '筛选');
  for (const release of state.pending.splice(0)) release();
  state.hold = false;
  assert.equal(await page.locator('#settings-page').count(), 0);
  await page.locator('a[data-route][href="/settings"]').click(); await pageReady(page);
  await operation(page, 'LLM API Key').selectOption('replace'); await secret(page, 'LLM API Key').fill('logout-key');
  state.hold = true;
  await page.getByRole('button', { name: '保存设置', exact: true }).click();
  await page.waitForFunction(() => document.querySelector('.settings-form')?.getAttribute('aria-busy') === 'true');
  await page.locator('#user-menu').click(); await page.locator('#logout').click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  for (const release of state.pending.splice(0)) release();
  state.hold = false;
  assert.equal(await page.locator('#settings-page').count(), 0);
  await login(page); await pageReady(page);
  await operation(page, 'LLM API Key').selectOption('replace'); await secret(page, 'LLM API Key').fill('expired-key');
  state.session = false;
  await page.getByRole('button', { name: '重新加载设置' }).click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#settings-page').count(), 0);
  assert.deepEqual(errors, []);
});
