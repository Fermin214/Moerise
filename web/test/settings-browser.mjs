import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { text as readRequestText } from 'node:stream/consumers';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { returningBrowser } from './browser-helpers.mjs';
import { createWebServer } from './fixture-server.mjs';

const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
const { [process.env.MOERISE_PLAYWRIGHT_ENGINE || 'chromium']: browserType } = runtime
  ? await import(pathToFileURL(runtime).href) : await import('playwright');
const dates = ['09:00'];

function settings() {
  return {
    version: 1,
    settings: {
      timezone: 'Asia/Hong_Kong',
      review: { count: 3, min_age_hours: 72, recent_days: 7, include_archived: false },
      notifications: { enabled: false, times: [...dates] },
      embedding: { enabled: false, provider: 'bailian', protocol: 'dashscope', base_url: null,
        model: 'qwen3.7-text-embedding', dimensions: 1024, send_dimensions: false, batch_size: 20,
        timeout_seconds: 30, max_concurrency: 2, max_retries: 2, query_instruction: null },
      channel: { kind: 'none', app_id: null, user_open_id: null, destination: null, feishu_mode: 'websocket',
        qq_app_id: null, webhook_images: false, webhook_app_id: null },
      stargazing: { enabled: true },
    },
    secrets: Object.fromEntries(['embedding_api_key', 'app_secret', 'verification_token', 'encrypt_key', 'webhook_url',
      'webhook_secret', 'bot_token', 'qq_app_secret', 'webhook_app_secret'].map(name => [name, { configured: false }])),
    channel_status: {}, channel_capabilities: { text: false, images: false, record: false },
  };
}

