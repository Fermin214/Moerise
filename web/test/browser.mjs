import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { pathToFileURL } from 'node:url';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { test } from 'node:test';
import { createWebServer } from '../server.mjs';

// Optional browser runtime: MOERISE_PLAYWRIGHT_MODULE points at an installed playwright/index.mjs.
const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
if (!runtime) throw new Error('Set MOERISE_PLAYWRIGHT_MODULE to the installed playwright/index.mjs');
const { chromium } = await import(pathToFileURL(runtime).href);
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=', 'base64');

async function fixture(t) {
  const notes = new Map();
  for (let i = 0; i < 47; i++) {
    const id = `n${String(i).padStart(2, '0')}`;
    notes.set(id, {
      name: `memos/${id}`, content: `笔记 ${i}：保留文字与 #想法${i % 2 ? '' : ' #灵感'}\n\n第二段文字。`,
      state: 'NORMAL', tags: i % 2 ? ['想法'] : ['想法', '灵感'], source_hash: `v-${id}`,
      create_time: new Date(Date.UTC(2026, 9, 29, 4, 59 - i)).toISOString(), attachments: [],
    });
  }
  notes.get('n02').attachments = [
    { name: 'attachments/pic', filename: 'image.png', type: 'image/png', size: png.length, content_url: '/api/app/v1/notes/n02/attachments/pic/content' },
    { name: 'attachments/doc', filename: 'document.pdf', type: 'application/pdf', size: 3, content_url: '/api/app/v1/notes/n02/attachments/doc/content' },
  ];
  notes.get('n02').content += '\n\n[资料](/file/attachments/doc/document.pdf) ![原图](/file/attachments/pic/image.png)';
  notes.set('old', { ...notes.get('n00'), name: 'memos/old', state: 'ARCHIVED', content: '归档原文 #想法', source_hash: 'v-old' });
  const state = { notes, requests: [], unknownCreate: false, unknownPatch: false, conflict: false, csrfRejected: false, created: 0, session: false };
  const idempotency = new Map();
  const core = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const path = url.pathname.replace('/api/app/v1', '');
    let body;
    if (['POST', 'PATCH', 'DELETE'].includes(req.method)) {
      let raw = '';
      for await (const chunk of req) raw += chunk;
      body = raw ? JSON.parse(raw) : null;
    }
    state.requests.push({ method: req.method, path, query: url.searchParams, body, headers: req.headers });
    function json(value, status = 200) { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); }
    function error(code, status = 409) { json({ error: { code, retryable: false } }, status); }
    if (path === '/session') {
      if (req.method === 'DELETE') {
        state.session = false; res.setHeader('Set-Cookie', 'session=; Max-Age=0; Path=/'); res.writeHead(204); res.end(); return;
      }
      if (req.method === 'POST') {
        state.session = true; res.setHeader('Set-Cookie', 'session=webtest; HttpOnly; SameSite=Strict; Path=/');
      } else if (!state.session || !req.headers.cookie?.includes('session=webtest')) { error('SESSION_UNAUTHORIZED', 401); return; }
      json({ user: { name: 'users/1', display_name: '测试作者' }, csrf_token: 'csrf', timezone: 'Asia/Hong_Kong' }); return;
    }
    if (!state.session || !req.headers.cookie?.includes('session=webtest')) { error('SESSION_UNAUTHORIZED', 401); return; }
    if (['POST', 'PATCH', 'DELETE'].includes(req.method)) {
      assert.equal(req.headers['x-csrf-token'], 'csrf');
      assert.ok(req.headers.origin);
      if (state.csrfRejected) { state.csrfRejected = false; error('CSRF_REJECTED', 403); return; }
    }
    const source = [...notes.values()].filter(note => note.state === (url.searchParams.get('state') || 'NORMAL'));
    if (path === '/tags') {
      const counts = new Map();
      for (const note of source) for (const tag of note.tags) counts.set(tag, (counts.get(tag) || 0) + 1);
      json({ tags: [...counts].map(([tag, count]) => ({ tag, count })) }); return;
    }
    if (path === '/calendar') {
      json({ month: url.searchParams.get('month'), days: [{ date: '2026-10-29', count: source.length }] }); return;
    }
    if (path === '/notes' && req.method === 'GET') {
      assert.equal(url.searchParams.get('order'), 'created_desc');
      assert.equal(url.searchParams.get('page_size'), '20');
      const result = source.filter(note => note.content.includes(url.searchParams.get('query') || '') &&
        url.searchParams.getAll('tag').every(tag => note.tags.includes(tag)) &&
        (!url.searchParams.get('date') || note.create_time.startsWith(url.searchParams.get('date'))))
        .sort((a, b) => Date.parse(b.create_time) - Date.parse(a.create_time) || (a.name < b.name ? 1 : a.name > b.name ? -1 : 0));
      const start = Number(url.searchParams.get('page_token') || 0);
      json({ notes: result.slice(start, start + 20), next_page_token: start + 20 < result.length ? String(start + 20) : '' }); return;
    }
    if (path === '/notes' && req.method === 'POST') {
      const key = req.headers['idempotency-key']; assert.ok(key);
      if (idempotency.has(key)) { json(idempotency.get(key)); return; }
      assert.equal(body.tags, undefined);
      const id = `created-${++state.created}`;
      const note = { name: `memos/${id}`, content: body.content, attachments: [], source_hash: `v-${id}`,
        tags: [], state: 'NORMAL', create_time: '2026-10-29T10:00:00Z' };
      for (const [i, item] of (body.attachments || []).entries()) note.attachments.push({
        name: `attachments/upload-${i}`, filename: item.filename, type: item.type, size: Buffer.from(item.content, 'base64').length,
        content_url: `/api/app/v1/notes/${id}/attachments/upload-${i}/content`,
      });
      notes.set(id, note); idempotency.set(key, structuredClone(note));
      if (state.unknownCreate) { state.unknownCreate = false; error('WRITE_OUTCOME_UNKNOWN', 502); return; }
      json(note, 201); return;
    }
    const attachment = /^\/notes\/([^/]+)\/attachments\/([^/]+)\/content$/.exec(path);
    if (attachment) {
      res.writeHead(200, { 'Content-Type': attachment[2] === 'pic' ? 'image/png' : 'application/pdf', 'Content-Disposition': 'attachment; filename="file"' });
      res.end(attachment[2] === 'pic' ? png : Buffer.from('pdf')); return;
    }
    const id = /^\/notes\/([^/]+)$/.exec(path)?.[1], note = notes.get(id);
    if (!note) { error('SOURCE_NOT_FOUND', 404); return; }
    if (req.method === 'GET') { json(note); return; }
    if (state.conflict) { state.conflict = false; note.source_hash += '-external'; note.content = '外部修改 #想法'; error('SOURCE_CHANGED'); return; }
    if (req.method === 'PATCH' && idempotency.has(req.headers['idempotency-key'])) { json(idempotency.get(req.headers['idempotency-key'])); return; }
    if (body.source_hash !== note.source_hash) { error('WRITE_CONFLICT'); return; }
    if (req.method === 'DELETE') { notes.delete(id); res.writeHead(204); res.end(); return; }
    assert.equal(req.method, 'PATCH'); assert.ok(req.headers['idempotency-key']);
    for (const resource of body.remove_attachments || []) {
      if ((body.content ?? note.content).includes(`](/file/${resource}/`) && note.attachments.some(item => item.name === resource && item.type.startsWith('image/'))) {
        error('ATTACHMENT_REFERENCED', 422); return;
      }
    }
    if (body.content !== undefined) note.content = body.content;
    if (body.state) note.state = body.state;
    note.tags = [...note.content.matchAll(/#([^\s#]+)/g)].map(match => match[1]);
    note.attachments = note.attachments.filter(item => !(body.remove_attachments || []).includes(item.name));
    for (const [i, item] of (body.attachments || []).entries()) note.attachments.push({
      name: `attachments/add-${i}`, filename: item.filename, type: item.type, size: Buffer.from(item.content, 'base64').length,
      content_url: `/api/app/v1/notes/${id}/attachments/add-${i}/content`,
    });
    note.source_hash += '-edited'; idempotency.set(req.headers['idempotency-key'], structuredClone(note));
    if (state.unknownPatch) { state.unknownPatch = false; error('WRITE_OUTCOME_UNKNOWN', 502); return; }
    json(note);
  });
  core.listen(0, '127.0.0.1'); await once(core, 'listening');
  const web = createWebServer({ coreOrigin: `http://127.0.0.1:${core.address().port}`, publicOrigin: 'http://127.0.0.1:3000' });
  // The public origin is chosen after binding so the proxy validates real browser Origin.
  web.listen(0, '127.0.0.1'); await once(web, 'listening');
  const port = web.address().port;
  await new Promise(resolve => web.close(resolve));
  const live = createWebServer({ coreOrigin: `http://127.0.0.1:${core.address().port}`, publicOrigin: `http://127.0.0.1:${port}` });
  live.listen(port, '127.0.0.1'); await once(live, 'listening');
  let browser;
  t.after(async () => {
    await browser?.close();
    await Promise.all([core, live].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
  });
  browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  const context = await browser.newContext({ viewport: { width: 1360, height: 960 } });
  const page = await context.newPage(), errors = [];
  await page.clock.setFixedTime(new Date('2026-10-30T04:00:00Z'));
  page.on('pageerror', err => errors.push(err.message));
  return { page, context, state, errors, url: `http://127.0.0.1:${port}` };
}
async function login(page) {
  await page.locator('#login-form').waitFor({ state: 'visible' });
  await page.locator('#username').fill('test');
  await page.locator('#password').fill('test');
  await page.locator('#login').click();
  await page.locator('#workspace').waitFor({ state: 'visible' });
}
async function menu(page, id, label) {
  const card = page.locator(`[data-name="memos/${id}"]`);
  await card.getByRole('button', { name: '更多操作' }).click();
  await card.getByRole('menuitem', { name: label, exact: true }).click();
}
async function screenshot(page, filename) {
  if (!process.env.MOERISE_SCREENSHOT_DIR) return;
  await mkdir(process.env.MOERISE_SCREENSHOT_DIR, { recursive: true });
  await page.screenshot({ path: join(process.env.MOERISE_SCREENSHOT_DIR, filename), fullPage: false });
}

