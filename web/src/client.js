import { l } from './i18n.js';

export function sessionOwner(value) {
  return value ? `${value.instance_id || ''}\0${value.user.name}` : null;
}

export function createClient({ generation, session, onSession, onOwnerChange }) {
  const controllers = new Set();
  const stale = expected => {
    if (expected !== generation()) throw new DOMException('Stale request', 'AbortError');
  };
  async function request(path, { method = 'GET', body, headers = {}, controller = new AbortController(), maxBytes } = {}) {
    const expected = generation();
    controllers.add(controller);
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, 65_000);
    try {
      const response = await fetch(`/api/app/v1${path}`, {
        method, credentials: 'same-origin', cache: 'no-store', redirect: 'error',
        headers: { Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...headers },
        body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal,
      });
      stale(expected);
      let data = null;
      if (response.ok && maxBytes) {
        const reader = response.body.getReader(), decoder = new TextDecoder();
        let total = 0, text = '';
        for (;;) {
          const chunk = await reader.read();
          if (chunk.done) break;
          total += chunk.value.byteLength;
          if (total > maxBytes) { await reader.cancel(); throw new DOMException('Prefetch limit', 'AbortError'); }
          text += decoder.decode(chunk.value, { stream: true });
        }
        data = JSON.parse(text + decoder.decode());
      } else if (response.status !== 204) data = await response.json();
      stale(expected);
      if (!response.ok) {
        const failure = data?.error || {};
        throw Object.assign(new Error(failure.message || l("请求失败，请重试。")), {
          code: failure.code || 'HTTP_ERROR', status: response.status,
          retryable: failure.retryable === true, requestId: failure.request_id,
          changed_names: Array.isArray(failure.changed_names) ? failure.changed_names.filter(name => typeof name === 'string') : [],
        });
      }
      return data;
    } catch (err) {
      stale(expected);
      if (timedOut) throw Object.assign(new Error('Request timed out'), { code: 'REQUEST_TIMEOUT', retryable: true });
      if (err.name === 'AbortError' || err.code) throw err;
      throw Object.assign(new Error('Network request failed'), { code: 'NETWORK_ERROR', retryable: true });
    } finally {
      clearTimeout(timer);
      controllers.delete(controller);
    }
  }
  async function renewCSRF() {
    const owner = sessionOwner(session());
    const value = validIdentity(await request('/session'));
    if (!owner || owner !== sessionOwner(value)) {
      onOwnerChange();
      throw new DOMException('Session changed', 'AbortError');
    }
    onSession(value);
    return value.csrf_token;
  }
  async function sessionRequest(path, options = {}) {
    const expected = generation();
    const headers = { ...options.headers, 'X-CSRF-Token': session()?.csrf_token };
    try { return await request(path, { ...options, headers }); }
    catch (err) {
      if (err.code !== 'CSRF_REJECTED') throw err;
      stale(expected);
      let token;
      try { token = await renewCSRF(); }
      catch (refreshError) {
        if (path === '/session' && options.method === 'DELETE' && refreshError.status === 401) return request(path, { ...options, headers });
        throw refreshError;
      }
      stale(expected);
      return request(path, { ...options, headers: { ...headers, 'X-CSRF-Token': token } });
    }
  }
  return {
    request, sessionRequest, renewCSRF,
    cancel() { for (const controller of controllers) controller.abort(); controllers.clear(); },
  };
}

export function validIdentity(value) {
  if (!value?.user?.name || !value.csrf_token ||
      (value.stargazing_enabled !== undefined && typeof value.stargazing_enabled !== 'boolean')) {
    throw Object.assign(new Error('Invalid session response'), { code: 'SESSION_RESPONSE_INVALID' });
  }
  return value;
}
