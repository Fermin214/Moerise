import { setUILanguage, returningBrowser } from './browser-helpers.mjs';
import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { text as readRequestText } from 'node:stream/consumers';
import { pathToFileURL } from 'node:url';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { test } from 'node:test';
import { createWebServer } from './fixture-server.mjs';

const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
const { [process.env.MOERISE_PLAYWRIGHT_ENGINE || 'chromium']: browserType } = runtime ? await import(pathToFileURL(runtime).href) : await import('playwright');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=', 'base64');

async function fixture(t, language = 'zh-CN', options = {}) {
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
  const state = { notes, requests: [], unknownCreate: false, unknownPatch: false, conflict: false, csrfRejected: false,
    created: 0, session: false, instance: 'first', user: 'users/1', embedding: true, searchChanged: false, searchDelay: 0 };
  const idempotency = new Map();
  const core = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const path = url.pathname.replace('/api/app/v1', '');
    let body;
    if (['POST', 'PATCH', 'DELETE'].includes(req.method)) {
      const raw = await readRequestText(req);
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
      json({ instance_id: state.instance, user: { name: state.user, display_name: '测试作者' }, csrf_token: 'csrf', timezone: 'Asia/Hong_Kong' }); return;
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
      json({ month: url.searchParams.get('month'), days: [
        { date: '2026-10-29', count: source.length }, { date: '2026-10-30', count: 1 },
      ] }); return;
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
    if (path === '/search' && req.method === 'POST') {
      if (!state.embedding && body.mode === 'semantic') { error('EMBEDDING_DISABLED', 503); return; }
      if (state.searchChanged && body.page_token) {
        state.searchChanged = false; error('SOURCE_CHANGED'); return;
      }
      if (state.searchDelay) await new Promise(resolve => setTimeout(resolve, state.searchDelay));
      const result = [...notes.values()].filter(note => note.state === body.state &&
        body.tags.every(tag => note.tags.includes(tag)) &&
        (!body.date || note.create_time.startsWith(body.date)))
        .sort((a, b) => b.name.localeCompare(a.name));
      const start = Number(body.page_token || 0);
      json({ notes: result.slice(start, start + 20), next_page_token: start + 20 < result.length ? String(start + 20) : '',
        mode: state.embedding ? body.mode : 'keyword', semantic: { used: state.embedding, reason: state.embedding ? '' : 'EMBEDDING_DISABLED' } }); return;
    }
    if (path === '/reviews/daily' && req.method === 'GET') {
      json({ id: 'browser-daily', date: '2026-10-30', timezone: 'Asia/Hong_Kong',
        items: [...notes.values()].slice(0, 6).map(note => ({ name: note.name, unavailable: false, note })) }); return;
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
  browser = await browserType.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  const context = await browser.newContext({ locale: language, viewport: { width: 1360, height: 960 }, ...options });
  const page = await context.newPage(), errors = [];
  await returningBrowser(page);
  await page.clock.setFixedTime(new Date('2026-10-30T04:00:00Z'));
  page.on('pageerror', err => errors.push(err.message));
  return { page, context, state, errors, url: `http://127.0.0.1:${port}` };
}
async function login(page) {
  await page.locator('#login-form').waitFor({ state: 'visible' });
  assert.equal(await page.locator('.memos-brand').innerText(), 'Moerise');
  assert.match(await page.locator('.memos-welcome').innerText(), /^(欢迎回来|Welcome back)$/);
  await page.locator('#username').fill('test');
  await page.locator('#password').fill('test');
  await page.locator('#login').click();
  await page.locator('#workspace').waitFor({ state: 'visible' });
}

test('inline tags filter within the timeline and copy preserves the source even after clipboard rejection', async t => {
  const { page, context, state, url } = await fixture(t);
  const original = '  开头 **粗体** #想法。\n第二行\n\n#灵感\n  ';
  state.notes.get('n00').content = original;
  const archived = state.notes.get('old');
  archived.content = '归档 #想法。\n\n#灵感';
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto(url);
  await login(page);
  const current = page.locator('#notes article[data-name="memos/n00"]');
  const more = current.locator('.more');
  const inline = current.getByRole('button', { name: '#想法', exact: true });
  await inline.waitFor();
  assert.equal(await current.locator('.note-tags button').allTextContents().then(tags => tags.join()), '#灵感');
  await more.click();
  await current.getByRole('menuitem', { name: '复制', exact: true }).click();
  await page.locator('.snackbar').getByText('已复制', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), original);

  await setUILanguage(page, 'en');
  await page.evaluate(async () => {
    await navigator.clipboard.writeText('not the note');
    navigator.clipboard.writeText = async () => { throw new DOMException('Denied', 'NotAllowedError'); };
  });
  await more.click();
  await current.getByRole('menuitem', { name: 'Copy', exact: true }).click();
  await page.locator('.snackbar').getByText('Copied', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), original);
  await page.evaluate(() => { document.execCommand = () => false; });
  await more.click();
  await current.getByRole('menuitem', { name: 'Copy', exact: true }).click();
  await page.locator('.snackbar').getByText('Copy failed', { exact: true }).waitFor();

  await inline.click();
  await page.waitForURL(value => value.pathname === '/' && value.searchParams.get('tag') === '想法');
  await page.goto(url + '/archived');
  const old = page.locator('#notes article[data-name="memos/old"]');
  await old.getByRole('button', { name: '#灵感', exact: true }).click();
  await page.waitForURL(value => value.pathname === '/archived' && value.searchParams.get('tag') === '灵感');
});

test('archive undo uses the returned hash and expires after three seconds', async t => {
  const { page, state, url } = await fixture(t);
  await page.goto(url); await login(page);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  const note = state.notes.get('n00');
  const originalHash = note.source_hash;
  const card = page.locator('#notes [data-name="memos/n00"]');
  const archive = async () => {
    await card.locator('.more').click();
    await card.getByRole('menuitem', { name: '归档', exact: true }).click();
    await page.locator('.snackbar').getByRole('button', { name: '撤销' }).waitFor();
  };
  await archive();
  assert.notEqual(note.source_hash, originalHash);
  const archivedHash = note.source_hash;
  const restoring = page.waitForRequest(request => request.method() === 'PATCH' && request.postDataJSON()?.state === 'NORMAL');
  await page.locator('.snackbar').getByRole('button', { name: '撤销' }).click();
  assert.equal((await restoring).postDataJSON().source_hash, archivedHash);
  await card.waitFor();
  assert.equal(note.state, 'NORMAL');
  await page.clock.install();
  await archive();
  await page.clock.fastForward(3001);
  assert.equal(await page.locator('.snackbar button').count(), 0);
  assert.equal(note.state, 'ARCHIVED');
});

