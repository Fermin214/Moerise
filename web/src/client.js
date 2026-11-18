import { l } from './i18n.js';

export function createClient({ generation, session, onSession, logoutToken }) {
  const controllers = new Set();
  const stale = expected => {
    if (expected !== generation()) throw new DOMException('Stale request', 'AbortError');
  };
  async function request(path, { method = 'GET', body, headers = {}, controller = new AbortController() } = {}) {
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
      const data = response.status === 204 ? null : await response.json();
      stale(expected);
      if (!response.ok) {
        const failure = data?.error || {};
        throw Object.assign(new Error(failure.message || l("请求失败，请重试。")), {
          code: failure.code || 'HTTP_ERROR', status: response.status,
          retryable: failure.retryable === true, requestId: failure.request_id,
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
  async function sessionRequest(path, options = {}) {
    const headers = { ...options.headers, 'X-CSRF-Token': session()?.csrf_token || logoutToken() };
    try { return await request(path, { ...options, headers }); }
    catch (err) {
      if (err.code !== 'CSRF_REJECTED') throw err;
      let value;
      try { value = validIdentity(await request('/session')); }
      catch (refreshError) {
        if (options.method === 'DELETE' && refreshError.status === 401) return request(path, { ...options, headers });
        throw refreshError;
      }
      onSession(value);
      return request(path, { ...options, headers: { ...headers, 'X-CSRF-Token': value.csrf_token } });
    }
  }
  return {
    request, sessionRequest,
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