async function fixture(t, { language = 'zh-CN', mobile = false, returning = true } = {}) {
  const state = { value: settings(), sessions: new Map(), sessionRequests: [], sessionReadUnavailable: false,
    gets: 0, patches: [], checks: [], telemetry: false, batches: [],
    index: 'disabled', mapReads: 0, rebuilds: 0, deliveries: [], retries: [], fail: null, checkFail: null,
    hold: null, holdCheck: null, holdNotes: null, notesFail: null, nextSettingsRead: null, csrf: false,
    notes: [], notesReads: [], mapData: null, modelVersion: null, holdLogout: null };
  const core = http.createServer(async (req, res) => {
    const path = new URL(req.url, 'http://localhost').pathname;
    const raw = await readRequestText(req);
    const json = (data, status = 200) => {
      if (res.destroyed) return;
      res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(data));
    };
    const error = (code, status) => json({ error: { code } }, status);
    let sessionId = /(?:^|;\s*)settings-session=(\d+)/.exec(req.headers.cookie || '')?.[1];
    let csrf = state.sessions.get(sessionId);
    if (path === '/api/app/v1/session') {
      state.sessionRequests.push({ method: req.method, sessionId, csrf: req.headers['x-csrf-token'] });
      if (req.method === 'POST') {
        sessionId = String(state.sessionRequests.filter(request => request.method === 'POST').length);
        csrf = `csrf-settings-${sessionId}`;
        state.sessions.set(sessionId, csrf);
        res.setHeader('Set-Cookie', `settings-session=${sessionId}; HttpOnly; SameSite=Lax; Path=/`);
      }
      if (req.method === 'DELETE') {
        if (state.holdLogout) await state.holdLogout;
        if (csrf && req.headers['x-csrf-token'] !== csrf) { error('CSRF_REJECTED', 403); return; }
        state.sessions.delete(sessionId);
        res.setHeader('Set-Cookie', 'settings-session=; Max-Age=0; HttpOnly; SameSite=Lax; Path=/');
        res.writeHead(204); res.end(); return;
      }
      if (!csrf) { error('SESSION_UNAUTHORIZED', 401); return; }
      if (req.method === 'GET' && state.sessionReadUnavailable) { error('SOURCE_UNAVAILABLE', 503); return; }
      json({ user: { name: 'users/1', display_name: '设置测试' }, timezone: state.value.settings.timezone,
        stargazing_enabled: state.value.settings.stargazing.enabled, csrf_token: csrf });
      return;
    }
    if (!csrf) { error('SESSION_UNAUTHORIZED', 401); return; }
    if (path === '/api/app/v1/capabilities') {
      json({ version: '1.2.3', telemetry: { schema: 1, allowed: state.telemetry } }); return;
    }
    if (path === '/api/app/v1/telemetry') {
      assert.equal(req.headers['x-csrf-token'], csrf);
      state.batches.push(JSON.parse(raw)); res.writeHead(204); res.end(); return;
    }
    if (path === '/api/app/v1/models') {
      json({ version: state.modelVersion ?? state.value.version, embedding: state.value.settings.embedding, index_status: state.index,
        vector_index: { building: state.index === 'building' ? { chunks: 12, completed_chunks: 4 } : null } }); return;
    }
    if (path === '/api/app/v1/map') {
      state.mapReads++;
      json(state.mapData || { status: 'disabled', algorithm: 'umap-balanced-v1', projection_revision: 0, layout_version: '',
        layout_frame: '', vector_generation: '', points: [] }); return;
    }
    if (path === '/api/app/v1/map/rebuild') {
      assert.equal(req.headers['x-csrf-token'], csrf);
      state.rebuilds++; json({ status: 'queued' }); return;
    }
    if (['/api/app/v1/models/embedding/check', '/api/app/v1/channels/check'].includes(path)) {
      assert.equal(req.headers['x-csrf-token'], csrf);
      const body = JSON.parse(raw);
      state.checks.push({ path, ...body });
      if (body.version !== state.value.version) { error('SETTINGS_CONFLICT', 409); return; }
      await state.holdCheck;
      if (state.checkFail) { error(state.checkFail, 503); return; }
      json({ version: body.version, status: 'ok', duration_ms: 17 }); return;
    }
    if (path === '/api/app/v1/notes') {
      state.notesReads.push(Object.fromEntries(new URL(req.url, 'http://localhost').searchParams));
      await state.holdNotes;
      if (state.notesFail) { error('NOTE_LIST_INVALID', state.notesFail); return; }
      json({ notes: state.notes, next_page_token: '' }); return;
    }
    if (path === '/api/app/v1/tags') { json({ tags: [] }); return; }
    if (path === '/api/app/v1/calendar') {
      json({ month: new URL(req.url, 'http://localhost').searchParams.get('month'), days: [] }); return;
    }
    if (path === '/api/app/v1/admin/notifications') { json({ notifications: state.deliveries }); return; }
    if (path.endsWith('/retry')) {
      const id = path.split('/').at(-2);
      state.retries.push(id); state.deliveries.find(item => item.id === id).status = 'pending';
      json({ notification: state.deliveries.find(item => item.id === id) }); return;
    }
    if (path !== '/api/app/v1/settings') { error('SOURCE_NOT_FOUND', 404); return; }
    if (req.method === 'GET') {
      state.gets++;
      const reply = structuredClone(state.nextSettingsRead || state.value);
      state.nextSettingsRead = null;
      await state.hold;
      json(reply); return;
    }
    assert.equal(req.method, 'PATCH');
    assert.equal(req.headers['x-csrf-token'], csrf);
    if (state.csrf) { state.csrf = false; error('CSRF_REJECTED', 403); return; }
    const body = JSON.parse(raw);
    state.patches.push(body);
    if (state.fail) {
      const code = state.fail; state.fail = null;
      error(code, code === 'SETTINGS_CONFLICT' ? 409 : code === 'SETTINGS_BUSY' ? 503 : 422); return;
    }
    if (body.version !== state.value.version) { error('SETTINGS_CONFLICT', 409); return; }
    for (const [key, value] of Object.entries(body.settings)) {
      if (typeof value === 'object') Object.assign(state.value.settings[key], value);
      else state.value.settings[key] = value;
    }
    for (const [key, operation] of Object.entries(body.secrets)) {
      if (operation.action !== 'keep') state.value.secrets[key].configured = operation.action === 'replace';
    }
    state.value.version++;
    const reply = structuredClone(state.value);
    await state.hold;
    json(reply);
  });
  core.listen(0, '127.0.0.1'); await once(core, 'listening');
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port;
  await new Promise(resolve => probe.close(resolve));
  const url = `http://127.0.0.1:${port}`;
  const web = createWebServer({ coreOrigin: `http://127.0.0.1:${core.address().port}`, publicOrigin: url,
    dist: fileURLToPath(new URL('../dist/', import.meta.url)) });
  web.listen(port, '127.0.0.1'); await once(web, 'listening');
  let browser;
  t.after(async () => {
    await browser?.close();
    await Promise.all([web, core].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
  });
  browser = await browserType.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  const context = await browser.newContext({ locale: language, viewport: mobile ? { width: 390, height: 844 } : { width: 1360, height: 960 } });
  const page = await context.newPage(), errors = [];
  if (returning) await returningBrowser(page);
  page.on('pageerror', err => errors.push(err.message));
  return { page, state, url, errors };
}

async function login(page, url, path = '/settings') {
  await page.goto(url + path);
  await page.locator('#username').fill('test'); await page.locator('#password').fill('test');
  await page.locator('#login').click();
  await page.locator('#workspace').waitFor();
}

const tabs = page => page.getByRole('tablist');
const tab = (page, id) => page.locator(`#settings-tab-${id}`).click();
const settingsField = (page, path) => page.locator(`[name="${path}"]`);
const edit = (page, title) => page.locator('.settings-row').filter({ hasText: title }).getByRole('button', { name: /^(编辑|Edit)$/ }).click();
const dialog = page => page.locator('dialog.settings-edit[open]');
const save = page => dialog(page).locator('button.save').click();
const operation = (page, label) => dialog(page).getByRole('combobox', { name: `${label}操作`, exact: true });
const newSecret = (page, label) => dialog(page).getByLabel(`${label}新值`, { exact: true });
const confirmation = (page, text) => page.locator('.snackbar').getByText(text, { exact: true }).waitFor();

