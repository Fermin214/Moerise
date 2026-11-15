import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';

const config = JSON.parse(await readFile(process.env.MOERISE_ANDROID_FIXTURE, 'utf8'));
const origin = new URL(config.origin);
assert.equal(config.username, 'android-owner');
assert.equal(origin.protocol, 'http:');
assert.ok(['127.0.0.1', 'localhost', '[::1]'].includes(origin.hostname));
let cookie = '', csrf = '';
async function api(method, path, body, key) {
  const response = await fetch(`${config.origin}/api/app/v1${path}`, {
    method, redirect: 'error', signal: AbortSignal.timeout(65_000),
    headers: { Origin: config.origin, ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(cookie ? { Cookie: cookie, 'X-CSRF-Token': csrf } : {}), ...(key ? { 'Idempotency-Key': key } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  for (const value of response.headers.getSetCookie()) cookie = value.split(';')[0];
  if (response.status === 204) return null;
  const data = await response.json();
  if (!response.ok) {
    const error = new Error(`${method} ${path}: ${data.error?.code || response.status}`);
    error.code = data.error?.code;
    error.retryable = data.error?.retryable === true || response.status === 429 || response.status === 503;
    error.retryAfter = Number(response.headers.get('Retry-After')) || 0;
    throw error;
  }
  return data;
}
const identity = await api('POST', '/session', { username: config.username, password: config.password });
csrf = identity.csrf_token;
try {
  for (let index = 0; index < 100; index++) {
    const content = index === 99 ? `${'Long Markdown\n\n'.repeat(4680)}#benchmark`.slice(0, 65536) :
      `固定笔记 ${index}\n\nAndroid 启动基准，包含中文和 English。 #benchmark`;
    const attachments = index === 99 ? Array.from({ length: 10 }, (_, file) => ({
      filename: `benchmark-${file}.txt`, type: 'text/plain', content: Buffer.from(`fixture ${file}`).toString('base64'),
    })) : undefined;
    await delay(1100);
    for (let attempt = 0; ; attempt++) {
      try {
        await api('POST', '/notes', { content, ...(attachments ? { attachments } : {}) }, `android-benchmark-fixed-${index}`);
        break;
      } catch (error) {
        if (!error.retryable || error.code === 'UPSTREAM_REQUEST_REJECTED' || attempt >= 8) throw error;
        const wait = Math.max(error.retryAfter * 1000, Math.min(30_000, 5000 * 2 ** attempt));
        console.log(`来源暂不可用（${error.code || 'HTTP'}），${wait / 1000} 秒后用原键重试。`);
        await delay(wait);
      }
    }
  }
  const page = await api('GET', '/notes?page_size=100&order=created_desc');
  const fixed = page.notes.filter(note => note.content.startsWith('固定笔记 ') || note.content.startsWith('Long Markdown'));
  assert.equal(fixed.length, 100, '基准使用独立 fixture 数据集');
  const long = fixed.find(note => note.content.startsWith('Long Markdown'));
  assert.equal(Buffer.byteLength(long.content), 65536);
  assert.equal(long.attachments.length, 10);
  console.log('已通过 Core 准备 100 篇固定笔记，含 64 KiB 长文与 10 个附件。');
} finally {
  await api('DELETE', '/session');
}