test('browser timeline management, attachment retries, history, conflicts and private cleanup', { timeout: 120_000 }, async t => {
  const { page, context, state, errors, url } = await fixture(t);
  await page.goto(`${url}/notes/n02?from=${encodeURIComponent('/?tag=想法')}`);
  await login(page);
  await page.locator('article.note').waitFor();
  assert.equal(new URL(page.url()).pathname, '/notes/n02');
  assert.equal(await page.locator('.attachment-list img').count(), 1);
  assert.equal(await page.locator('a[download="document.pdf"]').count(), 1);
  const nativeLink = page.locator('.note-content a', { hasText: '资料' });
  assert.equal(await nativeLink.getAttribute('href'), '/api/app/v1/notes/n02/attachments/doc/content');
  assert.equal(await page.locator('.note-content img').getAttribute('src'), '/api/app/v1/notes/n02/attachments/pic/content');
  const downloaded = page.waitForEvent('download');
  await nativeLink.click();
  assert.equal((await downloaded).url(), `${url}/api/app/v1/notes/n02/attachments/doc/content`);
  await page.reload();
  await page.locator('article.note').waitFor();
  await page.locator('#back').click();
  await page.waitForURL('**/?tag=*');
  await page.locator('[data-name="memos/n00"]').waitFor();
  await screenshot(page, 'desktop.png');
  await page.locator('#tags button', { hasText: '#灵感' }).waitFor();
  await page.evaluate(() => {
    const search = document.getElementById('q');
    search.value = '笔记';
    search.dispatchEvent(new Event('input', { bubbles: true }));
    [...document.querySelectorAll('#tags button')].find(button => button.textContent.includes('#灵感')).click();
  });
  await page.waitForFunction(() => new URL(location.href).searchParams.getAll('tag').length === 2);
  await page.locator('#calendar button[aria-label^="2026-10-29"]').click();
  await page.waitForFunction(() => new URL(location.href).searchParams.get('date') === '2026-10-29');
  await page.locator('[data-name="memos/n00"]').waitFor();
  const combined = state.requests.filter(item => item.path === '/notes' && item.method === 'GET').at(-1);
  assert.deepEqual(combined.query.getAll('tag'), ['想法', '灵感']);
  assert.equal(combined.query.get('query'), '笔记');
  assert.equal(combined.query.get('date'), '2026-10-29');
  await page.locator('#load-more').scrollIntoViewIfNeeded();
  await page.locator('[data-name="memos/n46"]').waitFor();
  await page.locator('[data-name="memos/n40"] .dlink').click();
  await page.locator('article.note').waitFor();
  await page.locator('#back').click();
  await page.locator('[data-name="memos/n46"]').waitFor();
  assert.ok(await page.evaluate(() => window.scrollY) > 100);
  await page.goForward();
  await page.locator('article.note').waitFor();
  await page.reload();
  await page.locator('article.note').waitFor();
  await page.locator('#back').click();
  await page.locator('[data-name="memos/n46"]').waitFor();
  assert.ok(await page.evaluate(() => window.scrollY) > 100);
  await page.locator('a[href="/review"]').click();
  await page.locator('#capability').waitFor();
  await page.locator('#q').fill('笔记 4');
  await page.locator('#q').press('Enter');
  await page.waitForFunction(() => location.pathname === '/' && new URL(location.href).searchParams.get('query') === '笔记 4');
  await page.locator('[data-name="memos/n40"]').waitFor();
  await page.locator('#filters').getByRole('button', { name: '清除全部' }).click();
  await page.locator('[data-name="memos/n00"]').waitFor();
  await menu(page, 'n00', '编辑');
  const editor = page.locator('form.editor');
  const raw = '  **保留 Markdown**\n#原生标签 #第二标签\n<script>globalThis.pwned = true</script>\n[危险](javascript:alert(1))';
  await editor.locator('textarea').fill(raw);
  await editor.locator('textarea').press('Escape');
  await page.locator('[data-name="memos/n00"] .note-content').waitFor();
  assert.equal(state.notes.get('n00').content.includes('保留 Markdown'), false);
  await menu(page, 'n00', '编辑');
  await editor.locator('textarea').fill(raw);
  state.csrfRejected = true;
  await editor.locator('textarea').press('Control+Enter');
  await page.locator('[data-name="memos/n00"] strong').waitFor();
  assert.equal(state.notes.get('n00').content, raw);
  assert.equal(await page.evaluate(() => globalThis.pwned), undefined);
  assert.equal(await page.locator('[data-name="memos/n00"] a[href^="javascript:"]').count(), 0);
  await menu(page, 'n00', '编辑');
  await editor.locator('textarea').fill('覆盖之前');
  state.conflict = true;
  await editor.locator('textarea').press('Control+Enter');
  await editor.getByText('笔记已在其他入口修改，请刷新后再编辑。').waitFor();
  await editor.getByRole('button', { name: '刷新笔记' }).click();
  await page.locator('[data-name="memos/n00"] .note-content', { hasText: '外部修改' }).waitFor();
  await menu(page, 'n00', '归档');
  await page.locator('[data-name="memos/n00"]').waitFor({ state: 'detached' });
  await page.getByRole('button', { name: '用户菜单' }).click();
  await page.locator('a[href="/archived"]').click();
  await page.locator('[data-name="memos/n00"]').waitFor();
  await menu(page, 'n00', '恢复到主页');
  await page.locator('[data-name="memos/n00"]').waitFor({ state: 'detached' });
  await page.locator('a[href="/"]').click();
  await page.locator('[data-name="memos/n00"]').waitFor();
  await page.locator('#attachment-input').setInputFiles({ name: 'only.txt', mimeType: 'text/plain', buffer: Buffer.from('附件原字节') });
  state.unknownCreate = true;
  await page.locator('#save').click();
  await page.locator('#save-error').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#content').getAttribute('readonly'), '');
  await page.locator('#save').click();
  await page.locator('[data-name="memos/created-1"]').waitFor();
  const writes = state.requests.filter(item => item.path === '/notes' && item.method === 'POST');
  assert.equal(writes.length, 2);
  assert.equal(writes[0].headers['idempotency-key'], writes[1].headers['idempotency-key']);
  assert.deepEqual(writes[0].body, writes[1].body);
  assert.equal(writes[0].body.content, '');
  assert.equal(Buffer.from(writes[0].body.attachments[0].content, 'base64').toString(), '附件原字节');
  assert.equal(state.created, 1);
  await menu(page, 'created-1', '编辑');
  await editor.locator('input[type="file"]').setInputFiles({ name: 'replace.txt', mimeType: 'text/plain', buffer: Buffer.from('new bytes') });
  await editor.getByRole('button', { name: '移除附件 only.txt' }).click();
  state.unknownPatch = true;
  await editor.locator('textarea').press('Control+Enter');
  await editor.getByText('保存结果尚未确认。').waitFor();
  await editor.getByRole('button', { name: '取消', exact: true }).click();
  await page.locator('[data-name="memos/created-1"]').getByRole('button', { name: '重试原提交' }).click();
  await page.locator('a[download="replace.txt"]').waitFor();
  assert.equal(state.notes.get('created-1').content, '');
  const patch = state.requests.filter(item => item.path === '/notes/created-1' && item.method === 'PATCH').at(-1);
  assert.deepEqual(patch.body.remove_attachments, ['attachments/upload-0']);
  const patches = state.requests.filter(item => item.path === '/notes/created-1' && item.method === 'PATCH');
  assert.equal(patches.length, 2);
  assert.deepEqual(patches[0].body, patches[1].body);
  assert.equal(patches[0].headers['idempotency-key'], patches[1].headers['idempotency-key']);
  await menu(page, 'created-1', '删除');
  assert.equal(state.notes.has('created-1'), true);
  await page.locator('[data-name="memos/created-1"] .cfm .danger').click();
  await page.locator('[data-name="memos/created-1"]').waitFor({ state: 'detached' });
  assert.equal(state.notes.has('created-1'), false);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => scrollTo(0, 0));
  await screenshot(page, 'mobile.png');
  await page.locator('#mobile-filters').click();
  await page.locator('#calendar').waitFor({ state: 'visible' });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.locator('#content').fill('退出应清理的草稿');
  await page.locator('#attachment-input').setInputFiles({ name: 'private.txt', mimeType: 'text/plain', buffer: Buffer.from('private') });
  await page.locator('#user-menu').click();
  await page.locator('#logout').click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#notes').textContent(), '');
  assert.equal(await page.locator('#content').inputValue(), '');
  assert.equal(await page.locator('#draft-attachments').textContent(), '');
  await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  assert.equal(await page.locator('#draft-attachments').textContent(), '');
  assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
  assert.deepEqual(errors, []);
  await context.close();
});

