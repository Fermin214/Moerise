import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { text } from 'node:stream/consumers';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('./verify-chat-stream.mjs', import.meta.url));
const chatId = 'eca60a6b-b07b-4fe4-8dc7-0c467f540bd4';
const chatPath = `/chats/${chatId}`;
const username = 'private-test-user', password = 'private-test-password', csrf = 'private-test-csrf';
const cookie = 'moerise_local_session=private-opaque-cookie';
const partial = 'Private partial material 😀';
const prose = 'Private fixture words appear here as synthetic example material.';
const long = `## 1. Findings\n${prose}\n\n## 2. Themes\n${prose}\n`;
const fenceExamples = {
  'fenced-headings': `\`\`\`\`md\n## 1. Findings\n${prose}\n\`\`\`\n## 2. Themes\n${prose}\n\`\`\`\`\n`,
  'fenced-words': `## 1. Findings\nOne word.\n> ~~~text\n> ${prose}\n> ~~~~\n## 2. Themes\nTwo words.\n- \`\`\`text\n  ${prose}\n  \`\`\`\n`,
  'overindented-fence': `\`\`\`md\n    \`\`\`\n${long}\`\`\`\n`,
  'overindented-list-fence': `- Example\n    \`\`\`md\n      \`\`\`\n  ${long.trimEnd().replaceAll('\n', '\n  ')}\n  \`\`\`\n`,
  'valid-indented-fence': `   \`\`\`md\n## 1. Example\n${prose}\n \`\`\`\n${long}`,
  'valid-list-fence': `1. Example\n    \`\`\`md\n    ## 1. Example\n    ${prose}\n      \`\`\`\n\n${long}`,
};

function json(response, status, body, headers = {}) {
  response.writeHead(status, { 'Content-Type': 'application/json', ...headers });
  response.end(body === undefined ? undefined : JSON.stringify(body));
}

function event(response, name, value) {
  response.write(`event: ${name}\r\ndata: ${JSON.stringify(value)}\r\n\r\n`);
}

