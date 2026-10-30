import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { pathToFileURL } from 'node:url';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { test } from 'node:test';
import { createWebServer } from '../server.mjs';

const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
if (!runtime) throw new Error('Set MOERISE_PLAYWRIGHT_MODULE to the installed playwright/index.mjs');
const { chromium } = await import(pathToFileURL(runtime).href);
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=', 'base64');

async function fixture(t) {
  const notes = Array.from({ length: 110 }, (_, i) => ({
    name: `memos/n${i}`, content: `真实笔记 ${i}\n\n${i === 0 ? '完整正文'.repeat(120) : '第二段内容'}\n\n末尾文字 ${i}`,
    state: 'NORMAL', tags: ['原始标签', '第二标签'], source_hash: `hash-${i}`,
    create_time: '2026-10-20T08:00:00Z', attachments: [],
  }));
  notes[0].attachments = [
    { name: 'attachments/image', filename: 'photo.png', type: 'image/png', size: png.length,
      content_url: '/api/app/v1/notes/n0/attachments/image/content' },
    { name: 'attachments/file', filename: 'source.pdf', type: 'application/pdf', size: 3,
      content_url: '/api/app/v1/notes/n0/attachments/file/content' },
  ];
  const state = {
    session: false, notes, requests: [], pending: [], aborts: [], csrf: 'csrf-1', rejectCSRF: false,
    empty: false, reviewError: null, walkError: null, modelsError: null,
    exhausted: false, technical: false, embedding: true, hold: null,
  };
  const core = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost'), path = url.pathname.replace('/api/app/v1', '');
    let raw = ''; for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : undefined;
    state.requests.push({ path, method: req.method, body, headers: req.headers });
    res.on('close', () => { if (!res.writableEnded) state.aborts.push(path); });
    const json = (data, status = 200) => {
      if (res.destroyed) return;
      res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(data));
    };
    const error = (code, status = 503) => json({ error: { code, retryable: status >= 500 } }, status);
    const reply = value => {
      const copy = structuredClone(value);
      if (state.hold === path) state.pending.push({ path, release: () => json(copy) });
      else json(copy);
    };
    if (path === '/session') {
      if (req.method === 'POST') {
        state.session = true; res.setHeader('Set-Cookie', 'session=discovery; HttpOnly; SameSite=Strict; Path=/');
      } else if (req.method === 'DELETE') { state.session = false; res.writeHead(204); res.end(); return; }
      if (!state.session) { error('SESSION_UNAUTHORIZED', 401); return; }
      json({ user: { name: 'users/1', display_name: '测试作者' }, csrf_token: state.csrf, timezone: 'Asia/Hong_Kong' }); return;
    }
    if (!state.session || !req.headers.cookie?.includes('session=discovery')) { error('SESSION_UNAUTHORIZED', 401); return; }
    if (req.method === 'POST') {
      if (state.rejectCSRF) { state.rejectCSRF = false; state.csrf = 'csrf-2'; error('CSRF_REJECTED', 403); return; }
      assert.equal(req.headers['x-csrf-token'], state.csrf); assert.ok(req.headers.origin);
    }
    if (path === '/tags') { json({ tags: [] }); return; }
    if (path === '/calendar') { json({ month: url.searchParams.get('month'), days: [] }); return; }
    if (path === '/models') {
      if (state.modelsError) { error(state.modelsError); return; }
      json({ embedding: { enabled: state.embedding }, index_status: state.embedding ? 'ready' : 'disabled' }); return;
    }
    if (path === '/notes') { json({ notes: notes.slice(0, 3), next_page_token: '' }); return; }
    if (path === '/reviews/daily') {
      assert.equal(req.method, 'GET');
      if (state.reviewError) { error(state.reviewError); return; }
      reply({ id: 'fixed-day', date: '2026-10-30', timezone: 'Asia/Hong_Kong', items: state.empty ? [] :
        notes.slice(0, 6).map((note, index) => index === 3 ? { name: note.name, unavailable: true } :
          { name: note.name, unavailable: false, note }) }); return;
    }
    const attachment = /^\/notes\/n0\/attachments\/(image|file)\/content$/.exec(path);
    if (attachment) {
      res.writeHead(200, { 'Content-Type': attachment[1] === 'image' ? 'image/png' : 'application/pdf' });
      res.end(attachment[1] === 'image' ? png : Buffer.from('pdf')); return;
    }
    const id = /^\/notes\/n(\d+)$/.exec(path)?.[1];
    if (id !== undefined) { json(notes[Number(id)]); return; }
    if (path === '/walks/start' || path === '/walks/next') {
      assert.equal(req.method, 'POST');
      if (state.walkError) { error(state.walkError); return; }
      if (state.technical) {
        reply({ mode: 'continue', path: body.path || [], exhausted: false, warnings: ['RESOURCE_LIMIT'] }); return;
      }
      if (state.exhausted) {
        reply({ mode: 'continue', path: body.path || [], exhausted: true, warnings: [] }); return;
      }
      const visited = body.path || [], name = path === '/walks/start' ? body.name || notes[0].name :
        notes.find(note => !visited.includes(note.name)).name;
      const note = notes.find(note => note.name === name);
      reply({ mode: path === '/walks/start' ? 'random' : 'continue', path: [...visited, name],
        item: { note, score: 0.817, matches: [] }, exhausted: false, warnings: [] }); return;
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
    for (const entry of state.pending.splice(0)) entry.release();
    await browser?.close();
    await Promise.all([web, core].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
  });
  browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  const page = await browser.newPage({ viewport: { width: 1360, height: 960 } }), errors = [];
  page.on('pageerror', error => errors.push(error.message));
  t.after(() => assert.deepEqual(errors, []));
  return { page, state, url };
}
async function login(page, url, path) {
  await page.goto(url + path);
  await page.locator('#username').fill('author'); await page.locator('#password').fill('password');
  await page.locator('#login').click(); await page.locator('#workspace').waitFor({ state: 'visible' });
}
const card = page => page.locator('.discovery-note:visible');
const next = page => page.getByRole('button', { name: '继续漫步', exact: true });
const requests = (state, path) => state.requests.filter(request => request.path === path);
const waitFor = async condition => {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (condition()) return;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.ok(condition(), 'Expected API activity');
};
async function screenshot(page, name) {
  if (!process.env.MOERISE_BROWSER_ARTIFACTS) return;
  await mkdir(process.env.MOERISE_BROWSER_ARTIFACTS, { recursive: true });
  await page.screenshot({ path: join(process.env.MOERISE_BROWSER_ARTIFACTS, name), fullPage: true });
}