test('long timeline notes fold by height and collapse beneath the mobile sticky header', async t => {
  const { page, state, url } = await fixture(t, 'zh-CN', { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const note = state.notes.get('n09');
  note.content = Array.from({ length: 35 }, (_, index) => `正文 ${index} · 长卡片回归`).join('\n\n');
  note.attachments = Array.from({ length: 4 }, (_, index) => ({
    name: `attachments/doc${index}`, filename: `part-${index}.pdf`, type: 'application/pdf', size: 3,
    content_url: `/api/app/v1/notes/n09/attachments/doc${index}/content`,
  }));
  await page.goto(url); await login(page);
  const card = page.locator('#notes [data-name="memos/n09"]');
  const preview = card.locator('.note-fold-body');
  const expand = card.getByRole('button', { name: '展开', exact: true });
  const collapse = card.getByRole('button', { name: '收起', exact: true });
  await expand.waitFor();
  assert.equal(await preview.evaluate(element => element.offsetHeight), 260);
  assert.equal(await preview.locator('.attachment-list a').count(), 4);
  assert.equal(await preview.locator('.note-tags').count(), 1);
  const colophon = card.locator(':scope > .note-colophon');
  assert.equal(await colophon.locator('time').innerText(), '10月29日');
  const more = colophon.getByRole('button', { name: '更多操作' });
  assert.equal(await more.evaluate(node => getComputedStyle(node).opacity), '1');
  const removed = await more.evaluate(element => {
    element.style.transition = 'none';
    let count = 0;
    function removeHoverFallback(container) {
      for (let index = container.cssRules.length - 1; index >= 0; index--) {
        const rule = container.cssRules[index];
        if (rule.conditionText === '(hover: none)' && rule.cssRules[0]?.selectorText === '.more') {
          container.deleteRule(index); count++;
        } else if (rule.cssRules) removeHoverFallback(rule);
      }
    }
    for (const sheet of document.styleSheets) removeHoverFallback(sheet);
    return count;
  });
  assert.equal(removed, 1);
  assert.equal(await more.evaluate(node => getComputedStyle(node).opacity), '1');
  await expand.click();
  assert.ok(await preview.evaluate(element => element.offsetHeight) > 380);
  await page.evaluate(() => scrollTo(0, 0));
  await collapse.scrollIntoViewIfNeeded();
  assert.ok(await card.evaluate(element => element.getBoundingClientRect().top <
    document.querySelector('.mobile-header').getBoundingClientRect().bottom));
  await collapse.click();
  await page.waitForFunction(() => {
    const card = document.querySelector('#notes [data-name="memos/n09"]');
    const header = document.querySelector('.mobile-header');
    return Math.abs(card.getBoundingClientRect().top - header.getBoundingClientRect().bottom) <= 1;
  });
  await expand.click();
  await card.locator('.note-tags').getByRole('button', { name: '#想法', exact: true }).click();
  await page.waitForURL(value => value.searchParams.get('tag') === '想法');
  await collapse.waitFor();
  await collapse.click();
  await expand.waitFor();
  await page.goto(`${url}/?query=${encodeURIComponent('长卡片回归')}`);
  await card.waitFor();
  assert.equal(await preview.count(), 0);
});

test('paper login disables incomplete and submitting forms, retaining rejected input', async t => {
  const { page, url } = await fixture(t);
  const auth = page.locator('#auth'), form = page.locator('#login-form');
  const username = page.locator('#username'), password = page.locator('#password'), submit = page.locator('#login');
  const language = auth.locator('[data-language]');
  await page.goto(url);
  await form.waitFor();
  assert.equal(await page.locator('.memos-brand').innerText(), 'Moerise');
  assert.deepEqual(await auth.evaluate(node => [getComputedStyle(node).backgroundColor,
    getComputedStyle(node.querySelector('.memos-login')).backgroundColor]), ['rgb(247, 246, 242)', 'rgba(0, 0, 0, 0)']);
  assert.deepEqual(await password.evaluate(node => {
    const style = getComputedStyle(node);
    return [style.backgroundColor, style.borderColor, style.height];
  }), ['rgb(252, 251, 248)', 'rgb(145, 139, 128)', '44px']);
  assert.equal(await submit.isDisabled(), true);
  await username.evaluate(node => { node.value = ' owner '; });
  assert.equal(await submit.isDisabled(), true);
  await password.evaluate(node => { node.value = 'sample-password'; });
  await page.waitForFunction(() => !document.getElementById('login').disabled);
  assert.equal(await submit.isEnabled(), true);
  const { promise: arrived, resolve: captured } = Promise.withResolvers();
  const { promise: hold, resolve: release } = Promise.withResolvers();
  await page.route('**/api/app/v1/session', async route => {
    if (route.request().method() !== 'POST') return route.continue();
    captured(); await hold;
    await route.fulfill({ status: 401, json: { error: { code: 'INVALID_CREDENTIALS' } } });
  }, { times: 1 });
  try {
    await password.press('Enter'); await arrived;
    for (const control of [username, password, submit, language]) assert.equal(await control.isDisabled(), true);
  } finally { release(); }
  await page.locator('#auth-error').waitFor();
  assert.equal(await username.inputValue(), ' owner ');
  assert.equal(await password.inputValue(), 'sample-password');
  assert.equal(await submit.isEnabled(), true);
  await submit.click();
  await page.locator('#workspace').waitFor();
  assert.equal(await password.inputValue(), '');
});

test('mounted timeline colophons roll over in place without refetching or moving the editor', async t => {
  const { page, state, url } = await fixture(t);
  await page.clock.install({ time: new Date('2026-10-29T15:58:00Z') });
  await page.clock.setSystemTime(new Date('2026-10-29T15:58:00Z'));
  await page.goto(url); await login(page);
  const card = page.locator('#notes [data-name="memos/n00"]');
  const date = card.locator('.note-colophon time').filter({ hasText: '12:59' });
  await date.waitFor();
  await page.locator('#content').fill('未提交的新笔记');
  const editing = page.locator('#notes [data-name="memos/n05"]');
  await editing.getByRole('button', { name: '更多操作' }).click();
  await editing.getByRole('menuitem', { name: '编辑', exact: true }).click();
  await page.locator('.editor textarea').fill('未提交的编辑');
  const scroll = await page.evaluate(() => {
    const editor = document.querySelector('.editor');
    globalThis.midnightView = { editor, date: document.querySelector('[data-name="memos/n00"] time') };
    return scrollY;
  });
  const reads = state.requests.filter(item => item.path === '/notes').length;
  await page.clock.fastForward(121_000);
  await card.locator('time').filter({ hasText: '10月29日' }).waitFor({ state: 'attached' });
  await setUILanguage(page, 'en');
  assert.equal(await card.locator('time').textContent(), 'Oct 29');
  await page.evaluate(() => {
    Object.defineProperty(document, 'hidden', { configurable: true, value: true });
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await page.clock.setSystemTime(new Date('2027-01-01T00:00:00Z'));
  assert.equal(await card.locator('time').textContent(), 'Oct 29');
  await page.evaluate(() => {
    Reflect.deleteProperty(document, 'hidden');
    document.dispatchEvent(new Event('visibilitychange'));
  });
  assert.equal(await card.locator('time').textContent(), '2026-10-29');
  assert.deepEqual(await page.evaluate(() => {
    const { editor, date } = globalThis.midnightView;
    return [editor === document.querySelector('.editor'), date === document.querySelector('[data-name="memos/n00"] time'),
      editor.querySelector('textarea') === document.activeElement, scrollY];
  }), [true, true, true, scroll]);
  assert.equal(await page.locator('.editor textarea').inputValue(), '未提交的编辑');
  assert.equal(await page.locator('#content').inputValue(), '未提交的新笔记');
  assert.equal(state.requests.filter(item => item.path === '/notes').length, reads);
});

test('service midnight keeps sidebar focus and an active date filter', async t => {
  const { page, state, url } = await fixture(t);
  await page.clock.install({ time: new Date('2026-10-29T15:58:00Z') });
  await page.clock.setSystemTime(new Date('2026-10-29T15:58:00Z'));
  await page.goto(url); await login(page);
  const day = value => page.locator(`#calendar [data-date="${value}"]`);
  const aggregates = () => state.requests.filter(({ path }) => path === '/calendar' || path === '/tags').length;
  await page.locator('#tags .tag-row').first().waitFor();
  const today = day('2026-10-29');
  assert.match(await today.getAttribute('class'), /\btoday\b/);
  await today.focus();
  const before = aggregates();
  await page.clock.fastForward(121_000);
  assert.match(await day('2026-10-30').getAttribute('class'), /\btoday\b/);
  assert.doesNotMatch(await today.getAttribute('class'), /\btoday\b/);
  assert.equal(await today.evaluate(node => node === document.activeElement), true);
  assert.equal(aggregates(), before);

  await page.clock.setSystemTime(new Date('2026-10-31T15:58:00Z'));
  await page.goto(`${url}/?date=2026-10-31`);
  const selected = day('2026-10-31');
  await page.locator('#calendar .today.sel').waitFor();
  await page.locator('#tags .tag-row').first().waitFor();
  await selected.focus();
  const filtered = aggregates();
  await page.clock.fastForward(121_000);
  assert.equal(new URL(page.url()).searchParams.get('date'), '2026-10-31');
  assert.equal(await page.locator('#calendar-title').textContent(), '2026年10月');
  assert.equal(await selected.getAttribute('aria-pressed'), 'true');
  assert.doesNotMatch(await selected.getAttribute('class'), /\btoday\b/);
  assert.equal(await selected.evaluate(node => node === document.activeElement), true);
  assert.equal(aggregates(), filtered);

  await page.clock.setSystemTime(new Date('2026-10-31T15:58:00Z'));
  await page.goto(url);
  await page.locator('#calendar .today').waitFor();
  const tag = page.locator('#tags .tag-row').first();
  await tag.focus();
  const unfiltered = aggregates();
  await page.clock.fastForward(121_000);
  await day('2026-11-01').and(page.locator('.today:enabled')).waitFor();
  assert.equal(await tag.evaluate(node => node === document.activeElement), true);
  assert.equal(aggregates(), unfiltered + 1);
});

test('one visibility event after service midnight refreshes the sidebar without taking focus', async t => {
  const { page, state, url } = await fixture(t);
  await page.clock.install({ time: new Date('2026-10-29T15:58:00Z') });
  await page.clock.setSystemTime(new Date('2026-10-29T15:58:00Z'));
  await page.goto(url); await login(page);
  const tag = page.locator('#tags [title="#想法"]');
  const day = page.locator('#calendar [data-date="2026-10-29"]');
  await tag.waitFor();
  await day.and(page.locator(':enabled')).waitFor();
  await tag.focus();
  assert.equal(await tag.locator('em').textContent(), '47');
  state.notes.set('external', { ...state.notes.get('n00'), name: 'memos/external', tags: ['想法'] });

  const result = page.waitForResponse(response => response.ok() && new URL(response.url()).pathname === '/api/app/v1/tags', { timeout: 5_000 });
  await page.clock.setSystemTime(new Date('2026-10-29T16:00:01Z'));
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
  await result;
  await day.and(page.getByRole('button', { name: '2026-10-29，48 条笔记', exact: true })).waitFor({ timeout: 5_000 });
  assert.equal(await tag.evaluate(node => node === document.activeElement), true);
  await day.focus();
  await tag.locator('em').filter({ hasText: /^48$/ }).waitFor({ timeout: 5_000 });
  assert.equal(await day.evaluate(node => node === document.activeElement), true);
});

test('a delayed tag refresh after saving a note survives the service month rollover', { timeout: 20_000 }, async t => {
  const { page, url } = await fixture(t);
  await page.clock.install({ time: new Date('2026-10-31T15:59:20Z') });
  await page.clock.setSystemTime(new Date('2026-10-31T15:59:20Z'));
  await page.goto(url); await login(page);
  await page.locator('#tags .tag-row').first().waitFor();
  await page.locator('#calendar .today:enabled').waitFor();
  const card = page.locator('#notes [data-name="memos/n00"]');
  await card.getByRole('button', { name: '更多操作' }).click();
  await card.getByRole('menuitem', { name: '编辑', exact: true }).click();
  await page.locator('form.editor textarea').fill('保存后刷新 #跨月');

  const { promise: arrived, resolve: captured } = Promise.withResolvers();
  const { promise: hold, resolve: release } = Promise.withResolvers();
  await page.route('**/api/app/v1/tags?*', async route => {
    const response = await route.fetch();
    captured(); await hold;
    await route.fulfill({ response });
  }, { times: 1 });
  try {
    await page.locator('form.editor button.save').click();
    await arrived;
    await page.clock.fastForward(41_000);
    await page.locator('#calendar .today:enabled[data-date="2026-11-01"]').waitFor({ timeout: 5_000 });
  } finally { release(); }
  await page.locator('#tags [title="#跨月"] em').filter({ hasText: /^1$/ }).waitFor({ timeout: 5_000 });
});

test('CSRF renewal discards drafts and never replays a note after an instance or author change', async t => {
  const { page, state, url } = await fixture(t);
  await page.goto(url);
  for (const [field, value] of [['instance', 'second'], ['user', 'users/2']]) {
    await login(page);
    await page.locator('[data-name="memos/n00"]').waitFor();
    await page.locator('#content').fill(`private-${field}`);
    state.csrfRejected = true;
    state[field] = value;
    await page.locator('#save').click();
    await page.locator('#login-form').waitFor({ state: 'visible' });
    assert.equal(await page.locator('#notes article').count(), 0);
    assert.equal(await page.locator('#content').inputValue(), '');
    assert.equal(state.created, 0);
  }
  assert.equal(state.requests.filter(item => item.path === '/notes' && item.method === 'POST').length, 2);
});

test('large tag lists share sidebar scrolling and reveal account controls on upward scrolling and keyboard focus', { timeout: 90_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  const extra = Array.from({ length: 180 }, (_, index) => `分类-${String(index).padStart(3, '0')}`);
  state.notes.get('n00').tags.push(...extra);
  await page.goto(url); await login(page);
  await page.locator('#tags button').nth(181).waitFor({ state: 'attached' });
  assert.match(await page.locator('#tags button').first().innerText(), /#想法/);
  assert.equal(await page.locator('#sidebar input[type="search"]').count(), 1);
  await page.locator('#tags button', { hasText: '#分类-179' }).click();
  await page.waitForURL('**/?tag=*');
  await page.locator('#tags button[aria-pressed="true"]').waitFor({ state: 'attached' });
  assert.match(await page.locator('#tags button').first().innerText(), /#分类-179/);
  const body = page.locator('.sidebar-body');
  const accountVisible = hidden => page.waitForFunction(hidden => {
    const side = document.getElementById('sidebar'), account = document.getElementById('account');
    const frame = document.querySelector('.sidebar-content').getBoundingClientRect();
    return hidden ? side.classList.contains('account-hidden') && account.getBoundingClientRect().top >= frame.bottom - 1 :
      !side.classList.contains('account-hidden') && getComputedStyle(account).transform === 'none';
  }, hidden);
  const metrics = () => body.evaluate(node => ({
    top: node.scrollTop, height: node.clientHeight, extent: node.scrollHeight, page: window.scrollY,
  }));
  for (const language of ['zh-CN', 'en']) {
    await setUILanguage(page, language);
    await page.emulateMedia({ reducedMotion: language === 'en' ? 'reduce' : 'no-preference' });
    for (const viewport of [{ width: 1360, height: 960 }, { width: 390, height: 844 }, { width: 932, height: 430 }]) {
      await page.setViewportSize(viewport);
      const mobile = viewport.width <= 960;
      if (mobile) await page.locator('#navigation-toggle').click();
      if (language === 'zh-CN' && !mobile) await body.evaluate(node => { node.scrollTop = 0; });
      await accountVisible(false);
      await body.evaluate(node => { node.scrollTop = 0; });
      await page.waitForFunction(() => document.querySelector('.sidebar-body').scrollTop === 0);
      assert.deepEqual(await page.locator('#sidebar *').evaluateAll(nodes => nodes
        .filter(node => /^(auto|scroll)$/.test(getComputedStyle(node).overflowY) && node.scrollHeight > node.clientHeight)
        .map(node => node.className)), ['sidebar-body']);
      assert.equal(await page.locator('#tags').evaluate(node => node.scrollTop), 0);
      assert.ok((await page.locator('#account').boundingBox()).y > viewport.height - 140);
      if (language === 'en') {
        assert.equal(await page.locator('#account-settings').getAttribute('aria-label'), 'Settings');
        assert.equal(await page.locator('#account').evaluate(node => getComputedStyle(node).transitionDuration), '0s');
      }

      await body.hover({ position: { x: 40, y: 60 } });
      const before = await metrics();
      await page.mouse.wheel(0, 720);
      await accountVisible(true);
      const down = await metrics();
      assert.ok(down.top > before.top);
      assert.equal(down.page, before.page);
      await body.evaluate(() => new Promise(resolve => {
        let frames = 12;
        function next() { if (--frames) requestAnimationFrame(next); else resolve(); }
        requestAnimationFrame(next);
      }));
      assert.deepEqual(await metrics(), down);
      await page.mouse.wheel(0, -32);
      await accountVisible(false);
      const up = await metrics();
      assert.ok(up.top > 0 && up.top < down.top);
      assert.equal(up.height, down.height);
      assert.equal(up.extent, down.extent);

      await page.mouse.wheel(0, 256);
      await accountVisible(true);
      await page.locator('#tags button').last().focus();
      await page.keyboard.press('Tab');
      assert.equal(await page.locator('#account-settings').evaluate(node => node === document.activeElement), true);
      await accountVisible(false);
      assert.equal(await page.locator('.sidebar-content').evaluate(node => node.scrollTop), 0);
      await page.keyboard.press('Shift+Tab');
      assert.equal(await page.locator('#tags button').last().evaluate(node => node === document.activeElement), true);
      const focusedTag = await page.locator('#tags button').last().boundingBox();
      assert.ok(focusedTag.y + focusedTag.height <= (await page.locator('#account').boundingBox()).y);
      await page.keyboard.press('Tab');
      await page.keyboard.press('Tab');
      assert.equal(await page.locator('#sync-trigger').evaluate(node => node === document.activeElement), true);

      await page.locator('#tags button').last().focus();
      await page.keyboard.press('Control+Home');
      await page.waitForFunction(() => document.querySelector('.sidebar-body').scrollTop === 0);
      await accountVisible(false);
      await body.hover({ position: { x: 40, y: 60 } });
      await page.mouse.wheel(0, 640);
      await accountVisible(true);
      if (mobile) {
        await page.locator('#navigation-close').click();
        await page.locator('#navigation-toggle').click();
        await accountVisible(false);
        assert.ok((await metrics()).top > 0);
        await page.keyboard.press('Escape');
      }
    }
  }
  assert.deepEqual(errors, []);
});

test('browser explicit search modes, relevance pagination, filters, return and model fallback', { timeout: 120_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(url);
  await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  assert.equal(await page.locator('#search-mode').inputValue(), 'keyword');
  assert.equal(state.requests.some(request => request.path === '/search'), false);
  await page.locator('#q').fill('园艺联想');
  await page.locator('#search-mode').selectOption('semantic');
  await page.locator('[data-name="memos/n46"]').waitFor();
  assert.equal(await page.locator('#notes [data-name]').first().getAttribute('data-name'), 'memos/n46');
  const first = state.requests.findLast(request => request.path === '/search');
  assert.deepEqual(first.body, { query: '园艺联想', mode: 'semantic', tags: [], date: '', state: 'NORMAL', page_size: 20, page_token: '' });
  await page.locator('#load-more').click();
  await page.locator('[data-name="memos/n07"]').waitFor();
  await page.locator('[data-name="memos/n07"] .dlink').click();
  await page.locator('article.note').waitFor();
  await page.reload();
  await page.locator('article.note').waitFor();
  await page.locator('#back').click();
  await page.locator('[data-name="memos/n46"]').waitFor();
  assert.equal(new URL(page.url()).searchParams.get('mode'), 'semantic');
  assert.equal(await page.locator('#search-mode').inputValue(), 'semantic');
  await page.locator('#tags button', { hasText: '#灵感' }).click();
  await page.locator('#calendar button[aria-label^="2026-10-29"]').click();
  await page.waitForFunction(() => new URL(location.href).searchParams.get('date') === '2026-10-29');
  await page.waitForFunction(() => new URL(location.href).searchParams.get('date') === '2026-10-29' &&
    document.querySelector('#notes [data-name]') && document.getElementById('notes').getAttribute('aria-busy') === 'false');
  const combined = state.requests.findLast(request => request.path === '/search');
  assert.deepEqual(combined.body.tags, ['灵感']);
  assert.equal(combined.body.date, '2026-10-29');
  assert.equal(combined.body.mode, 'semantic');
  state.embedding = false;
  await page.locator('#search-mode').selectOption('hybrid');
  await page.locator('#capability', { hasText: '语义检索暂不可用' }).waitFor();
  await page.locator('#search-mode').selectOption('semantic');
  await page.locator('#list-error', { hasText: 'Embedding' }).waitFor();
  await page.locator('#search-mode').selectOption('keyword');
  await page.locator('#q').fill('笔记 4');
  await page.locator('#q').press('Enter');
  await page.locator('[data-name="memos/n40"]').waitFor();
  assert.equal(await page.locator('#capability').isHidden(), true);
  await page.setViewportSize({ width: 390, height: 844 });
  if (await page.locator('#q').isHidden()) await page.locator('#search-toggle').click();
  await page.locator('#search-mode').selectOption('hybrid');
  await page.locator('#capability', { hasText: '语义检索暂不可用' }).waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await screenshot(page, 'search-mobile.png');
  state.embedding = true;
  await page.goto(`${url}/archived?query=归档&mode=semantic`);
  await page.locator('[data-name="memos/old"]').waitFor();
  assert.equal(state.requests.findLast(request => request.path === '/search').body.state, 'ARCHIVED');
  state.searchDelay = 250;
  const delayed = page.waitForRequest(request => request.url().endsWith('/search'));
  await page.locator('#search-toggle').click();
  await page.locator('#q').fill('迟到的查询');
  await page.locator('#q').press('Enter');
  await delayed;
  await page.locator('#navigation-toggle').click();
  await page.locator('a[href="/"]').click();
  await page.locator('[data-name="memos/n00"]').waitFor();
  await page.waitForTimeout(350);
  assert.equal(await page.locator('[data-name="memos/old"]').count(), 0);
  assert.deepEqual(errors, []);
});

test('archive tags stay scoped to archive while calendar dates always filter home', async t => {
  const { page, state, url } = await fixture(t);
  const archived = state.notes.get('old');
  archived.tags = ['归档标签'];
  await page.goto(url); await login(page);
  const day = page.locator('#calendar [data-date="2026-10-29"]');
  const archiveTag = page.locator('#tags button[title="#归档标签"]');
  const normalTag = page.locator('#tags button[title="#想法"]');
  for (const route of ['/settings', '/review']) {
    await page.locator('nav a[href="/archived"]').click();
    await archiveTag.click();
    await page.waitForURL(value => value.pathname === '/archived' && value.searchParams.get('tag') === '归档标签');
    assert.equal(await day.getAttribute('aria-label'), '2026-10-29，47 条笔记');
    await page.locator(route === '/settings' ? '#account-settings' : 'nav a[href="/review"]').click();
    await normalTag.waitFor();
    assert.equal(await archiveTag.count(), 0);
    assert.equal(await day.getAttribute('aria-label'), '2026-10-29，47 条笔记');
    await day.click();
    await page.waitForURL(value => value.pathname === '/' && value.search === '?date=2026-10-29');
    await page.locator('[data-name="memos/n00"]').waitFor();
    await day.click();
    await page.waitForURL(value => value.pathname === '/' && !value.search);
  }
  assert.ok(state.requests.some(request => request.path === '/tags' && request.query.get('state') === 'ARCHIVED'));
  assert.ok(state.requests.filter(request => request.path === '/calendar').every(request => request.query.get('state') === 'NORMAL'));
});

test('timeline colophons align with full touch targets below both tagged and untagged bodies', async t => {
  const { page, state, url } = await fixture(t);
  state.notes.get('n00').content = '带标签正文\n\n#想法';
  state.notes.get('n00').tags = ['想法'];
  state.notes.get('n01').content = '正文';
  state.notes.get('n01').tags = [];
  await page.goto(url); await login(page);
  for (const [id, tagged] of [['n00', true], ['n01', false]]) {
    const layout = await page.locator(`article[data-name="memos/${id}"]`).evaluate(node => {
      const rect = selector => node.querySelector(selector).getBoundingClientRect();
      const content = rect('.note-content'), tags = rect('.note-tags'), body = rect('.note-fold-body');
      const date = rect('.note-colophon .dlink'), more = rect('.note-colophon .more');
      const dots = rect('.note-colophon .more svg');
      return {
        tags: tags.height ? tags.top - content.bottom : null,
        dateTop: date.top - body.bottom, dateBottom: node.getBoundingClientRect().bottom - date.bottom,
        hitTop: more.top - body.bottom, width: more.width, height: more.height,
        icon: (dots.top + dots.bottom - date.top - date.bottom) / 2,
      };
    });
    assert.equal(layout.tags, tagged ? 8 : null);
    assert.ok(layout.dateTop >= 8 && layout.dateTop <= 10, JSON.stringify(layout));
    assert.ok(layout.dateBottom >= 14 && layout.dateBottom <= 16, JSON.stringify(layout));
    assert.ok(layout.hitTop >= 0, JSON.stringify(layout));
    assert.equal(layout.width, 44);
    assert.equal(layout.height, 44);
    assert.ok(Math.abs(layout.icon) <= 1, JSON.stringify(layout));
  }
});

test('note images center independently of file attachments', async t => {
  const { page, state, url } = await fixture(t);
  const note = state.notes.get('n02');
  const source = note.content;
  note.content = '图片与文件';
  await page.goto(url); await login(page);
  const card = page.locator('article[data-name="memos/n02"]');
  const image = card.locator('.attachment-list img');
  await image.waitFor();
  const centered = node => {
    const bounds = node.getBoundingClientRect();
    const parent = node.closest('.attachment-list, .note-content').getBoundingClientRect();
    return Math.abs(bounds.left + bounds.right - parent.left - parent.right) <= 2;
  };
  assert.equal(await image.evaluate(centered), true);
  const file = card.locator('.attachment-list .attachment');
  assert.equal(await file.evaluate(node => node.getBoundingClientRect().left ===
    node.closest('.attachment-list').getBoundingClientRect().left), true);
  note.content = source;
  await page.reload();
  const inline = card.locator('.note-content img');
  await inline.waitFor();
  assert.equal(await inline.evaluate(centered), true);
});
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

test('mobile navigation, touch gestures, search and rotation preserve drafts and keyboard access', { timeout: 60_000 }, async t => {
  const { page, context, state, errors, url } = await fixture(t, 'zh-CN', {
    viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true,
  });
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  const drawer = page.getByRole('dialog', { name: '主导航' });
  const toggle = page.locator('#navigation-toggle'), search = page.locator('#q');
  assert.equal(await drawer.isVisible(), false);
  assert.equal(await search.isVisible(), false);
  assert.equal((await page.locator('.mobile-toolbar').boundingBox()).height, 56);
  assert.ok((await page.locator('#composer').boundingBox()).y < 90);
  await page.locator('#content').fill('旋转与筛选保留这份草稿');
  await page.locator('#content').blur();
  await screenshot(page, 'home-touch.png');

  await toggle.tap();
  await drawer.waitFor({ state: 'visible' });
  assert.equal(await page.locator('#navigation-close').evaluate(node => node === document.activeElement), true);
  await page.locator('#calendar').waitFor({ state: 'visible' });
  await drawer.evaluate(node => Promise.all(node.getAnimations().map(animation => animation.finished)));
  await screenshot(page, 'navigation-mobile.png');
  await page.locator('#account-settings').focus();
  await page.keyboard.press('Tab');
  assert.equal(await page.evaluate(() => document.activeElement === document.body ||
    document.querySelector('#navigation-dialog').contains(document.activeElement)), true);
  await page.locator('#navigation-close').focus();
  await page.locator('#content').evaluate(node => node.focus());
  assert.equal(await page.locator('#navigation-close').evaluate(node => node === document.activeElement), true);
  await page.keyboard.press('Escape');
  await drawer.waitFor({ state: 'hidden' });
  assert.equal(await toggle.evaluate(node => node === document.activeElement), true);
  await toggle.tap();
  await page.touchscreen.tap(380, 120);
  await drawer.waitFor({ state: 'hidden' });
  await toggle.tap();
  await page.locator('a[data-route][href="/"]').tap();
  await drawer.waitFor({ state: 'hidden' });
  assert.equal(await page.locator('#content').inputValue(), '旋转与筛选保留这份草稿');

  await toggle.tap();
  await page.locator('#tags button', { hasText: '#灵感' }).tap();
  await drawer.waitFor({ state: 'hidden' });
  await page.waitForURL('**/?tag=*');
  await toggle.tap();
  await page.locator('#calendar button[aria-label^="2026-10-29"]').tap();
  await drawer.waitFor({ state: 'hidden' });
  await page.waitForFunction(() => new URL(location.href).searchParams.get('date') === '2026-10-29');
  assert.equal(await page.locator('#content').inputValue(), '旋转与筛选保留这份草稿');
  await page.locator('#filters').getByRole('button', { name: '清除全部' }).tap();

  await page.getByRole('button', { name: '搜索笔记', exact: true }).tap();
  assert.equal(await search.evaluate(node => node === document.activeElement), true);
  await search.fill('笔记 4'); await search.press('Enter');
  await page.locator('[data-name="memos/n40"]').waitFor();
  await page.locator('#search-mode').selectOption('hybrid');
  await page.locator('[data-name="memos/n46"]').waitFor();
  await screenshot(page, 'search-touch.png');
  await page.getByRole('button', { name: '收起搜索', exact: true }).tap();
  assert.equal(await search.isVisible(), false);
  await page.locator('#search-toggle').tap();
  assert.equal(await search.inputValue(), '笔记 4');
  assert.equal(await page.locator('#search-mode').inputValue(), 'hybrid');
  await page.keyboard.press('Escape');
  assert.equal(await search.isVisible(), false);
  assert.equal(await page.locator('#search-toggle').evaluate(node => node === document.activeElement), true);

  const client = await context.newCDPSession(page);
  const swipe = async (from, to) => {
    await client.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: from[0], y: from[1], id: 1 }] });
    for (let step = 1; step <= 8; step++) {
      const fraction = step / 8;
      await client.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{
        x: from[0] + (to[0] - from[0]) * fraction, y: from[1] + (to[1] - from[1]) * fraction, id: 1,
      }] });
      await page.waitForTimeout(16);
    }
    await client.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  };
  await swipe([8, 600], [10, 450]);
  assert.equal(await drawer.isVisible(), false);
  await swipe([8, 220], [125, 220]);
  await drawer.waitFor({ state: 'visible' });
  await drawer.evaluate(node => Promise.all(node.getAnimations().map(animation => animation.finished)));
  await swipe([220, 220], [90, 220]);
  await drawer.waitFor({ state: 'hidden' });

  await toggle.tap();
  await setUILanguage(page, 'en');
  await page.getByRole('button', { name: 'Close navigation', exact: true }).tap();
  await page.getByRole('button', { name: 'Search notes', exact: true }).tap();
  await page.getByRole('button', { name: 'Close search', exact: true }).waitFor();
  assert.equal(await search.inputValue(), '笔记 4');
  await screenshot(page, 'search-en-mobile.png');
  await page.getByRole('button', { name: 'Close search', exact: true }).tap();
  for (const size of [{ width: 320, height: 568 }, { width: 430, height: 932 }, { width: 932, height: 430 }]) {
    await page.setViewportSize(size);
    await page.getByRole('button', { name: 'Open navigation', exact: true }).waitFor();
    assert.equal(await search.isVisible(), false);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  }
  await screenshot(page, 'home-landscape.png');
  await toggle.tap();
  await page.locator('#account-settings').scrollIntoViewIfNeeded();
  assert.ok((await page.locator('#account-settings').boundingBox()).y < 430);
  await page.getByRole('button', { name: 'Close navigation', exact: true }).tap();
  await page.setViewportSize({ width: 1280, height: 900 });
  await search.waitFor({ state: 'visible' });
  assert.equal(await page.locator('#mobile-header').isVisible(), false);
  assert.equal(await search.inputValue(), '笔记 4');
  await search.focus();
  await page.setViewportSize({ width: 390, height: 844 });
  await search.waitFor({ state: 'visible' });
  assert.equal(await search.evaluate(node => node === document.activeElement), true);
  assert.equal(await page.locator('#content').inputValue(), '旋转与筛选保留这份草稿');

  await toggle.tap();
  state.session = false;
  await page.locator('#month-next').tap();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#navigation-dialog').isVisible(), false);
  assert.notEqual(await page.evaluate(() => getComputedStyle(document.documentElement).overflow), 'hidden');
  assert.equal(await page.locator('#notes').textContent(), '');
  assert.deepEqual(errors, []);
});

