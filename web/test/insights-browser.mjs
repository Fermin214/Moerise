import { setUILanguage } from './browser-helpers.mjs';
import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { text as readRequestText } from 'node:stream/consumers';
import { pathToFileURL } from 'node:url';
import { mkdir } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { createWebServer } from '../server.mjs';

const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
const { [process.env.MOERISE_PLAYWRIGHT_ENGINE || 'chromium']: browserType } = runtime ? await import(pathToFileURL(runtime).href) : await import('playwright');
const chatID = '68f91ebc-bbed-4fc5-a82b-18e162e715d0';
const reply = '## 新的解释\n\n**完整回复**，保留换行。 #联想\n\n<script>unsafe</script>\n';

async function fixture(t, language = 'zh-CN') {
  const notes = [
    { name: 'memos/normal', content: '正常材料 😀 #共同 #细分', state: 'NORMAL' },
    { name: 'memos/archive', content: '归档材料 #共同 #细分', state: 'ARCHIVED' },
    { name: 'memos/other', content: '其他材料 #共同', state: 'NORMAL' },
  ].map((note, index) => ({ ...note, source_hash: String(index).repeat(64),
    create_time: '2026-10-20T08:00:00Z', tags: index < 2 ? ['共同', '细分'] : ['共同'], attachments: [] }));
  const state = { session: false, csrf: 'csrf-1', requests: [], chat: null, calls: 0, saves: 0,
    stream: 'complete', finish: null, sourceFailure: false, lastSave: null, rejectCSRF: false,
    chats: new Map(), creations: new Map(), receipts: new Map(), canceling: new Set(), streams: new Map(), cancelDelay: 0, cancels: 0, pageSize: 100 };
  let clock = 0;
  const touch = chat => { chat.updated_at = new Date(Date.UTC(2026, 9, 31, 0, 0, clock++)).toISOString(); };
  const core = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost'), path = url.pathname.replace('/api/app/v1', '');
    const raw = await readRequestText(req);
    const body = raw ? JSON.parse(raw) : undefined;
    state.requests.push({ path, query: url.searchParams, body, method: req.method });
    const json = (value, status = 200) => {
      if (!res.destroyed) { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); }
    };
    const error = (code, status = 503) => json({ error: { code, retryable: status >= 500 } }, status);
    if (path === '/session') {
      if (req.method === 'POST') {
        state.session = true; res.setHeader('Set-Cookie', 'session=insights; HttpOnly; SameSite=Strict; Path=/');
      } else if (req.method === 'DELETE') { state.session = false; res.writeHead(204); res.end(); return; }
      if (!state.session) { error('SESSION_UNAUTHORIZED', 401); return; }
      json({ instance_id: state.instance || 'local', user: { name: state.author || 'users/1', display_name: '作者' },
        csrf_token: state.csrf, timezone: 'Asia/Hong_Kong' }); return;
    }
    if (!state.session || !req.headers.cookie?.includes('session=insights')) { error('SESSION_UNAUTHORIZED', 401); return; }
    if (req.method !== 'GET') {
      if (state.rejectCSRF) { state.rejectCSRF = false; state.csrf = 'csrf-2'; error('CSRF_REJECTED', 403); return; }
      assert.equal(req.headers['x-csrf-token'], state.csrf);
    }
    if (path === '/tags') { json({ tags: ['共同', '细分'].map(tag => ({ tag, count: 2 })) }); return; }
    if (path === '/calendar') { json({ month: url.searchParams.get('month'), days: [{ date: '2026-10-20', count: 2 }] }); return; }
    if (path === '/notes') { json({ notes: notes.filter(note => note.state === 'NORMAL'), next_page_token: '' }); return; }
    if (path.startsWith('/notes/')) { json(notes.find(note => note.name === path.slice(1))); return; }
    if (path === '/chat-materials') {
      const tags = url.searchParams.getAll('tag'), any = url.searchParams.get('tag_match') === 'any';
      const candidates = notes.filter(note => !tags.length || (any ? tags.some(tag => note.tags.includes(tag)) : tags.every(tag => note.tags.includes(tag))));
      const offset = Number(url.searchParams.get('page_token') || 0);
      const items = candidates.slice(offset, offset + 1);
      json({ ...(url.searchParams.get('format') === 'compact' ? { candidates: items.map(note => ({
        name: note.name, source_hash: note.source_hash, preview: note.content.slice(0, 120), characters: [...note.content].length,
      })) } : { notes: items }), character_limit: 20000, next_page_token: offset + 1 < candidates.length ? String(offset + 1) : '' }); return;
    }
    if (path === '/chat-materials/resolve') {
      if (state.sourceFailure) { error('SOURCE_NOT_FOUND', 404); return; }
      const selected = body.names.map(name => notes.find(note => note.name === name));
      json({ notes: selected, materials: selected.map(({ name, source_hash }) => ({ name, source_hash })), limit: 20000 }); return;
    }
    if (body?.materials) {
      const changed = body.materials.filter(ref => !notes.some(note => note.name === ref.name && note.source_hash === ref.source_hash));
      if (changed.length) { json({ error: { code: 'SOURCE_CHANGED', changed_names: changed.map(ref => ref.name) } }, 409); return; }
    }
    const names = body?.names || body?.materials?.map(ref => ref.name);
    if (path === '/chats') {
      if (req.method === 'GET') {
        const all = [...state.chats.values()].sort((a, b) => b.updated_at.localeCompare(a.updated_at) || b.id.localeCompare(a.id));
        const offset = Number(url.searchParams.get('page_token') || 0), size = Math.min(state.pageSize, Number(url.searchParams.get('page_size') || 20));
        json({ chats: all.slice(offset, offset + size).map(chat => ({ ...chat,
          preview: chat.turns[0]?.kind === 'insight' ? '一键洞察' : chat.turns[0]?.user.content || chat.title,
          material_count: chat.materials.length, preview_kind: chat.turns[0]?.kind || '',
        })), next_page_token: offset + size < all.length ? String(offset + size) : '' }); return;
      }
      let chat = state.creations.get(body.request_id);
      if (!chat) {
        chat = { id: state.chats.size ? randomUUID() : chatID, title: body.title, version: 1, material_version: 1, turns: [],
          materials: names.map(name => ({ name, source_hash: notes.find(note => note.name === name).source_hash })) };
        touch(chat); chat.created_at = chat.updated_at;
        state.chats.set(chat.id, chat); state.creations.set(body.request_id, chat);
      }
      state.chat = chat; json(chat, 201); return;
    }
    const resource = /^\/chats\/([^/]+)/.exec(path), chat = resource && state.chats.get(resource[1]);
    if (resource && !chat) { error('CHAT_NOT_FOUND', 404); return; }
    const base = resource && `/chats/${resource[1]}`;
    if (path === base) {
      if (req.method === 'DELETE') {
        if (chat.version !== body.version) { error('CHAT_CONFLICT', 409); return; }
        if (chat.turns.some(turn => turn.assistant.status === 'running')) { error('CHAT_BUSY', 409); return; }
        state.chats.delete(chat.id); if (state.chat === chat) state.chat = null;
        res.writeHead(204); res.end(); return;
      }
      if (req.method === 'PATCH') {
        if (chat.version !== body.version) { error('CHAT_CONFLICT', 409); return; }
        chat.version++; chat.material_version++; chat.materials = names.map(name => ({
          name, source_hash: notes.find(note => note.name === name).source_hash,
        })); touch(chat);
      }
      json(chat); return;
    }
    if (path === `${base}/messages`) {
      let turn = chat.turns.find(turn => turn.request_id === body.request_id);
      if (!turn) {
        state.calls++;
        turn = { id: `turn-${state.calls}`, request_id: body.request_id, kind: body.kind, material_version: chat.material_version,
          user: { content: body.kind === 'insight' ? '默认 Prompt' : body.content, version: 1 },
          assistant: { content: '', version: 1, status: 'running', saved_name: '' } };
        chat.turns.push(turn); touch(chat);
      }
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      const event = (type, value) => res.write(`event: ${type}\ndata: ${JSON.stringify(value)}\n\n`);
      event('message', { type: 'message', turn: structuredClone(turn) });
      if (turn.assistant.status === 'completed') {
        event('complete', { type: 'complete', turn }); res.end(); return;
      }
      const append = text => {
        turn.assistant.content += text; turn.assistant.version++;
        event('delta', { type: 'delta', text });
      };
      state.append = append;
      if (state.stream !== 'delayed') append(reply);
      const finish = () => {
        if (turn.assistant.status !== 'running') return;
        turn.assistant.status = 'completed';
        if (!res.destroyed) { event('complete', { type: 'complete', turn }); res.end(); }
      };
      if (state.stream === 'complete') finish();
      else if (state.stream === 'eof') { turn.assistant.status = 'interrupted'; res.end(); }
      else {
        state.finish = finish;
        state.streams.set(turn.request_id, () => {
          turn.assistant.status = 'canceled'; turn.assistant.error_code = 'CHAT_CANCELED';
          if (!res.destroyed) {
            event('error', { error: { code: 'CHAT_CANCELED' }, turn }); res.end();
          }
        });
        res.on('close', () => {
          if (!res.writableEnded && turn.assistant.status === 'running' && !state.canceling.has(turn.request_id)) turn.assistant.status = 'interrupted';
        });
      }
      return;
    }
    if (path === `${base}/save-selection`) {
      if (!state.receipts.has(body.operation_key)) {
        const selected = chat.turns.flatMap(turn => ['user', 'assistant'].flatMap(role => body.messages.some(ref =>
          ref.request_id === turn.request_id && ref.role === role && turn[role].version === ref.version) ? [{ role, content: turn[role].content }] : []));
        const content = selected.length === 1 ? selected[0].content : selected.map(item =>
          `**${item.role === 'assistant' ? 'Moerise' : body.language === 'en' ? 'Me' : '我'}**\n\n${item.content}`).join('\n\n');
        state.lastSave = { name: `memos/saved-${++state.saves}`, content }; state.receipts.set(body.operation_key, state.lastSave);
      }
      json(state.receipts.get(body.operation_key), 201); return;
    }
    if (/\/messages\/[^/]+\/save$/.test(path)) {
      const turn = chat.turns.find(turn => path.includes(`/${turn.request_id}/`));
      state.saves++; state.lastSave = { version: body.version, content: turn.assistant.content };
      turn.assistant.saved_name = 'memos/saved'; json({ name: 'memos/saved' }, 201); return;
    }
    if (/\/messages\/[^/]+\/cancel$/.test(path)) {
      const turn = chat.turns.find(turn => path.includes(`/${turn.request_id}/`));
      state.cancels++; state.canceling.add(turn.request_id);
      json(turn);
      setTimeout(() => {
        if (turn.assistant.status === 'running') {
          if (state.streams.has(turn.request_id)) state.streams.get(turn.request_id)();
          else { turn.assistant.status = 'canceled'; turn.assistant.error_code = 'CHAT_CANCELED'; }
        }
      }, state.cancelDelay);
      return;
    }
    error('NOT_FOUND', 404);
  });
  core.listen(0, '127.0.0.1'); await once(core, 'listening');
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
  const url = `http://127.0.0.1:${port}`;
  const web = createWebServer({ coreOrigin: `http://127.0.0.1:${core.address().port}`, publicOrigin: url });
  web.listen(port, '127.0.0.1'); await once(web, 'listening');
  let browser;
  t.after(async () => {
    await browser?.close();
    await Promise.all([web, core].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
  });
  browser = await browserType.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  const page = await browser.newPage({ locale: language, viewport: { width: 1360, height: 960 } }), errors = [];
  await page.addInitScript(() => localStorage.setItem('moerise.privacy.v1', 'off'));
  await page.clock.setFixedTime('2026-10-31T00:00:00Z');
  page.on('pageerror', error => errors.push(error.message));
  t.after(() => assert.deepEqual(errors, []));
  return { page, state, url, notes };
}
async function login(page, url, path = '/insights') {
  await page.goto(url + path);
  await page.locator('#username').fill('author'); await page.locator('#password').fill('password');
  await page.locator('#login').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
}
const waitFor = async condition => {
  for (let i = 0; i < 150; i++) {
    if (condition()) return;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.ok(condition(), 'Expected chat activity');
};
async function loadMaterials(page, count) {
  await page.locator('.insight-choice').nth(count - 1).waitFor();
}
async function openPicker(page) {
  if (await page.locator('[data-chat="add"]').isVisible()) await page.locator('[data-chat="add"]').click();
  else await menuAction(page, '修改材料');
}
const applyMaterials = page => page.locator('.material-footer .insight-done').click();
async function saveLatest(page) {
  await menuAction(page, '保存为笔记'); await page.getByRole('dialog').getByRole('button', { name: '保存为笔记', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
}
async function pickAll(page) {
  await openPicker(page);
  const reselect = page.getByRole('dialog').getByRole('button', { name: '重新选择', exact: true });
  if (await reselect.isVisible()) await reselect.click();
  await page.getByRole('button', { name: '按标签', exact: true }).click();
  await loadMaterials(page, 3);
  const all = page.getByRole('button', { name: '全选', exact: true }); if (await all.isVisible()) await all.click();
  await applyMaterials(page);
  await page.getByRole('dialog').waitFor({ state: 'detached' });
}

test('selects all pages including archive, streams Markdown, follows up and saves exact reply', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url); await pickAll(page);
  assert.equal(state.chat.materials.length, 3);
  assert.equal(state.calls, 0);
  await page.locator('[data-chat="ask"]').fill('还有哪些可能？');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('.insight-answer strong').textContent(), '完整回复');
  assert.equal(await page.locator('.insight-answer script').count(), 0);
  await saveLatest(page);
  await page.getByText('已保存为笔记', { exact: true }).waitFor();
  assert.equal(state.lastSave.content, reply);
  assert.equal(state.saves, 1);
  await page.locator('[data-chat="ask"]').fill('再深入一点');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.calls, 2);
  const stored = await page.evaluate(() => JSON.stringify([localStorage, sessionStorage]));
  assert.doesNotMatch(stored, /完整回复|还有哪些可能/);
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('.insight-assistant').count(), 2);
  await mkdir('.local/chat-browser', { recursive: true });
  await page.screenshot({ path: '.local/chat-browser/desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.screenshot({ path: '.local/chat-browser/mobile.png', fullPage: true });
});

