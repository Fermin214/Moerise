import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { text as readRequestText } from 'node:stream/consumers';
import { pathToFileURL } from 'node:url';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { test } from 'node:test';
import { createWebServer } from '../server.mjs';

const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
const { chromium } = runtime ? await import(pathToFileURL(runtime).href) : await import('playwright');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=', 'base64');

async function fixture(t, mobile = false) {
  const notes = Array.from({ length: 6 }, (_, i) => ({ name: `memos/n${i}`, content: `\u5b8c\u6574\u7b14\u8bb0 ${i}\n\n${'\u957f\u6b63\u6587'.repeat(120)}\n\n\u539f\u6587\u672b\u5c3e`,
    state: i === 1 ? 'ARCHIVED' : 'NORMAL', tags: ['\u60f3\u6cd5', '\u7075\u611f', '\u6458\u5f55', '\u6559\u8bad', 'constructor', 'other'].slice(i, i + 1),
    source_hash: `hash-${i}`, create_time: '2026-10-20T08:00:00Z', attachments: [] }));
  notes[0].attachments = [
    { name: 'attachments/image', filename: 'photo.png', type: 'image/png', size: png.length, content_url: '/api/app/v1/notes/n0/attachments/image/content' },
    { name: 'attachments/file', filename: 'original.pdf', type: 'application/pdf', size: 3, content_url: '/api/app/v1/notes/n0/attachments/file/content' },
  ];
  notes[5].content = ''; notes[5].attachments = [{ name: 'attachments/file', filename: 'only-file.pdf', type: 'application/pdf', size: 3, content_url: '/api/app/v1/notes/n5/attachments/file/content' }];
  const state = { notes, session: false, status: 'ready', frame: 'b'.repeat(64), version: 'a'.repeat(64),
    requests: [], pending: [], aborted: [], hold: new Set(), error: null, noteError: null, malformed: null };
  const core = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost'), path = url.pathname.replace('/api/app/v1', '');
    const raw = await readRequestText(req);
    state.requests.push({ path, method: req.method, headers: req.headers, body: raw ? JSON.parse(raw) : null });
    res.on('close', () => { if (!res.writableEnded) state.aborted.push(path); });
    const json = (data, status = 200) => {
      if (res.destroyed) return;
      res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(data));
    };
    const error = (code, status = 503, retryable = true) => json({ error: { code, retryable } }, status);
    const reply = value => { const copy = structuredClone(value); if (state.hold.has(path)) state.pending.push({ path, release: () => json(copy) }); else json(copy); };
    if (path === '/session') {
      if (req.method === 'POST') { state.session = true; res.setHeader('Set-Cookie', 'session=map; HttpOnly; SameSite=Strict; Path=/'); }
      else if (req.method === 'DELETE') { state.session = false; res.writeHead(204); res.end(); return; }
      if (!state.session) { error('SESSION_UNAUTHORIZED', 401, false); return; }
      json({ user: { name: 'users/1', display_name: '\u4f5c\u8005' }, csrf_token: 'csrf', timezone: 'Asia/Hong_Kong' }); return;
    }
    if (!state.session || !req.headers.cookie?.includes('session=map')) { error('SESSION_UNAUTHORIZED', 401, false); return; }
    if (path === '/tags') { json({ tags: [{ tag: '\u60f3\u6cd5', count: 1 }] }); return; }
    if (path === '/calendar') { json({ month: url.searchParams.get('month'), days: [] }); return; }
    if (path === '/notes') { json({ notes: notes.filter(note => note.state === 'NORMAL'), next_page_token: '' }); return; }
    if (path === '/map') {
      assert.equal(req.method, 'GET'); assert.equal(url.search, '');
      if (state.error) { error(state.error.code, state.error.status, state.error.retryable); return; }
      const ready = ['ready', 'empty'].includes(state.status);
      reply(state.malformed || { status: state.status, algorithm: 'pca-fixed-v2', layout_version: ready ? state.version : '',
        layout_frame: ready ? state.frame : '', vector_generation: ready ? 'published' : '', projection_revision: 3,
        points: state.status !== 'ready' ? [] : notes.map((note, i) => ({ name: note.name, source_hash: note.source_hash,
          x: i === 5 ? 4 : [-2, -1, 0, 1, 2][i], y: [0, 1, -1, 1, 0, 0][i], semantic: i !== 5, tags: note.tags })) }); return;
    }
    const attachment = /^\/notes\/n[05]\/attachments\/(image|file)\/content$/.exec(path);
    if (attachment) { res.writeHead(200, { 'Content-Type': attachment[1] === 'image' ? 'image/png' : 'application/pdf' }); res.end(attachment[1] === 'image' ? png : Buffer.from('pdf')); return; }
    const match = /^\/notes\/n(\d+)$/.exec(path);
    if (match) { if (state.noteError) error(state.noteError); else reply(notes[Number(match[1])]); return; }
    error('NOT_FOUND', 404, false);
  });
  core.listen(0, '127.0.0.1'); await once(core, 'listening');
  const probe = http.createServer(); probe.listen(0, '127.0.0.1'); await once(probe, 'listening');
  const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
  const url = `http://127.0.0.1:${port}`, web = createWebServer({ coreOrigin: `http://127.0.0.1:${core.address().port}`, publicOrigin: url });
  web.listen(port, '127.0.0.1'); await once(web, 'listening');
  let browser;
  t.after(async () => {
    for (const entry of state.pending.splice(0)) entry.release();
    await browser?.close();
    await Promise.all([web, core].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
  });
  browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  const context = await browser.newContext({ viewport: mobile ? { width: 390, height: 844 } : { width: 1360, height: 960 }, hasTouch: mobile, isMobile: mobile });
  const page = await context.newPage(), errors = [];
  page.on('pageerror', error => errors.push(error.message)); t.after(() => assert.deepEqual(errors, []));
  return { state, page, url, context };
}
async function login(page, url) {
  await page.goto(url + '/map'); await page.locator('#username').fill('owner'); await page.locator('#password').fill('password'); await page.locator('#login').click();
  await page.locator('#map-field').waitFor();
}
const marker = (page, i) => page.locator(`.map-dot[data-name="memos/n${i}"]`);
const card = page => page.locator('.map-note');
const view = page => page.evaluate(() => history.state.mapView);
async function ready(page) { await page.waitForFunction(() => document.querySelectorAll('.map-dot').length === 6 && document.querySelector('.map-page').getAttribute('aria-busy') === 'false'); }
async function wait(condition) { for (let i = 0; i < 100; i++) { if (condition()) return; await new Promise(resolve => setTimeout(resolve, 20)); } assert.ok(condition()); }
async function capture(page, name) { if (process.env.MOERISE_BROWSER_ARTIFACTS) { await mkdir(process.env.MOERISE_BROWSER_ARTIFACTS, { recursive: true }); await page.screenshot({ path: join(process.env.MOERISE_BROWSER_ARTIFACTS, name), fullPage: true }); } }

