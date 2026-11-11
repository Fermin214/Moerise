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
const { chromium } = runtime ? await import(pathToFileURL(runtime).href) : await import('playwright');
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
    chats: new Map(), creations: new Map(), canceling: new Set(), streams: new Map(), cancelDelay: 0, cancels: 0, pageSize: 100 };
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
      json({ user: { name: 'users/1', display_name: '作者' }, csrf_token: state.csrf, timezone: 'Asia/Hong_Kong' }); return;
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
      const candidates = notes.filter(note => url.searchParams.getAll('tag').every(tag => note.tags.includes(tag)));
      const offset = Number(url.searchParams.get('page_token') || 0);
      json({ notes: candidates.slice(offset, offset + 1), next_page_token: offset + 1 < candidates.length ? String(offset + 1) : '' }); return;
    }
    if (path === '/chat-materials/resolve') {
      if (state.sourceFailure) { error('SOURCE_NOT_FOUND', 404); return; }
      const selected = body.names.map(name => notes.find(note => note.name === name));
      json({ notes: selected, materials: selected.map(({ name, source_hash }) => ({ name, source_hash })), limit: 20000 }); return;
    }
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
        chat = { id: state.chats.size ? randomUUID() : chatID, title: body.title, version: 1, turns: [],
          materials: body.names.map(name => ({ name, source_hash: notes.find(note => note.name === name).source_hash })) };
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
        chat.version++; chat.materials = body.names.map(name => ({
          name, source_hash: notes.find(note => note.name === name).source_hash,
        })); touch(chat);
      }
      json(chat); return;
    }
    if (path === `${base}/messages`) {
      let turn = chat.turns.find(turn => turn.request_id === body.request_id);
      if (!turn) {
        state.calls++;
        turn = { id: `turn-${state.calls}`, request_id: body.request_id, kind: body.kind,
          user: { content: body.kind === 'insight' ? '默认 Prompt' : body.content },
          assistant: { content: '', version: 1, status: 'running', saved_name: '' } };
        chat.turns.push(turn); touch(chat);
      }
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      const event = (type, value) => res.write(`event: ${type}\ndata: ${JSON.stringify(value)}\n\n`);
      event('message', { type: 'message', turn: structuredClone(turn) });
      if (turn.assistant.status === 'completed') {
        event('complete', { type: 'complete', turn }); res.end(); return;
      }
      turn.assistant.content = reply; turn.assistant.version++;
      event('delta', { type: 'delta', text: reply });
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
  browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  const page = await browser.newPage({ locale: language, viewport: { width: 1360, height: 960 } }), errors = [];
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
async function pickAll(page) {
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
  await page.getByRole('button', { name: '全选这 3 条', exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
}

test('selects all pages including archive, streams Markdown, follows up and saves exact reply', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url); await pickAll(page);
  assert.equal(state.chat.materials.length, 3);
  assert.equal(state.calls, 0);
  await page.locator('[data-chat="ask"]').fill('还有哪些可能？');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.getByRole('button', { name: '保存为笔记', exact: true }).waitFor({ state: 'visible' });
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('.insight-answer strong').textContent(), '完整回复');
  assert.equal(await page.locator('.insight-answer script').count(), 0);
  await page.getByRole('button', { name: '保存为笔记', exact: true }).click();
  await page.getByRole('button', { name: '已保存为笔记', exact: true }).waitFor();
  assert.equal(state.lastSave.content, reply);
  assert.equal(state.saves, 1);
  await page.locator('[data-chat="ask"]').fill('再深入一点');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.calls, 2);
  const stored = await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length }));
  assert.deepEqual(stored, { local: 0, session: 0 });
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('.insight-assistant').count(), 2);
  await mkdir('.local/chat-browser', { recursive: true });
  await page.screenshot({ path: '.local/chat-browser/desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.screenshot({ path: '.local/chat-browser/mobile.png', fullPage: true });
});

