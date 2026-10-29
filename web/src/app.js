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
  $('app').hidden = true;
  $('workspace').hidden = true;
  $('auth').hidden = false;
  $('notes').replaceChildren();
  $('content').value = '';
  showError($('save-error'), null);
  showError($('list-error'), null);
  $('empty').hidden = true;
  pageObserver.unobserve($('page-end'));
  notes.clear();
  nextToken = '';
  identity = null;
  $('account-name').textContent = '';
  $('account').hidden = true;
  closeAccountMenu();
}

function authControls({ busy = false, login = true } = {}) {
  authBusy = busy;
  $('auth').setAttribute('aria-busy', String(busy));
  $('login-form').hidden = !login;
  for (const id of ['username', 'password', 'login']) $(id).disabled = busy;
}

function expire(err) {
  invalidate();
  hidePrivate();
  $('password').value = '';
  logoutCSRF = null;
  authControls();
  showError($('auth-error'), err);
  $('username').focus();
}

function handleFailure(element, err) {
  if (err.name === 'AbortError') return;
  if (err.status === 401) expire(err);
  else showError(element, err);
}

function updateComposer() {
  $('save').disabled = saving || !identity || (!draft.pending && !draft.content.trim());
  $('content').readOnly = saving || !!draft.pending;
  $('composer').setAttribute('aria-busy', String(saving));
}

async function enter(value) {
  identity = validIdentity(value);
  logoutCSRF = null;
  $('auth').hidden = true;
  $('app').hidden = false;
  $('workspace').hidden = false;
  $('password').value = '';
  $('account-name').textContent = identity.user.display_name || identity.user.name;
  $('account').hidden = false;
  $('content').value = draft.content;
  updateComposer();
  closeAccountMenu();
  await loadNotes(true);
}

async function restore() {
  const generation = invalidate();
  hidePrivate();
  authControls({ busy: true, login: false });
  showError($('auth-error'), null);
  try {
    const value = await request('/session');
    stale(generation);
    authControls();
    await enter(value);
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls();
    showError($('auth-error'), err.status === 401 ? null : err);
  }
}

$('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  if (authBusy) return;
  if (logoutCSRF) {
    await logout();
    if (logoutCSRF) return;
  }
  const generation = invalidate();
  authControls({ busy: true });
  showError($('auth-error'), null);
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
    showError($('auth-error'), err);
    $('password').focus();
  } finally {
    body.password = '';
  }
});

function formatTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const options = { timeZone: identity?.timezone || identity?.user.timezone };
  try {
    const parts = new Intl.DateTimeFormat('zh-CN', { ...options, month: 'numeric', day: 'numeric' }).formatToParts(date);
    const month = parts.find(part => part.type === 'month').value;
    const day = parts.find(part => part.type === 'day').value;
    const weekday = new Intl.DateTimeFormat('zh-CN', { ...options, weekday: 'short' }).format(date);
    return `${month}月${day}日 ${weekday}`;
  } catch {
    return `${date.getMonth() + 1}月${date.getDate()}日`;
  }
}

function renderNotes() {
  const fragment = document.createDocumentFragment();
  // Resource names break equal creation-time ties without using locale-specific collation.
  const ordered = [...notes.values()].sort((a, b) =>
    Date.parse(b.create_time) - Date.parse(a.create_time) || (a.name < b.name ? 1 : a.name > b.name ? -1 : 0));
  for (const note of ordered) {
    const card = document.createElement('article');
    card.className = 'card';
    const header = document.createElement('div');
    header.className = 'hd';
    const time = document.createElement('time');
    time.textContent = formatTime(note.create_time);
    if (Number.isFinite(Date.parse(note.create_time))) time.dateTime = note.create_time;
    const content = document.createElement('p');
    content.className = 'note-content';
    content.textContent = note.content;
    header.append(time);
    card.append(header, content);
    fragment.append(card);
  }
  $('notes').replaceChildren(fragment);
}

function updateListControls() {
  $('notes').setAttribute('aria-busy', String(listing));
}

const pageObserver = new IntersectionObserver(entries => {
  if (entries.some(entry => entry.isIntersecting) && nextToken && !listing) {
    pageObserver.unobserve($('page-end'));
    loadNotes();
  }
}, { rootMargin: '200px' });

async function loadNotes(reset = false) {
  if (!identity || (!reset && (listing || !nextToken))) return;
  const generation = epoch;
  const sequence = ++listSequence;
  listController?.abort();
  listController = new AbortController();
  listing = true;
  pageObserver.unobserve($('page-end'));
  updateListControls();
  showError($('list-error'), null);
  $('empty').hidden = true;
  let succeeded = false;
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
    $('empty').hidden = !!notes.size || !!nextToken;
    succeeded = true;
  } catch (err) {
    if (generation !== epoch || sequence !== listSequence) return;
    handleFailure($('list-error'), err);
  } finally {
    if (generation === epoch && sequence === listSequence) {
      listing = false;
      updateListControls();
      if (succeeded && nextToken) pageObserver.observe($('page-end'));
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

async function logout() {
  if (authBusy) return;
  logoutCSRF ||= identity?.csrf_token;
  if (!logoutCSRF) return;
  const generation = invalidate();
  hidePrivate();
  authControls({ busy: true, login: false });
  showError($('auth-error'), null);
  try {
    await request('/session', { method: 'DELETE', headers: { 'X-CSRF-Token': logoutCSRF } });
    stale(generation);
    logoutCSRF = null;
    authControls();
    $('username').focus();
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls();
    showError($('auth-error'), err);
  }
}

$('logout').addEventListener('click', logout);
document.querySelector('nav a').addEventListener('click', event => {
  event.preventDefault();
  if (identity) loadNotes(true);
});
document.querySelectorAll('a[aria-disabled="true"]').forEach(link => {
  link.addEventListener('click', event => event.preventDefault());
});
const calendarDate = new Date();
$('calendar-title').textContent = `${calendarDate.getFullYear()}年${calendarDate.getMonth() + 1}月`;
const leadingDays = (new Date(calendarDate.getFullYear(), calendarDate.getMonth(), 1).getDay() + 6) % 7;
const monthDays = new Date(calendarDate.getFullYear(), calendarDate.getMonth() + 1, 0).getDate();
for (let index = 0; index < leadingDays + monthDays; index++) {
  const cell = document.createElement('button');
  cell.type = 'button';
  cell.disabled = true;
  if (index < leadingDays) cell.className = 'blank';
  else {
    const day = index - leadingDays + 1;
    cell.textContent = String(day);
    if (day === calendarDate.getDate()) cell.className = 'today';
  }
  $('calendar').append(cell);
}
function closeAccountMenu() {
  $('account-menu').hidden = true;
  $('user-menu').setAttribute('aria-expanded', 'false');
}
$('user-menu').addEventListener('click', () => {
  $('account-menu').hidden = !$('account-menu').hidden;
  $('user-menu').setAttribute('aria-expanded', String(!$('account-menu').hidden));
  if (!$('account-menu').hidden) $('logout').focus();
});
document.addEventListener('click', event => {
  if (!$('account').contains(event.target)) closeAccountMenu();
});
document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && !$('account-menu').hidden) {
    closeAccountMenu();
    $('user-menu').focus();
  }
});
window.addEventListener('pagehide', () => { invalidate(); hidePrivate(); });
window.addEventListener('pageshow', event => { if (event.persisted) restore(); });
restore();