test('late list and file reads cannot restore private data or overwrite a newer route', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(url);
  await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  let captured;
  const hasRequest = new Promise(resolve => { captured = resolve; });
  await page.route('**/api/app/v1/notes?*query=slow*', async route => {
    captured();
    await delayed;
    await route.fulfill({ json: { notes: [state.notes.get('n00')], next_page_token: '' } }).catch(() => {});
  });
  await page.locator('#q').fill('slow');
  await page.locator('#q').press('Enter');
  await hasRequest;
  await page.getByRole('button', { name: '用户菜单' }).click();
  await page.locator('a[href="/archived"]').click();
  await page.locator('[data-name="memos/old"]').waitFor();
  release();
  await page.waitForFunction(() => document.querySelector('#notes')?.getAttribute('aria-busy') === 'false');
  assert.equal(await page.locator('[data-name="memos/n00"]').count(), 0);
  await page.locator('a[href="/"]').click();
  await page.locator('[data-name="memos/n00"]').waitFor();
  await page.evaluate(() => {
    globalThis.originalRead = File.prototype.arrayBuffer;
    File.prototype.arrayBuffer = async function() {
      await new Promise(resolve => { globalThis.releaseFile = resolve; });
      return globalThis.originalRead.call(this);
    };
  });
  await page.locator('#attachment-input').setInputFiles({ name: 'late.txt', mimeType: 'text/plain', buffer: Buffer.from('private late bytes') });
  await page.locator('#user-menu').click();
  await page.locator('#logout').click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  await page.evaluate(() => { globalThis.releaseFile(); File.prototype.arrayBuffer = globalThis.originalRead; });
  await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  assert.equal(await page.locator('#draft-attachments').textContent(), '');
  assert.equal(await page.locator('#content').inputValue(), '');
  assert.deepEqual(errors, []);
});