async function beginOfflineLogout(page) {
  await page.locator('#empty').waitFor();
  await page.context().setOffline(true);
  await page.locator('#account-settings').click();
  await page.locator('#logout').click();
  await page.locator('#retry-logout').waitFor();
  assert.equal(await page.locator('#login-form').isHidden(), true);
  assert.equal(await page.locator('#app').isHidden(), true);
  await page.context().setOffline(false);
}

test('first offline settings visit offers logout and retries without restoring private content', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/');
  await beginOfflineLogout(page);
  assert.equal(state.sessions.size, 1);
  await page.locator('#retry-logout').click();
  await page.locator('#login-form').waitFor();
  assert.equal(state.sessions.size, 0);
});

test('reloading after offline logout revokes the session while upstream authentication is unavailable', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/');
  await beginOfflineLogout(page);
  assert.equal(state.sessions.size, 1);
  state.sessionReadUnavailable = true;
  const before = state.sessionRequests.length;
  let release;
  state.holdLogout = new Promise(resolve => { release = resolve; });
  try {
    const revoking = page.waitForRequest(request => request.method() === 'DELETE' && request.url().endsWith('/session'), { timeout: 5000 });
    await page.reload();
    await revoking;
    assert.equal(await page.locator('#app').isHidden(), true);
    assert.equal(await page.locator('#login-form').isHidden(), true);
    assert.equal(state.sessions.size, 1);
  } finally { release(); state.holdLogout = null; }
  await page.locator('#login-form').waitFor();
  assert.equal(state.sessions.size, 0);
  assert.deepEqual(state.sessionRequests.slice(before), [{ method: 'DELETE', sessionId: '1', csrf: 'csrf-settings-1' }]);
  assert.equal(await page.evaluate(() => sessionStorage.length), 0);
});

test('a tab retrying its old logout cannot revoke a new login of the same user in another tab', async t => {
  const { page, state, url } = await fixture(t);
  const newTab = await page.context().newPage();
  await returningBrowser(newTab);
  await newTab.goto(url);
  await newTab.locator('#login-form').waitFor();
  await login(page, url, '/');
  await beginOfflineLogout(page);
  await newTab.locator('#username').fill('test');
  await newTab.locator('#password').fill('test');
  await newTab.locator('#login').click();
  await newTab.locator('#workspace').waitFor();
  const before = state.sessionRequests.length;
  await page.reload();
  await page.locator('#login-form').waitFor();
  assert.equal(await page.locator('#app').isHidden(), true);
  assert.equal(await page.evaluate(() => sessionStorage.length), 0);
  assert.deepEqual(state.sessionRequests.slice(before), [{ method: 'DELETE', sessionId: '2', csrf: 'csrf-settings-1' }]);
  await newTab.reload();
  await newTab.locator('#workspace').waitFor();
  assert.equal(state.sessions.get('2'), 'csrf-settings-2');
});

test('settings request failure leaves the account and logout usable', async t => {
  const { page, state, url } = await fixture(t);
  await page.route('**/api/app/v1/settings', route => route.fulfill({ status: 500, json: { error: { code: 'HTTP_500' } } }));
  await login(page, url);
  await page.locator('#settings-page').getByRole('alert').waitFor();
  await page.locator('#settings-account').getByText('设置测试').waitFor();
  await page.locator('#logout').click();
  await page.locator('#login-form').waitFor();
  assert.equal(state.sessions.size, 0);
});

test('settings card tools and dialog dismissal share the secondary button colors', async t => {
  const { page, state, url } = await fixture(t);
  state.value.settings.embedding.enabled = true;
  state.index = 'ready';
  await login(page, url);
  async function assertSecondary(control) {
    await control.waitFor();
    assert.deepEqual(await control.evaluate(node => {
      const { backgroundColor, color, borderTopColor, borderTopWidth, borderRadius } = getComputedStyle(node);
      return [backgroundColor, color, borderTopColor, borderTopWidth, borderRadius];
    }), ['rgb(252, 251, 248)', 'rgb(31, 30, 28)', 'rgb(228, 225, 216)', '1px', '10px']);
  }
  await assertSecondary(page.locator('#settings-account #logout'));
  await assertSecondary(page.locator('#settings-page').getByRole('button', { name: '同步', exact: true }));
  const editTimezone = page.locator('.settings-row').filter({ hasText: '时区' }).getByRole('button', { name: '编辑' });
  await assertSecondary(editTimezone);
  await editTimezone.click();
  await assertSecondary(dialog(page).getByRole('button', { name: '关闭' }));
  await dialog(page).getByRole('button', { name: '关闭' }).click();
  await tab(page, 'search');
  await assertSecondary(page.locator('#settings-rebuild-map'));
});