test('combines date and exact tags, cancels temporary selection and preserves detail return', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url);
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '2026-10-20', exact: true }).click();
  await page.getByRole('button', { name: '2026-10-21', exact: true }).click();
  await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
  await page.getByRole('button', { name: '细分', exact: true }).click();
  await page.getByRole('button', { name: '共同', exact: true }).click();
  await page.getByRole('button', { name: '全选这 2 条', exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  const combination = state.requests.filter(request => request.path === '/chat-materials' &&
    request.query.getAll('tag').length === 2);
  assert.ok(combination.length >= 2);
  assert.equal(combination.at(-1).query.get('from'), '2026-10-20');
  assert.equal(combination.at(-1).query.get('until'), '2026-10-22');
  assert.equal(state.chat.materials.length, 2);
  await page.locator('.insight-material .chip').first().click();
  await page.getByRole('button', { name: '‹ 返回AI 洞察', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.ok(page.url().endsWith(`/insights?chat=${chatID}`));
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
  await page.getByRole('button', { name: '全选这 3 条', exact: true }).click();
  await page.keyboard.press('Escape');
  assert.equal(state.chat.materials.length, 2);
});

test('removes the final material without losing history and restores history with invalid sources', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await page.getByRole('button', { name: '移除这条笔记', exact: true }).click();
  await waitFor(() => state.chat.materials.length === 0);
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  assert.equal(await page.locator('[data-chat="send"]').isDisabled(), true);
  state.chat.materials = [{ name: 'memos/normal', source_hash: '0'.repeat(64) }];
  state.sourceFailure = true;
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  assert.match(await page.locator('[data-chat="warning"]').textContent(), /删除或无法访问/);
  await page.getByRole('button', { name: '移除这条笔记', exact: true }).click();
  await waitFor(() => state.chat.materials.length === 0);
});

test('abnormal EOF retains partial reply without success or save, and explicit new question can continue', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal'); state.stream = 'eof';
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.calls, 1);
  assert.equal(await page.getByRole('button', { name: '保存为笔记', exact: true }).isDisabled(), true);
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
  await page.getByText('请求结果尚未确认，请使用原内容重试。', { exact: true }).waitFor();
  assert.equal(await page.locator('[data-chat="ask"]').getAttribute('readonly'), '');
  assert.equal(await page.getByRole('button', { name: '添加笔记', exact: true }).isDisabled(), true);
  await page.locator('#app [data-language]').selectOption('en');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
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
  await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
  await page.locator('.insight-choice').first().waitFor();
  await page.locator('.insight-choice input').first().check();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.locator('.insight-picker > .insight-warning:not([hidden])').waitFor();
  assert.equal(state.chat.materials.length, 1);
  await page.getByRole('button', { name: '全选这 3 条', exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
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
  await page.getByText('超过字数上限，请减少所选笔记。', { exact: true }).waitFor();
});

test('follow-up questions do not count toward the material character limit', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  const question = '😀'.repeat(20001);
  await page.locator('[data-chat="ask"]').fill(question);
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.getByRole('button', { name: '保存为笔记', exact: true }).waitFor();
  assert.equal(state.chat.turns[0].user.content, question);
  assert.equal(state.calls, 1);
  assert.equal(await page.locator('[data-chat="ask"]').inputValue(), '');
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), false);
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
  assert.equal(await page.getByRole('button', { name: '添加笔记', exact: true }).isDisabled(), true);
  assert.match(await page.locator('.insight-material .chip').textContent(), /正常材料/);
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