test('material pagination retains choices and cursor failures, and native dialogs isolate background focus', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url);
  let fail = true;
  const tokens = [];
  await page.route('**/api/app/v1/chat-materials?*', async route => {
    const token = new URL(route.request().url()).searchParams.get('page_token');
    if (token) {
      tokens.push(token);
      if (fail) { fail = false; await route.fulfill({ status: 503, json: { error: { code: 'INTERNAL', retryable: true } } }); return; }
    }
    await route.continue();
  });
  await openPicker(page);
  const dialog = page.getByRole('dialog', { name: '选择笔记', exact: true });
  await dialog.locator('.insight-choice input').first().check();
  await page.locator('#user-menu').evaluate(node => node.focus());
  assert.equal(await dialog.evaluate(node => node.contains(document.activeElement)), true);
  await dialog.locator('.insight-warning:not([hidden])').waitFor();
  assert.equal(await page.locator('.insight-choice input:checked').count(), 1);
  await dialog.locator('.insight-more').click();
  await loadMaterials(page, 3);
  assert.deepEqual(tokens, ['1', '1', '2']);
  assert.equal(await page.locator('.insight-choice input:checked').count(), 1);
  await page.keyboard.press('Escape'); await dialog.waitFor({ state: 'detached' });
  assert.equal(state.chat, null);
  assert.ok(await page.locator('[data-chat="add"]').evaluate(node => node === document.activeElement));
  await menuAction(page, '历史会话');
  const history = page.getByRole('dialog', { name: '历史会话', exact: true });
  await page.locator('#user-menu').evaluate(node => node.focus());
  assert.ok(await history.evaluate(node => node.contains(document.activeElement)));
  await page.keyboard.press('Escape'); await history.waitFor({ state: 'detached' });
});

