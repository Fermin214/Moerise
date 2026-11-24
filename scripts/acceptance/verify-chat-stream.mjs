import { randomUUID } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { text } from 'node:stream/consumers';
import { setTimeout as delay } from 'node:timers/promises';
import { parseArgs } from 'node:util';

const uuid = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;
const terminalStatuses = new Set(['completed', 'failed', 'interrupted', 'canceled']);
const errorCodes = new Set([
  'CHAT_BUSY', 'CHAT_CANCELED', 'CHAT_CONFLICT', 'CHAT_FAILED', 'CHAT_INTERRUPTED',
  'CHAT_MESSAGE_NOT_FOUND', 'CHAT_NOT_FOUND', 'CHAT_STREAM_CLOSED',
  'LLM_AUTH', 'LLM_CANCELED', 'LLM_DISABLED', 'LLM_OUTPUT_LIMIT', 'LLM_PROTOCOL',
  'LLM_RATE_LIMITED', 'LLM_REJECTED', 'LLM_TIMEOUT', 'LLM_UNAVAILABLE',
]);
const safeCode = value => value === '' ? '' : errorCodes.has(value) ? value : 'UNKNOWN';

function origin(value) {
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password ||
      ![url.origin, `${url.origin}/`].includes(value)) throw new Error('invalid_origin');
  return url.origin;
}

function minimum(value) {
  if (!/^[1-9]\d*$/.test(value) || !Number.isSafeInteger(Number(value))) throw new Error('invalid_minimum');
  return Number(value);
}

function options(args) {
  const { values } = parseArgs({ args, options: {
    origin: { type: 'string' }, connection: { type: 'string' }, notes: { type: 'string' },
    'fail-marker': { type: 'string' }, language: { type: 'string', default: 'zh-CN' },
    'min-headings': { type: 'string', default: '3' },
    'min-words-per-section': { type: 'string', default: '40' },
  } });
  const notes = values.notes?.split(',').map(name => name.trim()).map(name => name.startsWith('memos/') ? name : `memos/${name}`);
  const failMarker = values['fail-marker'];
  if (!values.connection || !notes?.length || notes.length > 1000 || new Set(notes).size !== notes.length ||
      !notes.every(name => /^memos\/[A-Za-z0-9_-]+$/.test(name)) || !['zh-CN', 'en'].includes(values.language) ||
      (failMarker !== undefined && !failMarker.trim())) throw new Error('invalid_arguments');
  return { origin: origin(values.origin), connection: values.connection, notes, failMarker, language: values.language,
    headings: minimum(values['min-headings']), words: minimum(values['min-words-per-section']) };
}

function turnSummary(name) {
  return { name, status: 'not_run', request_id: null, non_empty_delta_count: 0,
    sse_terminal: null, sse_error_code: null, get_status: null, get_error_code: null,
    content_match: false, content_chars: 0 };
}

function proseOutsideFences(content) {
  let fence, listDepth = 0, listIndents = [''];
  return content.split(/\r\n|\r|\n/).map(line => {
    const quote = /^(?: {0,3}>[ \t]?)+/.exec(line)?.[0] ?? '';
    const depth = quote.replace(/[ \t]/g, '').length;
    const body = line.slice(quote.length);
    if (fence && (depth < fence.depth ||
        (depth === fence.depth && body.trim() && !body.startsWith(fence.indent)))) fence = undefined;
    if (fence) {
      const closing = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(body.slice(fence.indent.length))?.[1];
      if (depth === fence.depth && closing?.[0] === fence.marker[0] && closing.length >= fence.marker.length) {
        fence = undefined;
      }
      return '';
    }
    if (depth !== listDepth) listIndents = [''];
    listDepth = depth;
    while (body.trim() && !body.startsWith(listIndents.at(-1))) listIndents.pop();
    let indent = listIndents.at(-1);
    const item = /^ {0,3}(?:[-+*]|\d{1,9}[.)]) {1,4}(?=\S)/.exec(body.slice(indent.length))?.[0];
    if (item) {
      indent += ' '.repeat(item.length);
      listIndents.push(indent);
    }
    const opening = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(body.slice(indent.length));
    if (opening && (opening[1][0] === '~' || !opening[2].includes('`'))) {
      fence = { marker: opening[1], depth, indent };
      return '';
    }
    return line;
  }).join('\n');
}