test('an incomplete successful settings response keeps logout and settings recovery usable', async t => {
  const { page, state, url, errors } = await fixture(t);
  const incomplete = structuredClone(state.value);
  delete incomplete.settings.stargazing;
  state.nextSettingsRead = incomplete;
  await login(page, url);
  await page.getByRole('alert').filter({ hasText: '服务返回的设置不完整' }).waitFor({ timeout: 5000 });
  await page.locator('#settings-account').getByText('设置测试').waitFor();
  assert.equal(await page.locator('#logout').isVisible(), true);
  await page.getByRole('button', { name: '重新加载设置' }).click();
  await page.locator('#settings-panel-general #logout').click();
  await page.locator('#login-form').waitFor();
  assert.equal(state.sessions.size, 0);
  assert.deepEqual(errors, []);
});

for (const language of ['zh-CN', 'en-US']) for (const mobile of [false, true]) {
  test(`stargazing is atomic and independent of embeddings (${language}, ${mobile ? 'mobile' : 'desktop'})`, async t => {
    const { page, state, url, errors } = await fixture(t, { language, mobile });
    await login(page, url);
    await tab(page, 'search');
    const saved = language === 'zh-CN' ? '设置已保存。' : 'Settings saved.';
    const link = page.locator('nav a[href="/map"]');
    const embedding = structuredClone(state.value.settings.embedding);
    await settingsField(page, 'stargazing.enabled').uncheck();
    await confirmation(page, saved);
    assert.equal(await link.evaluate(node => node.hidden), true);
    assert.deepEqual(state.patches[0], { version: 1, settings: { stargazing: { enabled: false } }, secrets: {} });
    assert.equal(await page.locator('#settings-rebuild-map').isVisible(), false);
    await page.goto(`${url}/map`);
    await page.locator('#capability').filter({ hasText: language === 'zh-CN' ? '观星已关闭。' : 'Stargazing is disabled.' }).waitFor();
    assert.equal(state.mapReads, 0);
    await page.goto(`${url}/settings`); await tab(page, 'search');
    await settingsField(page, 'stargazing.enabled').check();
    await confirmation(page, saved);
    assert.equal(await link.evaluate(node => node.hidden), false);
    assert.deepEqual(state.value.settings.embedding, embedding);
    assert.equal(state.patches[1].version, 2);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    assert.deepEqual(errors, []);
  });
}

test('atomic saves advance versions, restore CSRF and keep daily settings in their own commits', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url);
  state.csrf = true;
  await edit(page, '时区');
  await settingsField(page, 'timezone').fill('UTC');
  await save(page); await confirmation(page, '已保存，次日生效。');
  await tab(page, 'review');
  assert.equal(await settingsField(page, 'notifications.enabled').isDisabled(), true);
  await edit(page, '每日回顾');
  await settingsField(page, 'review.count').fill('5');
  await settingsField(page, 'review.include_archived').check();
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  await edit(page, '推送时段');
  await page.getByRole('button', { name: '添加时段' }).click();
  await page.getByLabel('推送时段 2').fill('18:00');
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  assert.deepEqual(state.patches.map(({ version, settings }) => [version, settings]), [
    [1, { timezone: 'UTC' }], [2, { review: { count: 5, include_archived: true } }],
    [3, { notifications: { times: ['09:00', '18:00'] } }],
  ]);
  await tab(page, 'search');
  let release;
  state.hold = new Promise(resolve => { release = resolve; });
  const submitting = page.waitForRequest(request => request.method() === 'PATCH' && request.url().endsWith('/settings'));
  await settingsField(page, 'stargazing.enabled').uncheck();
  await settingsField(page, 'stargazing.enabled').check();
  await submitting;
  const next = page.waitForResponse(response => response.url().endsWith('/settings') &&
    response.request().method() === 'PATCH' && response.request().postDataJSON().version === 5);
  release(); state.hold = null;
  await next;
  await confirmation(page, '设置已保存。');
  assert.deepEqual(state.patches.slice(-2).map(({ version, settings }) => [version, settings.stargazing.enabled]), [[4, false], [5, true]]);
});

test('embedding tests a frozen candidate, preserves an edited draft and disables with a cleared key', async t => {
  const { page, state, url, errors } = await fixture(t, { mobile: true });
  await login(page, url); await tab(page, 'search'); await edit(page, '向量模型');
  await settingsField(page, 'embedding.enabled').check();
  await settingsField(page, 'embedding.protocol').selectOption('openai-compatible');
  await settingsField(page, 'embedding.base_url').fill('https://embedding.example/v1');
  await settingsField(page, 'embedding.model').fill('fixture-embedding');
  await operation(page, 'Embedding API Key').selectOption('replace');
  await newSecret(page, 'Embedding API Key').fill('candidate-key');
  let release;
  state.holdCheck = new Promise(resolve => { release = resolve; });
  const checked = page.waitForResponse('**/models/embedding/check');
  await save(page);
  await settingsField(page, 'embedding.model').fill('replacement-model');
  release(); state.holdCheck = null;
  await checked;
  await dialog(page).locator('button.save:enabled').waitFor();
  assert.equal(state.patches.length, 0);
  assert.equal(state.checks[0].settings.embedding.model, 'fixture-embedding');
  assert.equal(await newSecret(page, 'Embedding API Key').inputValue(), 'candidate-key');
  state.checkFail = 'EMBEDDING_REJECTED';
  await save(page);
  await dialog(page).getByRole('alert').waitFor();
  assert.equal(await dialog(page).locator('details').getAttribute('open'), '');
  assert.equal(state.patches.length, 0);
  state.checkFail = null;
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  const { path, ...candidate } = state.checks.at(-1);
  assert.match(path, /embedding\/check$/);
  assert.deepEqual(state.patches[0], candidate);
  assert.equal(candidate.settings.embedding.model, 'replacement-model');
  await page.locator('#settings-rebuild-map').click();
  await confirmation(page, '已请求重新生成星图。');
  assert.equal(state.rebuilds, 1);
  await edit(page, '向量模型');
  await operation(page, 'Embedding API Key').selectOption('clear');
  const total = state.checks.length;
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  assert.equal(state.checks.length, total);
  assert.deepEqual(state.patches.at(-1), { version: 2, settings: { embedding: { enabled: false } },
    secrets: { embedding_api_key: { action: 'clear' } } });
  assert.deepEqual(errors, []);
});