test('daily review exposes all six fixed positions, full text/images, unavailable items and detail return without read writes', { timeout: 60_000 }, async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/review');
  await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).waitFor();
  assert.equal(await page.locator('[data-review-index]').count(), 6);
  assert.match(await card(page).innerText(), new RegExp('完整正文'.repeat(120)));
  assert.match(await card(page).innerText(), /末尾文字 0/);
  assert.equal(await card(page).locator('.tag').count(), 2);
  await page.waitForFunction(() => document.querySelector('.discovery-note img')?.naturalWidth > 0);
  await card(page).getByRole('link', { name: /source.pdf/ }).waitFor();
  await screenshot(page, 'review-desktop.png');
  await page.getByRole('button', { name: '第 4 / 6 条', exact: true }).click();
  assert.match(await card(page).innerText(), /已不存在或无法访问/);
  assert.equal(await card(page).getByRole('button', { name: '打开笔记' }).count(), 0);
  await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).click();
  await card(page).getByRole('button', { name: '打开笔记' }).click();
  await page.locator('article.note[data-name="memos/n5"]').waitFor();
  assert.equal(new URL(page.url()).searchParams.get('from'), '/review');
  await page.getByRole('button', { name: '‹ 返回每日回顾', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 5' }).waitFor();
  assert.equal(await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).getAttribute('aria-pressed'), 'true');
  const reads = requests(state, '/reviews/daily').length;
  await page.getByRole('button', { name: '再看一遍', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  assert.equal(requests(state, '/reviews/daily').length, reads);
  await page.reload();
  await page.getByRole('button', { name: '第 6 / 6 条', exact: true }).waitFor();
  assert.deepEqual(state.requests.filter(request => request.method !== 'GET').map(request => request.path), ['/session']);
  assert.equal(await page.getByRole('button', { name: /完成|略过|换一组/ }).count(), 0);
});

test('review empty pool differs from source failure and can retry; leaving aborts the pending daily read', { timeout: 60_000 }, async t => {
  const { page, state, url } = await fixture(t);
  state.reviewError = 'UPSTREAM_UNAVAILABLE';
  await login(page, url, '/review');
  await page.getByRole('alert').filter({ hasText: 'Memos 服务暂时无法连接' }).waitFor();
  assert.equal(await page.getByText('今天没有可回顾的旧笔记。', { exact: true }).count(), 0);
  state.reviewError = null; state.empty = true;
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await page.getByText('今天没有可回顾的旧笔记。', { exact: true }).waitFor();
  state.hold = '/reviews/daily'; state.empty = false;
  await page.locator('a[data-route][href="/"]').click();
  await page.locator('a[data-route][href="/review"]').click();
  await waitFor(() => state.pending.length === 1);
  await page.locator('a[data-route][href="/"]').click();
  await waitFor(() => state.aborts.includes('/reviews/daily'));
  state.pending.shift().release();
  await page.locator('#notes [data-name="memos/n0"]').waitFor();
  assert.equal(await page.locator('#discovery-page').count(), 0);
});

test('walk random start uses session CSRF, can continue past 100 notes, and restores the same path after detail return', { timeout: 120_000 }, async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/walk');
  await page.getByRole('button', { name: '随机开始', exact: true }).waitFor();
  state.rejectCSRF = true;
  await page.getByRole('button', { name: '随机开始', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  assert.deepEqual(requests(state, '/walks/start').map(request => request.body), [{}, {}]);
  assert.deepEqual(requests(state, '/walks/start').map(request => request.headers['x-csrf-token']), ['csrf-1', 'csrf-2']);
  assert.match(await card(page).innerText(), new RegExp('完整正文'.repeat(120)));
  await screenshot(page, 'walk-desktop.png');
  for (let index = 1; index <= 102; index++) {
    await next(page).click();
    await page.locator(`.discovery-note[data-name="memos/n${index}"]:visible`).waitFor();
  }
  const last = requests(state, '/walks/next').at(-1);
  assert.equal(last.body.name, 'memos/n101');
  assert.equal(last.body.path.length, 102);
  assert.equal(new Set(last.body.path).size, 102);
  assert.match(await page.locator('#discovery-page').innerText(), /已漫步 103 条/);
  assert.doesNotMatch(await page.locator('#discovery-page').innerText(), /强关联|弱关联|0\.817|下一站可能/);
  await card(page).getByRole('button', { name: '打开笔记' }).click();
  await page.locator('article.note[data-name="memos/n102"]').waitFor();
  await page.getByRole('button', { name: '‹ 返回随机漫步', exact: true }).click();
  await page.locator('.discovery-note[data-name="memos/n102"]:visible').waitFor();
  await next(page).click();
  await page.locator('.discovery-note[data-name="memos/n103"]:visible').waitFor();
  assert.equal(requests(state, '/walks/next').at(-1).body.path.length, 103);
  assert.equal(state.requests.some(request => /reads|opened|skip|complete/.test(request.path)), false);
});

test('detail and review can start at the selected note; mobile cards keep full text and return to the walk', { timeout: 60_000 }, async t => {
  const { page, state, url } = await fixture(t);
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page, url, '/notes/n5?from=%2Freview');
  await page.locator('article.note').getByRole('button', { name: '从这条漫步', exact: true }).click();
  await page.locator('.discovery-note[data-name="memos/n5"]:visible').waitFor();
  assert.deepEqual(requests(state, '/walks/start').at(-1).body, { name: 'memos/n5' });
  await card(page).getByRole('button', { name: '打开笔记' }).click();
  await page.getByRole('button', { name: '‹ 返回随机漫步', exact: true }).click();
  await page.locator('.discovery-note[data-name="memos/n5"]:visible').waitFor();
  assert.equal(requests(state, '/walks/start').length, 1);
  await next(page).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await screenshot(page, 'walk-mobile.png');
  await page.locator('a[data-route][href="/review"]').click();
  await page.getByRole('button', { name: '第 3 / 6 条', exact: true }).click();
  await page.getByRole('button', { name: '从这条漫步', exact: true }).click();
  await page.locator('.discovery-note[data-name="memos/n2"]:visible').waitFor();
  assert.deepEqual(requests(state, '/walks/start').at(-1).body, { name: 'memos/n2' });
});

test('resource errors and non-exhausted technical responses can retry the same path; exhaustion preserves the last note', { timeout: 60_000 }, async t => {
  const { page, state, url } = await fixture(t);
  await login(page, url, '/walk');
  await page.getByRole('button', { name: '随机开始', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  state.walkError = 'RESOURCE_LIMIT';
  await next(page).click();
  await page.getByRole('alert').filter({ hasText: '技术资源限制' }).waitFor();
  assert.equal(await page.getByText('能走的路都走完了。', { exact: true }).count(), 0);
  state.walkError = null; state.technical = true;
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await page.locator('#discovery-page[aria-busy="false"]').waitFor();
  await page.getByRole('alert').filter({ hasText: '技术资源限制' }).waitFor();
  assert.match(await card(page).innerText(), /末尾文字 0/);
  state.technical = false;
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 1' }).waitFor();
  assert.deepEqual(requests(state, '/walks/next').slice(0, 3).map(request => request.body),
    Array(3).fill({ name: 'memos/n0', path: ['memos/n0'] }));
  state.exhausted = true; await next(page).click();
  await page.getByText('能走的路都走完了。', { exact: true }).waitFor();
  assert.match(await card(page).innerText(), /末尾文字 1/);
  assert.equal(await next(page).count(), 0);
  await page.getByRole('button', { name: '停止', exact: true }).click();
  await page.getByRole('button', { name: '随机开始', exact: true }).waitFor();
});

test('stop, navigation and logout abort pending walk requests and isolate late responses', { timeout: 60_000 }, async t => {
  const { page, state, url } = await fixture(t);
  await page.addInitScript(() => {
    const original = window.fetch;
    window.discoveryResponses = 0; window.discoveryAborts = 0;
    window.fetch = async (input, options) => {
      if (!String(input).includes('/walks/')) return original(input, options);
      options?.signal?.addEventListener('abort', () => window.discoveryAborts++);
      const response = await original(input, window.ignoreDiscoveryAbort ? { ...options, signal: undefined } : options);
      window.discoveryResponses++; return response;
    };
  });
  await login(page, url, '/walk');
  state.hold = '/walks/start';
  await page.getByRole('button', { name: '随机开始', exact: true }).click();
  await waitFor(() => state.pending.length === 1);
  await page.getByRole('button', { name: '停止', exact: true }).click();
  await waitFor(() => state.aborts.includes('/walks/start'));
  state.pending.shift().release(); state.hold = null;
  await page.getByRole('button', { name: '随机开始', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  await page.evaluate(() => { window.ignoreDiscoveryAbort = true; });
  state.hold = '/walks/next';
  await next(page).click(); await waitFor(() => state.pending.length === 1);
  await page.getByRole('button', { name: '停止', exact: true }).click();
  assert.equal(await page.evaluate(() => window.discoveryAborts), 2);
  const count = await page.evaluate(() => window.discoveryResponses);
  state.pending.shift().release(); state.hold = null;
  await page.waitForFunction(count => window.discoveryResponses > count, count);
  await page.evaluate(() => new Promise(requestAnimationFrame));
  assert.equal(await card(page).count(), 0);
  await page.getByRole('button', { name: '随机开始', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  state.hold = '/walks/next';
  await next(page).click(); await waitFor(() => state.pending.length === 1);
  await card(page).getByRole('button', { name: '打开笔记' }).click();
  await page.locator('article.note[data-name="memos/n0"]').waitFor();
  state.pending.shift().release(); state.hold = null;
  await page.getByRole('button', { name: '‹ 返回随机漫步', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  state.hold = '/walks/next'; await next(page).click(); await waitFor(() => state.pending.length === 1);
  await page.locator('#user-menu').click(); await page.locator('#logout').click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  state.pending.shift().release();
  await page.evaluate(() => new Promise(requestAnimationFrame));
  assert.equal(await page.locator('#discovery-page').count(), 0);
});

test('model availability can retry, disabled Embedding links to configuration, and session expiry removes discovery content', { timeout: 60_000 }, async t => {
  const { page, state, url } = await fixture(t);
  state.embedding = false; state.modelsError = 'CORE_UNAVAILABLE';
  await login(page, url, '/walk');
  await page.getByRole('alert').filter({ hasText: '服务暂时无法连接' }).waitFor();
  assert.equal(await page.getByRole('button', { name: '停止', exact: true }).count(), 0);
  state.modelsError = null;
  await page.getByRole('button', { name: '重试', exact: true }).click();
  await page.getByRole('status').filter({ hasText: '请在设置中启用 Embedding' }).waitFor();
  assert.ok(await page.getByRole('button', { name: '随机开始', exact: true }).isDisabled());
  assert.equal(await page.getByRole('link', { name: '打开设置', exact: true }).getAttribute('href'), '/settings');
  assert.equal(requests(state, '/walks/start').length, 0);
  state.embedding = true;
  await page.reload();
  await page.getByRole('button', { name: '随机开始', exact: true }).click();
  await card(page).filter({ hasText: '末尾文字 0' }).waitFor();
  state.session = false; await next(page).click();
  await page.locator('#login-form').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#discovery-page').count(), 0);
});