function sectionMetrics(content, language) {
  const prose = proseOutsideFences(content);
  const headings = [...prose.matchAll(/^ {0,3}#{1,6}[ \t]+([1-9]\d*)[.．、)）][ \t]*\S[^\n]*/gm)];
  const segmenter = new Intl.Segmenter(language, { granularity: 'word' });
  const words = headings.map((heading, index) => [...segmenter.segment(
    prose.slice(heading.index + heading[0].length, headings[index + 1]?.index),
  )].filter(segment => segment.isWordLike).length);
  return { heading_count: headings.length, section_word_counts: words,
    numbered: headings.every((heading, index) => Number(heading[1]) === index + 1) };
}

async function readStream(response, summary, fragments) {
  if (response.status !== 200 || !/^text\/event-stream(?:;|$)/i.test(response.headers.get('content-type') ?? '')) {
    await response.body?.cancel();
    throw new Error('invalid_sse_response');
  }
  let buffer = '', kind = '', data = [], started = false;
  for await (const chunk of response.body.pipeThrough(new TextDecoderStream())) {
    buffer += chunk;
    let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline).replace(/\r$/, '');
      buffer = buffer.slice(newline + 1);
      if (line.startsWith('event:')) kind = line.slice(6).trim();
      else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''));
      else if (line === '') {
        if (data.length) {
          if (summary.sse_terminal) throw new Error('event_after_terminal');
          const value = JSON.parse(data.join('\n'));
          if (kind === 'message' && !started && value.turn?.request_id === summary.request_id) started = true;
          else if (kind === 'delta' && started && typeof value.text === 'string') {
            fragments.push(value.text);
            if (value.text.length) summary.non_empty_delta_count++;
          } else if ((kind === 'complete' || kind === 'error') && started && value.turn?.request_id === summary.request_id) {
            summary.sse_terminal = kind;
            summary.sse_error_code = kind === 'error' ? safeCode(value.error?.code) : '';
          } else throw new Error('unexpected_sse_event');
        }
        kind = '';
        data = [];
      }
    }
  }
  if (!summary.sse_terminal || buffer || data.length) throw new Error('sse_incomplete');
}

