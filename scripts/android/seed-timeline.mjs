import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';

const fixture = JSON.parse(await readFile(process.argv[2], 'utf8'));
const origin = new URL(fixture.origin);
assert.equal(origin.protocol, 'http:');
assert.ok(['127.0.0.1', 'localhost', '[::1]'].includes(origin.hostname));
assert.equal(fixture.username, 'android-owner');
assert.ok(typeof fixture.password === 'string' && fixture.password.length > 0);
let cookie = '', csrf = '';

async function api(method, path, body, key) {
  const response = await fetch(`${fixture.origin}/api/app/v1${path}`, {
    method, redirect: 'error', signal: AbortSignal.timeout(65_000),
    headers: {
      Origin: fixture.origin,
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(cookie ? { Cookie: cookie, 'X-CSRF-Token': csrf } : {}),
      ...(key ? { 'Idempotency-Key': key } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  for (const value of response.headers.getSetCookie()) cookie = value.split(';')[0];
  if (response.status === 204) return null;
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(`${method} ${path}: ${data.error?.code || response.status}`);
    error.code = data.error?.code;
    error.retryable = data.error?.retryable === true || response.status === 429 || response.status === 503;
    error.retryAfter = Number(response.headers.get('Retry-After')) || 0;
    throw error;
  }
  return data;
}

const identity = await api('POST', '/session', { username: fixture.username, password: fixture.password });
csrf = identity.csrf_token;
try {
  const page = await api('GET', '/notes?state=NORMAL&page_size=50&order=created_desc');
  for (let index = (page.notes || []).length; index < 35; index++) {
    const key = `android-timeline-seed-${index}-${Date.now()}`;
    await delay(1100);
    for (let attempt = 0; ; attempt++) {
      try {
        await api('POST', '/notes', { content: `Timeline position ${index}` }, key);
        break;
      } catch (error) {
        if (!error.retryable || error.code === 'UPSTREAM_REQUEST_REJECTED' || attempt >= 8) throw error;
        const wait = Math.max(error.retryAfter * 1000, Math.min(30_000, 5000 * 2 ** attempt));
        await delay(wait);
      }
    }
  }
  const seeded = await api('GET', '/notes?state=NORMAL&page_size=50&order=created_desc');
  assert.ok(new Set(seeded.notes.map(note => note.name)).size >= 35, '长列表需要 35 篇不同的正常笔记');
} finally {
  if (cookie && csrf) await api('DELETE', '/session').catch(() => {});
}
