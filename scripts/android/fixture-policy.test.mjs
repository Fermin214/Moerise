import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createModelGate, tuneFixtureConfig, applyModelPolicy } from './fixture-policy.mjs';

test('模型请求串行排队，队列满时拒绝，释放后可继续', async () => {
  const gate = createModelGate({ maxPending: 2, intervalMs: 0 });
  const releaseFirst = await gate();
  let secondEntered = false;
  const second = gate().then(release => { secondEntered = true; return release; });
  assert.equal(await gate(), null);
  await Promise.resolve();
  assert.equal(secondEntered, false);
  releaseFirst();
  (await second)();
  (await gate())();
});

test('部署限流保留模型空间、密钥和同步重叠窗口', () => {
  const config = { memos: { token: 'fixture-token' }, sync: { overlap_seconds: 300 },
    embedding: { base_url: 'http://127.0.0.1:12345/v1', api_key: 'fixture-key',
      model: 'fixture-1024', dimensions: 1024 } };
  const space = { ...config.embedding };
  tuneFixtureConfig(config);
  for (const [key, value] of Object.entries(space)) assert.equal(config.embedding[key], value);
  assert.equal(config.sync.overlap_seconds, 300);
  assert.equal(config.memos.token, 'fixture-token');
});

test('持久模型设置冲突后重读版本，更新非秘密字段并注销临时会话', async t => {
  const calls = [];
  let conflict = true, version = 7;
  const model = { enabled: true, base_url: 'http://127.0.0.1:12345/v1', model: 'fixture',
    dimensions: 1024, max_concurrency: 2, max_retries: 0, batch_size: 20, timeout_seconds: 30 };
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    const path = new URL(url).pathname;
    const body = options.body ? JSON.parse(options.body) : null;
    calls.push({ path, ...options, body });
    if (path.endsWith('/session') && options.method === 'POST') {
      return new Response(JSON.stringify({ csrf_token: 'fixture-csrf' }), {
        headers: { 'Set-Cookie': 'fixture=session; HttpOnly' },
      });
    }
    if (options.method === 'DELETE') return new Response(null, { status: 204 });
    if (options.method === 'GET') {
      return new Response(JSON.stringify({ version, settings: { embedding: model, stargazing: { enabled: false } } }));
    }
    assert.equal(body.version, version);
    assert.deepEqual(body.settings.stargazing, { enabled: true });
    assert.equal(body.settings.embedding.base_url, model.base_url);
    assert.equal(body.settings.embedding.dimensions, 1024);
    assert.equal(body.settings.embedding.api_key, undefined);
    assert.equal(body.secrets, undefined);
    assert.equal(options.headers.Cookie, 'fixture=session');
    assert.equal(options.headers['X-CSRF-Token'], 'fixture-csrf');
    if (conflict) { conflict = false; version++; return new Response('', { status: 409 }); }
    return new Response('{}');
  });
  await applyModelPolicy('http://127.0.0.1:1502', 'http://127.0.0.1:1501', 'android-owner', 'fixture-password', true);
  assert.equal(calls.filter(call => call.method === 'PATCH').length, 2);
  assert.equal(calls.at(-1).method, 'DELETE');
});
