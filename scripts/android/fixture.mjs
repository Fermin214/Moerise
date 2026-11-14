import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { createServer } from 'node:http';
import { text as requestText } from 'node:stream/consumers';
import { mkdir, readFile, writeFile, open, rm } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { runProcess } from '../lib/process.mjs';
import { createWebServer } from '../../web/server.mjs';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const root = resolve(process.argv[2] || '.local/android/fixture');
assert.ok(root.startsWith(resolve(repo, '.local/android/') + '/'), 'fixture 必须放在 .local/android/ 下');
const name = `moerise-android-${randomBytes(6).toString('hex')}`;
const username = 'android-owner', password = randomBytes(24).toString('hex');
let core, web, model, containerStarted = false;
const stop = new AbortController();
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => stop.abort());

async function docker(args) {
  try { return await runProcess('docker', args, { cwd: repo, timeout: 120_000 }); }
  catch { throw new Error(`Docker ${args[0]} 失败`); }
}
async function port() {
  const server = createServer().listen(0, '127.0.0.1');
  await once(server, 'listening');
  const value = server.address().port;
  await new Promise(done => server.close(done));
  return value;
}
async function request(origin, path, { body, token } = {}) {
  const response = await fetch(origin + path, {
    method: body === undefined ? 'GET' : 'POST', redirect: 'error',
    headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(15_000),
  });
  if (!response.ok) throw new Error(`fixture HTTP ${response.status} ${path}`);
  return response.json();
}
async function waitReady(origin, path) {
  const end = Date.now() + 90_000;
  while (Date.now() < end && !stop.signal.aborted) {
    try { await request(origin, path); return; } catch { await delay(1_000); }
  }
  throw new Error('fixture 启动超时');
}
await mkdir(root, { recursive: true, mode: 0o700 });
try {
  const sourcePort = await port(), corePort = await port(), webPort = await port();
  const sourceOrigin = `http://127.0.0.1:${sourcePort}`;
  const coreOrigin = `http://127.0.0.1:${corePort}`;
  const origin = `http://127.0.0.1:${webPort}`;
  const modelKey = randomBytes(32).toString('hex'), modelStats = resolve(root, 'model-stats.json');
  let modelCalls = 0;
  await writeFile(modelStats, JSON.stringify({ calls: 0 }), { mode: 0o600 });
  model = createServer(async (req, res) => {
    try {
      assert.equal(req.url, '/v1/chat/completions');
      assert.equal(req.headers.authorization, `Bearer ${modelKey}`);
      const body = JSON.parse(await requestText(req));
      assert.equal(body.model, 'android-fixture'); assert.equal(body.stream, true);
      modelCalls++;
      await writeFile(modelStats, JSON.stringify({ calls: modelCalls }), { mode: 0o600 });
      const question = body.messages.at(-1).content;
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      res.write(`data: ${JSON.stringify({ model: 'android-fixture', choices: [{
        index: 0, delta: { content: 'Android 洞察 🌌\n\n来自隔离模型的回复。' }, finish_reason: null,
      }] })}\n\n`);
      if (question.includes('ANDROID_HOLD')) return;
      if (question.includes('ANDROID_FAIL')) { res.end(); return; }
      res.end(`data: ${JSON.stringify({ model: 'android-fixture', choices: [{
        index: 0, delta: {}, finish_reason: 'stop',
      }] })}\n\ndata: [DONE]\n\n`);
    } catch {
      if (!res.headersSent) res.writeHead(400);
      res.end();
    }
  });
  model.listen(0, '127.0.0.1'); await once(model, 'listening');
  await docker(['run', '-d', '--name', name, '--cpus', '0.5', '--memory', '512m',
    '--mount', 'type=volume,target=/var/opt/memos', '-p', `127.0.0.1:${sourcePort}:5230`,
    'neosmemo/memos:0.31.0@sha256:24c2707ddd8fbd2ceaa7a61ecab86a53cdcde640b24ce572b73da14b00b5785f']);
  containerStarted = true;
  await waitReady(sourceOrigin, '/api/v1/instance/profile');
  await request(sourceOrigin, '/api/v1/users', { body: { username, password, role: 'ADMIN' } });
  const login = await request(sourceOrigin, '/api/v1/auth/signin', { body: { passwordCredentials: { username, password } } });
  const pat = (await request(sourceOrigin, `/api/v1/${login.user.name}/personalAccessTokens`, {
    body: { description: 'Android fixture', expiresInDays: 0 }, token: login.accessToken,
  })).token;
  const config = JSON.parse(await readFile(resolve(repo, 'config.example.json'), 'utf8'));
  config.memos = { ...config.memos, instance_id: name, base_url: sourceOrigin, public_url: sourceOrigin, user: login.user.name, token: pat };
  config.web = { enabled: true, public_origin: origin, state_encryption_key: randomBytes(32).toString('base64') };
  config.state_dir = resolve(root, 'state'); config.index_dir = resolve(root, 'index');
  config.cursor_secret = randomBytes(32).toString('hex');
  config.access_tokens = [{ name: 'fixture-read', token: randomBytes(32).toString('hex'), scopes: ['notes:read'] }];
  config.llm = { ...config.llm, enabled: true, protocol: 'openai-compatible',
    base_url: `http://127.0.0.1:${model.address().port}/v1`, api_key: modelKey, model: 'android-fixture', max_retries: 0 };
  config.embedding.enabled = false;
  config.notifications.enabled = false;
  await writeFile(resolve(root, 'config.json'), JSON.stringify(config), { mode: 0o600 });
  const log = await open(resolve(root, 'core.log'), 'w', 0o600);
  const coreEnv = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('MOERISE_')));
  core = spawn(resolve(root, 'moerise'), ['serve', '--config', resolve(root, 'config.json'), '--host', '127.0.0.1', '--port', String(corePort)], {
    cwd: repo, env: { ...coreEnv, GOMAXPROCS: '1' }, stdio: ['ignore', log.fd, log.fd],
  });
  await log.close();
  core.once('exit', () => stop.abort());
  await waitReady(coreOrigin, '/health/ready');
  web = createWebServer({ coreOrigin, publicOrigin: origin, dist: resolve(repo, 'web/src') });
  web.listen(webPort, '127.0.0.1'); await once(web, 'listening');
  const fixture = { origin, username, password, sourceOrigin, sourceToken: pat, modelStats,
    coreCommit: (await runProcess('git', ['rev-parse', 'HEAD'], { cwd: repo })).trim() };
  await writeFile(resolve(root, 'connection.json'), JSON.stringify(fixture), { mode: 0o600 });
  console.log(`Android fixture: ${origin}`);
  console.log(`连接档案：${resolve(root, 'connection.json')}`);
  console.log(`真机反向端口：adb reverse tcp:${webPort} tcp:${webPort}`);
  if (!stop.signal.aborted) await once(stop.signal, 'abort');
} finally {
  if (web) { web.closeAllConnections(); await new Promise(done => web.close(done)); }
  if (core && core.exitCode === null) { core.kill('SIGTERM'); await Promise.race([once(core, 'exit'), delay(5_000)]); if (core.exitCode === null) core.kill('SIGKILL'); }
  if (model) { model.closeAllConnections(); await new Promise(done => model.close(done)); }
  try {
    if (containerStarted) await docker(['rm', '-fv', name]);
  } finally {
    for (const entry of ['config.json', 'connection.json', 'state', 'index', 'moerise', 'model-stats.json']) await rm(resolve(root, entry), { force: true, recursive: true });
  }
}