test('date and tag selections use independent filters and closing discards the changes', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url);
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '选择日期', exact: true }).click();
  await page.locator('[data-day="2026-10-20"]').click(); await page.locator('[data-day="2026-10-21"]').click();
  await page.getByRole('button', { name: '应用日期', exact: true }).click();
  await page.getByRole('button', { name: '应用所选 3 条', exact: true }).waitFor(); await loadMaterials(page, 3);
  await page.getByRole('button', { name: '按标签', exact: true }).click();
  await page.getByRole('button', { name: '细分', exact: true }).click();
  await page.getByRole('button', { name: '共同', exact: true }).click();
  await loadMaterials(page, 3);
  await applyMaterials(page);
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  const combination = state.requests.filter(request => request.path === '/chat-materials' &&
    request.query.getAll('tag').length === 2);
  assert.ok(combination.length >= 2);
  assert.equal(combination.at(-1).query.get('tag_match'), 'any');
  assert.equal(combination.at(-1).query.get('from'), null);
  assert.equal(state.chat.materials.length, 3);
  assert.ok(state.requests.some(request => request.query.get('until') === '2026-10-22'));
  assert.ok(page.url().endsWith(`/insights?chat=${chatID}`));
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '查看已选', exact: true }).click();
  await loadMaterials(page, 3);
  await page.locator('.insight-choice input').first().click();
  await page.keyboard.press('Escape');
  assert.equal(state.chat.materials.length, 3);
});

test('removes the final material without losing history and restores history with invalid sources', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const clear = async () => {
    await openPicker(page); await page.getByRole('button', { name: '查看已选', exact: true }).click();
    await page.locator('.insight-choice input').click(); await page.getByRole('button', { name: '清空材料', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'detached' });
  };
  await clear();
  await waitFor(() => state.chat.materials.length === 0);
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  assert.equal(await page.locator('[data-chat="send"]').isDisabled(), true);
  state.chat.materials = [{ name: 'memos/normal', source_hash: '0'.repeat(64) }];
  state.sourceFailure = true;
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  assert.match(await page.locator('[data-chat="warning"]').textContent(), /删除或无法访问/);
  await clear();
  await waitFor(() => state.chat.materials.length === 0);
});

test('cross-month automatic selection respects manual removals and can stop enumeration', async t => {
  const { page, state, url, notes } = await fixture(t);
  await login(page, url);
  await page.setViewportSize({ width: 390, height: 844 });
  await openPicker(page);
  await page.getByRole('button', { name: '选择日期', exact: true }).click();
  await page.locator('[data-day="2026-10-20"]').click();
  await page.getByRole('button', { name: '上个月', exact: true }).click();
  await page.locator('[data-day="2026-09-30"]').click();
  let release;
  await page.route('**/chat-materials?*', async route => {
    const params = new URL(route.request().url()).searchParams;
    if (params.get('from') === '2026-09-30' && params.get('page_token') === '1' && !release) {
      await new Promise(resolve => { release = resolve; });
    }
    await route.continue().catch(() => {});
  });
  await page.getByRole('button', { name: '应用日期', exact: true }).click();
  await waitFor(() => !!release);
  const first = page.locator('.insight-choice input').first();
  await first.uncheck();
  assert.ok(await page.locator('.insight-done').isDisabled());
  release();
  await page.getByRole('button', { name: '应用所选 2 条', exact: true }).waitFor();
  assert.equal(await first.isChecked(), false);
  await applyMaterials(page); await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.deepEqual(state.chat.materials.map(ref => ref.name), notes.slice(1).map(note => note.name));
  await openPicker(page);
  await page.getByRole('button', { name: /2026-09-30 – 2026-10-20/ }).click();
  release = null;
  await page.getByRole('button', { name: '应用日期', exact: true }).click();
  await waitFor(() => !!release);
  await first.check();
  await page.getByRole('button', { name: '停止批量选择，改为逐条勾选', exact: true }).click();
  release();
  await applyMaterials(page); await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.equal(state.chat.materials.length, 3);
});