test('confirming existing choices refreshes all materials including those outside the active filter', async t => {
  const { page, state, url, notes } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fother');
  notes[2].content = '😀'.repeat(20001); notes[2].source_hash = '9'.repeat(64);
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: '细分', exact: true }).click();
  await page.getByRole('button', { name: '全选这 2 条', exact: true }).waitFor();
  assert.equal(await page.locator('.insight-choice input:checked').count(), 0);
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.equal(state.chat.materials.length, 1);
  assert.equal(state.chat.materials[0].source_hash, notes[2].source_hash);
  assert.match(await page.locator('[data-chat="material-toggle"]').textContent(), /20001 \/ 20000/);
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), true);

  notes[2].content = '更新后的正文'; notes[2].source_hash = '8'.repeat(64);
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.match(await page.locator('.insight-material .chip').textContent(), /更新后的正文/);
  assert.match(await page.locator('[data-chat="material-toggle"]').textContent(), /6 \/ 20000/);
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), false);

  await page.route(`**/api/app/v1/chats/${state.chat.id}`, async route => {
    if (route.request().method() === 'PATCH') {
      notes[2].content = '确认期间再次变化'; notes[2].source_hash = '7'.repeat(64);
    }
    await route.continue();
  }, { times: 1 });
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  await page.getByText('所选材料已修改，请重新选择这些笔记。', { exact: true }).waitFor();
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), true);
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.match(await page.locator('.insight-material .chip').textContent(), /确认期间再次变化/);
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), false);
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
  assert.equal(await page.locator('.insight-save').isDisabled(), true);
  assert.match(await page.locator('[data-chat="warning"]').textContent(), /中断/);
  assert.equal(state.calls, 1);
});

test('removing a material refreshes the remaining sources and their size limit', async t => {
  const { page, state, url, notes } = await fixture(t);
  await login(page, url); await pickAll(page);
  notes[0].content = '😀'.repeat(20001); notes[0].source_hash = '9'.repeat(64);
  await page.locator('.insight-material').filter({ hasText: '归档材料' })
    .getByRole('button', { name: '移除这条笔记', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.chat.materials.length, 2);
  assert.match(await page.locator('[data-chat="material-toggle"]').textContent(),
    new RegExp(`${20001 + [...notes[2].content].length} / 20000`));
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), true);
  await page.getByText('超过字数上限，请减少所选笔记。', { exact: true }).waitFor();

  notes[0].content = '更新正文'; notes[0].source_hash = '8'.repeat(64);
  await page.locator('.insight-material').filter({ hasText: '其他材料' })
    .getByRole('button', { name: '移除这条笔记', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.match(await page.locator('[data-chat="material-toggle"]').textContent(), /1 条 · 4 \/ 20000/);
  assert.match(await page.locator('.insight-material .chip').textContent(), /更新正文/);
  assert.equal(await page.locator('[data-chat="one"]').isDisabled(), false);
  assert.equal(await page.locator('[data-chat="warning"]').isHidden(), true);
  assert.equal(state.calls, 0);
});

test('a timed out material refresh keeps temporary choices and retries in the same picker', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '按标签 / 逐条', exact: true }).click();
  await page.getByRole('button', { name: '全选这 3 条', exact: true }).click();
  await page.clock.install({ time: new Date('2026-10-31T00:00:00Z') });
  let resolves = 0, release;
  await page.route('**/api/app/v1/chat-materials/resolve', async route => {
    if (++resolves === 1) {
      await new Promise(resolve => { release = resolve; });
      try { await route.continue(); } catch {}
    } else await route.continue();
  });
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await waitFor(() => resolves === 1);
  await page.clock.fastForward(65001);
  await page.locator('.insight-picker > .insight-warning:not([hidden])').waitFor();
  assert.equal(state.chat.version, 1);
  assert.equal(await page.locator('.insight-choice input:checked').count(), 3);
  release();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.getByRole('dialog').waitFor({ state: 'detached' });
  assert.equal(resolves, 2);
  assert.equal(state.chat.materials.length, 3);
  assert.equal(state.chat.version, 2);
});