test('settings conflicts preserve secrets across reload and require confirmation to discard the dialog', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url); await tab(page, 'review'); await edit(page, 'QQ 私聊');
  await settingsField(page, 'channel.qq_app_id').fill('12345');
  await operation(page, 'QQ AppSecret').selectOption('replace');
  await newSecret(page, 'QQ AppSecret').fill('uncommitted-credential');
  state.fail = 'SETTINGS_CONFLICT';
  await save(page); await dialog(page).getByRole('alert').waitFor();
  assert.equal(await dialog(page).locator('button.save').isDisabled(), true);
  assert.equal(await newSecret(page, 'QQ AppSecret').inputValue(), 'uncommitted-credential');
  await dialog(page).getByRole('button', { name: '重新加载设置' }).click();
  assert.equal(await settingsField(page, 'channel.qq_app_id').inputValue(), '12345');
  await page.keyboard.press('Escape');
  const discard = page.getByRole('dialog', { name: '放弃未保存的修改？' });
  await discard.getByRole('button', { name: '继续编辑' }).click();
  assert.equal(await newSecret(page, 'QQ AppSecret').inputValue(), 'uncommitted-credential');
  await dialog(page).getByRole('button', { name: '关闭' }).click();
  await discard.getByRole('button', { name: '放弃修改' }).click();
  await dialog(page).waitFor({ state: 'hidden' });
  assert.equal(await page.getByLabel('QQ AppSecret新值').count(), 0);
  assert.doesNotMatch(await page.evaluate(() => JSON.stringify(localStorage)), /uncommitted-credential/);
});

test('channel checks are isolated and disabling the selected destination turns off review delivery atomically', async t => {
  const { page, state, url } = await fixture(t);
  state.value.settings.channel.kind = 'telegram';
  state.value.settings.notifications.enabled = true;
  state.value.secrets.bot_token.configured = true;
  state.value.secrets.app_secret.configured = true;
  state.value.channel_status = { telegram: { state: 'connected', detail: '', peer: '42', link: 'https://t.me/MoeriseBot?start=sample' } };
  state.deliveries.push({ id: 'delivery', status: 'failed', date: '2026-11-25', clock: '09:00', channel_kind: 'telegram' });
  await login(page, url); await tab(page, 'review');
  await page.getByRole('link', { name: '打开机器人' }).waitFor();
  await page.getByRole('button', { name: '重试', exact: true }).click();
  assert.deepEqual(state.retries, ['delivery']);
  await edit(page, 'QQ 私聊');
  await settingsField(page, 'channel.qq_app_id').fill('12345');
  await operation(page, 'QQ AppSecret').selectOption('replace');
  await newSecret(page, 'QQ AppSecret').fill('qq-credential');
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  assert.deepEqual(state.checks[0], { path: '/api/app/v1/channels/check', target: 'qq', version: 1,
    settings: { channel: { qq_app_id: '12345' } }, secrets: { qq_app_secret: { action: 'replace', value: 'qq-credential' } } });
  assert.equal(state.value.secrets.app_secret.configured, true);
  await edit(page, 'Telegram');
  await operation(page, 'Telegram Bot Token').selectOption('clear');
  await save(page); await confirmation(page, '已保存，回顾推送已关闭。');
  assert.equal(state.checks.length, 1);
  assert.deepEqual(state.patches.at(-1), { version: 2, settings: { notifications: { enabled: false } },
    secrets: { bot_token: { action: 'clear' } } });
  assert.equal(state.value.secrets.qq_app_secret.configured, true);
  assert.equal(state.value.secrets.app_secret.configured, true);
});