test('mobile sync conflict has one cloud status and a distinct sidebar action', async t => {
  const { page, state, url } = await fixture(t, 'zh-CN', {
    viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true,
  });
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  await menu(page, 'n00', '编辑');
  const editor = page.locator('form.editor');
  await editor.locator('textarea').fill('引发冲突的修改');
  state.conflict = true;
  await editor.locator('textarea').press('Control+Enter');
  const conflict = page.getByRole('img', { name: '同步冲突' });
  await conflict.waitFor();
  const statusPath = await conflict.locator('path').getAttribute('d');

  await page.locator('#navigation-toggle').tap();
  const action = page.locator('#sync-trigger');
  await action.waitFor();
  assert.notEqual(await action.locator('path').getAttribute('d'), statusPath);
  assert.equal(await action.locator('svg').getAttribute('aria-hidden'), 'true');
  assert.equal(await page.locator('#sync-status[data-sync="conflict"]').count(), 1);
  assert.equal(await conflict.locator('.sync-dot').count(), 0);
  assert.equal(await conflict.evaluate(node => getComputedStyle(node).color), 'rgb(180, 35, 24)');
});

test('search composition, menu keyboard, route focus and refresh protection preserve input', async t => {
  const { page, state, url, errors } = await fixture(t);
  await page.goto(url); await login(page);
  const query = page.locator('#q');
  await query.focus();
  await query.evaluate(node => {
    node.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }));
    node.value = '组词中的文字';
    node.dispatchEvent(new InputEvent('input', { bubbles: true, isComposing: true }));
    node.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, keyCode: 229, bubbles: true }));
  });
  await page.waitForTimeout(300);
  assert.equal(new URL(page.url()).search, '');
  await query.evaluate(node => { node.value = '笔记'; node.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })); });
  await page.waitForURL(value => value.searchParams.get('query') === '笔记');
  assert.ok(await query.evaluate(node => node === document.activeElement));
  await query.fill(''); await query.press('Enter');
  await page.waitForURL(value => !value.search);
  await page.locator('#notes[aria-busy="false"]').waitFor();
  const more = page.locator('[data-name="memos/n00"] .more');
  await more.focus(); await page.keyboard.press('ArrowDown');
  await page.locator('[data-name="memos/n00"] .more[aria-expanded="true"]').waitFor();
  await page.getByRole('menuitem', { name: '编辑', exact: true }).waitFor();
  assert.equal(await page.evaluate(() => document.activeElement.textContent), '编辑');
  await page.keyboard.press('End');
  assert.equal(await page.evaluate(() => document.activeElement.textContent), '删除');
  await page.keyboard.press('Home');
  await page.keyboard.press('ArrowDown');
  assert.equal(await page.evaluate(() => document.activeElement.textContent), '打开笔记');
  await page.keyboard.press('Escape');
  assert.ok(await more.evaluate(node => node === document.activeElement));
  const link = page.locator('[data-name="memos/n00"] .dlink');
  await link.focus(); await page.keyboard.press('Enter');
  await page.locator('article.note').waitFor();
  assert.equal(await page.evaluate(() => document.activeElement.id), 'main');
  await page.locator('#back').click();
  await page.waitForFunction(() => document.activeElement.matches('.dlink'));
  await page.locator('#content').fill('刷新保护正文');
  const rejected = page.waitForEvent('dialog').then(dialog => { assert.equal(dialog.type(), 'beforeunload'); return dialog.dismiss(); });
