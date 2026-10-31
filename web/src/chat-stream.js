function streamFailure(code = 'CHAT_INTERRUPTED') {
  return Object.assign(new Error('聊天流未完成，请重新读取会话状态。'), { code, retryable: false });
}

export async function readChatStream(response, onEvent) {
  if (!response.ok) {
    const value = await response.json();
    throw Object.assign(new Error(value?.error?.message || '聊天请求失败。'), value?.error, { status: response.status });
  }
  if (!/^text\/event-stream(?:;|$)/i.test(response.headers.get('content-type') || '') || !response.body) {
    throw streamFailure('CHAT_PROTOCOL');
  }
  const reader = response.body.getReader(), decoder = new TextDecoder('utf-8', { fatal: true });
  let buffer = '', complete = false;
  const frame = async value => {
    if (!value.trim()) return;
    let kind = 'message';
    const data = [];
    for (const line of value.split('\n')) {
      if (line.startsWith('event:')) kind = line.slice(6).trim();
      else if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
    }
    if (!data.length) return;
    if (complete || !['message', 'delta', 'complete', 'error'].includes(kind)) throw streamFailure('CHAT_PROTOCOL');
    let event;
    try { event = JSON.parse(data.join('\n')); }
    catch { throw streamFailure('CHAT_PROTOCOL'); }
    if (kind === 'error') {
      if (!event?.error?.code) throw streamFailure('CHAT_PROTOCOL');
      if (event.turn?.id) await onEvent({ type: 'message', turn: event.turn });
      throw Object.assign(new Error(event.error.message || '聊天生成失败。'), event.error);
    }
    if (event?.type !== kind ||
        (kind === 'delta' && (typeof event.text !== 'string' || !event.text)) ||
        (kind !== 'delta' && (!event.turn?.id || !event.turn.assistant || !event.turn.user)) ||
        (kind === 'complete' && event.turn.assistant.status !== 'completed')) throw streamFailure('CHAT_PROTOCOL');
    await onEvent(event);
    complete = kind === 'complete';
  };
  try {
    for (;;) {
      const { value, done } = await reader.read();
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true });
      if (buffer.length > 8 * 1024 * 1024) throw streamFailure('CHAT_PROTOCOL');
      buffer = buffer.replace(/\r\n/g, '\n');
      let end;
      while ((end = buffer.indexOf('\n\n')) !== -1) {
        const value = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        await frame(value);
      }
      if (done) break;
    }
    if (buffer.trim() || !complete) throw streamFailure();
  } finally {
    try { await reader.cancel(); } catch { /* 连接失败时仍释放读取器。 */ }
    reader.releaseLock();
  }
}
