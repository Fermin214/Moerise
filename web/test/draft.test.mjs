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
