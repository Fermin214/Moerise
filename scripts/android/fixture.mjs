import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, createHash } from 'node:crypto';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { createServer } from 'node:http';
import { text as requestText } from 'node:stream/consumers';
import { mkdir, readFile, writeFile, open, rm } from 'node:fs/promises';
import { resolve, relative, isAbsolute } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runProcess } from '../lib/process.mjs';
import { createFixtureProxy } from './fixture-proxy.mjs';
import { tuneFixtureConfig, createModelGate, applyModelPolicy, fixtureInsightReply, observeFixtureApi } from './fixture-policy.mjs';
import { createMemosThrottle } from './memos-throttle.mjs';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const root = resolve(process.argv[2] || '.local/android/fixture');
const resume = process.argv.includes('--resume');
const commonRoot = process.platform === 'win32' ? 'C:\\Projects\\Moerise' : repo;
if (process.platform === 'win32') assert.equal(resolve(repo).toLowerCase(), resolve(commonRoot).toLowerCase(),
  'Windows fixture 从整合仓库 C:\\Projects\\Moerise 启动');
const fixtureRelative = relative(resolve(commonRoot, '.local/android'), root);
assert.ok(fixtureRelative && !fixtureRelative.startsWith('..') && !isAbsolute(fixtureRelative),
  'fixture 必须放在 .local/android/ 下');
