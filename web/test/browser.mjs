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
        .sort((a, b) => b.create_time.localeCompare(a.create_time) || b.name.localeCompare(a.name));
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
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1360, height: 960 } });
  const page = await context.newPage(), errors = [];
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
  await page.reload();
  await page.locator('article.note').waitFor();
  await page.locator('#back').click();
  await page.waitForURL('**/?tag=*');
  await page.locator('[data-name="memos/n00"]').waitFor();
  await screenshot(page, 'desktop.png');
  await page.locator('#q').fill('笔记');
  await page.locator('#q').press('Enter');
  await page.waitForURL('**/*query=*');
  await page.locator('#tags button', { hasText: '#灵感' }).click();
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
  await page.locator('a[href="/archived"]').click();
  await page.locator('[data-name="memos/n00"]').waitFor();
  await menu(page, 'n00', '恢复到时间线');
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
