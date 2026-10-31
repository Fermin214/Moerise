import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readChatStream } from '../src/chat-stream.js';

const turn = status => ({ id: 'turn', user: { content: '问题' }, assistant: { status, content: '回复' } });
const event = (type, data) => `event: ${type}\ndata: ${JSON.stringify(data)}\n\n`;
function response(parts) {
  return new Response(new ReadableStream({
    start(controller) {
      for (const part of parts) controller.enqueue(typeof part === 'string' ? new TextEncoder().encode(part) : part);
      controller.close();
    },
  }), { headers: { 'Content-Type': 'text/event-stream; charset=utf-8' } });
}

test('reads split UTF-8 and SSE boundaries and requires durable completion', async () => {
  const data = new TextEncoder().encode(event('message', { type: 'message', turn: turn('running') }) +
    event('delta', { type: 'delta', text: '中文😀\n' }) + event('complete', { type: 'complete', turn: turn('completed') }));
  const seen = [];
  await readChatStream(response([...data].map(byte => new Uint8Array([byte]))), value => seen.push(value));
  assert.deepEqual(seen.map(value => value.type), ['message', 'delta', 'complete']);
  assert.equal(seen[1].text, '中文😀\n');
});

test('EOF, malformed protocol, false completion and trailing output never succeed', async () => {
  for (const parts of [
    [], [event('delta', { type: 'delta', text: 'partial' })], ['event: delta\ndata: {}'],
    [event('complete', { type: 'complete', turn: turn('failed') })],
    [event('complete', { type: 'complete', turn: turn('completed') }), event('delta', { type: 'delta', text: 'late' })],
    ['event: delta\ndata: not-json\n\n'], [new Uint8Array([0xff])],
  ]) await assert.rejects(readChatStream(response(parts), () => {}));
});

test('explicit stream errors restore the terminal snapshot and preserve the error code', async () => {
  const seen = [];
  const stream = response([event('error', {
    error: { code: 'LLM_PROTOCOL', message: '生成失败。', retryable: false }, turn: turn('failed'),
  })]);
  await assert.rejects(readChatStream(stream, value => seen.push(value)), { code: 'LLM_PROTOCOL' });
  assert.equal(seen[0].turn.assistant.status, 'failed');
});

test('completed request replays need no delta and preflight JSON keeps session errors', async () => {
  await readChatStream(response([event('message', { type: 'message', turn: turn('completed') }),
    event('complete', { type: 'complete', turn: turn('completed') })]), () => {});
  await assert.rejects(readChatStream(new Response(JSON.stringify({
    error: { code: 'SESSION_UNAUTHORIZED', message: '登录已失效。' },
  }), { status: 401 }), () => {}), { code: 'SESSION_UNAUTHORIZED', status: 401 });
});

test('SSE comments share frames with data and CRLF can split across chunks', async () => {
  const frame = ': keepalive\r\nevent: complete\r\ndata: ' +
    JSON.stringify({ type: 'complete', turn: turn('completed') }) + '\r\n\r\n';
  const seen = [];
  await readChatStream(response([...frame]), value => seen.push(value));
  assert.equal(seen.length, 1);
  assert.equal(seen[0].type, 'complete');
});