test('session renewal restores unsaved drafts and replays ambiguous creates and edits with the original body and key', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  await page.locator('#content').fill('创建草稿原文');
  await page.locator('#attachment-input').setInputFiles({ name: 'draft.txt', mimeType: 'text/plain', buffer: Buffer.from('draft bytes') });
  await menu(page, 'n00', '编辑');
  const editor = page.locator('[data-name="memos/n00"].editor'), text = editor.locator('textarea');
  await text.fill('编辑草稿原文');
  await editor.locator('input[type="file"]').setInputFiles({ name: 'edit.txt', mimeType: 'text/plain', buffer: Buffer.from('edit bytes') });
  async function expireViaList(query) {
    state.session = false;
    await page.locator('#q').fill(query); await page.locator('#q').press('Enter');
    await page.locator('#login-form').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#notes').textContent(), '');
    assert.equal(await page.locator('#content').inputValue(), '');
    await login(page); await editor.waitFor();
  }
  await expireViaList('笔记');
  assert.equal(await page.locator('#content').inputValue(), '创建草稿原文');
  assert.match(await page.locator('#draft-attachments').textContent(), /draft.txt/);
  assert.equal(await text.inputValue(), '编辑草稿原文');
  assert.equal(await text.getAttribute('readonly'), null);
  assert.match(await editor.textContent(), /edit.txt/);

  state.unknownCreate = true;
  await page.locator('#save').click(); await page.locator('#save-error').waitFor({ state: 'visible' });
  await expireViaList('笔记 ');
  assert.equal(await page.locator('#content').getAttribute('readonly'), '');
  await page.locator('#save').click();
  await page.waitForFunction(() => document.getElementById('content').value === '');
  const creates = state.requests.filter(item => item.path === '/notes' && item.method === 'POST');
  assert.equal(creates.length, 2); assert.equal(state.created, 1);
  assert.equal(creates[0].headers['idempotency-key'], creates[1].headers['idempotency-key']);
  assert.deepEqual(creates[0].body, creates[1].body);
  assert.equal(Buffer.from(creates[1].body.attachments[0].content, 'base64').toString(), 'draft bytes');

  state.unknownPatch = true;
  await text.press('Control+Enter'); await editor.getByText('保存结果尚未确认。').waitFor();
  await expireViaList('');
  assert.equal(await text.getAttribute('readonly'), '');
  assert.equal(await text.inputValue(), '编辑草稿原文');
  await text.press('Control+Enter');
  await page.waitForFunction(() => !document.querySelector('form.editor'));
  const patches = state.requests.filter(item => item.path === '/notes/n00' && item.method === 'PATCH');
  assert.equal(patches.length, 2);
  assert.equal(patches[0].headers['idempotency-key'], patches[1].headers['idempotency-key']);
  assert.deepEqual(patches[0].body, patches[1].body);
  assert.equal(Buffer.from(patches[1].body.attachments[0].content, 'base64').toString(), 'edit bytes');
  assert.equal(state.notes.get('n00').attachments.length, 1);
  assert.deepEqual(errors, []);
});