test('failed material refresh retains the selection and a late retry cannot update a departed chat', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('.side a[href="/"]').click();
  await page.goBack();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const before = structuredClone(state.chat), count = await page.locator('[data-chat="material-toggle"]').textContent();
  state.sourceFailure = true;
  await page.getByRole('button', { name: '添加笔记', exact: true }).click();
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await page.locator('.insight-picker > .insight-warning:not([hidden])').waitFor();
  assert.deepEqual(state.chat, before);
  assert.equal(await page.locator('[data-chat="material-toggle"]').textContent(), count);

  state.sourceFailure = false;
  let release;
  await page.route('**/api/app/v1/chat-materials/resolve', async route => {
    const response = await route.fetch();
    await new Promise(resolve => { release = resolve; });
    await route.fulfill({ response });
  });
  await page.getByRole('button', { name: '完成', exact: true }).click();
  await waitFor(() => !!release);
  await page.goForward();
  release(); await page.waitForTimeout(100);
  assert.deepEqual(state.chat, before);
  assert.equal(new URL(page.url()).pathname, '/');
  assert.equal(await page.locator('.insights-page').count(), 0);
});

async function menuAction(page, name) {
  await page.getByRole('button', { name: '会话', exact: true }).click();
  await page.getByRole('menuitem', { name, exact: true }).click();
}

test('new drafts stay empty across reload and paged history restores the chosen conversation without generation', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
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
  await dialog.locator('.insight-history-row').waitFor();
  assert.equal(await dialog.locator('.insight-history-row').count(), 1);
  await dialog.getByRole('button', { name: '加载更多', exact: true }).click();
  await dialog.locator('.insight-history-row').nth(1).waitFor();
  await mkdir('.local/chat-browser', { recursive: true });
  await page.screenshot({ path: '.local/chat-browser/history-desktop.png', fullPage: true });
  await dialog.getByRole('button', { name: /最初的问题/ }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(new URL(page.url()).searchParams.get('chat'), first);
  assert.equal(await page.locator('.insight-user').textContent(), '最初的问题');
  assert.equal(state.calls, 2);
  await page.setViewportSize({ width: 390, height: 844 });
  await menuAction(page, '历史会话');
  await dialog.locator('.insight-history-row').waitFor();
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
  assert.ok(await page.getByRole('button', { name: '保存为笔记', exact: true }).isDisabled());
  await page.getByText('已停止生成，已保留收到的内容。', { exact: true }).waitFor();
  state.stream = 'complete';
  await page.locator('[data-chat="ask"]').fill('停止后继续');
  await page.getByRole('button', { name: '发送', exact: true }).click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  assert.equal(state.calls, 2);
});

test('deletion confirms intent, cancels the live reply and drains saving before deleting', async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await menuAction(page, '删除当前会话');
  await page.locator('.cfm').getByRole('button', { name: '取消', exact: true }).click();
  assert.equal(state.requests.filter(request => request.method === 'DELETE' && request.path.startsWith('/chats/')).length, 0);
  let release;
  await page.route('**/messages/*/save', async route => {
    const response = await route.fetch(); await new Promise(resolve => { release = resolve; }); await route.fulfill({ response });
  });
  await page.getByRole('button', { name: '保存为笔记', exact: true }).click();
  await waitFor(() => !!release);
  state.stream = 'hold'; state.cancelDelay = 100;
  await page.locator('[data-chat="one"]').click();
  await page.locator('.insight-answer strong').nth(1).waitFor();
  await menuAction(page, '删除当前会话');
  await page.locator('.cfm').getByRole('button', { name: '删除', exact: true }).click();
  await waitFor(() => state.chat.turns.at(-1).assistant.status === 'canceled');
  assert.equal(state.requests.filter(request => request.method === 'DELETE' && request.path.startsWith('/chats/')).length, 0);
  release();
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
  await menuAction(page, '删除当前会话');
  await page.locator('.cfm').getByRole('button', { name: '删除', exact: true }).click();
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
  await menuAction(page, '删除当前会话');
  await page.locator('.cfm').getByRole('button', { name: '删除', exact: true }).click();
  await page.getByText('会话已在其他页面更改，请重新进入后继续。', { exact: true }).waitFor();
  assert.equal(state.requests.filter(request => request.method === 'DELETE').length, 0);
  assert.equal(await page.locator('.insight-assistant').count(), 1);
  await page.reload(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  const reject = async route => {
    if (route.request().method() !== 'DELETE') { await route.continue(); return; }
    await route.fulfill({ status: 503, json: { error: { code: 'UPSTREAM_UNAVAILABLE', retryable: true } } });
  };
  await page.route('**/api/app/v1/chats/*', reject);
  await menuAction(page, '删除当前会话');
  const remove = page.locator('.cfm').getByRole('button', { name: '删除', exact: true });
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
  await menuAction(page, '删除当前会话');
  await page.locator('.cfm').getByRole('button', { name: '删除', exact: true }).click();
  await waitFor(() => !!release);
  await page.locator('.side a[href="/"]').click();
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
  await dialog.getByRole('button', { name: '加载更多', exact: true }).click();
  await dialog.locator('.insight-warning:not([hidden])').waitFor();
  assert.equal(await dialog.locator('.insight-history-row').count(), 1);
  await dialog.getByRole('button', { name: '加载更多', exact: true }).click();
  await dialog.locator('.insight-history-row').nth(1).waitFor();
  assert.deepEqual(tokens, ['1', '1']);
  assert.equal(state.calls, 0);
});

