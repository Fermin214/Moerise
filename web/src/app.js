import { Draft } from './draft.js';
import { errorText } from './errors.js';

const $ = id => document.getElementById(id);
const draft = new Draft();
const controllers = new Set();
let epoch = 0;
let identity = null;
let notes = new Map();
let nextToken = '';
let listSequence = 0;
let listing = false;
let saving = false;
let authBusy = false;
let logoutCSRF = null;
let listController = null;

function showError(element, err) {
  element.textContent = err ? errorText(err) : '';
  element.hidden = !err;
}

function invalidate() {
  epoch++;
  listSequence++;
  for (const controller of controllers) controller.abort();
  controllers.clear();
  listController = null;
  listing = false;
  saving = false;
  return epoch;
}

function stale(generation) {
  if (generation !== epoch) throw new DOMException('Stale request', 'AbortError');
}

async function request(path, { method = 'GET', body, headers = {}, controller = new AbortController() } = {}) {
  const generation = epoch;
  controllers.add(controller);
  let timedOut = false;
  const timer = setTimeout(() => { timedOut = true; controller.abort(); }, 65_000);
  try {
    const response = await fetch(`/api/app/v1${path}`, {
      method, credentials: 'same-origin', cache: 'no-store', redirect: 'error',
      headers: { Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...headers },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: controller.signal,
    });
    stale(generation);
    const data = response.status === 204 ? null : await response.json();
    stale(generation);
    if (!response.ok) {
      const failure = data?.error || {};
      throw Object.assign(new Error(failure.message || '请求失败，请重试。'), {
        code: failure.code || 'HTTP_ERROR', status: response.status,
        retryable: failure.retryable === true, requestId: failure.request_id,
      });
    }
    return data;
  } catch (err) {
    stale(generation);
    if (timedOut) throw Object.assign(new Error('Request timed out'), { code: 'REQUEST_TIMEOUT', retryable: true });
    if (err.name === 'AbortError' || err.code) throw err;
    throw Object.assign(new Error('Network request failed'), { code: 'NETWORK_ERROR', retryable: true });
  } finally {
    clearTimeout(timer);
    controllers.delete(controller);
  }
}

function validIdentity(value) {
  if (!value?.user?.name || !value.csrf_token) {
    throw Object.assign(new Error('Invalid session response'), { code: 'SESSION_RESPONSE_INVALID' });
  }
  return value;
}

function hidePrivate() {
  $('workspace').hidden = true;
  $('auth').hidden = false;
  $('notes').replaceChildren();
  $('content').value = '';
  $('pending-help').textContent = '';
  $('save-status').textContent = '';
  $('list-status').textContent = '';
  showError($('save-error'), null);
  showError($('list-error'), null);
  notes.clear();
  nextToken = '';
  identity = null;
  $('account-name').textContent = '尚未登录';
  $('account-zone').textContent = '';
  $('logout').hidden = true;
  $('abandon-dialog').close();
}

function authControls({ busy = false, login = true, restore = false, logout = false } = {}) {
  authBusy = busy;
  $('auth').setAttribute('aria-busy', String(busy));
  $('login-form').hidden = !login;
  for (const id of ['username', 'password', 'login']) $(id).disabled = busy;
  $('login').textContent = busy ? '正在登录…' : '登录';
  $('auth-retry').hidden = !restore;
  $('auth-retry').disabled = busy;
  $('logout-retry').hidden = !logout;
  $('logout-retry').disabled = busy;
}

function expire(err) {
  invalidate();
  hidePrivate();
  $('password').value = '';
  logoutCSRF = null;
  authControls();
  $('auth-status').textContent = draft.content || draft.pending
    ? '正文和原提交暂存在此页，登录后可继续。关闭或刷新页面会丢失这些内容。'
    : '请重新登录。';
  showError($('auth-error'), err);
  closeMenu(false);
  $('username').focus();
}

function handleFailure(element, err) {
  if (err.name === 'AbortError') return;
  if (err.status === 401) expire(err);
  else showError(element, err);
}

function updateComposer() {
  $('save').disabled = saving || !identity || (!draft.pending && !draft.content.trim());
  $('save').textContent = saving ? '正在保存…' : draft.pending ? '重试原提交' : '保存笔记';
  $('content').readOnly = saving;
  $('composer').setAttribute('aria-busy', String(saving));
  $('pending-help').hidden = !draft.pending;
  $('pending-help').textContent = !draft.pending ? '' : draft.content === draft.pending.content
    ? '此提交尚待确认。重试会沿用原正文与提交标识；重新登录后也可继续。'
    : '编辑框文字已改变。重试仍发送上一次的原正文；保存这些新改动前，请明确放弃原提交的重试。';
  $('abandon').hidden = !draft.pending;
  $('abandon').disabled = saving;
}