test('a sidebar 401 interrupts an in-flight edit without losing its retry or leaving it busy', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  let release, captured;
  const delayed = new Promise(resolve => { release = resolve; }), submitted = new Promise(resolve => { captured = resolve; });
  await page.route('**/api/app/v1/notes/n00', async route => {
    if (route.request().method() !== 'PATCH') { await route.continue(); return; }
    const response = await route.fetch();
    captured(); await delayed;
    await route.fulfill({ response }).catch(() => {});
  });
  await menu(page, 'n00', '编辑');
  const editor = page.locator('form.editor');
  await editor.locator('textarea').fill('来源已保存但响应尚未到达');
  await editor.locator('textarea').press('Control+Enter'); await submitted;
  state.session = false;
  await page.locator('#month-next').click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  release(); await page.unroute('**/api/app/v1/notes/n00');
  await login(page); await editor.waitFor();
  assert.equal(await editor.getByRole('button', { name: '保存' }).isEnabled(), true);
  await editor.locator('textarea').press('Control+Enter');
  await page.locator('[data-name="memos/n00"] .note-content', { hasText: '来源已保存但响应尚未到达' }).waitFor();
  const patches = state.requests.filter(item => item.path === '/notes/n00' && item.method === 'PATCH');
  assert.equal(patches.length, 2);
  assert.equal(patches[0].headers['idempotency-key'], patches[1].headers['idempotency-key']);
  assert.deepEqual(patches[0].body, patches[1].body);
  assert.deepEqual(errors, []);
});