test('local language and privacy stay local and diagnostics can still be edited or copied offline', async t => {
  const { page, state, url } = await fixture(t, { returning: false });
  state.telemetry = true;
  await login(page, url);
  const welcome = page.getByRole('dialog', { name: '欢迎使用 Moerise' });
  assert.equal(await welcome.getByRole('switch').isChecked(), false);
  await page.keyboard.press('Escape');
  await page.locator('#settings-language').selectOption('en');
  await tabs(page).getByRole('tab', { name: 'Privacy' }).click();
  const sharing = page.getByRole('switch', { name: 'Share usage and error statistics' });
  assert.equal(await sharing.isChecked(), false);
  assert.equal(state.patches.length, 0);
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'], { origin: url });
  await page.context().setOffline(true);
  await page.getByRole('button', { name: 'Preview diagnostic summary' }).click();
  const preview = page.getByRole('dialog', { name: 'Diagnostic summary' });
  assert.doesNotMatch(await preview.locator('textarea').inputValue(), /users\/1|https?:\/\//);
  await preview.getByRole('textbox').fill('{"platform":"web"}');
  await preview.getByRole('button', { name: 'Copy diagnostic summary' }).click();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), '{"platform":"web"}');
  await preview.getByRole('button', { name: 'Close' }).click();
  await page.getByRole('button', { name: 'Copy diagnostic summary' }).click();
  assert.match(await page.evaluate(() => navigator.clipboard.readText()), /"platform"/);
  assert.equal(state.patches.length, 0);
  assert.equal(state.batches.length, 0);
});

test('leaving settings during a read discards the old response', async t => {
  const { page, state, url, errors } = await fixture(t);
  await login(page, url, '/');
  await page.locator('#empty').waitFor();
  let release;
  state.hold = new Promise(resolve => { release = resolve; });
  const reading = page.waitForRequest('**/api/app/v1/settings');
  await page.locator('#account-settings').click(); await reading;
  await page.locator('nav a[href="/"]').click(); release(); state.hold = null;
  await page.locator('#empty').waitFor();
  assert.equal(await page.locator('#settings-page').count(), 0);
  await page.locator('#account-settings').click(); await tabs(page).waitFor();
  assert.deepEqual(errors, []);
});

test('a queued push disable completes after navigating away from settings', async t => {
  const { page, state, url } = await fixture(t);
  state.value.settings.channel.kind = 'telegram';
  state.value.settings.notifications.enabled = true;
  await login(page, url); await tab(page, 'search');
  let release;
  state.hold = new Promise(resolve => { release = resolve; });
  const first = page.waitForRequest(request => request.method() === 'PATCH' && request.url().endsWith('/settings'));
  await settingsField(page, 'stargazing.enabled').uncheck(); await first;
  await tab(page, 'review');
  await settingsField(page, 'notifications.enabled').uncheck();
  await page.locator('nav a[href="/"]').click();
  await page.locator('#settings-page').waitFor({ state: 'detached' });
  assert.equal(state.patches.length, 1);
  const disabled = page.waitForResponse(response => response.url().endsWith('/settings') &&
    response.request().method() === 'PATCH' && response.request().postDataJSON().version === 2);
  release(); state.hold = null;
  assert.equal((await disabled).status(), 200);
  assert.deepEqual(state.patches[1], { version: 2, settings: { notifications: { enabled: false } }, secrets: {} });
  assert.equal(state.value.settings.notifications.enabled, false);
});

test('Feishu callback token clear preserves a replacement until App Secret clear is explicit', async t => {
  const { page, state, url } = await fixture(t);
  Object.assign(state.value.settings.channel, { kind: 'feishu-app', feishu_mode: 'callback', app_id: 'cli_configured' });
  state.value.settings.notifications.enabled = true;
  state.value.secrets.app_secret.configured = state.value.secrets.verification_token.configured = true;
  await login(page, url); await tab(page, 'review'); await edit(page, '飞书应用机器人');
  await operation(page, 'App Secret').selectOption('replace');
  await newSecret(page, 'App Secret').fill('replacement-secret');
  await operation(page, 'Verification Token').selectOption('clear');
  await save(page);
  await dialog(page).getByRole('alert').getByText(/明确选择清空 App Secret/).waitFor();
  assert.equal(await newSecret(page, 'App Secret').inputValue(), 'replacement-secret');
  assert.deepEqual(state.patches, []);
  assert.deepEqual(state.checks, []);
  await operation(page, 'App Secret').selectOption('clear');
  await save(page);
  await confirmation(page, '已保存，回顾推送已关闭。');
  assert.deepEqual(state.patches, [{ version: 1, settings: { notifications: { enabled: false } },
    secrets: { app_secret: { action: 'clear' }, verification_token: { action: 'clear' } } }]);
});

test('an in-flight model PATCH locks the draft until the saved baseline is available', async t => {
  const { page, state, url } = await fixture(t);
  state.value.settings.embedding.enabled = true;
  state.value.secrets.embedding_api_key.configured = true;
  await login(page, url); await tab(page, 'search'); await edit(page, '向量模型');
  const original = state.value.settings.embedding.model;
  await settingsField(page, 'embedding.model').fill('new-model');
  let release;
  state.hold = new Promise(resolve => { release = resolve; });
  const submitting = page.waitForRequest(request => request.method() === 'PATCH' && request.url().endsWith('/settings'));
  await save(page); await submitting;
  assert.equal(await settingsField(page, 'embedding.model').isDisabled(), true);
  release(); state.hold = null;
  await dialog(page).waitFor({ state: 'hidden' });
  await edit(page, '向量模型');
  assert.equal(await settingsField(page, 'embedding.model').inputValue(), 'new-model');
  await settingsField(page, 'embedding.model').fill(original);
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  assert.deepEqual(state.patches.map(patch => [patch.version, patch.settings.embedding.model]), [[1, 'new-model'], [2, original]]);
});