test('abnormal EOF retains partial reply without success or save, and explicit new question can continue', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal'); state.stream = 'eof';
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.calls, 1);
  await menuAction(page, '保存为笔记');
  assert.ok(await page.getByRole('dialog').getByRole('button', { name: '保存为笔记' }).isDisabled());
  await page.keyboard.press('Escape');
  assert.match(await page.locator('[data-chat="warning"]').textContent(), /中断/);
  state.stream = 'complete';
  await page.locator('[data-chat="ask"]').fill('继续');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.calls, 2);
});

test('navigation aborts the live stream and late output cannot return to the old route', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal'); state.stream = 'hold';
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insight-answer strong').waitFor();
  await page.locator('.side a[href="/"]').click();
  await waitFor(() => state.chat.turns[0].assistant.status === 'interrupted');
  state.finish(); await page.waitForTimeout(50);
  assert.equal(new URL(page.url()).pathname, '/');
  assert.equal(await page.locator('.insights-page').count(), 0);
});

test('CSRF renewal retries the unchanged generate request once before calling the model', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal'); state.rejectCSRF = true;
  await page.locator('[data-chat="ask"]').fill('原问题');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const posts = state.requests.filter(request => request.path.endsWith('/messages'));
  assert.equal(posts.length, 2);
  assert.deepEqual(posts[0].body, posts[1].body);
  assert.equal(state.calls, 1);
});

test('an unconfirmed generate request freezes its content and reuses the original request ID', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  const sent = [];
  await page.route('**/api/app/v1/chats/*/messages', async route => {
    sent.push(route.request().postDataJSON());
    if (sent.length === 1) await route.abort('failed'); else await route.continue();
  });
  await page.locator('[data-chat="ask"]').fill('尚未确认的原问题');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.getByText('未找到此请求的回复。', { exact: true }).waitFor();
  assert.equal(await page.locator('[data-chat="ask"]').getAttribute('readonly'), '');
  assert.ok(await page.locator('[data-chat="one"]').isHidden());
  await setUILanguage(page, 'en');
  await page.getByRole('button', { name: 'Send again', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.deepEqual(sent[0], sent[1]);
  assert.equal(sent[1].language, 'zh-CN');
  assert.equal(state.calls, 1);
});

test('a lost create response replays the frozen selection before explicitly updating the new selection', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url);
  const sent = [];
  await page.route('**/api/app/v1/chats', async route => {
    if (route.request().method() !== 'POST') { await route.continue(); return; }
    sent.push(route.request().postDataJSON());
    if (sent.length === 1) { await route.fetch(); await route.abort('failed'); }
    else await route.continue();
  });
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签', exact: true }).click();
  await page.locator('.insight-choice').first().waitFor();
  await page.locator('.insight-choice input').first().check();
  await applyMaterials(page);
  await page.locator('.insight-picker .insight-warning:not([hidden])').waitFor();
  assert.equal(state.chat.materials.length, 1);
  await loadMaterials(page, 3);
  await page.getByRole('button', { name: '全选', exact: true }).click();
  await applyMaterials(page);
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.deepEqual(sent[0], sent[1]);
  assert.equal(state.chat.materials.length, 3);
  assert.equal(state.chat.version, 2);
});

test('late material creation cannot replace the URL after navigating away', async t => {
  const { page, state, url } = await fixture(t);
  let release;
  await page.route('**/api/app/v1/chats', async route => {
    if (route.request().method() !== 'POST') { await route.continue(); return; }
    const response = await route.fetch();
    await new Promise(resolve => { release = resolve; });
    await route.fulfill({ response });
  });
  await page.goto(url + '/insights?note=memos%2Fnormal');
  await page.locator('#username').fill('author'); await page.locator('#password').fill('password');
  await page.locator('#login').click();
  await waitFor(() => !!release);
  await page.locator('.side a[href="/"]').click();
  release(); await page.waitForTimeout(100);
  assert.equal(new URL(page.url()).pathname, '/');
  assert.equal(await page.locator('.insights-page').count(), 0);
  assert.ok(state.chat);
});

test('the material count uses Unicode code points and oversize input never starts a model request', async t => {
  const { page, state, url, notes } = await fixture(t);
  notes[0].content = '😀'.repeat(20001);
  await login(page, url, '/insights?note=memos%2Fnormal');
  assert.match(await page.locator('[data-chat="material-toggle"]').textContent(), /20001 \/ 20000/);
  assert.equal(await page.locator('[data-chat="send"]').isDisabled(), true);
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), true);
  assert.equal(state.calls, 0);
  await openPicker(page); assert.ok(await page.locator('.insight-done').isDisabled());
});

test('follow-up questions do not count toward the material character limit', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  const question = '😀'.repeat(20001);
  await page.locator('[data-chat="ask"]').fill(question);
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.chat.turns[0].user.content, question);
  assert.equal(state.calls, 1);
  assert.equal(await page.locator('[data-chat="ask"]').inputValue(), '');
  assert.ok(await page.locator('[data-chat="one"]').isHidden());
});

test('recovery never adopts materials changed by another page without showing a version conflict', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  state.stream = 'eof';
  await page.route('**/api/app/v1/chats/*/messages', async route => {
    const response = await route.fetch();
    state.chat.version++;
    state.chat.materials = [{ name: 'memos/other', source_hash: '2'.repeat(64) }];
    await route.fulfill({ response });
  });
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await page.getByText('会话已在其他页面更改，请重新进入后继续。', { exact: true }).waitFor();
  assert.equal(await page.locator('[data-chat="send"]').isDisabled(), true);
  assert.ok(await page.locator('[data-chat="change"]').isDisabled());
  assert.ok(await page.locator('[data-chat="server"]').isVisible());
  assert.equal(state.calls, 1);
});

test('changed source warning takes priority over a historical interrupted reply after reload', async t => {
  const { page, state, url, notes } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  state.stream = 'eof';
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  notes[0].content = '已修改的来源'; notes[0].source_hash = '9'.repeat(64);
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await page.getByText('所选材料已修改，请重新选择这些笔记。', { exact: true }).waitFor();
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  await pickAll(page);
  assert.equal(await page.locator('[data-chat="warning"]').isHidden(), true);
});

test('polling a recovered generation preserves unresolved source warnings until reselection', async t => {
  const { page, state, url, notes } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  state.chat.turns[0].assistant.status = 'running';
  notes[0].content = '生成期间修改的来源'; notes[0].source_hash = '9'.repeat(64);
  await page.reload();
  await page.getByText('所选材料已修改，请重新选择这些笔记。', { exact: true }).waitFor();
  state.chat.turns[0].assistant.status = 'interrupted';
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('[data-chat="warning"]').textContent(), '所选材料已修改，请重新选择这些笔记。');
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  await pickAll(page);
  assert.equal(await page.locator('[data-chat="warning"]').isHidden(), true);
});