test('automatic pagination preserves the connected editor, focus and selection across equal-time resource names', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  const ids = [...Array.from({ length: 19 }, (_, i) => `z${i}`), 'a', 'Z'];
  for (const id of ids) state.notes.set(id, {
    ...state.notes.get('n00'), name: `memos/${id}`, source_hash: `v-${id}`, create_time: '2026-10-30T00:00:00Z',
  });
  let release, captured;
  const delayed = new Promise(resolve => { release = resolve; }), loading = new Promise(resolve => { captured = resolve; });
  await page.route('**/api/app/v1/notes?*', async route => {
    if (new URL(route.request().url()).searchParams.get('page_token') === '20') { captured(); await delayed; }
    await route.continue().catch(() => {});
  });
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/a"]').waitFor();
  await menu(page, 'a', '编辑'); await loading;
  const text = page.locator('form.editor textarea');
  await text.fill('分页时连续输入');
  await page.evaluate(() => {
    globalThis.activeEditor = document.querySelector('form.editor');
    globalThis.editorRemoved = false;
    new MutationObserver(records => {
      if (records.some(record => [...record.removedNodes].includes(globalThis.activeEditor))) globalThis.editorRemoved = true;
    }).observe(document.getElementById('notes'), { childList: true });
    document.activeElement.setSelectionRange(2, 5);
  });
  release(); await page.locator('[data-name="memos/Z"]').waitFor();
  assert.deepEqual(await page.evaluate(() => ({
    connected: globalThis.activeEditor.isConnected,
    same: globalThis.activeEditor === document.querySelector('form.editor'),
    removed: globalThis.editorRemoved,
    focused: document.activeElement === globalThis.activeEditor.querySelector('textarea'),
    selection: [document.activeElement.selectionStart, document.activeElement.selectionEnd],
  })), { connected: true, same: true, removed: false, focused: true, selection: [2, 5] });
  assert.deepEqual(await page.locator('#notes > [data-name]').evaluateAll(nodes => nodes.slice(0, 21).map(node => node.dataset.name)),
    ids.sort((a, b) => a < b ? 1 : a > b ? -1 : 0).map(id => `memos/${id}`));
  await text.press('End'); await text.press('Control+Enter');
  await page.locator('[data-name="memos/a"] .note-content', { hasText: '分页时连续输入' }).waitFor();
  assert.deepEqual(errors, []);
});