test('settings reload is single-flight and an older response cannot replace the current version', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url);
  const original = structuredClone(state.value);
  await edit(page, '时区'); await settingsField(page, 'timezone').fill('Asia/Tokyo');
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  await edit(page, '时区'); await settingsField(page, 'timezone').fill('UTC');
  state.fail = 'SETTINGS_CONFLICT';
  await save(page); await dialog(page).getByRole('alert').waitFor();
  const reload = dialog(page).getByRole('button', { name: '重新加载设置' });
  let release;
  state.nextSettingsRead = original;
  state.hold = new Promise(resolve => { release = resolve; });
  const before = state.gets;
  const request = page.waitForRequest(request => request.method() === 'GET' && request.url().endsWith('/settings'));
  await reload.evaluate(button => { button.click(); button.click(); button.click(); });
  await request;
  const response = page.waitForResponse(response => response.request().method() === 'GET' && response.url().endsWith('/settings'));
  release(); state.hold = null; await response;
  await dialog(page).getByRole('button', { name: '重新加载设置', exact: true }).and(page.locator(':enabled')).waitFor();
  assert.equal(await dialog(page).locator('button.save').isDisabled(), true);
  assert.equal(state.gets, before + 1);
  assert.equal(await settingsField(page, 'timezone').inputValue(), 'UTC');
  await reload.click(); await dialog(page).locator('button.save:enabled').waitFor();
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  assert.equal(state.patches.at(-1).version, 2);
});

test('manual refresh waits for a successful notes read and safely handles navigation and expiry', async t => {
  const { page, state, url, errors } = await fixture(t);
  await login(page, url, '/'); await page.locator('#empty').waitFor();
  state.notesFail = 503;
  const trigger = page.locator('#sync-trigger');
  await trigger.click();
  await page.locator('#list-error').waitFor();
  assert.equal(await page.locator('#sync-status').getAttribute('data-sync'), 'failure');
  await page.locator('#account-settings').click();
  const sync = page.locator('#settings-panel-general').getByRole('button', { name: '同步' });
  state.notesFail = null;
  let release;
  state.holdNotes = new Promise(resolve => { release = resolve; });
  const reading = page.waitForRequest('**/api/app/v1/notes?*');
  await sync.click(); await reading;
  await page.locator('nav a[href="/"]').click();
  await page.locator('#settings-page').waitFor({ state: 'detached' });
  release(); state.holdNotes = null;
  await page.locator('#empty').waitFor();
  await page.locator('#account-settings').click();
  state.notesFail = 401;
  await sync.click();
  await page.locator('#login-form').waitFor();
  assert.deepEqual(errors, []);
});

test('reloading settings after an Embedding origin change requires explicit confirmation before probing', async t => {
  const { page, state, url } = await fixture(t);
  Object.assign(state.value.settings.embedding, { enabled: true, base_url: 'https://first.example/v1' });
  state.value.secrets.embedding_api_key.configured = true;
  await login(page, url); await tab(page, 'search'); await edit(page, '向量模型');
  await operation(page, 'Embedding API Key').selectOption('replace');
  await newSecret(page, 'Embedding API Key').fill('draft-credential');
  await settingsField(page, 'embedding.model').fill('draft-model');
  state.value.version++;
  Object.assign(state.value.settings.embedding, { base_url: 'https://second.example:8443/v2', batch_size: 6 });
  await save(page); await dialog(page).getByRole('alert').waitFor();
  await dialog(page).getByRole('button', { name: '重新加载设置' }).click();
  const confirm = dialog(page).getByRole('button', { name: '确认向 https://second.example:8443 发送密钥' });
  await confirm.waitFor();
  assert.equal(await settingsField(page, 'embedding.base_url').inputValue(), 'https://second.example:8443/v2');
  assert.equal(await settingsField(page, 'embedding.batch_size').inputValue(), '6');
  assert.equal(await settingsField(page, 'embedding.model').inputValue(), 'draft-model');
  assert.equal(await newSecret(page, 'Embedding API Key').inputValue(), 'draft-credential');
  const checksBefore = state.checks.length;
  await save(page);
  await dialog(page).getByRole('alert').filter({ hasText: '请确认密钥的发送地址' }).waitFor();
  assert.equal(await dialog(page).getByRole('button', { name: '检查 Embedding 连接' }).count(), 0);
  assert.equal(state.checks.length, checksBefore);
  assert.deepEqual(state.patches, []);
  await confirm.click();
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  assert.equal(state.checks.length, checksBefore + 1);
  assert.deepEqual(state.checks.at(-1).settings, { embedding: { model: 'draft-model' } });
  assert.deepEqual(state.patches.at(-1).secrets, { embedding_api_key: { action: 'replace', value: 'draft-credential' } });
});