test('prototype map, protected complete reading, archive, pure file and independent detail return', async t => {
  const { page, state, url } = await fixture(t); await login(page, url); await ready(page);
  const style = await page.locator('#map-field').evaluate(element => ({ height: element.getBoundingClientRect().height, background: getComputedStyle(element).backgroundColor }));
  assert.equal(style.height, 440); assert.equal(style.background, 'rgb(251, 252, 254)');
  assert.equal(await marker(page, 0).evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(127, 166, 232)');
  assert.equal(await marker(page, 4).evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(154, 163, 175)');
  await marker(page, 0).click(); await card(page).waitFor();
  assert.deepEqual(await card(page).locator('.note-content p').allTextContents(), state.notes[0].content.split('\n\n'));
  assert.equal(await card(page).locator('a.attachment').getAttribute('href'), '/api/app/v1/notes/n0/attachments/file/content');
  await page.waitForFunction(() => document.querySelector('.map-note img')?.complete);
  assert.ok(await card(page).locator('img').evaluate(image => image.naturalWidth > 0));
  await capture(page, 'map-desktop.png');
  const saved = await view(page);
  await card(page).getByRole('button', { name: '\u6253\u5f00\u7b14\u8bb0', exact: true }).click(); await page.waitForURL('**/notes/n0?from=%2Fmap');
  await page.reload(); await page.locator('#notes .note').waitFor();
  await page.locator('#back').click(); await page.waitForURL('**/map'); await card(page).waitFor();
  assert.deepEqual(await view(page), saved);
  await marker(page, 1).click(); await page.waitForFunction(() => document.querySelector('.map-note')?.dataset.name === 'memos/n1');
  assert.ok((await card(page).textContent()).includes('\u539f\u6587\u672b\u5c3e'));
  await marker(page, 5).click(); await page.waitForFunction(() => document.querySelector('.map-note')?.dataset.name === 'memos/n5');
  assert.equal(await card(page).locator('a.attachment').getAttribute('href'), '/api/app/v1/notes/n5/attachments/file/content');
  assert.equal(state.requests.filter(req => !['GET', 'HEAD'].includes(req.method) && req.path !== '/session').length, 0);
  assert.equal(state.requests.filter(req => req.path === '/models').length, 0);
  assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
  assert.equal(await page.evaluate(() => JSON.stringify(history.state).includes('\u957f\u6b63\u6587')), false);
});

test('pan, cursor-anchored wheel and keyboard zoom persist across refresh; changed layout frames reset', async t => {
  const { page, state, url } = await fixture(t); await login(page, url); await ready(page);
  const field = page.locator('#map-field'), rect = await field.boundingBox(), first = await view(page);
  await page.mouse.move(rect.x + 40, rect.y + 40); await page.mouse.down(); await page.mouse.move(rect.x + 130, rect.y + 80, { steps: 6 }); await page.mouse.up();
  const pan = await view(page); assert.ok(Math.abs(pan.x - first.x + 90 / first.scale) < 1e-6); assert.ok(Math.abs(pan.y - first.y + 40 / first.scale) < 1e-6);
  const anchor = { x: 70, y: -30 }; await page.mouse.move(rect.x + rect.width / 2 + anchor.x, rect.y + rect.height / 2 + anchor.y); await page.mouse.wheel(0, -200);
  await page.waitForFunction(scale => history.state.mapView.scale > scale, pan.scale); const zoom = await view(page);
  assert.ok(Math.abs(pan.x + anchor.x / pan.scale - zoom.x - anchor.x / zoom.scale) < 1e-5);
  await field.focus(); await page.keyboard.press('+'); await page.keyboard.press('ArrowLeft'); const keyboard = await view(page);
  assert.ok(keyboard.scale > zoom.scale); assert.ok(keyboard.x < zoom.x);
  await page.reload(); await ready(page); assert.deepEqual(await view(page), keyboard);
  state.version = 'c'.repeat(64); await field.press('r'); await ready(page); assert.deepEqual(await view(page), keyboard);
  state.frame = 'd'.repeat(64); await field.press('r'); await page.waitForFunction(frame => history.state.mapView.frame === frame, state.frame);
  const reset = await view(page); assert.equal(reset.scale, first.scale); assert.equal(reset.x, first.x); assert.equal(reset.y, first.y);
  await field.press('+'); await field.press('Home'); assert.equal((await view(page)).scale, first.scale);
});

test('late point and note replies cannot rewrite navigation; switching points cancels the old read', async t => {
  const { page, state, url } = await fixture(t); await login(page, url); await ready(page);
  state.hold.add('/notes/n0'); await marker(page, 0).click(); await wait(() => state.pending.length === 1);
  await marker(page, 1).click(); await page.waitForFunction(() => document.querySelector('.map-note')?.dataset.name === 'memos/n1');
  await wait(() => state.aborted.includes('/notes/n0')); state.pending.shift().release();
  assert.equal(await card(page).getAttribute('data-name'), 'memos/n1');
  state.hold.add('/map'); await page.locator('#map-field').press('r'); await wait(() => state.pending.length === 1);
  await page.locator('a[data-route][href="/"]').click(); await page.waitForURL(url + '/');
  await wait(() => state.aborted.includes('/map')); state.pending.shift().release();
  assert.equal(await page.locator('.map-page').count(), 0);
});

test('index states and temporary failures clear points then recover; published source changes refresh the reader', async t => {
  const { page, state, url } = await fixture(t); await login(page, url); await ready(page);
  await marker(page, 0).click(); await card(page).waitFor();
  state.error = { code: 'MAP_RESOURCE_LIMIT', status: 503, retryable: true };
  await page.locator('#map-field').press('r'); await page.getByRole('alert').waitFor();
  assert.equal(await page.locator('.map-dot').count(), 0); assert.equal(await card(page).count(), 0);
  state.error = null; state.notes[0].content = '\u66f4\u65b0\u540e\u7684\u5b8c\u6574\u6b63\u6587'; state.notes[0].source_hash = 'changed'; state.version = 'c'.repeat(64);
  await page.waitForFunction(() => document.querySelector('.map-note .note-content')?.textContent.includes('\u66f4\u65b0\u540e\u7684\u5b8c\u6574\u6b63\u6587'));
  for (const status of ['waiting', 'building', 'unavailable', 'disabled', 'empty']) {
    state.status = status; await page.locator('#map-field').press('r');
    await page.waitForFunction(() => document.querySelectorAll('.map-dot').length === 0 && document.querySelector('.map-page').getAttribute('aria-busy') === 'false');
    assert.equal(await card(page).count(), 0); assert.ok(await page.locator('.map-page > p[role="status"]').isVisible());
  }
  state.status = 'ready'; await page.locator('#map-field').press('r'); await ready(page);
  state.malformed = { status: 'ready', points: [] }; await page.locator('#map-field').press('r'); await page.getByRole('alert').waitFor();
  assert.equal(await page.locator('.map-dot').count(), 0);
});

test('mobile touch drag and pinch zoom preserve the prototype frame and never select on canceled gestures', async t => {
  const { page, state, url, context } = await fixture(t, true); await login(page, url); await ready(page);
  const field = page.locator('#map-field'), rect = await field.boundingBox(), first = await view(page), client = await context.newCDPSession(page);
  const touch = (type, points) => client.send('Input.dispatchTouchEvent', { type, touchPoints: points });
  await touch('touchStart', [{ x: rect.x + 25, y: rect.y + 35, id: 1 }]);
  await touch('touchMove', [{ x: rect.x + 85, y: rect.y + 65, id: 1 }]); await touch('touchEnd', []);
  const pan = await view(page); assert.ok(Math.abs(pan.x - first.x + 60 / first.scale) < 1e-5);
  const x = rect.x + rect.width / 2, y = rect.y + 150;
  await touch('touchStart', [{ x: x - 30, y, id: 1 }, { x: x + 30, y, id: 2 }]);
  await touch('touchMove', [{ x: x - 60, y: y + 10, id: 1 }, { x: x + 60, y: y + 10, id: 2 }]); await touch('touchEnd', []);
  const zoom = await view(page); assert.ok(zoom.scale > pan.scale * 1.8);
  await field.evaluate(element => { element.focus(); element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true })); });
  await marker(page, 0).tap(); await card(page).waitFor();
  assert.equal(await field.evaluate(element => element.getBoundingClientRect().height), 440);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
  await capture(page, 'map-mobile.png');
  const count = state.requests.filter(req => req.path.startsWith('/notes/n')).length;
  await touch('touchStart', [{ x: rect.x + 40, y: rect.y + 40, id: 1 }]); await touch('touchCancel', []);
  assert.equal(state.requests.filter(req => req.path.startsWith('/notes/n')).length, count);
});