test('recovered generation retries a temporary polling failure and stops retrying after navigation', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  state.chat.turns[0].assistant.status = 'running';
  let reads = 0, failPolling = false;
  await page.route(`**/api/app/v1/chats/${state.chat.id}`, async route => {
    if (route.request().method() !== 'GET') { await route.continue(); return; }
    reads++;
    if (reads === 2 || (failPolling && reads > 1)) {
      state.chat.turns[0].assistant.status = failPolling ? 'running' : 'completed';
      await route.fulfill({ status: 503, contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'SESSION_UNAVAILABLE', retryable: true } }) });
    } else await route.continue();
  });
  await page.reload();
  await waitFor(() => reads >= 2);
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.ok(reads >= 3);
  assert.equal(await page.locator('[data-chat="send"]').textContent(), '发送');
  assert.equal(await page.locator('[data-chat="ask"]').isDisabled(), false);
  assert.equal(await page.locator('.insight-save').isDisabled(), false);
  assert.equal(await page.locator('[data-chat="warning"]').isHidden(), true);
  assert.equal(state.calls, 1);

  failPolling = true; reads = 0; state.chat.turns[0].assistant.status = 'running';
  await page.reload();
  await waitFor(() => reads >= 2);
  await page.locator('.side a[href="/"]').click();
  const before = reads;
  await page.waitForTimeout(3500);
  assert.equal(reads, before);
  assert.equal(await page.locator('.insights-page').count(), 0);
});

test('changed sources stay separate from unaffected choices and require explicit reselection', async t => {
  const { page, state, url, notes } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fother');
  notes[2].content = '新的原文'; notes[2].source_hash = '9'.repeat(64);
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: '细分', exact: true }).click();
  await loadMaterials(page, 2);
  assert.equal(await page.locator('.insight-choice input:checked').count(), 0);
  await page.getByRole('button', { name: '全选', exact: true }).click();
  await applyMaterials(page);
  await page.getByRole('button', { name: '重新选择', exact: true }).waitFor();
  assert.equal(await page.locator('.insight-choice input:checked').count(), 3);
  assert.equal(await page.locator('.insight-choice .pbtn:visible').count(), 1);
  assert.ok(await page.locator('.insight-done').isDisabled());
  await page.getByRole('button', { name: '重新选择', exact: true }).click();
  await applyMaterials(page);
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.equal(state.chat.materials.length, 3);
  assert.equal(state.chat.materials.find(ref => ref.name === notes[2].name).source_hash, notes[2].source_hash);
  assert.equal(state.calls, 0);
});

test('stream interruption retries its first failed status read without rerunning generation', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  state.stream = 'eof';
  let reads = 0;
  await page.route(`**/api/app/v1/chats/${state.chat.id}`, async route => {
    if (route.request().method() !== 'GET') { await route.continue(); return; }
    if (++reads === 1) {
      await route.fulfill({ status: 503, contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'SESSION_UNAVAILABLE', retryable: true } }) });
    } else await route.continue();
  });
  await page.locator('[data-chat="one"]').click();
  await waitFor(() => reads >= 1);
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.ok(reads >= 2);
  assert.equal(await page.locator('[data-chat="send"]').textContent(), '发送');
  assert.equal(await page.locator('[data-chat="ask"]').isDisabled(), false);
  assert.ok(await page.getByRole('button', { name: '复制正文' }).isVisible());
  assert.match(await page.locator('[data-chat="warning"]').textContent(), /中断/);
  assert.equal(state.calls, 1);
});

test('a partial group remains selectable past the character budget', async t => {
  const { page, state, url, notes } = await fixture(t);
  notes[0].content = '😀'.repeat(20001); notes[0].source_hash = '9'.repeat(64);
  await login(page, url); await openPicker(page); await loadMaterials(page, 3);
  await page.getByRole('button', { name: '全选', exact: true }).click();
  assert.ok(await page.locator('.insight-done').isDisabled());
  await page.locator('.insight-choice input').first().uncheck(); await applyMaterials(page);
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.equal(state.chat.materials.length, 2);
  assert.equal(state.calls, 0);
});

test('a timed out material update keeps temporary choices and retries in the same picker', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签', exact: true }).click();
  await loadMaterials(page, 3);
  for (const row of await page.locator('.insight-choice input').all()) await row.check();
  await page.clock.install({ time: new Date('2026-10-31T00:00:00Z') });
  let patches = 0, release;
  await page.route('**/api/app/v1/chats/*', async route => {
    if (route.request().method() === 'PATCH' && ++patches === 1) {
      await new Promise(resolve => { release = resolve; });
      try { await route.continue(); } catch {}
    } else await route.continue();
  });
  await applyMaterials(page);
  await waitFor(() => patches === 1);
  await page.clock.fastForward(65001);
  await page.locator('.insight-picker .insight-warning:not([hidden])').waitFor();
  assert.equal(state.chat.version, 1);
  assert.equal(await page.locator('.insight-choice input:checked').count(), 3);
  release();
  await applyMaterials(page);
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.ok(patches >= 2);
  assert.equal(state.chat.materials.length, 3);
  assert.ok(state.chat.version >= 2);
});

test('failed material update retains the selection and a late retry cannot update a departed chat', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('.side a[href="/"]').click();
  await page.goBack();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const before = structuredClone(state.chat), count = await page.locator('[data-chat="material-toggle"]').textContent();
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.route('**/api/app/v1/chats/*', async route => {
    if (route.request().method() === 'PATCH' && state.sourceFailure) {
      await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'SOURCE_NOT_FOUND' } }) });
    } else await route.continue();
  });
  state.sourceFailure = true;
  await applyMaterials(page);
  await page.locator('.insight-picker .insight-warning:not([hidden])').waitFor();
  assert.deepEqual(state.chat, before);
  assert.equal(await page.locator('[data-chat="material-toggle"]').textContent(), count);

  state.sourceFailure = false;
  let release;
  await page.unroute('**/api/app/v1/chats/*');
  await page.route('**/api/app/v1/chats/*', async route => {
    if (route.request().method() === 'PATCH') {
      await new Promise(resolve => { release = resolve; });
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ...before, version: 2, material_version: 2 }) }).catch(() => {});
    } else await route.continue();
  });
  await applyMaterials(page);
  await waitFor(() => !!release);
  await page.goForward();
  release(); await page.waitForTimeout(100);
  assert.deepEqual(state.chat, before);
  assert.equal(new URL(page.url()).pathname, '/');
  assert.equal(await page.locator('.insights-page').count(), 0);
});

async function menuAction(page, name) {
  await page.locator('[data-chat="menu"]').click();
  await page.getByRole('menuitem', { name, exact: true }).click();
}