test('request timeout leaves settings reload, candidate checks and PATCH retries usable', async t => {
  const { page, state, url, errors } = await fixture(t);
  await page.clock.install();
  let release;
  state.hold = new Promise(resolve => { release = resolve; });
  const loading = page.waitForRequest('**/api/app/v1/settings');
  await login(page, url); await loading;
  await page.clock.fastForward(65_100);
  const settingsPage = page.locator('#settings-page');
  await settingsPage.getByText(/请求超时/).waitFor();
  release(); state.hold = null;
  await settingsPage.getByRole('button', { name: '重新加载设置' }).click();
  await tab(page, 'search'); await edit(page, '向量模型');
  await settingsField(page, 'embedding.enabled').check();
  await settingsField(page, 'embedding.base_url').fill('https://embedding.example/v1');
  await operation(page, 'Embedding API Key').selectOption('replace');
  await newSecret(page, 'Embedding API Key').fill('draft-credential');
  state.holdCheck = new Promise(resolve => { release = resolve; });
  const probing = page.waitForRequest('**/api/app/v1/models/embedding/check');
  await save(page); await probing;
  await page.clock.fastForward(65_100);
  await dialog(page).getByRole('alert').filter({ hasText: /请求超时/ }).waitFor();
  assert.equal(await newSecret(page, 'Embedding API Key').inputValue(), 'draft-credential');
  assert.deepEqual(state.patches, []);
  release(); state.holdCheck = null;
  state.modelVersion = 1;
  state.hold = new Promise(resolve => { release = resolve; });
  const patching = page.waitForRequest(request => request.method() === 'PATCH' && request.url().endsWith('/settings'));
  await save(page); await patching;
  await page.clock.fastForward(65_100);
  await dialog(page).getByRole('alert').filter({ hasText: /请求超时/ }).waitFor();
  assert.equal(await newSecret(page, 'Embedding API Key').inputValue(), 'draft-credential');
  release(); state.hold = null;
  await save(page);
  const reload = dialog(page).getByRole('button', { name: '重新加载设置' });
  await reload.waitFor();
  state.modelVersion = null;
  await reload.click();
  await save(page); await dialog(page).waitFor({ state: 'hidden' });
  assert.deepEqual(state.patches.map(patch => patch.version), [1, 2]);
  assert.deepEqual(errors, []);
});

test('refreshing from settings updates the filtered timeline snapshot for offline history return', async t => {
  const { page, state, url, errors } = await fixture(t);
  const note = { name: 'memos/archived', content: 'Old archived content', state: 'ARCHIVED',
    source_hash: 'old-hash', create_time: '2026-11-24T01:02:03Z', tags: ['work'], attachments: [] };
  state.notes = [note];
  await login(page, url, '/archived?tag=work');
  const card = page.locator('#notes article[data-name="memos/archived"]');
  await card.getByText('Old archived content').waitFor();
  await page.locator('#account-settings').click();
  state.notes = [{ ...note, content: 'Refreshed archived content', source_hash: 'next-hash' }];
  await page.locator('#settings-panel-general').getByRole('button', { name: '同步' }).click();
  await confirmation(page, '已从 Core 刷新笔记。');
  assert.equal(state.notesReads.at(-1).tag, 'work');
  assert.equal(state.notesReads.at(-1).state, 'ARCHIVED');
  await page.context().setOffline(true);
  await page.goBack();
  await card.getByText('Refreshed archived content').waitFor();
  assert.equal(await card.getByText('Old archived content').count(), 0);
  assert.deepEqual(errors, []);
});

test('saving a queued stargazing disable immediately removes the already open map', async t => {
  const { page, state, url, errors } = await fixture(t);
  state.mapData = { status: 'ready', algorithm: 'umap-balanced-v1', projection_revision: 1, layout_version: 'a'.repeat(64),
    layout_frame: 'b'.repeat(64), vector_generation: 'first', topics: [], links: [],
    points: [{ name: 'memos/star', source_hash: 'star-hash', x: 0, y: 0, semantic: true, tags: [],
      stellar: { seed: 0.1, role: 'ordinary', radius: 1, luminosity: 0.9 } }] };
  await login(page, url); await tab(page, 'search');
  let release;
  state.hold = new Promise(resolve => { release = resolve; });
  const pending = page.waitForRequest(request => request.method() === 'PATCH' && request.url().endsWith('/settings'));
  await settingsField(page, 'stargazing.enabled').uncheck(); await pending;
  await page.locator('nav a[href="/map"]').click();
  await page.locator('#map-field[data-rendered="true"]').waitFor();
  assert.equal(state.mapReads, 1);
  release(); state.hold = null;
  await page.locator('#capability').filter({ hasText: '观星已关闭。' }).waitFor();
  assert.equal(await page.locator('#map-field').count(), 0);
  assert.equal(await page.locator('nav a[href="/map"]').isHidden(), true);
  assert.equal(state.mapReads, 1);
  assert.deepEqual(errors, []);
});