test('successful writes are silent, failed and unconfirmed writes use the warning cloud, and offline is visible', async t => {
  const { page, context, url } = await fixture(t);
  await page.goto(url); await login(page);
  const status = page.locator('#sync-status');
  assert.equal(await status.isHidden(), true);
  let release;
  t.after(() => release?.());
  const gate = new Promise(resolve => { release = resolve; });
  const writes = route => route.request().method() === 'POST';
  const endpoint = '**/api/app/v1/notes';
  const delayed = async route => {
    if (writes(route)) await gate;
    await route.continue();
  };
  await page.route(endpoint, delayed);
  const request = page.waitForRequest(req => req.url().endsWith('/notes') && req.method() === 'POST');
  await page.locator('#content').fill('测试同步');
  await page.locator('#save').click();
  await request;
  assert.equal(await status.isHidden(), true);
  release();
  await page.locator('[data-name="memos/created-1"]').waitFor();
  assert.equal(await status.isHidden(), true);
  await page.unroute(endpoint, delayed);
  await page.locator('#content').fill('测试失败与重试');
  for (const [error, mode] of [['UPSTREAM_UNAVAILABLE', 'failure'], ['WRITE_OUTCOME_UNKNOWN', 'unknown']]) {
    const fail = route => writes(route) ? route.fulfill({ status: 503, json: { error: { code: error } } }) : route.continue();
    await page.route(endpoint, fail);
    await page.locator('#save').click();
    await status.and(page.locator(`[data-sync="${mode}"]`)).waitFor();
    assert.equal(await status.locator('.sync-dot').count(), 0);
    await page.unroute(endpoint, fail);
  }
  await context.setOffline(true);
  await status.and(page.locator('[data-sync="offline"]')).waitFor();
  assert.equal(await status.locator('.sync-dot').count(), 0);
  await context.setOffline(false);
});

  const reload = page.reload({ timeout: 1500 }).catch(() => {});
  await rejected; await reload;
  assert.equal(await page.locator('#content').inputValue(), '刷新保护正文');
  await page.locator('#account-settings').click(); await page.locator('#logout').click();
  await page.getByRole('button', { name: '继续编辑', exact: true }).click();
  assert.equal(state.session, true);
  assert.equal(await page.locator('#content').inputValue(), '刷新保护正文');
  assert.deepEqual(await page.evaluate(() => Object.keys(localStorage)), ['moerise.privacy.v1']);
  assert.equal(await page.evaluate(() => sessionStorage.length), 0);
  assert.deepEqual(errors, []);
});