test('English chat keeps its running stream and material picker through language switches', async t => {
  const { page, state, url, notes } = await fixture(t, 'en-US');
  await login(page, url, '/insights?note=memos%2Fnormal');
  await page.getByRole('button', { name: 'Add notes', exact: true }).click();
  await page.getByRole('dialog', { name: 'Select notes', exact: true }).waitFor();
  await page.getByRole('button', { name: 'By tag / individually', exact: true }).click();
  await page.getByRole('button', { name: 'Select all 3 notes', exact: true }).click();
  await page.locator('#app [data-language]').selectOption('zh-CN');
  await page.getByRole('dialog', { name: '选择笔记', exact: true }).waitFor();
  assert.equal(await page.locator('.insight-choice input:checked').count(), 3);
  await page.locator('#app [data-language]').selectOption('en');
  await page.getByRole('button', { name: 'Done', exact: true }).click();
  state.stream = 'hold';
  await page.getByRole('button', { name: 'Generate insight', exact: true }).first().click();
  await page.getByRole('button', { name: 'Stop', exact: true }).waitFor();
  await page.locator('.insight-answer strong').waitFor();
  const original = state.requests.find(request => request.path.endsWith('/messages')).body;
  assert.equal(original.language, 'en');
  await page.locator('#app [data-language]').selectOption('zh-CN');
  await page.getByRole('button', { name: '停止', exact: true }).waitFor();
  assert.equal(state.chat.turns[0].assistant.status, 'running');
  state.finish(); await page.locator('.insights-page[aria-busy="false"]').waitFor();
  await page.locator('#app [data-language]').selectOption('en');
  await page.getByRole('button', { name: 'Save as note', exact: true }).click();
  await page.getByRole('button', { name: 'Saved as note', exact: true }).waitFor();
  assert.equal(state.lastSave.content, reply); assert.equal(state.calls, 1);
  assert.equal(state.chat.materials.length, notes.length);
  await page.locator('[data-chat="menu"]').click();
  await page.getByRole('menuitem', { name: 'Chat history', exact: true }).click();
  await page.getByRole('dialog', { name: 'Chat history', exact: true }).waitFor();
  assert.match(await page.locator('.insight-history').textContent(), /Generate insight/);
  await page.getByRole('button', { name: 'Close', exact: true }).click();
  await mkdir('.local/i18n-review', { recursive: true });
  await page.screenshot({ path: '.local/i18n-review/chat-en.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.screenshot({ path: '.local/i18n-review/chat-en-mobile.png', fullPage: true });
});