test('new drafts stay empty across reload and paged history restores the chosen conversation without generation', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  assert.equal((await page.locator('.insight-heading').innerText()).trim(), '洞察');
  await page.locator('[data-chat="ask"]').fill('最初的问题');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const first = state.chat.id, creates = state.requests.filter(request => request.path === '/chats' && request.method === 'POST').length;
  await menuAction(page, '新建会话');
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('.insight-assistant').count(), 0);
  assert.match(await page.locator('[data-chat="material-toggle"]').textContent(), /0 条/);
  assert.equal(state.requests.filter(request => request.path === '/chats' && request.method === 'POST').length, creates);
  await pickAll(page);
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.notEqual(state.chat.id, first);
  assert.equal(state.chats.size, 2);
  state.pageSize = 1;
  await menuAction(page, '历史会话');
  const dialog = page.getByRole('dialog', { name: '历史会话', exact: true });
  await dialog.locator('.insight-history-row').nth(1).waitFor();
  await mkdir('.local/chat-browser', { recursive: true });
  await page.screenshot({ path: '.local/chat-browser/history-desktop.png', fullPage: true });
  await dialog.getByRole('button', { name: /最初的问题/ }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(new URL(page.url()).searchParams.get('chat'), first);
  assert.equal(await page.locator('.insight-user:visible').textContent(), '最初的问题');
  assert.equal(state.calls, 2);
  await page.setViewportSize({ width: 390, height: 844 });
  await menuAction(page, '历史会话');
  await dialog.locator('.insight-history-row').first().waitFor();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.screenshot({ path: '.local/chat-browser/history-mobile.png', fullPage: true });
  await page.keyboard.press('Escape');
  await dialog.waitFor({ state: 'detached' });
});

test('the send button stops a live reply, waits for canceled state and permits an explicit followup', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  state.stream = 'hold'; state.cancelDelay = 200;
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insight-answer strong').waitFor();
  await page.getByRole('button', { name: '停止', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.chat.turns[0].assistant.status, 'canceled');
  assert.equal(state.chat.turns[0].assistant.content, reply);
  assert.equal(state.cancels, 1);
  await menuAction(page, '保存为笔记');
  const dialog = page.getByRole('dialog'); assert.ok(await dialog.getByRole('button', { name: '保存为笔记' }).isDisabled());
  await dialog.getByLabel(/已停止（内容不完整）/).check();
  await dialog.getByRole('button', { name: '保存为笔记' }).click();
  await dialog.getByRole('button', { name: '确认保存' }).click(); await dialog.waitFor({ state: 'detached' });
  assert.equal(state.lastSave.content, reply);
  assert.equal(state.requests.find(req => req.path.endsWith('/save-selection')).body.confirm_incomplete, true);
  await page.getByText('已保存为笔记', { exact: true }).waitFor();
  state.stream = 'complete';
  await page.locator('[data-chat="ask"]').fill('停止后继续');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.calls, 2);
});

test('saving nonadjacent answers and a question preserves order and enforces the UTF-8 budget', async t => {
  const { page, state, url } = await fixture(t, 'en-US');
  await login(page, url, '/insights?note=memos%2Fnormal');
  const ask = page.locator('[data-chat="ask"]');
  for (const text of ['Beginning', 'Middle', 'End']) {
    await ask.fill(text); await ask.press('Enter');
    await page.locator('.insights-page[aria-busy="false"]').waitFor();
  }
  const [first, middle, last] = state.chat.turns;
  first.assistant.content = `First ${reply}`; last.assistant.content = `Last ${reply}`;
  middle.assistant.content = '😀'.repeat(16385);
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await menuAction(page, 'Save as note');
  const dialog = page.getByRole('dialog'), rows = dialog.locator('.save-choice input');
  await rows.nth(3).check();
  assert.ok(await dialog.getByRole('button', { name: 'Save as note', exact: true }).isDisabled());
  await rows.nth(3).uncheck(); await rows.nth(2).check(); await rows.nth(1).check();
  const content = `**Moerise**\n\n${first.assistant.content}\n\n**Me**\n\nMiddle\n\n**Moerise**\n\n${last.assistant.content}`;
  assert.ok((await dialog.locator('.material-footer').textContent()).includes(String(Buffer.byteLength(content))));
  await dialog.getByRole('button', { name: 'Save as note', exact: true }).click();
  await dialog.waitFor({ state: 'detached' });
  assert.equal(state.lastSave.content, content);
  await menuAction(page, 'Save as note');
  await rows.nth(5).uncheck(); await rows.first().check();
  await dialog.getByRole('button', { name: 'Save as note', exact: true }).click();
  await dialog.waitFor({ state: 'detached' });
  assert.equal(state.lastSave.content, 'Beginning');
});

test('a slow first delta replaces the placeholder without moving the reader or input focus', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  state.chat.turns[0].assistant.content = Array.from({ length: 100 }, (_, index) => `阅读段落 ${index}`).join('\n\n');
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const ask = page.locator('[data-chat="ask"]'), list = page.locator('[data-chat="messages"]');
  state.stream = 'delayed';
  await ask.fill('稍后回复'); await ask.press('Enter');
  await waitFor(() => state.calls === 2);
  await page.locator('.insight-assistant').last().getByText('正在洞察……', { exact: true }).waitFor();
  await list.evaluate(node => { node.scrollTop = 160; });
  await page.getByRole('button', { name: '回到最新', exact: true }).waitFor();
  const anchor = page.locator('.insight-answer p').nth(5), offset = await anchor.evaluate(node => node.getBoundingClientRect().top);
  state.append(reply);
  await page.locator('.insight-answer strong').last().waitFor({ state: 'attached' });
  assert.equal(await ask.evaluate(node => document.activeElement === node), true);
  assert.equal(await anchor.evaluate(node => node.getBoundingClientRect().top), offset);
  await page.getByRole('button', { name: '回到最新', exact: true }).click();
  assert.ok(await list.evaluate(node => node.scrollHeight - node.scrollTop - node.clientHeight < 28));
  state.finish(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
});

