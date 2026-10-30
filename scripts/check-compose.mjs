import { execFileSync } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import {
  basename, dirname, isAbsolute, resolve,
} from 'node:path';
import {
  existsSync, lstatSync, mkdirSync, readFileSync, realpathSync, rmSync,
  unlinkSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import assert from 'node:assert/strict';

const usage = 'node scripts/check-compose.mjs [--skip-build] [--keep]';
const flags = new Set(process.argv.slice(2));
if (flags.has('--help')) {
  console.log(usage);
  process.exit(0);
}
if ([...flags].some(flag => !['--skip-build', '--keep'].includes(flag))) {
  console.error(usage);
  process.exit(1);
}
assert.ok(Number(process.versions.node.split('.')[0]) >= 24, '需要 Node.js 24+');

const repo = fileURLToPath(new URL('../', import.meta.url));
const id = randomBytes(8).toString('hex');
const project = `moerise-acceptance-${id}`;
const directoryName = `compose-acceptance-${id}`;
const root = resolve(repo, '.local', directoryName);
const envFile = resolve(root, '.env');
const template = readFileSync(resolve(repo, '.env.example'), 'utf8');
const skipBuild = flags.has('--skip-build');
const imageTag = skipBuild ? (process.env.MOERISE_IMAGE_TAG || 'stage34') : project;
assert.match(imageTag, /^[\w][\w.-]{0,127}$/, 'MOERISE_IMAGE_TAG 必须是镜像标签');
const memosImage = 'neosmemo/memos:0.31.0@sha256:24c2707ddd8fbd2ceaa7a61ecab86a53cdcde640b24ce572b73da14b00b5785f';
const sourceName = `${project}-source`;
const oneOffNames = [`${project}-rebuild`, `${project}-backup`, `${project}-restore`];
const evidence = { project, imageTag, startedAt: new Date().toISOString(), scenarios: [], checks: [] };
const cancellation = new AbortController();
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => cancellation.abort(new Error(signal)));
}

// Shell variables must not override the isolated env file or select another Compose project.
const dockerEnv = { ...process.env };
const templateKeys = [...template.matchAll(/^([A-Z][A-Z0-9_]*)=/gm)].map(match => match[1]);
for (const key of Object.keys(dockerEnv)) {
  if (/^(COMPOSE_|MOERISE_)/i.test(key) || templateKeys.includes(key.toUpperCase())) delete dockerEnv[key];
}
dockerEnv.MOERISE_ENV_FILE = envFile;

let active = false;
let buildAttempted = false;
let succeeded = false;
let step = '初始化';
let scenario = '';
let runtime;

function docker(args, { cleanup = false, timeout = 180_000 } = {}) {
  if (!cleanup) cancellation.signal.throwIfAborted();
  try {
    return execFileSync('docker', args, {
      cwd: repo, env: dockerEnv, encoding: 'utf8', timeout,
      maxBuffer: 16 * 1024 * 1024, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true,
    }).trim();
  } catch (error) {
    // Docker errors can echo env configuration; evidence contains only operation and exit status.
    throw new Error(`docker ${args[0]} 失败 (${error.code || error.status || 'signal'})`);
  }
}

function compose(args, options) {
  return docker([
    'compose', '--file', resolve(repo, 'compose.yaml'),
    '--project-name', project, '--env-file', envFile, ...args,
  ], options);
}

function saveEvidence() {
  writeFileSync(resolve(root, 'checks.json'), `${JSON.stringify(evidence, null, 2)}\n`, { mode: 0o600 });
}

async function check(label, action) {
  step = label;
  console.log(`[${scenario || 'compose'}] ${label}`);
  const started = Date.now();
  try {
    await action();
    evidence.checks.push({ scenario, check: label, passed: true, durationMs: Date.now() - started });
  } catch (error) {
    evidence.checks.push({ scenario, check: label, passed: false, durationMs: Date.now() - started });
    throw error;
  } finally {
    saveEvidence();
  }
}