for (const mode of ['no-popover', 'no-anchor']) test(`menu fallback follows scrolling and viewport changes with ${mode}`, async t => {
  const { page, url } = await fixture(t);
  await page.addInitScript(mode => {
    if (mode === 'no-popover') delete HTMLElement.prototype.popover;
    else {
      const supports = CSS.supports.bind(CSS);
      CSS.supports = (...args) => String(args[0]).includes('position-area') ? false : supports(...args);
    }
    delete HTMLElement.prototype.focusGroup;
  }, mode);
  await page.goto(url); await login(page);
  const more = page.locator('[data-name="memos/n00"] .more');
  await more.waitFor();
  if (mode === 'no-anchor') await page.locator('.menu').evaluateAll(nodes => {
    for (const menu of nodes) { menu.style.setProperty('position-area', 'none'); menu.style.positionAnchor = 'auto'; }
  });
  await more.focus(); await page.keyboard.press('ArrowDown');
  const edit = page.getByRole('menuitem', { name: '编辑', exact: true });
  await edit.and(page.locator(':focus')).waitFor();
  assert.equal(await edit.evaluate(node => node === document.activeElement), true);
  const anchor = await more.boundingBox(), menu = await page.locator('.menu').filter({ has: edit }).boundingBox();
  assert.ok(Math.abs(menu.y - anchor.y - anchor.height - 4) < 2);
  assert.ok(menu.x >= 8 && menu.x + menu.width <= 1360 - 8);
  await page.keyboard.press('End');
  assert.equal(await page.evaluate(() => document.activeElement.textContent), '删除');
  await page.keyboard.press('Escape');
  assert.equal(await more.evaluate(node => node === document.activeElement), true);

  await more.click(); await edit.waitFor();
  await page.evaluate(() => scrollBy(0, 80));
  await page.waitForFunction(() => {
    const trigger = document.querySelector('[data-name="memos/n00"] .more').getBoundingClientRect();
    const menu = document.querySelector('[data-name="memos/n00"] .menu').getBoundingClientRect();
    return Math.abs(menu.top - trigger.bottom - 4) < 2;
  });
  await page.setViewportSize({ width: 800, height: 700 });
  await page.waitForFunction(() => {
    const menu = document.querySelector('[data-name="memos/n00"] .menu').getBoundingClientRect();
    return menu.left >= 8 && menu.right <= innerWidth - 8 && menu.top >= 8 && menu.bottom <= innerHeight - 8;
  });
  await page.evaluate(() => scrollBy(0, 500));
  await edit.waitFor({ state: 'hidden' });
  assert.equal(await more.getAttribute('aria-expanded'), 'false');
});