const coreExecutable = process.platform === 'win32' ? 'moerise.exe' : 'moerise';
async function readPrivateJson(path) {
  try { return JSON.parse(await readFile(path)); }
  catch { throw new Error('fixture 私有配置无法读取'); }
}
const prior = resume ? await readPrivateJson(resolve(root, 'connection.json')) : null;
const priorConfig = resume ? await readPrivateJson(resolve(root, 'config.json')) : null;
const name = priorConfig?.memos.instance_id || `moerise-android-${randomBytes(6).toString('hex')}`;
assert.match(name, /^moerise-android-[a-f0-9]{12}$/);
const username = 'android-owner', password = prior?.password || process.env.MOERISE_ANDROID_FIXTURE_PASSWORD || randomBytes(24).toString('hex');
const corpusPath = process.env.MOERISE_ANDROID_CORPUS;
const stars = !!prior?.stars || process.env.MOERISE_ANDROID_STARS === '1' || !!corpusPath;
assert.ok(!prior?.corpus || corpusPath, '既有语料 resume 需要原语料和向量路径');
let corpus, vectorByText, dimensions = 32, corpusMetadata;
if (corpusPath) {
  assert.ok(process.env.MOERISE_ANDROID_VECTORS, '既有语料需要 MOERISE_ANDROID_VECTORS');
  const noteBytes = await readFile(resolve(corpusPath));
  const vectorBytes = await readFile(resolve(process.env.MOERISE_ANDROID_VECTORS));
  corpus = JSON.parse(noteBytes);
  dimensions = Number(process.env.MOERISE_ANDROID_DIMENSIONS || 1024);
  assert.ok(Array.isArray(corpus) && corpus.length > 0 && corpus.length <= 10_000);
  assert.ok(Number.isInteger(dimensions) && dimensions > 0 && dimensions <= 4096);
  assert.equal(vectorBytes.length, corpus.length * dimensions * 4, '笔记顺序、数量与向量维度必须匹配');
  vectorByText = new Map(corpus.map((note, row) => {
    assert.ok(typeof note.content === 'string' && note.content.trim());
    const vector = Array.from({ length: dimensions }, (_, column) =>
      vectorBytes.readFloatLE((row * dimensions + column) * 4));
    assert.ok(vector.every(Number.isFinite));
    return [note.content, vector];
  }));
  corpusMetadata = {
    notes: corpus.length, dimensions,
    notesSha256: createHash('sha256').update(noteBytes).digest('hex'),
    vectorsSha256: createHash('sha256').update(vectorBytes).digest('hex'),
  };
  if (prior?.corpus) assert.deepEqual(corpusMetadata, prior.corpus, 'resume 必须复用相同语料和向量');
}
let core, web, model, control, memosProxy, containerStarted = false, layoutStarted = false, layoutOrigin;
let dispose = process.env.MOERISE_ANDROID_FIXTURE_DISPOSE_ON_EXIT === '1', lifecycle, ownsInstance = false;
const layoutName = `${name}-layout`;
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
async function request(origin, path, { body, token, method } = {}) {
  const response = await fetch(origin + path, {
    method: method || (body === undefined ? 'GET' : 'POST'), redirect: 'error',
    headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(15_000),
  });
  if (!response.ok) throw new Error(`fixture HTTP ${response.status} ${path}`);
  return response.json();
}
async function waitReady(origin, path, signal = stop.signal) {
  const end = Date.now() + 90_000;
  while (Date.now() < end && !signal.aborted) {
    try { await request(origin, path); return; } catch { await delay(1_000); }
  }
  throw new Error('fixture 启动超时');
}
await mkdir(root, { recursive: true, mode: 0o700 });
try {
  // 拒绝复用仍在运行的实例；停止动作由 fixture-control 发给该实例。
  try {
    const oldLifecycle = await readPrivateJson(resolve(root, 'lifecycle.json'));
    const response = await fetch(`${oldLifecycle.origin}/status`, {
      headers: { Authorization: `Bearer ${oldLifecycle.token}` }, signal: AbortSignal.timeout(2000),
    });
    if (response.ok && (await response.json()).id === oldLifecycle.id) throw new Error('fixture 实例仍在运行');
  } catch (error) { if (error.message === 'fixture 实例仍在运行') throw error; }
  if (!resume) {
    try { await readFile(resolve(root, 'config.json')); throw new Error('新实例目录已有配置'); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  ownsInstance = true;
  const sourcePort = prior ? new URL(prior.sourceOrigin).port : await port();
  const webPort = prior ? prior.webPort || Number(new URL(prior.origin).port) :
    process.env.MOERISE_ANDROID_FIXTURE_PORT ? Number(process.env.MOERISE_ANDROID_FIXTURE_PORT) : await port();
  assert.ok(Number.isInteger(webPort) && webPort > 0 && webPort <= 65535, 'fixture 端口无效');
  const sourceOrigin = `http://127.0.0.1:${sourcePort}`;
  const origin = prior?.origin || process.env.MOERISE_ANDROID_FIXTURE_ORIGIN || `http://127.0.0.1:${webPort}`;
  const publicUrl = new URL(origin);
  assert.equal(publicUrl.origin, origin, 'fixture 来源只包含协议、主机和端口');
  assert.ok(publicUrl.protocol === 'https:' || (publicUrl.protocol === 'http:' &&
    ['127.0.0.1', 'localhost', '[::1]'].includes(publicUrl.hostname)), 'fixture 来源需要 HTTPS 或回环 HTTP');
  const modelKey = priorConfig?.llm.api_key || randomBytes(32).toString('hex');
  const modelPort = priorConfig ? new URL(priorConfig.llm.base_url).port : 0;
  if (priorConfig?.embedding.enabled) {
    assert.ok(priorConfig.embedding.api_key === modelKey, 'fixture 模型密钥需一致');
    assert.equal(new URL(priorConfig.embedding.base_url).port, modelPort, 'fixture 模型端口需一致');
  }
  const modelStats = resolve(root, 'model-stats.json');
  let modelCalls = 0;
  const modelGate = createModelGate();
  await writeFile(modelStats, JSON.stringify({ calls: 0 }), { mode: 0o600 });
  model = createServer(async (req, res) => {
    let release;
    try {
      assert.equal(req.headers.authorization, `Bearer ${modelKey}`);
      release = await modelGate();
      if (!release) { res.writeHead(429, { 'Retry-After': '2' }); res.end(); return; }
      const body = JSON.parse(await requestText(req));
      if (stars && req.url === '/v1/embeddings') {
        assert.equal(body.model, 'android-map-fixture');
        const data = body.input.map((text, index) => {
          if (vectorByText) {
            const embedding = vectorByText.get(text);
            assert.ok(embedding, '向量请求必须匹配既有语料正文');
            return { index, embedding };
          }
          const hash = createHash('sha256').update(text).digest();
          const group = Number(/group ([0-3])/.exec(text)?.[1] ?? hash[0] % 4);
          const embedding = Array.from(hash, value => (value / 255 - .5) * .12);
          embedding[group * 4] += 3;
          return { index, embedding };
        });
        res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify({ data }));
        return;
      }
      assert.equal(req.url, '/v1/chat/completions');
      assert.equal(body.model, 'android-fixture'); assert.equal(body.stream, true);
      modelCalls++;
      await writeFile(modelStats, JSON.stringify({ calls: modelCalls }), { mode: 0o600 });
      const question = body.messages.at(-1).content;
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      res.write(`data: ${JSON.stringify({ model: 'android-fixture', choices: [{
        index: 0, delta: { content: fixtureInsightReply }, finish_reason: null,
      }] })}\n\n`);
      if (question.includes('ANDROID_HOLD')) return;
      if (question.includes('ANDROID_FAIL')) { res.end(); return; }
      if (question.includes('ANDROID_SILENT')) await delay(16_000);
      res.end(`data: ${JSON.stringify({ model: 'android-fixture', choices: [{
        index: 0, delta: {}, finish_reason: 'stop',
      }] })}\n\ndata: [DONE]\n\n`);
    } catch {
      if (!res.headersSent) res.writeHead(400);
      res.end();
    } finally { release?.(); }
  });
  model.listen(Number(modelPort), '127.0.0.1'); await once(model, 'listening');
  if (!resume) await docker(['run', '-d', '--name', name, '--cpus', '0.5', '--memory', '512m',
    '--mount', 'type=volume,target=/var/opt/memos', '-p', `127.0.0.1:${sourcePort}:5230`,
    'neosmemo/memos:0.31.0@sha256:24c2707ddd8fbd2ceaa7a61ecab86a53cdcde640b24ce572b73da14b00b5785f']);
  containerStarted = true;
  if (resume) await docker(['start', name]);
  await waitReady(sourceOrigin, '/api/v1/instance/profile');
  if (!resume) await request(sourceOrigin, '/api/v1/users', { body: { username, password, role: 'ADMIN' } });
  const login = await request(sourceOrigin, '/api/v1/auth/signin', { body: { passwordCredentials: { username, password } } });
  const pat = prior?.sourceToken || (await request(sourceOrigin, `/api/v1/${login.user.name}/personalAccessTokens`, {
    body: { description: 'Android fixture', expiresInDays: 0 }, token: login.accessToken,
  })).token;
  const memoSettingPath = '/api/v1/instance/settings/MEMO_RELATED';
  const memoSetting = await request(sourceOrigin, memoSettingPath, { token: pat });
  const updatedMemoSetting = await request(sourceOrigin, memoSettingPath, {
    token: pat, method: 'PATCH',
    body: { ...memoSetting, memoRelatedSetting: { ...memoSetting.memoRelatedSetting, contentLengthLimit: 65_536 } },
  });
  assert.equal(updatedMemoSetting.memoRelatedSetting.contentLengthLimit, 65_536);
  if (stars) {
    const layoutPort = prior ? new URL(prior.layoutOrigin).port : await port();
    const image = process.env.MOERISE_ANDROID_LAYOUT_IMAGE || 'moerise-android-layout:fixture';
    const cpu = process.env.MOERISE_ANDROID_CPU || (process.platform === 'win32' ? '0' :
      (await runProcess('python3', ['-c', 'import os; print(min(os.sched_getaffinity(0)))'], { cwd: repo })).trim());
    assert.match(cpu, /^[0-9]+$/);
    if (!resume) await docker(['run', '-d', '--name', layoutName, '--cpus', '0.5', '--cpuset-cpus', cpu, '--memory', '1g',
      '--security-opt', 'no-new-privileges:true', '--cap-drop', 'ALL', '--read-only', '--tmpfs', '/tmp:size=32m',
      '--mount', 'type=volume,target=/data', '-p', `127.0.0.1:${layoutPort}:8766`, image]);
    layoutStarted = true;
    if (resume) await docker(['start', layoutName]);
    layoutOrigin = `http://127.0.0.1:${layoutPort}`;
    await waitReady(layoutOrigin, '/health');
    const notes = corpus || Array.from({ length: 32 }, (_, index) => ({
      content: `Android star group ${index % 4} item ${index} #stars`,
    }));
    for (const [index, note] of (resume ? [] : notes).entries()) {
      await delay(550);
      for (let attempt = 0; ; attempt++) {
        try {
          await request(sourceOrigin, '/api/v1/memos', { body: {
            content: note.content, visibility: 'PRIVATE',
          }, token: pat });
          break;
        } catch (error) {
          if (!error.message.includes('429') || attempt >= 5) throw error;
          await delay(1100 * (attempt + 1));
        }
      }
      if ((index + 1) % 100 === 0) console.log(`已导入 ${index + 1} / ${notes.length} 篇笔记`);
    }
  }
  memosProxy = createMemosThrottle(sourceOrigin, stop.signal);
  memosProxy.listen(prior?.memosProxyOrigin ? Number(new URL(prior.memosProxyOrigin).port) : 0, '127.0.0.1');
  await once(memosProxy, 'listening');
  const memosProxyOrigin = `http://127.0.0.1:${memosProxy.address().port}`;
  const config = priorConfig || JSON.parse(await readFile(resolve(repo, 'config.example.json'), 'utf8'));
  config.memos = { ...config.memos, instance_id: name, base_url: memosProxyOrigin,
    public_url: sourceOrigin, user: login.user.name, token: pat };
  config.web = { enabled: true, public_origin: origin,
    state_encryption_key: priorConfig?.web.state_encryption_key || randomBytes(32).toString('base64') };
  config.state_dir = resolve(root, 'state'); config.index_dir = resolve(root, 'index');
  if (!resume) config.cursor_secret = randomBytes(32).toString('hex');
  if (!resume) config.access_tokens = [{ name: 'fixture-read', token: randomBytes(32).toString('hex'), scopes: ['notes:read'] }];
  config.llm = { ...config.llm, enabled: true, protocol: 'openai-compatible',
    base_url: `http://127.0.0.1:${model.address().port}/v1`, api_key: modelKey, model: 'android-fixture', max_retries: 0 };
  config.embedding.enabled = false;
  if (stars) {
    config.embedding = { ...config.embedding, enabled: true, provider: 'openai-compatible', protocol: 'openai-compatible',
      base_url: `http://127.0.0.1:${model.address().port}/v1`, api_key: modelKey, model: 'android-map-fixture',
      dimensions, batch_size: 20 };
  }
  config.notifications.enabled = false;
  tuneFixtureConfig(config);
  await writeFile(resolve(root, 'config.json'), JSON.stringify(config), { mode: 0o600 });
  const coreEnv = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('MOERISE_')));
  const coreLog = resolve(root, 'core.log');
  let coreOrigin;
  for (let attempt = 0; ; attempt++) {
    const corePort = await port();
    coreOrigin = `http://127.0.0.1:${corePort}`;
    const log = await open(coreLog, 'w', 0o600);
    const starting = new AbortController();
    core = spawn(resolve(root, coreExecutable), ['serve', '--config', resolve(root, 'config.json'), '--host', '127.0.0.1', '--port', String(corePort), '--web-dir', resolve(repo, process.env.MOERISE_ANDROID_WEB_DIST || 'web/src')], {
      cwd: repo, env: { ...coreEnv, GOMAXPROCS: '1', ...(stars ? { MOERISE_LAYOUT_URL: layoutOrigin, MOERISE_LAYOUT_CHECK_SECONDS: '2' } : {}) }, stdio: ['ignore', log.fd, log.fd],
    });
    core.once('exit', () => starting.abort());
    const spawned = once(core, 'spawn');
    await log.close();
    await spawned;
    try {
      await waitReady(coreOrigin, '/health/ready', AbortSignal.any([starting.signal, stop.signal]));
      if (starting.signal.aborted) throw new Error('fixture Core 已退出');
      break;
    } catch (error) {
      const bindFailed = /address already in use|only one usage of each socket address/i.test(await readFile(coreLog, 'utf8'));
      if (attempt >= 2 || !starting.signal.aborted || !bindFailed || stop.signal.aborted) throw error;
      console.warn(`Core 测试端口 ${corePort} 被占用，重新分配端口`);
    }
  }
  core.once('exit', () => stop.abort());
  await applyModelPolicy(coreOrigin, origin, username, password);
  web = createFixtureProxy(coreOrigin);
  web.prependListener('request', observeFixtureApi);
  web.listen(webPort, '127.0.0.1'); await once(web, 'listening');
  const fixture = { origin, webPort, username, password, sourceOrigin, memosProxyOrigin, sourceToken: pat, modelStats,
    ...(corpusMetadata ? { corpus: corpusMetadata } : {}),
    ...(stars ? { stars, layoutContainer: layoutName, layoutOrigin,
      layoutImage: (await docker(['image', 'inspect', '--format', '{{.Id}}',
        process.env.MOERISE_ANDROID_LAYOUT_IMAGE || 'moerise-android-layout:fixture'])).trim() } : {}),
    coreCommit: (await runProcess('git', ['rev-parse', 'HEAD'], { cwd: repo })).trim() };
  await writeFile(resolve(root, 'connection.json'), JSON.stringify(fixture), { mode: 0o600 });
  lifecycle = { id: randomBytes(12).toString('hex'), token: randomBytes(32).toString('hex') };
  control = createServer((req, res) => {
    if (req.headers.authorization !== `Bearer ${lifecycle.token}`) { res.writeHead(403); res.end(); return; }
    if (req.method === 'GET' && req.url === '/status') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ id: lifecycle.id, nodePid: process.pid, corePid: core.pid,
        sourceRoot: repo, root, origin, name, stars }));
    } else if (req.method === 'POST' && ['/stop', '/dispose'].includes(req.url)) {
      dispose = req.url === '/dispose'; res.writeHead(202); res.end();
      stop.abort();
    } else { res.writeHead(404); res.end(); }
  });
  control.listen(0, '127.0.0.1'); await once(control, 'listening');
  lifecycle.origin = `http://127.0.0.1:${control.address().port}`;
  await writeFile(resolve(root, 'lifecycle.json'), JSON.stringify(lifecycle), { mode: 0o600 });
  await writeFile(resolve(root, 'instance.json'), JSON.stringify({ id: lifecycle.id,
    nodePid: process.pid, corePid: core.pid, sourceRoot: repo, root, name, stars,
    startedAt: new Date().toISOString() }), { mode: 0o600 });
  console.log(`Android fixture: ${origin}`);
  console.log(`连接档案：${resolve(root, 'connection.json')}`);
  if (publicUrl.protocol === 'http:') console.log(`真机反向端口：adb reverse tcp:${webPort} tcp:${webPort}`);
  if (!stop.signal.aborted) await once(stop.signal, 'abort');
} finally {
  stop.abort();
  if (!resume && ownsInstance && !lifecycle) dispose = true;
  if (control) { control.closeAllConnections(); await new Promise(done => control.close(done)); }
  if (web) { web.closeAllConnections(); await new Promise(done => web.close(done)); }
  if (core?.pid && core.exitCode === null) { core.kill('SIGTERM'); await Promise.race([once(core, 'exit'), delay(5_000)]); if (core.exitCode === null) core.kill('SIGKILL'); }
  if (memosProxy) { memosProxy.closeAllConnections(); await new Promise(done => memosProxy.close(done)); }
  if (model) { model.closeAllConnections(); await new Promise(done => model.close(done)); }
  if (dispose) {
    const removals = await Promise.allSettled([
      ...(layoutStarted ? [docker(['rm', '-fv', layoutName])] : []),
      ...(containerStarted ? [docker(['rm', '-fv', name])] : []),
    ]);
    const failures = removals.filter(result => result.status === 'rejected');
    if (failures.length) throw new AggregateError(failures.map(result => result.reason), 'fixture 容器清理失败');
    if (!containerStarted && !layoutStarted) {
      // 没有创建容器时，只清理该启动进程产生的临时配置。
      for (const entry of ['config.json', 'model-stats.json']) await rm(resolve(root, entry), { force: true });
    } else {
      for (const entry of ['config.json', 'connection.json', 'state', 'index', coreExecutable,
        'model-stats.json', 'lifecycle.json', 'instance.json']) await rm(resolve(root, entry), { force: true, recursive: true });
    }
  }
}