function writeEnv(values) {
  const remaining = new Map(Object.entries(values));
  const quote = value => {
    assert.ok(!/[\r\n']/.test(String(value)), 'env 值包含不支持的字符');
    return `'${value}'`;
  };
  const body = template.replace(/^([A-Z][A-Z0-9_]*)=.*$/gm, (line, key) => {
    if (!remaining.has(key)) return line;
    const value = remaining.get(key);
    remaining.delete(key);
    return `${key}=${quote(value)}`;
  });
  writeFileSync(envFile, `${body.trimEnd()}\n${[...remaining].map(([key, value]) => `${key}=${quote(value)}`).join('\n')}\n`, { mode: 0o600 });
}

async function freePorts(count) {
  const listeners = [];
  try {
    for (let index = 0; index < count; index++) {
      const server = createServer();
      listeners.push(server);
      await new Promise((done, fail) => {
        server.once('error', fail);
        server.listen(0, '127.0.0.1', done);
      });
    }
    return listeners.map(server => server.address().port);
  } finally {
    await Promise.all(listeners.map(server => new Promise(done => server.close(done))));
  }
}

async function request(base, path, { method = 'GET', body, headers = {} } = {}) {
  const response = await fetch(`${base}${path}`, {
    method, redirect: 'error',
    headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.any([cancellation.signal, AbortSignal.timeout(15_000)]),
  });
  const bytes = Buffer.from(await response.arrayBuffer());
  const text = bytes.toString('utf8');
  let value;
  try { value = JSON.parse(text); } catch { /* Attachment responses contain raw bytes. */ }
  return { status: response.status, headers: response.headers, value, bytes, text };
}

function expectStatus(result, status, label) {
  // Avoid assertion diffs containing upstream tokens, passwords or response bodies.
  if (result.status !== status) throw new Error(`${label}: HTTP ${result.status}, 需要 ${status}`);
  return result;
}

async function waitFor(label, action, timeout = 180_000) {
  const deadline = Date.now() + timeout;
  do {
    cancellation.signal.throwIfAborted();
    try { if (await action()) return; } catch { cancellation.signal.throwIfAborted(); }
    await delay(1000, undefined, { signal: cancellation.signal });
  } while (Date.now() < deadline);
  throw new Error(`${label} 超时`);
}

async function ready(url) {
  await waitFor('Web 就绪', async () => (await request(url, '/health/ready')).status === 200);
}

function removeNamedContainer(name) {
  assert.ok([sourceName, ...oneOffNames].includes(name), '拒绝删除范围外容器');
  const found = docker(['ps', '-aq', '--filter', `name=^/${name}$`], { cleanup: true });
  if (found) docker(['rm', '-f', '-v', name], { cleanup: true });
}

function cleanupResources() {
  if (!active) return;
  // An external source has no Compose project labels and must be removed before its network.
  const errors = [];
  for (const name of [sourceName, ...oneOffNames]) {
    try { removeNamedContainer(name); } catch { errors.push(`删除 ${name} 失败`); }
  }
  try {
    compose(['down', '--volumes', '--remove-orphans', '--timeout', '10'], { cleanup: true });
  } catch { errors.push('compose down 失败'); }
  if (errors.length) throw new Error(errors.join('; '));
  active = false;
}

function guardTemporaryRoot() {
  const local = resolve(repo, '.local');
  const target = resolve(root);
  assert.ok(isAbsolute(envFile) && dirname(target) === local && basename(target) === directoryName);
  assert.match(directoryName, /^compose-acceptance-[a-f0-9]{16}$/);
  assert.ok(!lstatSync(target).isSymbolicLink(), '临时目录不能是符号链接');
  assert.equal(realpathSync(local), local, '.local 不能重定向到其他目录');
  assert.equal(realpathSync(target), target, '临时目录不能重定向到其他目录');
  return target;
}

function removeTemporaryRoot() {
  const target = guardTemporaryRoot();
  rmSync(target, { recursive: true, force: false });
  mkdirSync(target, { mode: 0o700 });
}

async function runScenario(kind) {
  scenario = kind;
  const [webPort, memosPort, corePort] = await freePorts(3);
  const url = `http://127.0.0.1:${webPort}`;
  const sourceURL = `http://127.0.0.1:${memosPort}`;
  const username = `acceptance-${randomBytes(6).toString('hex')}`;
  const password = randomBytes(24).toString('hex');
  const instance = `${project}-${kind}`;
  const values = {
    COMPOSE_PROFILES: kind === 'bundled' ? 'memos' : '',
    COMPOSE_PROJECT_NAME: project, MOERISE_IMAGE_TAG: imageTag,
    MOERISE_ENV_FILE: envFile.replaceAll('\\', '/'),
    WEB_BIND_ADDRESS: '127.0.0.1', WEB_PORT: webPort, MEMOS_PORT: memosPort, MOERISE_PORT: corePort,
    MOERISE_PUBLIC_ORIGIN: url, MOERISE_WEB_ENABLED: 'true',
    MOERISE_MEMOS_INSTANCE_ID: instance,
    MOERISE_MEMOS_BASE_URL: kind === 'bundled' ? 'http://memos:5230' : 'http://source:5230',
    MOERISE_MEMOS_PUBLIC_URL: sourceURL,
    MOERISE_MEMOS_USER: 'users/bootstrap', MOERISE_MEMOS_TOKEN: '',
    MOERISE_ACCESS_TOKEN: randomBytes(32).toString('hex'),
    MOERISE_CURSOR_SECRET: randomBytes(32).toString('hex'),
    MOERISE_STATE_ENCRYPTION_KEY: randomBytes(32).toString('base64'),
    MOERISE_NOTIFICATIONS_ENABLED: 'false',
  };
  writeEnv(values);
  active = true;
  evidence.scenarios.push({ scenario: kind, project, url, sourceURL });
  if (!skipBuild && !buildAttempted) {
    await check('构建 Core 与 Web', () => {
      buildAttempted = true;
      compose(['build', 'moerise', 'web'], { timeout: 1_200_000 });
    });
  }
  await check('启动独立 Memos 来源', async () => {
    if (kind === 'bundled') {
      compose(['up', '-d', 'memos']);
    } else {
      compose(['create', '--no-build', 'moerise']);
      docker([
        'run', '-d', '--name', sourceName, '--network', `${project}_local`,
        '--network-alias', 'source', '--publish', `127.0.0.1:${memosPort}:5230`,
        '--mount', 'type=volume,target=/var/opt/memos', memosImage,
      ]);
      const labels = JSON.parse(docker(['inspect', '--format', '{{json .Config.Labels}}', sourceName]));
      assert.ok(!labels?.['com.docker.compose.project'], 'external 来源不能属于 Compose 项目');
    }
    await waitFor('Memos 就绪', async () => (await request(sourceURL, '/api/v1/instance/profile')).status === 200);
    expectStatus(await request(sourceURL, '/healthz'), 200, 'Memos 健康检查');
  });

  let pat;
  await check('官方 REST 建立账户、PAT 与作者绑定', async () => {
    expectStatus(await request(sourceURL, '/api/v1/users', {
      method: 'POST', body: { username, password, role: 'ADMIN' },
    }), 200, '创建管理员');
    const login = expectStatus(await request(sourceURL, '/api/v1/auth/signin', {
      method: 'POST', body: { passwordCredentials: { username, password } },
    }), 200, 'Memos 登录').value;
    assert.ok(login?.accessToken && /^users\/[A-Za-z0-9_-]+$/.test(login?.user?.name), 'Memos 登录响应不完整');
    pat = expectStatus(await request(sourceURL, `/api/v1/${login.user.name}/personalAccessTokens`, {
      method: 'POST', headers: { Authorization: `Bearer ${login.accessToken}` },
      body: { description: 'Compose acceptance', expiresInDays: 0 },
    }), 200, '创建 PAT').value?.token;
    assert.ok(typeof pat === 'string' && pat.length > 0, 'Memos PAT 为空');
    const me = expectStatus(await request(sourceURL, '/api/v1/auth/me', {
      headers: { Authorization: `Bearer ${pat}` },
    }), 200, 'PAT 身份').value;
    assert.ok(me?.user?.name === login.user.name, 'PAT 作者不匹配');
    values.MOERISE_MEMOS_USER = me.user.name;
    values.MOERISE_MEMOS_TOKEN = pat;
    writeEnv(values);
    compose(['up', '-d', '--no-build', 'moerise', 'web']);
    await ready(url);
  });

  let cookie = '';
  let csrf = '';
  const api = async (method, path, body, status = 200, extra = {}) => {
    const result = expectStatus(await request(url, path, {
      method, body, headers: {
        Cookie: cookie, Origin: url, 'X-CSRF-Token': csrf,
        ...(['POST', 'PATCH', 'DELETE'].includes(method) ? { 'Idempotency-Key': randomUUID() } : {}),
        ...extra,
      },
    }), status, `${method} ${path}`);
    assert.equal(result.headers.get('cache-control'), 'no-store');
    assert.ok(!result.text.includes(pat) && !result.text.includes('accessToken'), 'Web 泄露来源凭据');
    if (result.value?.csrf_token) csrf = result.value.csrf_token;
    return result;
  };
  const notePath = note => {
    assert.match(note?.name || '', /^memos\/[A-Za-z0-9_-]+$/);
    return `/api/app/v1/notes/${note.name.slice('memos/'.length)}`;
  };
  let note;
  let pure;
  let download;
  let saved;
  const original = '  #compose\nOriginal text with whitespace\n\nlast line \n ';
  const edited = '  #compose #edited\nEdited text\n\nlast line \n ';
  const attachmentBytes = Buffer.from('private compose attachment\n');
  await check('Web 登录、原文创建与编辑、纯附件及受保护读取', async () => {
    const login = await api('POST', '/api/app/v1/session', { username, password });
    const sessionCookie = login.headers.getSetCookie().find(value => /^moerise[^=]*=/i.test(value));
    assert.ok(sessionCookie && /;\s*HttpOnly/i.test(sessionCookie) && /;\s*SameSite=Lax/i.test(sessionCookie), '会话 Cookie 属性错误');
    cookie = sessionCookie.split(';')[0];
    assert.ok(csrf, '缺少 CSRF');
    note = (await api('POST', '/api/app/v1/notes', { content: original }, 201)).value;
    assert.equal(note.content, original);
    const source = expectStatus(await request(sourceURL, `/api/v1/${note.name}`, {
      headers: { Authorization: `Bearer ${pat}` },
    }), 200, 'Memos 原文读回').value;
    assert.equal(source.content, original);
    note = (await api('PATCH', notePath(note), { content: edited, source_hash: note.source_hash })).value;
    assert.equal(note.content, edited);
    note = (await api('PATCH', notePath(note), { state: 'ARCHIVED', source_hash: note.source_hash })).value;
    assert.ok((await api('GET', '/api/app/v1/notes?state=ARCHIVED')).value.notes.some(item => item.name === note.name));
    note = (await api('PATCH', notePath(note), { state: 'NORMAL', source_hash: note.source_hash })).value;
    assert.ok((await api('GET', '/api/app/v1/notes?tag=compose&query=Edited')).value.notes.some(item => item.name === note.name));
    const disposable = (await api('POST', '/api/app/v1/notes', { content: 'Delete this note' }, 201)).value;
    await api('DELETE', notePath(disposable), { source_hash: disposable.source_hash }, 204);
    await api('GET', notePath(disposable), undefined, 404);
    pure = (await api('POST', '/api/app/v1/notes', {
      content: '', attachments: [{
        filename: 'compose.txt', type: 'text/plain', content: attachmentBytes.toString('base64'),
      }],
    }, 201)).value;
    assert.equal(pure.content, '');
    assert.equal(pure.attachments?.length, 1);
    download = pure.attachments[0].content_url;
    assert.ok(download?.startsWith(`${notePath(pure)}/attachments/`) && download.endsWith('/content'), '附件 URL 越界');
    assert.ok((await api('GET', download)).bytes.equals(attachmentBytes), '附件内容改变');
    const anonymous = expectStatus(await request(url, download), 401, '匿名附件读取');
    assert.ok(!anonymous.bytes.includes(attachmentBytes), '匿名读取泄露附件');
    await api('GET', `${notePath(note)}/attachments/${pure.attachments[0].name.split('/')[1]}/content`, undefined, 404);
    await api('POST', '/api/app/v1/notes', { content: 'rejected' }, 403, { Origin: 'http://127.0.0.1:1' });
    await api('POST', '/api/app/v1/notes', { content: 'rejected' }, 403, { 'X-CSRF-Token': '' });
  });
  await check('设置按 version 保存', async () => {
    const initial = (await api('GET', '/api/app/v1/settings')).value;
    assert.ok(Number.isInteger(initial?.version), '设置版本错误');
    saved = (await api('PATCH', '/api/app/v1/settings', {
      version: initial.version, settings: { timezone: 'UTC', review: { count: 5 } },
    })).value;
    assert.equal(saved.version, initial.version + 1);
    await api('PATCH', '/api/app/v1/settings', {
      version: initial.version, settings: { review: { count: 6 } },
    }, 409);
  });

  async function retained() {
    const identity = (await api('GET', '/api/app/v1/session')).value;
    assert.equal(identity.user?.name, values.MOERISE_MEMOS_USER);
    const settings = (await api('GET', '/api/app/v1/settings')).value;
    assert.equal(settings.version, saved.version);
    assert.equal(settings.settings?.timezone, 'UTC');
    assert.equal(settings.settings?.review?.count, 5);
    assert.equal((await api('GET', notePath(note))).value.content, edited);
    assert.equal((await api('GET', notePath(pure))).value.content, '');
    assert.ok((await api('GET', download)).bytes.equals(attachmentBytes), '附件未保留');
  }
  const sourceControl = command => kind === 'bundled'
    ? compose([command, 'memos'])
    : docker([command, sourceName]);

  await check('重启 Core 与 Memos 后保留会话、设置、笔记和附件', async () => {
    compose(['stop', 'moerise']);
    sourceControl('restart');
    await waitFor('Memos 重启', async () => (await request(sourceURL, '/api/v1/instance/profile')).status === 200);
    compose(['start', 'moerise']);
    await ready(url);
    await retained();
  });
  await check('强制重建 Core/Web 容器后保留产品状态', async () => {
    compose(['up', '-d', '--no-build', '--force-recreate', 'moerise', 'web']);
    await ready(url);
    await retained();
  });
  await check('停止 Core 并重建索引后保留产品状态', async () => {
    compose(['stop', 'moerise']);
    compose(['run', '--rm', '--no-deps', '--name', oneOffNames[0], 'moerise', 'rebuild-index']);
    compose(['up', '-d', '--no-build', 'moerise', 'web']);
    await ready(url);
    await retained();
  });
  await check('导出备份并核对 manifest 与 SQLite 文件', () => {
    compose(['run', '--rm', '--no-deps', '--name', oneOffNames[1], 'moerise', 'backup', '--output', '/backups/acceptance']);
    for (const file of ['manifest.json', 'state.sqlite3']) {
      compose(['cp', `moerise:/backups/acceptance/${file}`, resolve(root, file)]);
    }
    const manifest = JSON.parse(readFileSync(resolve(root, 'manifest.json'), 'utf8'));
    const database = readFileSync(resolve(root, 'state.sqlite3'));
    assert.ok(database.subarray(0, 16).equals(Buffer.from('SQLite format 3\0')), '备份不是 SQLite');
    assert.equal(manifest.format, 1);
    assert.equal(manifest.instance_id, instance);
    assert.equal(manifest.user, values.MOERISE_MEMOS_USER);
    assert.equal(manifest.projection, 'rebuild-from-memos');
    assert.equal(manifest.state_sha256, createHash('sha256').update(database).digest('hex'));
  });
  await check('从宿主机导出快照恢复并清除旧会话', async () => {
    compose(['stop', 'moerise']);
    compose([
      'run', '--rm', '--no-deps', '--name', oneOffNames[2],
      '--volume', `${root}:/restore-source:ro`,
      'moerise', 'restore', '--source', '/restore-source', '--safety-backup', '/backups/before-restore',
    ]);
    compose(['up', '-d', '--no-build', 'moerise', 'web']);
    await ready(url);
    await api('GET', '/api/app/v1/session', undefined, 401);
    const login = await api('POST', '/api/app/v1/session', { username, password });
    cookie = login.headers.getSetCookie().find(value => /^moerise[^=]*=/i.test(value)).split(';')[0];
    await retained();
    unlinkSync(resolve(root, 'manifest.json'));
    unlinkSync(resolve(root, 'state.sqlite3'));
  });
  await check('来源断连时 live=200、ready=503 且 API 不返回缓存', async () => {
    sourceControl('stop');
    for (const path of ['/api/app/v1/notes', notePath(note), download]) {
      const result = await request(url, path, { headers: { Cookie: cookie } });
      assert.ok(result.status >= 500 && result.status <= 599, '来源断连仍返回成功或会话失效');
      assert.ok(!result.text.includes(edited) && !result.bytes.includes(attachmentBytes) &&
        !result.text.includes(pat) && !result.value?.notes && !result.value?.content, '来源断连泄露缓存或凭据');
    }
    // Core readiness reads persisted sync state; the default source poll interval is 60 seconds.
    await waitFor('来源断连就绪降级', async () => (await request(url, '/health/ready')).status === 503, 90_000);
    expectStatus(await request(url, '/health/live'), 200, 'Web 存活');
    expectStatus(await request(`http://127.0.0.1:${corePort}`, '/health/live'), 200, 'Core 存活');
    sourceControl('start');
    await ready(url);
    await retained();
  });
  await check('Web 不暴露机器 REST 与 MCP', async () => {
    expectStatus(await request(url, '/api/ext/v1/notes', { headers: { Cookie: cookie } }), 404, '机器 REST');
    expectStatus(await request(url, '/mcp', { method: 'POST', body: {}, headers: { Cookie: cookie, Origin: url } }), 404, 'MCP');
  });
  if (kind === 'external') {
    await check('应用停止后独立 Memos 仍存活', async () => {
      compose(['stop', 'web', 'moerise']);
      const source = expectStatus(await request(sourceURL, `/api/v1/${note.name}`, {
        headers: { Authorization: `Bearer ${pat}` },
      }), 200, '独立来源存活');
      assert.equal(source.value?.content, edited);
      compose(['up', '-d', '--no-build', 'moerise', 'web']);
      await ready(url);
      await retained();
    });
  }
  runtime = { root, project, envFile, url, username, password };
}

mkdirSync(resolve(repo, '.local'), { recursive: true });
assert.equal(realpathSync(resolve(repo, '.local')), resolve(repo, '.local'), '.local 不能重定向到其他目录');
mkdirSync(root, { mode: 0o700 });
try {
  docker(['compose', 'version']);
  docker(['info', '--format', '{{.ServerVersion}}']);
  await runScenario('bundled');
  await check('删除 bundled 资源以隔离 external 场景', cleanupResources);
  await runScenario('external');
  succeeded = true;
} catch (error) {
  // Requests and subprocess wrappers exclude response bodies and credentials.
  console.error(`验收失败：${scenario || 'compose'} / ${step}`);
  console.error(error.message);
  process.exitCode = 1;
} finally {
  let kept = succeeded && flags.has('--keep');
  if (kept) {
    try {
      writeFileSync(resolve(root, 'runtime.json'), `${JSON.stringify(runtime, null, 2)}\n`, { mode: 0o600 });
    } catch {
      kept = false;
      succeeded = false;
      process.exitCode = 1;
    }
  }
  if (!kept) {
    try {
      cleanupResources();
      evidence.cleanup = true;
    } catch {
      evidence.cleanup = false;
      succeeded = false;
      process.exitCode = 1;
      console.error(`资源回收失败，项目：${project}`);
    }
    if (buildAttempted) {
      try {
        docker(['image', 'rm', `moerise:${imageTag}`, `moerise-web:${imageTag}`], { cleanup: true });
      } catch {
        evidence.imageCleanup = false;
        succeeded = false;
        process.exitCode = 1;
        console.error('验收构建镜像回收失败');
      }
    }
    try { removeTemporaryRoot(); } catch {
      succeeded = false;
      process.exitCode = 1;
      // Delete credentials individually if a directory guard or recursive removal fails.
      try {
        guardTemporaryRoot();
        for (const file of ['.env', 'runtime.json', 'manifest.json', 'state.sqlite3']) {
          const target = resolve(root, file);
          if (existsSync(target)) unlinkSync(target);
        }
      } catch { /* A failed path guard forbids all further filesystem cleanup. */ }
      console.error('临时目录清理失败');
    }
  }
  evidence.passed = succeeded;
  evidence.kept = kept;
  evidence.finishedAt = new Date().toISOString();
  saveEvidence();
  console.log(`证据：${resolve(root, 'checks.json')}`);
  if (kept) console.log(`浏览器验收：${runtime.url}\n运行信息：${resolve(root, 'runtime.json')}`);
}
