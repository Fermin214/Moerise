import assert from 'node:assert/strict';
import { setTimeout as delay } from 'node:timers/promises';

export const fixtureInsightReply = 'Android 洞察 🌌\n\n来自隔离模型的回复。';

// 合并导入、分页、增量检查的压力，给 Memos 原文复查留出请求余量。
export function tuneFixtureConfig(config) {
  config.memos = { ...config.memos, page_size: 100 };
  config.sync = { ...config.sync, poll_seconds: 30, full_seconds: 21600, retry_seconds: 10 };
  config.llm = { ...config.llm, max_concurrency: 1, max_retries: 2 };
  config.embedding = { ...config.embedding, batch_size: 20, max_concurrency: 1,
    max_retries: 3, timeout_seconds: 60 };
  return config;
}

export function createModelGate({ maxPending = 4, intervalMs = 300 } = {}) {
  let pending = 0, tail = Promise.resolve(), nextAt = 0;
  return async function enter(signal) {
    if (pending >= maxPending) return null;
    pending++;
    const previous = tail;
    let release;
    tail = new Promise(done => { release = done; });
    let released = false;
    const finish = () => {
      assert.ok(!released, '模型许可不能重复释放');
      released = true;
      pending--;
      release();
    };
    await previous;
    if (signal?.aborted) { finish(); return null; }
    const wait = nextAt - Date.now();
    if (wait > 0) {
      try { await delay(wait, undefined, { signal }); }
      catch { finish(); return null; }
    }
    nextAt = Date.now() + intervalMs;
    return finish;
  };
}

// 产品模型设置保存在 state.sqlite3，恢复实例时使用正式版本化接口更新。
export async function applyModelPolicy(coreOrigin, publicOrigin, username, password) {
  let cookie = '', csrf = '';
  async function api(method, path, body) {
    const response = await fetch(coreOrigin + '/api/app/v1' + path, {
      method, redirect: 'error', signal: AbortSignal.timeout(30_000),
      headers: { Origin: publicOrigin, ...(body ? { 'Content-Type': 'application/json' } : {}),
        ...(cookie ? { Cookie: cookie, 'X-CSRF-Token': csrf } : {}) },
      body: body ? JSON.stringify(body) : undefined,
    });
    for (const value of response.headers.getSetCookie()) cookie = value.split(';')[0];
    if (!response.ok) {
      const error = new Error(`fixture 设置请求失败：HTTP ${response.status}`);
      error.retryable = [409, 429, 503].includes(response.status);
      throw error;
    }
    return response.status === 204 ? null : response.json();
  }
  try {
    const identity = await api('POST', '/session', { username, password });
    csrf = identity.csrf_token;
    for (let attempt = 0; ; attempt++) {
      try {
        const view = await api('GET', '/settings');
        const settings = {
          llm: { ...view.settings.llm, max_concurrency: 1, max_retries: 2 },
          embedding: { ...view.settings.embedding, batch_size: 20, max_concurrency: 1,
            max_retries: 3, timeout_seconds: 60 },
        };
        if (JSON.stringify(settings.llm) !== JSON.stringify(view.settings.llm) ||
            JSON.stringify(settings.embedding) !== JSON.stringify(view.settings.embedding)) {
          for (const group of Object.values(settings)) {
            for (const [key, value] of Object.entries(group)) {
              if (value === null) delete group[key];
            }
          }
          await api('PATCH', '/settings', { version: view.version, settings });
        }
        return;
      } catch (error) {
        if (!error.retryable || attempt >= 4) throw error;
        await new Promise(done => setTimeout(done, 2000 * (attempt + 1)));
      }
    }
  } finally {
    if (cookie && csrf) await api('DELETE', '/session');
  }
}
