import { setUILanguage } from './browser-helpers.mjs';
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

async function fixture(t, mobile = false, language = 'zh-CN') {
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
      reply(state.malformed || { status: state.status, algorithm: 'pca-topics-v3', layout_version: ready ? state.version : '',
        layout_frame: ready ? state.frame : '', vector_generation: ready ? 'published' : '', projection_revision: 3,
        points: state.status !== 'ready' ? [] : notes.map((note, i) => ({ name: note.name, source_hash: note.source_hash,
          x: i === 5 ? 4 : [-2, -1, 0, 1, 2][i], y: [0, 1, -1, 1, 0, 0][i], semantic: i !== 5, tags: note.tags })),
        topics: state.status !== 'ready' ? [] : [
          { id: 'topic-0000000000000001', label: '产品设计', x: -1, y: 0, members: ['memos/n0', 'memos/n1', 'memos/n2'] },
          { id: 'topic-0000000000000002', label: '软件工程', x: 1.5, y: 0.5, members: ['memos/n3', 'memos/n4'] },
        ],
        links: state.status !== 'ready' ? [] : [
          { source: 'memos/n0', target: 'memos/n1', weight: 0.92 },
          { source: 'memos/n0', target: 'memos/n2', weight: 0.83 },
          { source: 'memos/n3', target: 'memos/n4', weight: 0.89 },
        ] }); return;
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
  const context = await browser.newContext({ locale: language, viewport: mobile ? { width: 390, height: 844 } : { width: 1360, height: 960 }, hasTouch: mobile, isMobile: mobile });
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
async function ready(page) { await page.waitForFunction(() => document.querySelector('#map-field')?.dataset.rendered === 'true' && document.querySelector('.map-page').getAttribute('aria-busy') === 'false'); }
async function wait(condition) { for (let i = 0; i < 100; i++) { if (condition()) return; await new Promise(resolve => setTimeout(resolve, 20)); } assert.ok(condition()); }
async function capture(page, name) { if (process.env.MOERISE_BROWSER_ARTIFACTS) { await mkdir(process.env.MOERISE_BROWSER_ARTIFACTS, { recursive: true }); await page.screenshot({ path: join(process.env.MOERISE_BROWSER_ARTIFACTS, name), fullPage: true }); } }

test('full workspace map, protected complete reading, archive, pure file and independent detail return', async t => {
  const { page, state, url } = await fixture(t); await login(page, url); await ready(page);
  const style = await page.locator('#map-field').evaluate(element => ({ height: element.getBoundingClientRect().height, background: getComputedStyle(element).backgroundColor }));
  assert.ok(style.height > 700); assert.equal(style.background, 'rgb(251, 252, 254)');
  assert.equal(await marker(page, 0).evaluate(element => getComputedStyle(element).color), 'rgb(109, 148, 202)');
  assert.equal(await marker(page, 4).evaluate(element => getComputedStyle(element).color), 'rgb(103, 169, 151)');
  assert.deepEqual(await page.locator('.map-topic').allTextContents(), ['产品设计', '软件工程']);
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
  await page.locator('#map-field').press('Home');
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

test('semantic themes, live hover previews and elastic neighbor dragging preserve the authoritative viewport', async t => {
  const { page, url } = await fixture(t); await login(page, url); await ready(page);
  const start = await view(page), first = await marker(page, 0).boundingBox(), neighbor = await marker(page, 1).boundingBox();
  await page.mouse.move(first.x + 16, first.y + 16);
  await page.locator('.map-preview').waitFor({ state: 'visible' });
  assert.match(await page.locator('.map-preview').innerText(), /完整笔记 0/);
  await page.mouse.down();
  await page.mouse.move(first.x + 96, first.y + 46, { steps: 12 });
  await page.waitForFunction(x => document.querySelector('.map-dot[data-name="memos/n1"]').getBoundingClientRect().x > x + 2, neighbor.x);
  assert.deepEqual(await view(page), start);
  assert.equal(await card(page).count(), 0);
  await page.mouse.up();
  await page.mouse.move(1, 1);
  await page.waitForFunction(x => Math.abs(document.querySelector('.map-dot[data-name="memos/n1"]').getBoundingClientRect().x - x) < 1, neighbor.x);
  await page.getByRole('button', { name: '聚焦主题：产品设计', exact: true }).click();
  await page.waitForFunction(scale => history.state.mapView.scale > scale, start.scale);
  assert.deepEqual(await page.locator('.map-topic').allTextContents(), ['产品设计', '软件工程']);
});

test('worker failure preserves map rendering and reading with reduced motion', async t => {
  const { page, url } = await fixture(t);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  let attempts = 0;
  await page.route('**/map-worker*.js', route => { attempts++; return route.abort(); });
  await login(page, url); await ready(page);
  assert.equal(attempts, 1);
  await marker(page, 0).click(); await card(page).waitFor();
  assert.equal(await card(page).getAttribute('data-name'), 'memos/n0');
  await page.getByRole('button', { name: '放大', exact: true }).click();
  await page.getByRole('button', { name: '显示全图', exact: true }).click();
  await marker(page, 0).waitFor({ state: 'visible' });
});

test('a thousand notes retain regions through zoom and reveal bounded live excerpts on desktop and touch screens', async t => {
  const { page, state, url } = await fixture(t);
  const template = state.notes[0];
  state.notes.splice(0, state.notes.length, ...Array.from({ length: 1000 }, (_, index) => ({
    ...template, name: `memos/n${index}`, source_hash: `dense-${index}`, content: `语义笔记 ${index}：记录具体内容。`,
  })));
  const points = state.notes.map((note, index) => {
    const angle = index * 2.3999632297, radius = Math.sqrt((index % 500 + 1) / 500) * 1.8;
    return { name: note.name, source_hash: note.source_hash, x: Math.cos(angle) * radius + (index < 500 ? -8 : 8),
      y: Math.sin(angle) * radius, tags: [], semantic: true };
  });
  state.malformed = { status: 'ready', algorithm: 'pca-topics-v3', layout_frame: state.frame, layout_version: state.version,
    vector_generation: 'published', projection_revision: 3, points, links: [],
    topics: [0, 1].map(i => ({ id: `topic-000000000000000${i + 1}`, label: i ? '软件工程' : '产品设计', x: i ? 8 : -8, y: 0,
      members: points.slice(i * 500, (i + 1) * 500).map(point => point.name),
      districts: [0, 1, 2, 3].map(j => ({ id: `district-000000000000000${i * 4 + j + 1}`, label: `分区${i}-${j}`,
        x: (i ? 8 : -8) + Math.cos(j * Math.PI / 2) * 0.6, y: Math.sin(j * Math.PI / 2) * 0.6,
        members: points.slice(i * 500, (i + 1) * 500).filter((_, index) => index % 4 === j).map(point => point.name) })) })) };
  await login(page, url); await ready(page);
  assert.equal(await page.locator('#map-field').getAttribute('data-detail'), 'topics');
  assert.equal(await page.locator('#map-field').getAttribute('data-regions'), '2');
  assert.equal(await page.locator('.map-dot').count(), 0);
  const overview = await view(page), fieldBounds = await page.locator('#map-field').boundingBox();
  await page.mouse.click(fieldBounds.x + fieldBounds.width / 2 + (-7.1 - overview.x) * overview.scale,
    fieldBounds.y + fieldBounds.height / 2 + (0.8 - overview.y) * overview.scale);
  await page.waitForFunction(scale => history.state.mapView.scale > scale, overview.scale);
  const snapshotReads = state.requests.filter(request => request.path === '/map').length;
  async function districtDetail(label) {
    await page.getByRole('button', { name: `聚焦主题：${label}`, exact: true }).click();
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const district = page.locator('.map-district:visible').first();
    await district.waitFor();
    await district.click();
    for (let i = 0; i < 14; i++) {
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      if (await page.locator('.map-excerpt').count()) return;
      await page.getByRole('button', { name: '放大', exact: true }).click();
    }
  }
  for (const viewport of [{ width: 1360, height: 960 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    await page.getByRole('button', { name: '显示全图', exact: true }).click();
    if (viewport.width > 600) state.hold = new Set(state.notes.map(note => `/notes/${note.name.slice(6)}`));
    await districtDetail('产品设计');
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    assert.equal(await page.locator('#map-field').getAttribute('data-detail'), 'notes',
      JSON.stringify({ viewport, view: await view(page), cards: await page.locator('.map-excerpt').count() }));
    await page.locator('.map-excerpt').first().waitFor();
    const excerpts = await page.locator('.map-excerpt').count();
    assert.ok(excerpts <= (viewport.width < 600 ? 4 : 10));
    assert.ok(Number(await page.locator('#map-field').getAttribute('data-regions')) <= 220);
    assert.ok(await page.locator('.map-dot').count() <= 160);
    if (viewport.width > 600) {
      await wait(() => state.pending.length === Math.min(3, excerpts));
      state.hold.clear();
      for (const pending of state.pending.splice(0)) pending.release();
    }
    await page.waitForFunction(() => [...document.querySelectorAll('.map-excerpt')].every(node => node.textContent.startsWith('语义笔记')));
    const boxes = await page.locator('.map-excerpt').evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().toJSON()));
    const labels = await page.locator('.map-topic:visible').evaluateAll(nodes => nodes.map(node => node.getBoundingClientRect().toJSON()));
    for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
      const a = boxes[i], b = boxes[j];
      assert.ok(a.right <= b.x || b.right <= a.x || a.bottom <= b.y || b.bottom <= a.y);
    }
    for (const a of boxes) for (const b of labels) assert.ok(a.right <= b.x || b.right <= a.x || a.bottom <= b.y || b.bottom <= a.y);
    await capture(page, `map-dense-${viewport.width}.png`);
    const chosen = await page.locator('.map-excerpt').first().getAttribute('data-name');
    await page.locator('.map-excerpt').first().click();
    await page.waitForFunction(name => document.querySelector('.map-note')?.dataset.name === name, chosen);
    await page.getByRole('button', { name: '关闭笔记详情', exact: true }).click();
  }
  assert.equal(state.requests.filter(request => request.path === '/map').length, snapshotReads);
  await page.getByRole('button', { name: '显示全图', exact: true }).click();
  state.hold = new Set(state.notes.map(note => `/notes/${note.name.slice(6)}`));
  await districtDetail('软件工程');
  await wait(() => state.pending.length > 0);
  const pending = state.pending.splice(0);
  await page.locator('#navigation-toggle').click();
  await page.locator('a[data-route][href="/"]').click();
  await page.waitForURL(url + '/');
  await wait(() => pending.every(entry => state.aborted.includes(entry.path)));
  for (const entry of pending) entry.release();
  assert.equal(await page.locator('.map-excerpt').count(), 0);
});

test('workspace proportions adapt to desktop widths and switch navigation when content no longer fits', async t => {
  const { page, url } = await fixture(t); await login(page, url); await ready(page);
  for (const width of [1024, 1440, 1920, 2560, 3440]) {
    await page.setViewportSize({ width, height: 1000 });
    const geometry = await page.evaluate(() => ({
      sidebar: document.querySelector('#sidebar').getBoundingClientRect().width,
      map: document.querySelector('#map-field').getBoundingClientRect().toJSON(),
      overflow: document.documentElement.scrollWidth > innerWidth,
    }));
    assert.equal(geometry.overflow, false);
    assert.ok(Math.abs(geometry.sidebar - Math.max(288, Math.min(384, width * 0.16))) < 1);
    assert.ok(geometry.map.width > (width - geometry.sidebar) * 0.85);
    assert.ok(geometry.map.height > 800);
  }
  await page.setViewportSize({ width: 900, height: 700 });
  await page.getByRole('button', { name: '打开导航', exact: true }).waitFor({ state: 'visible' });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
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

test('mobile touch drag and pinch zoom fill the workspace and never select on canceled gestures', async t => {
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
  assert.ok(await field.evaluate(element => element.getBoundingClientRect().height) > 650);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
  await capture(page, 'map-mobile.png');
  const noteReads = () => state.requests.filter(req => /^\/notes\/[^/]+$/.test(req.path)).map(req => req.path);
  const readsBeforeCancel = noteReads();
  await touch('touchStart', [{ x: rect.x + 40, y: rect.y + 40, id: 1 }]); await touch('touchCancel', []);
  assert.deepEqual(noteReads(), readsBeforeCancel);
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
  await page.locator('#map-field').press('Home');
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
  assert.equal((await view(page)).frame, saved.frame);
});

test('English map labels and dates update without refetching notes or losing the viewport', async t => {
  const { page, state, url } = await fixture(t, false, 'en-US');
  await login(page, url); await ready(page);
  await page.getByRole('group', { name: 'Stargazing', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Note n0', exact: true }).click(); await card(page).waitFor();
  const readCount = () => state.requests.filter(request => ['/map', '/notes/n0'].includes(request.path)).length;
  const saved = await view(page), reads = readCount();
  await setUILanguage(page, 'zh-CN');
  await page.getByRole('group', { name: '观星', exact: true }).waitFor();
  assert.deepEqual(await view(page), saved); assert.equal(readCount(), reads);
  await setUILanguage(page, 'en');
  assert.match(await card(page).locator('time').textContent(), /Oct/);
  assert.match(await card(page).locator('.note-content').textContent(), /完整笔记/);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.getByRole('button', { name: 'Open navigation', exact: true }).click();
  await page.getByRole('region', { name: 'Calendar', exact: true }).waitFor({ state: 'visible' });
});