async function enter(value) {
  identity = validIdentity(value);
  logoutCSRF = null;
  $('auth').hidden = true;
  $('workspace').hidden = false;
  $('password').value = '';
  $('account-name').textContent = identity.user.display_name || identity.user.name;
  $('account-zone').textContent = identity.timezone || identity.user.timezone || '';
  $('logout').hidden = false;
  $('content').value = draft.content;
  updateComposer();
  closeMenu(false);
  $('main').focus({ preventScroll: true });
  await loadNotes(true);
}

async function restore() {
  const generation = invalidate();
  hidePrivate();
  authControls({ busy: true, login: false });
  $('auth-status').textContent = '正在恢复会话…';
  showError($('auth-error'), null);
  try {
    const value = await request('/session');
    stale(generation);
    authControls();
    await enter(value);
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls({ restore: err.status !== 401 });
    $('auth-status').textContent = '使用 Memos 用户名和密码登录。';
    showError($('auth-error'), err.status === 401 ? null : err);
  }
}

$('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  if (authBusy) return;
  const generation = invalidate();
  authControls({ busy: true });
  showError($('auth-error'), null);
  $('auth-status').textContent = '正在登录…';
  const body = { username: $('username').value, password: $('password').value };
  $('password').value = '';
  try {
    const value = await request('/session', { method: 'POST', body });
    stale(generation);
    authControls();
    await enter(value);
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls();
    $('auth-status').textContent = '请检查账户信息后重试。';
    showError($('auth-error'), err);
    $('password').focus();
  } finally {
    body.password = '';
  }
});

function formatTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '创建时间未知';
  const options = { year: 'numeric', month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit' };
  try {
    return new Intl.DateTimeFormat('zh-CN', { ...options, timeZone: identity?.timezone || identity?.user.timezone }).format(date);
  } catch {
    return new Intl.DateTimeFormat('zh-CN', options).format(date);
  }
}

function renderNotes() {
  const fragment = document.createDocumentFragment();
  // Resource names break equal creation-time ties without using locale-specific collation.
  const ordered = [...notes.values()].sort((a, b) =>
    Date.parse(b.create_time) - Date.parse(a.create_time) || (a.name < b.name ? 1 : a.name > b.name ? -1 : 0));
  for (const note of ordered) {
    const card = document.createElement('article');
    card.className = 'note';
    const time = document.createElement('time');
    time.textContent = formatTime(note.create_time);
    if (Number.isFinite(Date.parse(note.create_time))) time.dateTime = note.create_time;
    const content = document.createElement('p');
    content.className = 'note-content';
    content.textContent = note.content;
    card.append(time, content);
    fragment.append(card);
  }
  $('notes').replaceChildren(fragment);
}

function updateListControls() {
  $('refresh').disabled = listing;
  $('refresh').textContent = listing ? '读取中…' : '刷新';
  $('load-more').disabled = listing;
  $('load-more').textContent = listing ? '读取中…' : '加载更多';
  $('load-more').hidden = !nextToken;
  $('notes').setAttribute('aria-busy', String(listing));
}

async function loadNotes(reset = false) {
  if (!identity || (!reset && (listing || !nextToken))) return;
  const generation = epoch;
  const sequence = ++listSequence;
  listController?.abort();
  listController = new AbortController();
  listing = true;
  updateListControls();
  showError($('list-error'), null);
  $('list-status').textContent = reset ? '正在读取时间线…' : '正在读取更多笔记…';
  const query = new URLSearchParams({ state: 'NORMAL', order: 'created_desc', page_size: '20' });
  if (!reset) query.set('page_token', nextToken);
  try {
    const page = await request(`/notes?${query}`, { controller: listController });
    if (generation !== epoch || sequence !== listSequence) return;
    if (!Array.isArray(page?.notes) || page.notes.some(note =>
      typeof note.name !== 'string' || typeof note.content !== 'string' || note.state !== 'NORMAL')) {
      throw Object.assign(new Error('Invalid note list response'), { code: 'NOTE_LIST_INVALID' });
    }
    if (reset) notes = new Map();
    for (const note of page.notes) notes.set(note.name, note);
    nextToken = typeof page.next_page_token === 'string' ? page.next_page_token : '';
    renderNotes();
    $('list-status').textContent = !notes.size
      ? nextToken ? '这一页没有可显示的笔记，可以继续加载。' : '还没有正常笔记，写下第一条吧。'
      : nextToken ? '' : '已读到最后一条。';
  } catch (err) {
    if (generation !== epoch || sequence !== listSequence) return;
    $('list-status').textContent = '';
    handleFailure($('list-error'), err);
  } finally {
    if (generation === epoch && sequence === listSequence) {
      listing = false;
      updateListControls();
    }
  }
}

