import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { cp, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import http from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createWebServer } from '../server.mjs';

const runtime = process.env.MOERISE_PLAYWRIGHT_MODULE;
const { chromium } = runtime ? await import(pathToFileURL(runtime).href) : await import('playwright');
const close = server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });

test('an open page recovers lazy modules after replacing the deployed build', { timeout: 60_000 }, async t => {
  const root = await mkdtemp(join(tmpdir(), 'moerise-upgrade-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const build = join(root, 'build'), active = join(root, 'active');
  await cp(fileURLToPath(new URL('../src/', import.meta.url)), join(build, 'src'), { recursive: true });
  await cp(fileURLToPath(new URL('../build.mjs', import.meta.url)), join(build, 'build.mjs'));
  const deploy = async () => {
    execFileSync(process.execPath, [join(build, 'build.mjs')], { stdio: 'pipe' });
    await rm(active, { recursive: true, force: true });
    await cp(join(build, 'dist'), active, { recursive: true });
    return /app\.([a-f0-9]+)\.js/.exec(await readFile(join(active, 'index.html'), 'utf8'))[1];
  };
  const before = await deploy();
  const core = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://localhost');
    const data = {
      '/api/app/v1/session': { user: { name: 'users/owner', display_name: 'Owner' }, csrf_token: 'csrf', timezone: 'UTC' },
      '/api/app/v1/notes': { notes: [], next_page_token: '' },
      '/api/app/v1/tags': { tags: [] },
      '/api/app/v1/calendar': { days: [], month: url.searchParams.get('month') },
      '/api/app/v1/map': { status: 'disabled', algorithm: 'pca-fixed-v2', projection_revision: 0,
        layout_version: '', layout_frame: '', vector_generation: '', points: [] },
    }[url.pathname];
    res.writeHead(data ? 200 : 404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(data || {}));
  });
  core.listen(0, '127.0.0.1'); await once(core, 'listening');
  t.after(() => close(core));
  const web = createWebServer({ dist: active, coreOrigin: `http://127.0.0.1:${core.address().port}` });
  web.listen(0, '127.0.0.1'); await once(web, 'listening');
  t.after(() => close(web));
  const url = `http://127.0.0.1:${web.address().port}`;
  const browser = await chromium.launch({ headless: true, channel: process.env.MOERISE_PLAYWRIGHT_CHANNEL || undefined });
  t.after(() => browser.close());
  const page = await browser.newPage({ locale: 'zh-CN' }), failures = [], errors = [];
  page.on('pageerror', err => errors.push(err.message));
  page.on('response', response => {
    if (response.status() === 404) failures.push(new URL(response.url()).pathname);
  });
  await page.goto(url); await page.locator('#empty').waitFor({ state: 'visible' });
  await writeFile(join(build, 'build.mjs'), (await readFile(join(build, 'build.mjs'), 'utf8')) + '\n// Upgraded build pipeline.\n');
  const after = await deploy();
  assert.notEqual(before, after);
  await page.locator('nav a[href="/map"]').click();
  await page.locator('#list-error').waitFor({ state: 'visible' });
  assert.match(await page.locator('#list-error').textContent(), /刷新页面/);
  assert.ok(failures.includes(`/map.${before}.js`));
  assert.equal(await page.locator('.map-page').count(), 0);
  const navigation = page.waitForEvent('load');
  await page.getByRole('button', { name: '刷新页面', exact: true }).click();
  await navigation; await page.locator('#map-field').waitFor();
  assert.ok((await page.locator('script[type="module"]').getAttribute('src')).includes(after));
  assert.equal(failures.filter(path => path === `/map.${before}.js`).length, 1);
  assert.deepEqual(errors, []);
});