test('browser timeline management, attachment retries, history, conflicts and private cleanup', { timeout: 120_000 }, async t => {
  const { page, context, state, errors, url } = await fixture(t);
  await page.goto(`${url}/notes/n02?from=${encodeURIComponent('/?tag=想法')}`);
  await login(page);
  await page.locator('article.note').waitFor();
  assert.equal(new URL(page.url()).pathname, '/notes/n02');
  assert.equal(await page.locator('article.note img').count(), 1);
  assert.equal(await page.locator('.attachment-list img').count(), 0);
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
  await page.waitForFunction(() => window.scrollY > 100);
  await page.goForward();
  await page.locator('article.note').waitFor();
  await page.reload();
  await page.locator('article.note').waitFor();
  await page.locator('#back').click();
  await page.locator('[data-name="memos/n46"]').waitFor();
  await page.waitForFunction(() => window.scrollY > 100);
  await page.locator('a[href="/review"]').click();
  await page.locator('.review-positions').waitFor();
  await page.locator('#q').fill('笔记 4');
  await page.locator('#q').press('Enter');
  await page.waitForFunction(() => location.pathname === '/' && new URL(location.href).searchParams.get('query') === '笔记 4');
  await page.locator('[data-name="memos/n40"]').waitFor();
  await page.locator('#q').fill('');
  await page.locator('#q').press('Enter');
  await page.getByRole('button', { name: '清除全部', exact: true }).click();
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
  await menu(page, 'n00', '恢复');
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
  await page.locator('#navigation-toggle').click();
  await page.locator('#calendar').waitFor({ state: 'visible' });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.locator('#navigation-close').click();
  await page.locator('#content').fill('退出应清理的草稿');
  await page.locator('#attachment-input').setInputFiles({ name: 'private.txt', mimeType: 'text/plain', buffer: Buffer.from('private') });
  await page.locator('#navigation-toggle').click();
  await page.locator('#account-settings').click();
  await page.locator('#logout').click();
  await page.getByRole('button', { name: '放弃修改', exact: true }).click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#notes').textContent(), '');
  assert.equal(await page.locator('#content').inputValue(), '');
  assert.equal(await page.locator('#draft-attachments').textContent(), '');
  await login(page);
  await page.locator('#navigation-toggle').click();
  await page.locator('nav a[href="/"]').click();
  await page.locator('[data-name="memos/n00"]').waitFor();
  assert.equal(await page.locator('#draft-attachments').textContent(), '');
  assert.deepEqual(await page.evaluate(() => [...Object.keys(localStorage), ...Object.keys(sessionStorage)]), ['moerise.privacy.v1']);
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
  await page.locator('#account-settings').click();
  await page.locator('#logout').click();
  await page.getByRole('button', { name: '放弃修改', exact: true }).click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  await page.evaluate(() => { globalThis.releaseFile(); File.prototype.arrayBuffer = globalThis.originalRead; });
  await login(page);
  await page.locator('nav a[href="/"]').click();
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
    const list = request => request.pathname === '/api/app/v1/notes' && (request.searchParams.get('query') || '') === query;
    await page.route(list, route => { state.session = false; return route.continue(); }, { times: 1 });
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
  await page.route('**/api/app/v1/calendar?*month=2026-11', async route => {
    state.session = false;
    await route.continue();
  }, { times: 1 });
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

test('sidebar reuses aggregates, cancels obsolete months and refreshes only affected counts', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  let release;
  const delayed = new Promise(resolve => { release = resolve; });
  await page.route(/\/api\/app\/v1\/(?:tags|calendar)\?/, async route => { await delayed; await route.continue().catch(() => {}); });
  await page.goto(url); await login(page); await page.locator('[data-name="memos/n00"]').waitFor();
  for (const query of ['笔记', '笔记 1', '笔记 2']) {
    await page.locator('#q').fill(query); await page.locator('#q').press('Enter');
    await page.waitForFunction(query => new URL(location.href).searchParams.get('query') === query, query);
  }
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
  await page.locator('#save').click();
  await page.waitForFunction(() => document.getElementById('content').value === '');
  await page.locator('#tags button').first().waitFor();
  assert.equal(count('/tags'), before.tags); assert.equal(count('/calendar'), before.calendar);
  await page.locator('#month-next').click();
  await page.waitForFunction(() => document.getElementById('calendar-title').textContent === '2026年9月');
  const october = page.waitForResponse(response => response.url().includes('month=2026-10') && response.ok());
  await page.locator('#month-next').click(); await october;
  const refreshed = { tags: count('/tags'), calendar: count('/calendar') };
  await page.clock.setFixedTime(new Date('2026-10-30T04:01:01Z'));
  await page.locator('#q').fill('写入后'); await aggregatesAfter(() => page.locator('#q').press('Enter'));
  await page.locator('#tags button').first().waitFor();
  assert.equal(count('/tags'), refreshed.tags + 1); assert.equal(count('/calendar'), refreshed.calendar + 1);
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await page.locator('#tags button').first().waitFor();
  assert.equal(count('/tags'), refreshed.tags + 1); assert.equal(count('/calendar'), refreshed.calendar + 1);
  assert.deepEqual(errors, []);
});

test('sidebar selection and heatmap use warm-paper tones and a 3px current-day marker', async t => {
  const { page, url } = await fixture(t);
  await page.goto(url); await login(page);
  const activeStyle = node => {
    const { color, backgroundColor, fontWeight } = getComputedStyle(node);
    return [color, backgroundColor, fontWeight];
  };
  const accent = 'rgb(47, 93, 98)', muted = 'rgb(236, 241, 240)';
  assert.deepEqual(await page.locator('nav .ent.on').evaluate(activeStyle), [accent, muted, '600']);
  await page.locator('#tags .tag-row').first().click();
  assert.deepEqual(await page.locator('#tags .tag-row.on').evaluate(activeStyle), [accent, muted, '600']);
  const current = page.locator('#calendar .today');
  const marker = node => {
    const { backgroundColor, width, height, borderRadius, left } = getComputedStyle(node, '::after');
    return [backgroundColor, width, height, borderRadius,
      Math.abs(parseFloat(left) + 1.5 - node.getBoundingClientRect().width / 2) <= 1];
  };
  assert.deepEqual(await current.evaluate(activeStyle), [accent, 'rgb(227, 231, 227)', '600']);
  assert.deepEqual(await page.locator('#calendar .h2').evaluate(activeStyle), [accent, 'rgb(204, 215, 211)', '500']);
  assert.equal(await current.evaluate(node => getComputedStyle(node).boxShadow), 'none');
  assert.deepEqual(await current.evaluate(marker), [accent, '3px', '3px', '50%', true]);
  const luminance = values => values.map(value => value / 255)
    .map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4)
    .reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0);
  for (const background of [[227, 231, 227], [204, 215, 211], [236, 241, 240]]) {
    assert.ok((luminance(background) + .05) / (luminance([47, 93, 98]) + .05) >= 4.5);
  }
  await current.click();
  const today = page.locator('#calendar .today.sel');
  await today.waitFor();
  assert.deepEqual(await today.evaluate(activeStyle), ['rgb(255, 255, 255)', accent, '600']);
  assert.deepEqual(await today.evaluate(marker), ['rgb(255, 255, 255)', '3px', '3px', '50%', true]);
  await page.locator('#account-settings').click();
  assert.deepEqual(await page.locator('#account-settings').evaluate(activeStyle), [accent, muted, '600']);
});