$('content').addEventListener('input', () => {
  draft.content = $('content').value;
  updateComposer();
});
$('content').addEventListener('keydown', event => {
  if (event.ctrlKey && event.key === 'Enter' && !event.isComposing) {
    event.preventDefault();
    if (!$('save').disabled) $('composer').requestSubmit();
  }
});
$('composer').addEventListener('submit', async event => {
  event.preventDefault();
  if (saving || !identity) return;
  let attempt;
  try { attempt = draft.begin(); } catch (err) { showError($('save-error'), err); return; }
  const generation = epoch;
  saving = true;
  updateComposer();
  showError($('save-error'), null);
  $('save-status').textContent = '';
  try {
    const note = await request('/notes', {
      method: 'POST', body: { content: attempt.content },
      headers: { 'Idempotency-Key': attempt.key, 'X-CSRF-Token': identity.csrf_token },
    });
    stale(generation);
    if (!note?.name || typeof note.content !== 'string') {
      throw Object.assign(new Error('Invalid note create response'), { code: 'NOTE_SAVE_UNCONFIRMED' });
    }
    draft.confirm(attempt);
    $('content').value = draft.content;
    if (note.state === 'NORMAL') {
      notes.set(note.name, note);
      renderNotes();
    }
    $('save-status').textContent = '笔记已保存。';
    await loadNotes(true);
  } catch (err) {
    if (generation !== epoch) return;
    handleFailure($('save-error'), err);
  } finally {
    if (generation === epoch) {
      saving = false;
      updateComposer();
    }
  }
});

$('abandon').addEventListener('click', () => $('abandon-dialog').showModal());
$('keep-retry').addEventListener('click', () => $('abandon-dialog').close());
$('confirm-abandon').addEventListener('click', () => {
  if (saving) return;
  draft.abandon();
  $('abandon-dialog').close();
  showError($('save-error'), null);
  $('save-status').textContent = '原提交的重试已放弃，编辑框文字可作为新记录保存。';
  updateComposer();
  $('content').focus();
});

async function logout() {
  if (authBusy) return;
  logoutCSRF ||= identity?.csrf_token;
  if (!logoutCSRF) return;
  const generation = invalidate();
  hidePrivate();
  closeMenu(false);
  authControls({ busy: true, login: false });
  $('auth-status').textContent = '正在退出登录…';
  showError($('auth-error'), null);
  try {
    await request('/session', { method: 'DELETE', headers: { 'X-CSRF-Token': logoutCSRF } });
    stale(generation);
    logoutCSRF = null;
    authControls();
    $('auth-status').textContent = draft.content || draft.pending
      ? '已退出。未确认的正文暂存在此页，登录后可继续；关闭或刷新页面会丢失。'
      : '已退出登录。';
    $('username').focus();
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls({ login: false, logout: true });
    $('auth-status').textContent = '退出结果尚未确认，请重试退出。';
    showError($('auth-error'), err);
  }
}

$('refresh').addEventListener('click', () => loadNotes(true));
$('load-more').addEventListener('click', () => loadNotes());
$('auth-retry').addEventListener('click', restore);
$('logout').addEventListener('click', logout);
$('logout-retry').addEventListener('click', logout);

const narrow = window.matchMedia('(max-width: 760px)');
function closeMenu(focus = true) {
  $('sidebar').classList.remove('open');
  $('sidebar').removeAttribute('role');
  $('sidebar').removeAttribute('aria-modal');
  $('scrim').hidden = true;
  $('menu-open').setAttribute('aria-expanded', 'false');
  $('main').inert = false;
  document.querySelector('.mobile-bar').inert = false;
  document.body.style.overflow = '';
  if (focus && narrow.matches) $('menu-open').focus();
}
$('menu-open').addEventListener('click', () => {
  $('sidebar').classList.add('open');
  $('sidebar').setAttribute('role', 'dialog');
  $('sidebar').setAttribute('aria-modal', 'true');
  $('scrim').hidden = false;
  $('menu-open').setAttribute('aria-expanded', 'true');
  $('main').inert = true;
  document.querySelector('.mobile-bar').inert = true;
  document.body.style.overflow = 'hidden';
  $('menu-close').focus();
});
$('menu-close').addEventListener('click', () => closeMenu());
$('scrim').addEventListener('click', () => closeMenu());
document.querySelector('.nav-entry').addEventListener('click', () => closeMenu(false));
$('sidebar').addEventListener('keydown', event => {
  if (!narrow.matches || !$('sidebar').classList.contains('open')) return;
  if (event.key === 'Escape') { event.preventDefault(); closeMenu(); }
  if (event.key === 'Tab') {
    const items = [...$('sidebar').querySelectorAll('button:not([hidden]), a[href]')];
    const first = items[0];
    const last = items.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }
});
narrow.addEventListener('change', () => closeMenu(false));
window.addEventListener('pagehide', () => { invalidate(); hidePrivate(); });
window.addEventListener('pageshow', event => { if (event.persisted) restore(); });
restore();