test('sidebar aggregates reuse loaded and in-flight data, cancel replaced months and refresh after writes', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  await page.route(/\/api\/app\/v1\/(?:tags|calendar)\?/, async route => { await delayed; await route.continue().catch(() => {}); });
  await page.goto(url); await login(page); await page.locator('[data-name="memos/n00"]').waitFor();
  const requests = [];
  page.on('request', request => { if (/\/api\/app\/v1\/(?:tags|calendar)\?/.test(request.url())) requests.push(request.url()); });
  for (const query of ['笔记', '笔记 1', '笔记 2']) {
    await page.locator('#q').fill(query); await page.locator('#q').press('Enter');
    await page.waitForFunction(query => new URL(location.href).searchParams.get('query') === query, query);
  }
  assert.equal(requests.length, 0);
  release(); await page.locator('#tags button').first().waitFor();
  await page.unroute(/\/api\/app\/v1\/(?:tags|calendar)\?/);
  const count = path => state.requests.filter(item => item.path === path).length;
  async function aggregatesAfter(action) {
    const responses = Promise.all(['tags', 'calendar'].map(name =>
      page.waitForResponse(response => new URL(response.url()).pathname === `/api/app/v1/${name}` && response.ok())));
    await action(); await responses;
  }
  assert.equal(count('/tags'), 1); assert.equal(count('/calendar'), 1);
  await page.locator('#month-next').click();
  await page.waitForFunction(() => document.querySelector('#calendar button:not([disabled])') && document.getElementById('calendar-title').textContent === '2026年11月');
  assert.equal(count('/tags'), 1); assert.equal(count('/calendar'), 2);
  await page.locator('#month-prev').click();
  await page.waitForFunction(() => document.getElementById('calendar-title').textContent === '2026年10月');
  assert.equal(count('/calendar'), 2);
  let cancelRelease, captured;
  const cancelled = new Promise(resolve => { cancelRelease = resolve; }), pending = new Promise(resolve => { captured = resolve; });
  await page.route('**/api/app/v1/calendar?*month=2026-09*', async route => {
    captured(); await cancelled; await route.fulfill({ json: { month: '2026-09', days: [] } }).catch(() => {});
  });
  const aborted = page.waitForEvent('requestfailed', request => request.url().includes('month=2026-09'));
  await page.locator('#month-prev').click(); await pending;
  await page.locator('#month-prev').click(); await aborted;
  cancelRelease();
  await page.waitForFunction(() => document.querySelector('#calendar button:not([disabled])') && document.getElementById('calendar-title').textContent === '2026年8月');
  const before = { tags: count('/tags'), calendar: count('/calendar') };
  await page.locator('#content').fill('写入后刷新聚合');
  await aggregatesAfter(() => page.locator('#save').click());
  await page.waitForFunction(() => document.getElementById('content').value === '');
  await page.locator('#tags button').first().waitFor();
  assert.equal(count('/tags'), before.tags + 1); assert.equal(count('/calendar'), before.calendar + 1);
  const refreshed = { tags: count('/tags'), calendar: count('/calendar') };
  await page.clock.setFixedTime(new Date('2026-10-30T04:01:01Z'));
  await page.locator('#q').fill('写入后'); await aggregatesAfter(() => page.locator('#q').press('Enter'));
  await page.locator('#tags button').first().waitFor();
  assert.equal(count('/tags'), refreshed.tags + 1); assert.equal(count('/calendar'), refreshed.calendar + 1);
  await aggregatesAfter(() => page.evaluate(() => window.dispatchEvent(new Event('focus'))));
  await page.locator('#tags button').first().waitFor();
  assert.equal(count('/tags'), refreshed.tags + 2); assert.equal(count('/calendar'), refreshed.calendar + 2);
  assert.deepEqual(errors, []);
});

