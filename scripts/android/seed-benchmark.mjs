import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const connectionFile = await readFile(process.env.MOERISE_ANDROID_FIXTURE);
const connectionSha256 = createHash('sha256').update(connectionFile).digest('hex');
const receiptPath = join(dirname(process.env.MOERISE_ANDROID_FIXTURE), 'benchmark-seed.json');
const config = JSON.parse(connectionFile.toString('utf8'));
const origin = new URL(config.origin);
assert.equal(config.username, 'android-owner');
assert.equal(origin.protocol, 'http:');
assert.ok(['127.0.0.1', 'localhost', '[::1]'].includes(origin.hostname));
assert.equal(origin.pathname, '/');
assert.ok(!origin.search && !origin.hash && !origin.username && !origin.password);
assert.ok(typeof config.password === 'string' && config.password.length > 0);
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
  const isFixed = note => /^固定笔记 \d+\n/.test(note.content) || note.content.startsWith('Long Markdown');
  const firstPagePath = '/notes?state=NORMAL&page_size=20&order=created_desc';
  async function listFixedNotes() {
    const notes = [];
    let firstPage, token = '';
    do {
      const page = await api('GET', firstPagePath + (token ? `&page_token=${encodeURIComponent(token)}` : ''));
      firstPage ??= page;
      assert.ok(Array.isArray(page.notes) && page.notes.length <= 20, '基准分页每页最多 20 条');
      notes.push(...page.notes);
      token = page.next_page_token || '';
      assert.ok(notes.length <= 100 && (!token || notes.length < 100 && page.notes.length > 0),
        '固定数据集不能超过 100 条，分页必须正常推进');
      assert.equal(new Set(notes.map(note => note.name)).size, notes.length, '固定分页不能重复笔记');
    } while (token);
    return { notes, firstPage };
  }
  function checkFirstPage(page, longName) {
    assert.equal(page.notes.length, 20);
    assert.ok(page.next_page_token, '首屏必须保留后续分页');
    assert.ok(page.notes.every(isFixed), '首屏只能包含固定基准笔记');
    const long = page.notes.find(note => note.name === longName);
    assert.ok(long?.content.startsWith('Long Markdown'), '64 KiB 长文必须位于启动首屏');
    assert.equal(Buffer.byteLength(long.content), 65536);
    assert.equal(long.attachments.length, 10);
  }
  let receipt;
  try {
    receipt = JSON.parse(await readFile(receiptPath, 'utf8'));
  } catch (error) {
    if (error.code !== 'ENOENT' && !(error instanceof SyntaxError)) throw error;
  }
  const reusable = receipt?.schema_version === 1 && receipt.connection_sha256 === connectionSha256 &&
    receipt.fixture_notes === 100 && receipt.cached_notes === 20 &&
    receipt.cached_long_note_bytes === 65536 && receipt.cached_long_note_attachments === 10 &&
    typeof receipt.long_note_name === 'string' && receipt.long_note_name.length > 0;
  if (reusable) {
    checkFirstPage(await api('GET', firstPagePath), receipt.long_note_name);
    console.log('已复用固定数据准备记录，并通过 Core 首屏确认 20 条缓存、64 KiB 长文与 10 个附件元数据。');
  } else {
    const existing = await listFixedNotes();
    assert.ok(existing.notes.every(isFixed), '基准需要只包含固定笔记的独立 fixture');
    const contents = new Set(existing.notes.map(note => note.content));
    let wrote = false;
    for (let index = 0; index < 100; index++) {
      const content = index === 99 ? `${'Long Markdown\n\n'.repeat(4680)}#benchmark`.slice(0, 65536) :
        `固定笔记 ${index}\n\nAndroid 启动基准，包含中文和 English。 #benchmark`;
      if (contents.has(content)) continue;
      assert.ok(existing.notes.length < 100, '已有 100 条笔记，但内容与固定数据集不匹配');
      const attachments = index === 99 ? Array.from({ length: 10 }, (_, file) => ({
        filename: `benchmark-${file}.txt`, type: 'text/plain', content: Buffer.from(`fixture ${file}`).toString('base64'),
      })) : undefined;
      await delay(1100);
      for (let attempt = 0; ; attempt++) {
        try {
          await api('POST', '/notes', { content, ...(attachments ? { attachments } : {}) }, `android-benchmark-fixed-${index}`);
          wrote = true;
          break;
        } catch (error) {
          if (!error.retryable || error.code === 'UPSTREAM_REQUEST_REJECTED' || attempt >= 8) throw error;
          const wait = Math.max(error.retryAfter * 1000, Math.min(30_000, 5000 * 2 ** attempt));
          console.log(`来源暂不可用（${error.code || 'HTTP'}），${wait / 1000} 秒后用原键重试。`);
          await delay(wait);
        }
      }
    }
    const page = wrote ? await listFixedNotes() : existing;
    const fixed = page.notes.filter(isFixed);
    assert.equal(fixed.length, 100, '基准使用独立 fixture 数据集');
    assert.equal(new Set(fixed.map(note => note.content)).size, 100, '固定笔记不能重复');
    const long = fixed.find(note => note.content.startsWith('Long Markdown'));
    assert.ok(long, '固定数据集缺少长文');
    assert.equal(Buffer.byteLength(long.content), 65536);
    assert.equal(long.attachments.length, 10);
    checkFirstPage(page.firstPage, long.name);
    await writeFile(receiptPath, JSON.stringify({
      schema_version: 1, connection_sha256: connectionSha256,
      fixture_notes: 100, cached_notes: 20, cached_long_note_bytes: 65536,
      cached_long_note_attachments: 10, long_note_name: long.name,
    }, null, 2) + '\n', 'utf8');
    console.log('后端固定数据集 100 条；启动首屏缓存 20 条，含 64 KiB 长文与 10 个附件元数据。');
  }
} finally {
  await api('DELETE', '/session');
}