test('session expiry and active logout remove private maps and cancel pending reads', async t => {
  const { page, state, url } = await fixture(t); await login(page, url); await ready(page);
  state.hold.add('/notes/n0'); await marker(page, 0).click(); await wait(() => state.pending.length === 1);
  await page.locator('#user-menu').click(); await page.locator('#logout').click(); await page.locator('#auth').waitFor({ state: 'visible' });
  await wait(() => state.aborted.includes('/notes/n0')); state.pending.shift().release();
  assert.equal(await page.locator('.map-page').count(), 0); assert.equal((await page.evaluate(() => history.state)).mapView, null);
  state.hold.clear(); await page.locator('#username').fill('owner'); await page.locator('#password').fill('password'); await page.locator('#login').click(); await ready(page);
  state.session = false; await page.locator('#map-field').press('r'); await page.locator('#auth').waitFor({ state: 'visible' });
  assert.equal(await page.locator('.map-page').count(), 0);
});

test('each map history entry restores its own viewport and reader on back, reload and forward', async t => {
  const { page, url } = await fixture(t); await login(page, url); await ready(page);
  await marker(page, 0).click(); await card(page).waitFor();
  await page.locator('#map-field').press('+'); await page.locator('#map-field').press('ArrowRight'); const first = await view(page);
  await page.locator('nav a[href="/"]').click(); await page.waitForURL(url + '/');
  await page.locator('nav a[href="/map"]').click(); await ready(page); assert.deepEqual(await view(page), first);
  await marker(page, 1).click(); await page.waitForFunction(() => document.querySelector('.map-note')?.dataset.name === 'memos/n1');
  await page.locator('#map-field').press('+'); await page.locator('#map-field').press('ArrowDown'); const second = await view(page);
  assert.notDeepEqual(first, second);
  await page.goBack(); await page.waitForURL(url + '/'); await page.goBack(); await ready(page);
  await page.locator('.map-note[data-name="memos/n0"]').waitFor(); assert.deepEqual(await view(page), first);
  await page.reload(); await ready(page); await page.locator('.map-note[data-name="memos/n0"]').waitFor(); assert.deepEqual(await view(page), first);
  await page.goForward(); await page.waitForURL(url + '/'); await page.goForward(); await ready(page);
  await page.locator('.map-note[data-name="memos/n1"]').waitFor(); assert.deepEqual(await view(page), second);
});


test('focus and live note changes keep the daily snapshot without automatic refresh', async t => {
  const { page, state, url } = await fixture(t); await login(page, url); await ready(page);
  const count = state.requests.filter(req => req.path === '/map').length;
  const saved = await view(page);
  state.notes[0].content = '实时修改后的原文'; state.notes[0].source_hash = 'live-change';
  await page.evaluate(() => {
    window.dispatchEvent(new Event('focus'));
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await marker(page, 0).click(); await card(page).waitFor();
  assert.equal(await card(page).locator('.note-content').innerText(), '实时修改后的原文');
  await page.waitForTimeout(3200);
  assert.equal(state.requests.filter(req => req.path === '/map').length, count);
  assert.equal((await view(page)).scale, saved.scale);
  assert.equal(await page.locator('.map-dot').count(), 6);
});