test('primary actions retain white text and note lists sit close to top-level paragraphs', async t => {
  const { page, state, url } = await fixture(t);
  state.notes.get('n00').content = '段落\n\n- 无序列表\n1.有序列表\n2、普通写法';
  await page.goto(url);
  await page.locator('#username').fill('test'); await page.locator('#password').fill('test');
  const primaryStyle = node => {
    const { backgroundColor, color } = getComputedStyle(node);
    return [backgroundColor, color];
  };
  const signIn = page.locator('#login');
  await signIn.hover();
  assert.deepEqual(await signIn.evaluate(primaryStyle), ['rgb(37, 75, 79)', 'rgb(255, 255, 255)']);
  await signIn.click();
  await page.locator('#content').fill('未保存正文');
  const save = page.locator('#save');
  assert.deepEqual(await save.evaluate(primaryStyle), ['rgb(47, 93, 98)', 'rgb(255, 255, 255)']);
  assert.equal(await save.locator('small').evaluate(node => getComputedStyle(node).color), 'rgb(232, 240, 238)');
  const body = page.locator('article[data-name="memos/n00"] .note-content');
  await body.waitFor();
  const layout = await body.evaluate(node => {
    const [intro, list, ordinary] = [...node.children].map(child => child.getBoundingClientRect());
    return { gap: list.top - intro.bottom, left: intro.left, ordinaryLeft: ordinary.left };
  });
  assert.ok(layout.gap >= 6 && layout.gap <= 8);
  assert.equal(layout.left, layout.ordinaryLeft);
  assert.match(await body.locator('p').last().innerText(), /^1\.有序列表\n2、普通写法$/);
});