test('referenced attachment rejection keeps the draft editable so its body can be corrected and saved', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(`${url}/notes/n02`); await login(page);
  await page.locator('article.note').waitFor();
  await page.getByRole('button', { name: '编辑', exact: true }).click();
  const editor = page.locator('form.editor'), text = editor.locator('textarea');
  const original = await text.inputValue();
  await editor.getByRole('button', { name: '移除附件 image.png' }).click();
  await text.press('Control+Enter');
  await editor.getByText('正文仍在引用该图片，请先移除图片引用再保存。').waitFor();
  assert.equal(await text.getAttribute('readonly'), null);
  assert.equal(await text.inputValue(), original);
  await text.fill(original.replace('![原图](/file/attachments/pic/image.png)', ''));
  await text.press('Control+Enter');
  await page.locator('article.note').waitFor();
  const patches = state.requests.filter(item => item.path === '/notes/n02' && item.method === 'PATCH');
  assert.equal(patches.length, 2);
  assert.notEqual(patches[0].headers['idempotency-key'], patches[1].headers['idempotency-key']);
  assert.deepEqual(state.notes.get('n02').attachments.map(item => item.name), ['attachments/doc']);
  assert.deepEqual(errors, []);
});

test('refreshing an unchanged note clears failed mutation controls', { timeout: 60_000 }, async t => {
  const { page, errors, url } = await fixture(t);
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  await page.route('**/api/app/v1/notes/n00', async route => {
    if (route.request().method() === 'PATCH') {
      await route.fulfill({ status: 503, json: { error: { code: 'UPSTREAM_UNAVAILABLE', retryable: true } } });
    } else await route.continue();
  });
  await menu(page, 'n00', '归档');
  const card = page.locator('[data-name="memos/n00"]');
  await card.getByRole('button', { name: '重试原提交' }).waitFor();
  await card.getByRole('button', { name: '刷新笔记' }).click();
  await card.locator('.note-error').waitFor({ state: 'detached' });
  await card.getByRole('button', { name: '更多操作' }).click();
  assert.equal(await card.getByRole('menuitem', { name: '归档', exact: true }).isEnabled(), true);
  assert.deepEqual(errors, []);
});

test('reordering multiple editors keeps the focused editor connected', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  await page.locator('#content').fill('触发时间线刷新');
  await menu(page, 'n00', '编辑'); await menu(page, 'n01', '编辑');
  const text = page.locator('[data-name="memos/n00"].editor textarea');
  await text.fill('连续输入中的草稿');
  state.notes.get('n01').create_time = '2027-01-01T00:00:00Z';
  await page.evaluate(() => {
    globalThis.focusedForm = document.activeElement.closest('form.editor');
    globalThis.focusedRemoved = false;
    new MutationObserver(records => {
      if (records.some(record => [...record.removedNodes].includes(globalThis.focusedForm))) globalThis.focusedRemoved = true;
    }).observe(document.getElementById('notes'), { childList: true });
    document.getElementById('composer').requestSubmit();
  });
  await page.waitForFunction(() => document.querySelector('#notes > :first-child')?.dataset.name === 'memos/n01');
  assert.deepEqual(await page.evaluate(() => ({
    removed: globalThis.focusedRemoved,
    focused: document.activeElement.closest('form.editor') === globalThis.focusedForm,
  })), { removed: false, focused: true });
  assert.equal(await text.inputValue(), '连续输入中的草稿');
  assert.deepEqual(errors, []);
});