test('an unconfirmed selection survives refresh and re-login only for its original instance, author and chat', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="ask"]').fill('最初的问题'); await page.locator('[data-chat="ask"]').press('Enter');
  const ready = () => page.locator('.insights-page[aria-busy="false"]').waitFor();
  const dialog = page.getByRole('dialog'), rows = dialog.getByRole('checkbox');
  await ready();
  const originalURL = page.url();
  const posts = () => state.requests.filter(req => req.path.endsWith('/save-selection'));
  const storedBodies = () => page.evaluate(() => Object.keys(sessionStorage)
    .filter(key => key.startsWith('moerise.chat-save.v1:')).map(key => JSON.parse(sessionStorage.getItem(key))));
  await page.route('**/save-selection', async route => {
    assert.deepEqual(await storedBodies(), [route.request().postDataJSON()]);
    await route.fetch(); await route.abort('failed');
  }, { times: 1 });
  await menuAction(page, '保存为笔记');
  await rows.first().check();
  await dialog.getByRole('button', { name: '保存为笔记' }).click();
  await dialog.getByRole('button', { name: '重试保存' }).waitFor();
  const frozen = posts()[0].body;
  assert.equal(frozen.messages.length, 2);
  assert.doesNotMatch(JSON.stringify(frozen), /最初的问题|完整回复/);
  assert.equal(state.saves, 1);

  const second = { ...structuredClone(state.chat), id: randomUUID() };
  state.chats.set(second.id, second);
  await page.goto(`${url}/insights?chat=${second.id}`);
  async function assertFreshSelection() {
    await ready(); await menuAction(page, '保存为笔记');
    assert.ok(await dialog.getByRole('button', { name: '保存为笔记' }).isEnabled());
    assert.ok(await rows.first().isEnabled());
    assert.equal(await rows.first().isChecked(), false);
    assert.deepEqual(await storedBodies(), [frozen]);
    assert.equal(posts().length, 1);
  }
  await assertFreshSelection();
  state.author = 'users/2';
  await page.goto(originalURL); await assertFreshSelection();
  state.author = 'users/1'; state.instance = 'second';
  await page.reload(); await assertFreshSelection();
  state.instance = 'local'; state.session = false;
  await login(page, url, originalURL.slice(url.length));
  await menuAction(page, '保存为笔记');
  assert.equal(await dialog.locator('input:checked:disabled').count(), 2);
  assert.equal(posts().length, 1);
  await dialog.getByRole('button', { name: '重试保存' }).click();
  await dialog.waitFor({ state: 'detached' });
  assert.deepEqual(posts().map(({ path, body }) => [path, body]), Array(2).fill([`/chats/${chatID}/save-selection`, frozen]));
  assert.deepEqual(await storedBodies(), []);
  assert.equal(state.saves, 1);
  assert.equal(state.lastSave.content, `**我**\n\n最初的问题\n\n**Moerise**\n\n${reply}`);
});

test('deleting a live conversation confirms and cancels while the saved reply survives', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await menuAction(page, '删除对话');
  assert.ok(await page.getByRole('button', { name: '取消', exact: true }).evaluate(node => node === document.activeElement));
  await page.keyboard.press('Escape');
  assert.equal(state.requests.filter(request => request.method === 'DELETE' && request.path.startsWith('/chats/')).length, 0);
  await saveLatest(page);
  state.stream = 'hold'; state.cancelDelay = 100;
  await page.locator('[data-chat="ask"]').fill('下一问'); await page.locator('[data-chat="ask"]').press('Enter');
  await page.locator('.insight-answer strong').nth(1).waitFor();
  await menuAction(page, '删除对话');
  await page.getByRole('dialog').getByRole('button', { name: '删除对话', exact: true }).click();
  await page.waitForURL('**/insights?new=1');
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.chats.size, 0);
  assert.equal(state.saves, 1); assert.equal(state.lastSave.content, reply);
  assert.equal(await page.locator('.insight-assistant').count(), 0);
});

test('a lost delete response is resolved by reading server state instead of restoring the deleted chat', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.route('**/api/app/v1/chats/*', async route => {
    if (route.request().method() !== 'DELETE') { await route.continue(); return; }
    await route.fetch(); await route.abort('failed');
  });
  await menuAction(page, '删除对话');
  await page.getByRole('dialog').getByRole('button', { name: '删除对话', exact: true }).click();
  await page.waitForURL('**/insights?new=1');
  assert.equal(state.chats.size, 0);
  assert.equal(state.requests.filter(request => request.method === 'DELETE' && request.path.startsWith('/chats/')).length, 1);
});

test('a failed delete retains the conversation for retry and stale versions require a fresh confirmation', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  state.chat.version++;
  await menuAction(page, '删除对话');
  await page.getByRole('dialog').getByRole('button', { name: '删除对话', exact: true }).click();
  await page.getByRole('dialog').getByText('会话已在其他页面更改，请重新进入后继续。', { exact: true }).waitFor();
  assert.equal(state.requests.filter(request => request.method === 'DELETE').length, 0);
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const reject = async route => {
    if (route.request().method() !== 'DELETE') { await route.continue(); return; }
    await route.fulfill({ status: 503, json: { error: { code: 'UPSTREAM_UNAVAILABLE', retryable: true } } });
  };
  await page.route('**/api/app/v1/chats/*', reject);
  await menuAction(page, '删除对话');
  const remove = page.getByRole('dialog').getByRole('button', { name: '删除对话', exact: true });
  await remove.click();
  await page.locator('[data-chat="warning"]:not([hidden])').waitFor();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.chats.size, 1);
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  await page.unroute('**/api/app/v1/chats/*', reject);
  await remove.click(); await page.waitForURL('**/insights?new=1');
  assert.equal(state.chats.size, 0);
});

test('late history and delete responses cannot reopen a private page after navigation', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  let release;
  const holdHistory = async route => {
    const response = await route.fetch(); await new Promise(resolve => { release = resolve; }); await route.fulfill({ response });
  };
  await page.route('**/api/app/v1/chats?*', holdHistory);
  await menuAction(page, '历史会话'); await waitFor(() => !!release);
  await page.keyboard.press('Escape');
  await page.locator('.side a[href="/"]').click();
  release(); await page.waitForTimeout(50);
  assert.equal(new URL(page.url()).pathname, '/'); assert.equal(await page.getByRole('dialog').count(), 0);
  await page.unroute('**/api/app/v1/chats?*', holdHistory);
  await page.goto(`${url}/insights?chat=${state.chat.id}`);
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  release = null;
  await page.route('**/api/app/v1/chats/*', async route => {
    if (route.request().method() !== 'DELETE') { await route.continue(); return; }
    await route.fetch(); await new Promise(resolve => { release = resolve; }); await route.abort('failed');
  });
  await menuAction(page, '删除对话');
  await page.getByRole('dialog').getByRole('button', { name: '删除对话', exact: true }).click();
  await waitFor(() => !!release);
  await page.goto(url + '/');
  release(); await page.waitForTimeout(100);
  assert.equal(new URL(page.url()).pathname, '/'); assert.equal(await page.locator('.insights-page').count(), 0);
});