function client(origin) {
  let cookie = '', csrf = '';
  return {
    get authenticated() { return Boolean(cookie); },
    get canMutate() { return Boolean(cookie && csrf); },
    async request(method, path, { body, stream = false, signal, timeout = 10_000 } = {}) {
      const deadline = AbortSignal.timeout(timeout);
      const response = await fetch(`${origin}/api/app/v1${path}`, {
        method, redirect: 'error', signal: signal ? AbortSignal.any([signal, deadline]) : deadline,
        headers: { Origin: origin, Cookie: cookie, 'X-CSRF-Token': csrf,
          Accept: stream ? 'text/event-stream' : 'application/json',
          ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      if (path === '/session' && method === 'POST' && response.ok) {
        cookie = response.headers.getSetCookie().find(item => /^(?:__Host-moerise_session|moerise_local_session)=/.test(item))
          ?.split(';', 1)[0] ?? '';
      }
      if (stream) return response;
      const value = response.status === 204 ? null : await response.json();
      if (path === '/session' && method === 'POST' && response.ok && typeof value?.csrf_token === 'string') csrf = value.csrf_token;
      return { status: response.status, value, code: safeCode(value?.error?.code) };
    },
  };
}

function createdChatPath(response) {
  return [200, 201].includes(response.status) && uuid.test(response.value?.id) ? `/chats/${response.value.id}` : null;
}

async function cleanChat(api, path, requestId, cleanup) {
  const signal = AbortSignal.timeout(20_000);
  let busy = false;
  while (!signal.aborted) {
    const current = await api.request('GET', path, { signal });
    if (current.status === 404) {
      cleanup.get_after_delete_status = 404;
      cleanup.delete_ok = true;
      return;
    }
    if (current.status !== 200 || !Number.isSafeInteger(current.value?.version)) throw new Error('cleanup_get_failed');
    const assistant = current.value.turns?.find(turn => turn.request_id === requestId)?.assistant;
    if (requestId && (busy || assistant?.status === 'running') && !cleanup.cancel_attempted) {
      cleanup.cancel_attempted = true;
      try {
        await api.request('POST', `${path}/messages/${requestId}/cancel`, { body: {}, signal, timeout: 5_000 });
      } catch (error) {
        if (signal.aborted) throw error;
      }
      continue;
    }
    if (assistant?.status !== 'running') {
      const deletion = await api.request('DELETE', path, { body: { version: current.value.version }, signal });
      if ([204, 404].includes(deletion.status)) {
        cleanup.delete_ok = true;
        cleanup.get_after_delete_status = (await api.request('GET', path, { signal })).status;
        if (cleanup.get_after_delete_status !== 404) throw new Error('cleanup_delete_unconfirmed');
        return;
      }
      if (!['CHAT_BUSY', 'CHAT_CONFLICT'].includes(deletion.code)) throw new Error('cleanup_delete_failed');
      busy = deletion.code === 'CHAT_BUSY';
    }
    await delay(200, undefined, { signal });
  }
  throw new Error('cleanup_timeout');
}

function longPrompt({ language, headings, words }) {
  if (language === 'en') return `Use the supplied synthetic notes. Reply with ${headings} numbered Markdown headings of the form ` +
    `## 1. Title. Write at least ${words} words of prose under each heading, excluding the heading.`;
  return `根据所选测试笔记，用简体中文撰写 ${headings} 节分析。标题依次写成“## 1. 标题”“## 2. 标题”等。` +
    `每节正文至少 ${words} 个词，标题不计入。`;
}

async function run(args, signal) {
  const failure = turnSummary('protocol_failure');
  const long = { ...turnSummary('long_reply'), heading_count: 0, section_word_counts: [] };
  const cleanup = { cancel_attempted: false, delete_ok: false, get_after_delete_status: null, logout_ok: false, error: null };
  const report = { origin: null, layers: ['sse', 'core_get'], probe_stage: 'arguments', material_count: 0,
    turns: [failure, long], cleanup, passed: false };
  let api, path, creation, requestId, version;
  try {
    const config = options(args);
    report.origin = config.origin;
    if (!config.failMarker) failure.status = 'skipped';
    report.probe_stage = 'connection';
    const input = config.connection === '-' ? await text(process.stdin) : await readFile(config.connection, 'utf8');
    const connection = JSON.parse(input.replace(/^\uFEFF/, ''));
    if (connection?.environment !== 'test' || origin(connection.origin) !== config.origin ||
        typeof connection.username !== 'string' || !connection.username ||
        typeof connection.password !== 'string' || !connection.password) throw new Error('invalid_connection');
    api = client(config.origin);
    report.probe_stage = 'login';
    const login = await api.request('POST', '/session', { body: { username: connection.username, password: connection.password }, signal });
    if (login.status !== 200 || !api.canMutate) throw new Error('login_failed');
    report.probe_stage = 'create_chat';
    signal.throwIfAborted();
    creation = { request_id: randomUUID(), title: 'Stream acceptance', names: config.notes };
    const created = await api.request('POST', '/chats', { body: creation, signal });
    path = createdChatPath(created);
    if (!path) {
      if (created.status >= 400 && created.status < 500 && ![408, 429].includes(created.status)) creation = undefined;
      throw new Error('create_failed');
    }
    version = created.value.version;
    const materials = created.value.materials?.map(material => material.name);
    if (!Number.isSafeInteger(version) || version < 1 || !materials ||
        materials.length !== config.notes.length || config.notes.some(name => !materials.includes(name))) {
      throw new Error('material_mismatch');
    }
    report.material_count = materials.length;
    for (const turn of report.turns) {
      if (turn.status === 'skipped') continue;
      report.probe_stage = turn.name;
      turn.status = 'failed';
      turn.request_id = requestId = randomUUID();
      const protocolFailure = turn === failure;
      const fragments = [];
      let streamOK = false;
      try {
        const response = await api.request('POST', `${path}/messages`, { stream: true, signal, timeout: 120_000,
          body: { request_id: requestId, version, kind: 'question', language: config.language,
            content: protocolFailure ? `${config.failMarker} stream acceptance` : longPrompt(config) } });
        await readStream(response, turn, fragments);
        streamOK = true;
      } catch (error) {
        if (signal.aborted) throw error;
      }
      const content = fragments.join('');
      turn.content_chars = Array.from(content).length;
      const readback = await api.request('GET', path, { signal });
      const assistant = readback.value?.turns?.find(item => item.request_id === requestId)?.assistant;
      turn.get_status = terminalStatuses.has(assistant?.status) || assistant?.status === 'running' ? assistant.status : null;
      turn.get_error_code = assistant ? safeCode(assistant.error_code) : null;
      turn.content_match = typeof assistant?.content === 'string' && content === assistant.content;
      version = readback.value?.version;
      let lengthOK = true;
      if (!protocolFailure) {
        const { numbered, ...metrics } = sectionMetrics(content, config.language);
        Object.assign(turn, metrics);
        lengthOK = numbered && turn.heading_count >= config.headings &&
          turn.section_word_counts.every(count => count >= config.words);
      }
      const code = protocolFailure ? 'LLM_PROTOCOL' : '';
      if (streamOK && readback.status === 200 && turn.non_empty_delta_count > 0 && turn.content_match && lengthOK &&
          turn.sse_terminal === (protocolFailure ? 'error' : 'complete') && turn.sse_error_code === code &&
          turn.get_status === (protocolFailure ? 'failed' : 'completed') && turn.get_error_code === code) turn.status = 'passed';
      if (!streamOK || !terminalStatuses.has(turn.get_status) || !Number.isSafeInteger(version)) break;
    }
    report.passed = [failure, long].every(turn => ['passed', 'skipped'].includes(turn.status));
    report.probe_stage = report.passed ? 'complete' : report.turns.find(turn => turn.status === 'failed')?.name ?? report.probe_stage;
  } catch {
    report.passed = false;
  } finally {
    const errors = [];
    if (creation && !path) {
      try {
        path = createdChatPath(await api.request('POST', '/chats', { body: creation }));
      } catch {}
      if (!path) errors.push('chat_creation_unconfirmed');
    }
    if (path) {
      try { await cleanChat(api, path, requestId, cleanup); }
      catch { errors.push('chat_cleanup_failed'); }
    }
    if (api?.authenticated) {
      try { cleanup.logout_ok = (await api.request('DELETE', '/session')).status === 204; } catch {}
      if (!cleanup.logout_ok) errors.push('logout_failed');
    }
    if (errors.length) {
      cleanup.error = errors.join(', ');
      if (report.passed) report.probe_stage = 'cleanup';
      report.passed = false;
    }
  }
  return report;
}

const abort = new AbortController();
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => abort.abort());
const report = await run(process.argv.slice(2), abort.signal);
console.log(JSON.stringify(report, null, 2));
process.exitCode = report.passed ? 0 : 1;
