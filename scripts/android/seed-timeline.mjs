import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';

const fixture = JSON.parse(await readFile(process.argv[2], 'utf8'));
const source = new URL(fixture.sourceOrigin);
assert.equal(source.protocol, 'http:');
assert.equal(source.hostname, '127.0.0.1');
assert.equal(fixture.username, 'android-owner');
const memos = new URL('/api/v1/memos', source);
const headers = { Authorization: `Bearer ${fixture.sourceToken}`, 'Content-Type': 'application/json' };

async function request(url, content) {
  for (let attempt = 0; ; attempt++) {
    const response = await fetch(url, {
      method: content ? 'POST' : 'GET', headers, redirect: 'error', signal: AbortSignal.timeout(20_000),
      body: content ? JSON.stringify({ content, visibility: 'PRIVATE' }) : undefined,
    });
    if (response.status === 429 && attempt < 5) {
      await response.body.cancel();
      await delay(1100 * (attempt + 1));
      continue;
    }
    assert.ok(response.ok, `Memos HTTP ${response.status}`);
    return response.json();
  }
}

const page = await request(new URL('?pageSize=35&state=NORMAL', memos));
for (let index = (page.memos || []).length; index < 35; index++) {
  await delay(1100);
  await request(memos, `Timeline position ${index}`);
}
const seeded = await request(new URL('?pageSize=35&state=NORMAL', memos));
assert.equal(new Set(seeded.memos.map(memo => memo.name)).size, 35, '长列表需要 35 篇不同的正常笔记');