async function fixture(context, mode) {
  let version = 1, exists = false, deleteAttempts = 0, loggedIn = false;
  let creation;
  const requests = [], turns = [], streams = [], errors = [];
  let origin;
  const server = createServer(async (request, response) => {
    try {
      const method = request.method;
      const path = request.url.replace(/^\/api\/app\/v1/, '');
      const wire = await text(request);
      const body = wire ? JSON.parse(wire) : null;
      requests.push({ method, path, body });
      assert.equal(request.headers.origin, origin);
      if (method === 'POST' && path === '/session') {
        assert.deepEqual(body, { username, password });
        loggedIn = true;
        return json(response, 200, { csrf_token: csrf }, { 'Set-Cookie': `${cookie}; HttpOnly; Path=/` });
      }
      assert.equal(loggedIn, true);
      assert.equal(request.headers.cookie, cookie);
      assert.equal(request.headers['x-csrf-token'], csrf);
      if (method === 'POST' && path === '/chats') {
        assert.deepEqual(body.names, ['memos/synthetic1', 'memos/synthetic2']);
        if (mode === 'create-rejected') return json(response, 422, { error: { code: 'INVALID_ARGUMENT' } });
        if (creation) {
          assert.deepEqual(body, creation);
          assert.equal(exists, true);
          if (mode === 'create-unconfirmed') return response.destroy();
          return json(response, 200, { id: chatId, version, materials: body.names.map(name => ({ name })) });
        }
        creation = body;
        exists = true;
        if (['create-disconnect', 'create-unconfirmed'].includes(mode)) return response.destroy();
        return json(response, 201, { id: chatId, version, materials: body.names.map(name => ({ name })) });
      }
      if (method === 'POST' && path === `${chatPath}/messages`) {
        assert.equal(body.version, version);
        assert.equal(body.language, 'en');
        assert.equal(body.kind, 'question');
        assert.match(request.headers.accept, /text\/event-stream/);
        const protocolFailure = body.content.includes('ANDROID_FAIL');
        const content = protocolFailure ? partial : mode === 'short' ?
          '## 1. Short\nOne word.\n## 2. Short\nTwo words.\n' : fenceExamples[mode] ?? long;
        const requestId = body.request_id;
        streams.push(requestId);
        const turn = { request_id: requestId, assistant: { content: '', status: 'running', error_code: '' } };
        turns.push(turn);
        const unrelated = { request_id: 'unrelated', assistant: { content, status: 'completed', error_code: '' } };
        if (mode === 'mismatch') turns.unshift(unrelated);
        response.writeHead(200, { 'Content-Type': 'text/event-stream; charset=utf-8' });
        event(response, 'message', { type: 'message', turn });
        event(response, 'delta', { type: 'delta', text: content.slice(0, 9) });
        event(response, 'delta', { type: 'delta', text: '' });
        event(response, 'delta', { type: 'delta', text: content.slice(9) });
        if (mode === 'busy') {
          turn.assistant = { content, status: 'interrupted', error_code: 'CHAT_INTERRUPTED' };
          return setImmediate(() => response.destroy());
        }
        turn.assistant = { content: mode === 'mismatch' ? `${content} differs` : content,
          status: protocolFailure ? 'failed' : 'completed', error_code: protocolFailure ? 'LLM_PROTOCOL' : '' };
        version++;
        if (protocolFailure) event(response, 'error', { error: { code: 'LLM_PROTOCOL', message: password }, turn });
        else event(response, 'complete', { type: 'complete', turn });
        return response.end();
      }
      if (method === 'GET' && path === chatPath) {
        return exists ? json(response, 200, { version, turns }) : json(response, 404, { error: { code: 'CHAT_NOT_FOUND' } });
      }
      if (method === 'POST' && path.endsWith('/cancel')) {
        assert.equal(path, `${chatPath}/messages/${streams.at(-1)}/cancel`);
        assert.deepEqual(body, {});
        turns.at(-1).assistant.status = 'canceled';
        version++;
        return json(response, 200, turns.at(-1));
      }
      if (method === 'DELETE' && path === chatPath) {
        assert.equal(body.version, version);
        deleteAttempts++;
        if (mode === 'busy' && deleteAttempts === 1) return json(response, 409, { error: { code: 'CHAT_BUSY' } });
        if (mode === 'delete-fail') return json(response, 500, { error: { code: password } });
        exists = false;
        return json(response, 204);
      }
      if (method === 'DELETE' && path === '/session') {
        loggedIn = false;
        return json(response, 204);
      }
      assert.fail(`Unexpected ${method} ${path}`);
    } catch (error) {
      errors.push(error);
      if (!response.headersSent) json(response, 500, {});
      else response.destroy();
    }
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  origin = `http://127.0.0.1:${server.address().port}`;
  context.after(async () => {
    const closed = new Promise(resolve => server.close(resolve));
    server.closeAllConnections();
    await closed;
    assert.deepEqual(errors, []);
  });
  return { origin, requests, streams };
}

async function invoke(context, fixture, { marker = true, environment = 'test', profileOrigin = fixture.origin,
  connectionFile = false, extra = [] } = {}) {
  const connection = JSON.stringify({ environment, origin: profileOrigin, username, password });
  let input = '-';
  if (connectionFile) {
    const directory = await mkdtemp(join(tmpdir(), 'moerise-stream-profile-'));
    context.after(() => rm(directory, { recursive: true, force: true }));
    input = join(directory, 'connection.json');
    await writeFile(input, '\uFEFF' + connection, { mode: 0o600 });
  }
  const child = spawn(process.execPath, [script, '--origin', fixture.origin, '--connection', input, '--language', 'en',
    '--notes', 'synthetic1,memos/synthetic2', '--min-headings', '2', '--min-words-per-section', '5',
    ...(marker ? ['--fail-marker', 'ANDROID_FAIL'] : []), ...extra], { stdio: ['pipe', 'pipe', 'pipe'] });
  child.stdin.end(connectionFile ? undefined : connection);
  const [[code], stdout, stderr] = await Promise.all([once(child, 'close'), text(child.stdout), text(child.stderr)]);
  assert.equal(stderr, '');
  for (const secret of [username, password, csrf, cookie, partial, prose, 'synthetic1', 'ANDROID_FAIL']) {
    assert.equal(stdout.includes(secret), false);
  }
  const report = JSON.parse(stdout);
  assert.equal(report.origin, extra.length ? null : fixture.origin);
  assert.deepEqual(report.layers, ['sse', 'core_get']);
  assert.equal(report.passed, code === 0);
  return { report, code };
}

test('checks both terminal states and matching persisted content using a private connection file', async context => {
  const service = await fixture(context, 'success');
  const { report, code } = await invoke(context, service, { connectionFile: true });
  assert.equal(code, 0);
  assert.equal(report.material_count, 2);
  const [failure, reply] = report.turns;
  assert.deepEqual(failure, { name: 'protocol_failure', status: 'passed', request_id: service.streams[0],
    non_empty_delta_count: 2, sse_terminal: 'error', sse_error_code: 'LLM_PROTOCOL', get_status: 'failed',
    get_error_code: 'LLM_PROTOCOL', content_match: true, content_chars: Array.from(partial).length });
  assert.deepEqual(reply, { name: 'long_reply', status: 'passed', request_id: service.streams[1],
    non_empty_delta_count: 2, sse_terminal: 'complete', sse_error_code: '', get_status: 'completed',
    get_error_code: '', content_match: true, content_chars: Array.from(long).length, heading_count: 2,
    section_word_counts: [9, 9] });
  assert.deepEqual(report.cleanup, { cancel_attempted: false, delete_ok: true, get_after_delete_status: 404,
    logout_ok: true, error: null });
});

test('skips the failure turn when the marker is omitted', async context => {
  const service = await fixture(context, 'success');
  const { report, code } = await invoke(context, service, { marker: false });
  assert.equal(code, 0);
  assert.equal(report.turns[0].status, 'skipped');
  assert.equal(report.turns[0].request_id, null);
  assert.equal(service.streams.length, 1);
});

for (const mode of ['mismatch', 'short']) {
  test(`fails when ${mode === 'mismatch' ? 'the matching GET turn differs from SSE' : 'a heading section is too short'}`, async context => {
    const service = await fixture(context, mode);
    const { report, code } = await invoke(context, service, { marker: false });
    const reply = report.turns[1];
    assert.equal(code, 1);
    assert.equal(report.probe_stage, 'long_reply');
    assert.equal(reply.get_status, 'completed');
    assert.equal(reply.content_match, mode === 'short');
    assert.equal(reply.heading_count, 2);
    if (mode === 'short') assert.deepEqual(reply.section_word_counts, [2, 2]);
    assert.equal(report.cleanup.get_after_delete_status, 404);
  });
}

for (const [mode, metrics] of [
  ['fenced-headings', [0, []]], ['fenced-words', [2, [2, 2]]],
  ['overindented-fence', [0, []]], ['overindented-list-fence', [0, []]],
]) {
  test(`excludes ${mode} from the long reply criteria`, async context => {
    const service = await fixture(context, mode);
    const { report, code } = await invoke(context, service, { marker: false });
    const reply = report.turns[1];
    assert.equal(code, 1);
    assert.equal(report.probe_stage, 'long_reply');
    assert.equal(reply.content_match, true);
    assert.deepEqual([reply.heading_count, reply.section_word_counts], metrics);
    assert.equal(report.cleanup.get_after_delete_status, 404);
  });
}

for (const mode of ['valid-indented-fence', 'valid-list-fence']) {
  test(`reads prose after a ${mode}`, async context => {
    const service = await fixture(context, mode);
    const { report, code } = await invoke(context, service, { marker: false });
    assert.equal(code, 0);
    assert.equal(report.turns[1].heading_count, 2);
    assert.deepEqual(report.turns[1].section_word_counts, [9, 9]);
  });
}

test('replays the same creation request after a lost response and cleans up the committed chat', async context => {
  const service = await fixture(context, 'create-disconnect');
  const { report, code } = await invoke(context, service, { marker: false });
  assert.equal(code, 1);
  assert.equal(report.probe_stage, 'create_chat');
  assert.equal(service.streams.length, 0);
  const creates = service.requests.filter(request => request.method === 'POST' && request.path === '/chats');
  assert.equal(creates.length, 2);
  assert.deepEqual(creates[0].body, creates[1].body);
  assert.deepEqual(report.cleanup, { cancel_attempted: false, delete_ok: true, get_after_delete_status: 404,
    logout_ok: true, error: null });
});

test('reports an unconfirmed creation separately from a definitive rejection', async context => {
  for (const [mode, count, error] of [['create-unconfirmed', 2, 'chat_creation_unconfirmed'], ['create-rejected', 1, null]]) {
    const service = await fixture(context, mode);
    const { report, code } = await invoke(context, service, { marker: false });
    assert.equal(code, 1);
    assert.equal(report.probe_stage, 'create_chat');
    assert.equal(report.cleanup.error, error);
    assert.equal(report.cleanup.delete_ok, false);
    assert.equal(report.cleanup.logout_ok, true);
    assert.equal(service.requests.filter(request => request.method === 'POST' && request.path === '/chats').length, count);
  }
});

test('on an interrupted stream, handles CHAT_BUSY, cancels its request, reloads version, then deletes', async context => {
  const service = await fixture(context, 'busy');
  const { report, code } = await invoke(context, service, { marker: false });
  assert.equal(code, 1);
  assert.equal(report.turns[1].sse_terminal, null);
  assert.deepEqual(report.cleanup, { cancel_attempted: true, delete_ok: true, get_after_delete_status: 404,
    logout_ok: true, error: null });
  const tail = service.requests.slice(-7);
  assert.deepEqual(tail.map(({ method, path }) => [method, path]), [
    ['DELETE', chatPath], ['GET', chatPath], ['POST', `${chatPath}/messages/${service.streams[0]}/cancel`],
    ['GET', chatPath], ['DELETE', chatPath], ['GET', chatPath], ['DELETE', '/session'],
  ]);
  assert.equal(tail[4].body.version, tail[0].body.version + 1);
});

test('reports cleanup failure separately and still logs out', async context => {
  const service = await fixture(context, 'delete-fail');
  const { report, code } = await invoke(context, service, { marker: false });
  assert.equal(code, 1);
  assert.equal(report.turns[1].status, 'passed');
  assert.equal(report.probe_stage, 'cleanup');
  assert.equal(report.cleanup.delete_ok, false);
  assert.equal(report.cleanup.logout_ok, true);
  assert.equal(report.cleanup.error, 'chat_cleanup_failed');
});

test('rejects non-test and mismatched connection profiles before sending credentials', async context => {
  const service = await fixture(context, 'success');
  for (const options of [{ environment: 'production' }, { profileOrigin: 'https://example.invalid' }]) {
    const { report, code } = await invoke(context, service, options);
    assert.equal(code, 1);
    assert.equal(report.probe_stage, 'connection');
  }
  assert.deepEqual(service.requests, []);
});

test('rejects credential CLI options before any request', async context => {
  const service = await fixture(context, 'success');
  const { report, code } = await invoke(context, service, { extra: ['--password', password] });
  assert.equal(code, 1);
  assert.equal(report.probe_stage, 'arguments');
  assert.deepEqual(service.requests, []);
});
