import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Draft, readAttachments, attachmentLimit } from '../src/draft.js';

test('unknown results and edited text keep the same original content/key pair', () => {
  const draft = new Draft();
  const original = '  第一行\n\n#标签 #第二个标签\n  ';
  draft.content = original;
  const first = draft.begin();
  assert.equal(first.content, original);
  assert.ok(first.key);
  draft.content = '用户修改后的新文字';
  const retry = draft.begin();
  assert.equal(retry, first);
  assert.equal(retry.content, original);
  assert.equal(retry.key, first.key);
  assert.ok(Object.isFrozen(first));
  draft.confirm(retry);
  assert.equal(draft.pending, null);
  assert.equal(draft.content, '用户修改后的新文字');
  assert.notEqual(draft.begin().key, first.key);
});

test('only confirmation or explicit abandonment permits a new key', () => {
  const draft = new Draft();
  draft.content = '正文';
  const first = draft.begin();
  assert.equal(draft.begin(), first);
  draft.abandon();
  assert.equal(draft.content, '正文');
  const second = draft.begin();
  assert.notEqual(second.key, first.key);
  draft.confirm(first);
  assert.equal(draft.pending, second);
  draft.confirm(second);
  assert.equal(draft.content, '');
  assert.equal(draft.pending, null);
});

test('only argument or media type rejection unlocks the original text for editing', () => {
  for (const code of ['INVALID_ARGUMENT', 'UNSUPPORTED_MEDIA_TYPE']) {
    const draft = new Draft();
    draft.content = '原文';
    const attempt = draft.begin();
    draft.reject(attempt, 'WRITE_OUTCOME_UNKNOWN');
    assert.equal(draft.begin(), attempt);
    assert.equal(draft.locked, true);
    draft.reject(attempt, code);
    assert.equal(draft.pending, null);
    assert.equal(draft.locked, false);
    assert.equal(draft.content, '原文');
    draft.content = '修改后的正文';
    const next = draft.begin();
    assert.notEqual(next.key, attempt.key);
    assert.equal(next.content, '修改后的正文');
    draft.reject(attempt, code);
    assert.equal(draft.pending, next);
    assert.equal(draft.locked, true);
  }
});

test('conflicts and unknown outcomes keep the original text locked and retry the same submission', () => {
  for (const code of [
    'WRITE_CONFLICT', 'SOURCE_NOT_FOUND', 'IDEMPOTENCY_CONFLICT', 'UPSTREAM_REQUEST_REJECTED',
    'WRITE_OUTCOME_UNKNOWN', 'NETWORK_ERROR', 'REQUEST_TIMEOUT', 'SESSION_UNAUTHORIZED',
    'NOTE_SAVE_UNCONFIRMED', 'NEW_BACKEND_ERROR', undefined,
  ]) {
    const draft = new Draft();
    const original = '  原文\n#标签\n  ';
    draft.content = original;
    const attempt = draft.begin();
    for (let retry = 0; retry < 2; retry++) {
      draft.reject(attempt, code);
      assert.equal(draft.locked, true);
      assert.equal(draft.content, original);
      assert.equal(draft.pending, attempt);
      assert.equal(draft.pending.key, attempt.key);
      assert.equal(draft.pending.content, original);
      assert.equal(draft.begin(), attempt);
      assert.ok(Object.isFrozen(attempt));
    }
  }
});

test('rejects whitespace-only or oversized new content without mutating content', () => {
  const draft = new Draft();
  draft.content = ' \n\t ';
  assert.throws(() => draft.begin());
  assert.equal(draft.pending, null);
  draft.content = '字'.repeat(21846);
  assert.throws(() => draft.begin(), /64 KiB/);
  assert.equal(draft.content.length, 21846);
  draft.content = 'a'.repeat(65536);
  assert.equal(draft.begin().content.length, 65536);
});

test('attachment-only submissions freeze the full body for ambiguous retries', () => {
  const draft = new Draft();
  draft.attachments = [{ filename: '原文件.txt', type: 'text/plain', content: 'aGVsbG8=' }];
  const first = draft.begin();
  draft.attachments[0].filename = '另一个文件.txt';
  draft.attachments[0].content = 'bmV3';
  draft.reject(first, 'WRITE_OUTCOME_UNKNOWN');
  assert.equal(draft.begin(), first);
  assert.deepEqual(first.body, { content: '', attachments: [{ filename: '原文件.txt', type: 'text/plain', content: 'aGVsbG8=' }] });
  assert.ok(Object.isFrozen(first.body));
  assert.ok(Object.isFrozen(first.body.attachments[0]));
  draft.confirm(first);
  assert.equal(draft.attachments[0].filename, '另一个文件.txt');
});

test('editing attachment-only notes preserves source version and removal list on retry', () => {
  const draft = new Draft();
  const remove = ['attachments/a'];
  const first = draft.begin({ source_hash: 'v1', remove_attachments: remove }, { retained: 1 });
  remove.push('attachments/b');
  assert.deepEqual(first.body.remove_attachments, ['attachments/a']);
  assert.equal(draft.begin({ source_hash: 'v2' }), first);
  assert.equal(first.body.source_hash, 'v1');
  draft.clear();
  assert.equal(draft.pending, null);
  assert.deepEqual(draft.attachments, []);
  assert.equal(draft.content, '');
  assert.throws(() => draft.begin(), /正文或添加附件/);
});

test('removing every existing attachment can leave an unmodified plain-text body', () => {
  const draft = new Draft();
  draft.content = '  只保留正文\n#原生标签  \n';
  const attempt = draft.begin({ source_hash: 'source-v1', remove_attachments: ['attachments/only'] });
  assert.equal(attempt.body.content, '  只保留正文\n#原生标签  \n');
  assert.deepEqual(attempt.body.attachments, []);
  assert.deepEqual(attempt.body.remove_attachments, ['attachments/only']);
  draft.confirm(attempt);
  assert.equal(draft.content, '');
  const empty = new Draft();
  assert.throws(() => empty.begin({ source_hash: 'v1', remove_attachments: ['attachments/only'] }), /正文或添加附件/);
});

test('file input enforces aggregate bytes/count before reading and preserves exact binary data', async () => {
  const file = new File([Uint8Array.from([0, 128, 255])], 'sample.bin', { type: 'application/octet-stream' });
  assert.deepEqual(await readAttachments([file]), [{ filename: 'sample.bin', type: 'application/octet-stream', content: 'AID/' }]);
  await assert.rejects(() => readAttachments(Array(11).fill(file)), err => err.code === 'ATTACHMENT_COUNT');
  await assert.rejects(() => readAttachments([new File([new Uint8Array(attachmentLimit + 1)], 'large.bin')]), err => err.code === 'ATTACHMENT_SIZE');
  const prior = [{ filename: 'one', type: '', content: 'AA==' }];
  const exact = await readAttachments([new File([new Uint8Array(attachmentLimit - 1)], 'exact.bin')], prior);
  const draft = new Draft();
  draft.attachments = [...prior, ...exact];
  assert.ok(draft.begin());
  draft.clear();
  assert.deepEqual(draft.attachments, []);
});
