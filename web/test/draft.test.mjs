import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Draft } from '../src/draft.js';

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