test('sidebar background refresh preserves tags and calendar through slow requests, failures and retry', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(url); await login(page);
  await page.locator('#calendar .h2').waitFor();
  await page.locator('#tags button', { hasText: '#想法' }).click();
  await page.locator('#tags button.on').waitFor();
  const snapshot = () => page.evaluate(() => ['tags', 'calendar'].map(id => document.getElementById(id).innerHTML));
  let displayed = await snapshot(), release, fail = false;
  let delayed = new Promise(resolve => { release = resolve; });
  t.after(() => release());
  await page.route(/\/api\/app\/v1\/(?:tags|calendar)\?/, async route => {
    await delayed;
    if (fail) await route.fulfill({ status: 503, json: { error: { code: 'SOURCE_UNAVAILABLE', retryable: true } } });
    else await route.continue().catch(() => {});
  });
  const requests = [];
  page.on('request', request => { if (/\/api\/app\/v1\/(?:tags|calendar)\?/.test(request.url())) requests.push(request.url()); });
  const focus = () => page.evaluate(() => {
    window.dispatchEvent(new Event('focus'));
    document.dispatchEvent(new Event('visibilitychange'));
  });
  async function start(action) {
    const requested = Promise.all(['tags', 'calendar'].map(name =>
      page.waitForRequest(request => new URL(request.url()).pathname === `/api/app/v1/${name}`)));
    await action(); await requested;
  }
  async function finish() {
    const responded = Promise.all(['tags', 'calendar'].map(name =>
      page.waitForResponse(response => new URL(response.url()).pathname === `/api/app/v1/${name}`)));
    release(); await responded;
  }
  await page.clock.setFixedTime(new Date('2026-10-30T04:01:01Z'));
  await start(focus);
  await focus();
  assert.deepEqual(await snapshot(), displayed);
  assert.equal(requests.length, 2);
  state.notes.set('sidebar-new', { ...state.notes.get('n00'), name: 'memos/sidebar-new', tags: ['后台标签'] });
  await finish();
  await page.locator('#tags button', { hasText: '#后台标签' }).waitFor();
  assert.equal(await page.locator('#tags button.on').count(), 1);
  const normalCount = [...state.notes.values()].filter(note => note.state === 'NORMAL').length;
  assert.equal(await page.locator('#calendar .h2').getAttribute('aria-label'), `2026-10-29，${normalCount} 条笔记`);
  displayed = await snapshot();
  delayed = new Promise(resolve => { release = resolve; }); fail = true;
  await page.clock.setFixedTime(new Date('2026-10-30T04:02:02Z'));
  await start(focus);
  assert.deepEqual(await snapshot(), displayed);
  await finish();
  await page.locator('#sidebar-error').waitFor({ state: 'visible' });
  assert.deepEqual(await snapshot(), displayed);
  assert.equal(requests.length, 4);
  delayed = new Promise(resolve => { release = resolve; }); fail = false;
  await start(() => page.locator('#sidebar-retry').click());
  assert.deepEqual(await snapshot(), displayed);
  await finish();
  await page.locator('#sidebar-error').waitFor({ state: 'hidden' });
  await page.locator('#tags button.on').waitFor();
  assert.equal(requests.length, 6);
  assert.deepEqual(await page.locator('.app, .side, #account').evaluateAll(nodes =>
    nodes.map(node => getComputedStyle(node).backgroundColor)), Array(3).fill('rgb(247, 246, 242)'));
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

test('inserting a saved note keeps both editors connected and preserves keyboard focus', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t);
  await page.goto(url); await login(page);
  await page.locator('[data-name="memos/n00"]').waitFor();
  await page.locator('#content').fill('触发时间线刷新');
  await menu(page, 'n00', '编辑'); await menu(page, 'n01', '编辑');
  const text = page.locator('[data-name="memos/n00"].editor textarea');
  await text.fill('连续输入中的草稿');
  const listReads = state.requests.filter(item => item.path === '/notes' && item.method === 'GET').length;
  await page.evaluate(() => {
    globalThis.focusedForm = document.activeElement.closest('form.editor');
    globalThis.focusedRemoved = false;
    new MutationObserver(records => {
      if (records.some(record => [...record.removedNodes].includes(globalThis.focusedForm))) globalThis.focusedRemoved = true;
    }).observe(document.getElementById('notes'), { childList: true });
    document.getElementById('composer').requestSubmit();
  });
  await page.waitForFunction(() => document.querySelector('#notes > :first-child')?.dataset.name === 'memos/created-1');
  assert.equal(state.requests.filter(item => item.path === '/notes' && item.method === 'GET').length, listReads);
  assert.deepEqual(await page.evaluate(() => ({
    removed: globalThis.focusedRemoved,
    focused: document.activeElement.closest('form.editor') === globalThis.focusedForm,
  })), { removed: false, focused: true });
  assert.equal(await text.inputValue(), '连续输入中的草稿');
  assert.deepEqual(errors, []);
});

test('English fallback, live language switching and stored preference preserve original notes and unsaved forms', { timeout: 60_000 }, async t => {
  const { page, state, errors, url } = await fixture(t, 'fr-FR');
  await page.goto(url);
  await page.getByRole('button', { name: 'Sign in', exact: true }).waitFor();
  assert.equal(await page.locator('html').getAttribute('lang'), 'en');
  await login(page);
  const note = page.locator('[data-name="memos/n00"]'); await note.waitFor();
  const renderedParagraphs = await note.locator('.note-content p').allTextContents();
  await page.getByRole('button', { name: 'Previous month' }).waitFor();
  await page.locator('#calendar-title').filter({ hasText: /^October 2026$/ }).waitFor();
  assert.equal(await note.locator('time').first().textContent(), 'Oct 29');
  await page.locator('#content').fill('未保存的想法 #标签');
  await page.locator('#attachment-input').setInputFiles({ name: '图片.png', mimeType: 'image/png', buffer: png });
  await note.getByRole('button', { name: 'More actions' }).click();
  await note.getByRole('menuitem', { name: 'Edit', exact: true }).click();
  const editor = page.locator('form.editor'); await editor.locator('textarea').fill('Editing 原文');
  await page.evaluate(() => { globalThis.languageEditor = document.querySelector('form.editor'); });
  await setUILanguage(page, 'zh-CN');
  assert.equal(await page.locator('#add-attachment').getAttribute('aria-label'), '添加附件');
  assert.equal(await page.locator('#calendar-title').textContent(), '2026年10月');
  assert.equal(await page.evaluate(() => globalThis.languageEditor === document.querySelector('form.editor')), true);
  assert.equal(await editor.locator('textarea').inputValue(), 'Editing 原文');
  assert.equal(await page.locator('#content').inputValue(), '未保存的想法 #标签');
  assert.match(await page.locator('#draft-attachments').textContent(), /图片.png/);
  await setUILanguage(page, 'en');
  assert.match(await page.locator('#draft-attachments').getByRole('button').getAttribute('aria-label'), /Remove attachment 图片.png/);
  await editor.getByRole('button', { name: 'Cancel', exact: true }).click();
  assert.deepEqual(await note.locator('.note-content p').allTextContents(), renderedParagraphs);
  await page.route('**/api/app/v1/search', route => route.fulfill({ status: 503, json: {
    error: { code: 'EMBEDDING_DISABLED', message: '敏感诊断 must not appear' },
  } }));
  await page.locator('#q').fill('test'); await page.locator('#search-mode').selectOption('semantic');
  await page.locator('#list-error').filter({ hasText: 'Enable embeddings in Settings' }).waitFor();
  assert.doesNotMatch(await page.locator('#list-error').textContent(), /敏感诊断/);
  await setUILanguage(page, 'zh-CN');
  assert.match(await page.locator('#list-error').textContent(), /请在设置中启用/);
  await page.reload(); await page.locator('#workspace').waitFor({ state: 'visible' });
  assert.equal(await page.locator('html').getAttribute('lang'), 'zh-CN');
  assert.deepEqual(await page.evaluate(() => ({ ...localStorage })), { 'moerise.language': 'zh-CN', 'moerise.privacy.v1': 'off' });
  await page.addInitScript(() => Object.defineProperty(window, 'localStorage', { get() { throw new Error('Storage blocked'); } }));
  await page.reload(); await page.locator('#workspace').waitFor({ state: 'visible' });
  assert.equal(await page.locator('html').getAttribute('lang'), 'en');
  await setUILanguage(page, 'zh-CN');
  assert.equal(await page.locator('html').getAttribute('lang'), 'zh-CN');
  assert.deepEqual(errors, []);
});