test('a lost cancel response reads the durable terminal state without starting another generation', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  state.stream = 'hold';
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insight-answer strong').waitFor();
  await page.route('**/messages/*/cancel', async route => { await route.fetch(); await route.abort('failed'); });
  await page.getByRole('button', { name: '停止', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await page.getByText('已停止生成，已保留收到的内容。', { exact: true }).waitFor();
  assert.equal(state.chat.turns[0].assistant.status, 'canceled');
  assert.equal(state.calls, 1);
});

test('history page failures retain loaded rows and retry the unchanged cursor', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  const older = structuredClone(state.chat); older.id = randomUUID(); older.updated_at = '2026-10-01T00:00:00Z';
  state.chats.set(older.id, older); state.pageSize = 1;
  let fail = true;
  const tokens = [];
  await page.route('**/api/app/v1/chats?*', async route => {
    const token = new URL(route.request().url()).searchParams.get('page_token');
    if (token) {
      tokens.push(token);
      if (fail) {
        fail = false; await route.fulfill({ status: 503, json: { error: { code: 'INTERNAL', retryable: true } } }); return;
      }
    }
    await route.continue();
  });
  await menuAction(page, '历史会话');
  const dialog = page.getByRole('dialog', { name: '历史会话', exact: true });
  await dialog.locator('.insight-history-row').waitFor();
  await dialog.locator('.insight-warning:not([hidden])').waitFor();
  assert.equal(await dialog.locator('.insight-history-row').count(), 1);
  await dialog.getByRole('button', { name: '重试', exact: true }).click();
  await dialog.locator('.insight-history-row').nth(1).waitFor();
  assert.deepEqual(tokens, ['1', '1']);
  assert.equal(state.calls, 0);
});

test('English chat keeps its running stream and material picker through language switches', async t => {
  const { page, state, url, notes } = await fixture(t, 'en-US');
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.getByRole('button', { name: 'Add notes', exact: true }).click();
  await page.getByRole('dialog', { name: 'Select notes', exact: true }).waitFor();
  await page.getByRole('button', { name: 'By tag', exact: true }).click();
  await loadMaterials(page, 3);
  await page.getByRole('button', { name: 'Select all', exact: true }).click();
  await setUILanguage(page, 'zh-CN');
  await page.getByRole('dialog', { name: '选择笔记', exact: true }).waitFor();
  assert.equal(await page.locator('.insight-choice input:checked').count(), 3);
  await setUILanguage(page, 'en');
  await page.getByRole('button', { name: 'Apply selection (3)', exact: true }).click();
  state.stream = 'hold';
  await page.getByRole('button', { name: 'Generate insight', exact: true }).first().click();
  await page.getByRole('button', { name: 'Stop', exact: true }).waitFor();
  await page.locator('.insight-answer strong').waitFor();
  const original = state.requests.find(request => request.path.endsWith('/messages')).body;
  assert.equal(original.language, 'en');
  await setUILanguage(page, 'zh-CN');
  await page.getByRole('button', { name: '停止', exact: true }).waitFor();
  assert.equal(state.chat.turns[0].assistant.status, 'running');
  state.finish(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await setUILanguage(page, 'en');
  await menuAction(page, 'Save as note'); await page.getByRole('dialog').getByRole('button', { name: 'Save as note', exact: true }).click();
  await page.getByText('Saved as note', { exact: true }).waitFor();
  assert.equal(state.lastSave.content, reply); assert.equal(state.calls, 1);
  assert.equal(state.chat.materials.length, notes.length);
  await page.locator('[data-chat="menu"]').click();
  await page.getByRole('menuitem', { name: 'Chat history', exact: true }).click();
  await page.getByRole('dialog', { name: 'Chat history', exact: true }).waitFor();
  await page.locator('.insight-history-row', { hasText: 'Generate insight' }).waitFor();
  await page.getByRole('button', { name: 'Close', exact: true }).click();
  await mkdir('.local/i18n-review', { recursive: true });
  await page.screenshot({ path: '.local/i18n-review/chat-en.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.screenshot({ path: '.local/i18n-review/chat-en-mobile.png', fullPage: true });
  const menu = page.getByRole('button', { name: 'More', exact: true });
  assert.equal((await menu.innerText()).trim(), '');
  assert.ok((await menu.boundingBox()).width >= 44);
  await menu.focus(); await page.keyboard.press('Enter');
  await page.getByRole('menuitem', { name: 'Chat history', exact: true }).waitFor();
  await page.keyboard.press('Escape');
  assert.equal(await menu.getAttribute('aria-expanded'), 'false');
  assert.ok(await menu.evaluate(node => node === document.activeElement));
});
test('material filters ignore a late previous response and show the API character limit', async t => {
  const { page, url, notes } = await fixture(t);
  await login(page, url);
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签', exact: true }).click();
  await page.locator('.insight-choice input').first().check();
  let release, arrived;
  const held = new Promise(resolve => { release = resolve; });
  const started = new Promise(resolve => { arrived = resolve; });
  await page.route('**/api/app/v1/chat-materials?*', async route => {
    const tags = new URL(route.request().url()).searchParams.getAll('tag');
    if (tags.includes('细分') && !tags.includes('共同')) {
      arrived(); await held;
      await route.fulfill({ json: { notes: [notes[1]], character_limit: 333, next_page_token: '' } }).catch(() => {});
    } else if (tags.includes('共同')) {
      await route.fulfill({ json: { notes: [notes[2]], character_limit: 777, next_page_token: '' } });
    } else await route.continue();
  });
  await page.getByRole('button', { name: '细分', exact: true }).click();
  await started;
  await page.getByRole('button', { name: '共同', exact: true }).click();
  await page.locator('.insight-choice').filter({ hasText: '其他材料' }).waitFor();
  release();
  await page.waitForTimeout(100);
  assert.equal(await page.locator('.insight-choice').count(), 1);
  assert.match(await page.locator('.insight-choice').textContent(), /其他材料/);
  assert.match(await page.locator('.material-footer').textContent(), /已选 1 条.*777/);
  await page.locator('.insight-choice input').check();
  assert.match(await page.locator('.material-footer').textContent(), /已选 2 条.*777/);
});

test('stream rendering preserves completed blocks and safely finishes a long fenced tail', async t => {
  const { page, url } = await fixture(t);
  await login(page, url);
  const result = await page.evaluate(async () => {
    const script = document.querySelector('script[type="module"]').src;
    const { createStreamRenderer } = await import(script.replace(/\/app(?=\.|$)/, '/stream-markdown'));
    const container = document.createElement('div'), render = createStreamRenderer(container);
    const prefix = '## 标题\n\n**已完成段落**\n\n';
    render.render(prefix, true);
    const first = container.querySelector('h2');
    const source = prefix + '```html\n<img src=x onerror=alert(1)>\n' + '长'.repeat(5000);
    render.render(source, true);
    const stable = container.querySelector('h2') === first, rawTail = container.lastChild.textContent;
    render.render(source + '\n```\n', false);
    return { stable, rawTail, code: container.querySelector('pre code')?.textContent,
      unsafe: container.querySelectorAll('img,script,[onerror]').length,
      headings: container.querySelectorAll('h2').length, paragraph: container.querySelector('strong')?.textContent };
  });
  assert.equal(result.stable, true);
  assert.ok(result.rawTail.startsWith('```html\n<img'));
  assert.ok(result.code.startsWith('<img src=x onerror=alert(1)>'));
  assert.equal(result.unsafe, 0);
  assert.equal(result.headings, 1);
  assert.equal(result.paragraph, '已完成段落');
});
