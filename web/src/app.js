import { Draft, readAttachments } from './draft.js';
import { errorText } from './errors.js';
import { pages, notePath, filters, timelineURL, listQuery, safeFrom, detailURL, noteID, attachmentURL, rasterImage } from './routes.js';
import { renderMarkdown } from './markdown.js';
import { createSettingsPage } from './settings.js';

const $ = id => document.getElementById(id);
const draft = new Draft(), controllers = new Set(), snapshots = new Map();
const editors = new Map(), pendingWrites = new Map(), failures = new Map();
const sidebarTags = new Map(), sidebarCalendars = new Map();
let epoch = 0, viewSequence = 0, listSequence = 0, sidebarSequence = 0;
let identity = null, logoutCSRF = null, authBusy = false, saving = false, readingFiles = false, listing = false;
let draftOwner = null;
let items = [], nextToken = '', listController = null, retryReset = true;
let retryTimer = null, retryDelay = 1000, searchTimer = null;
let currentURL = location.pathname + location.search;
let currentKey = history.state?.webKey || crypto.randomUUID();
let month = '', routeReturnKey = null;
let pageCount = 0, reloading = false;
let lastTimeline = { path: '/', values: filters('') };
let settingsPage = null;
history.replaceState({ ...history.state, webKey: currentKey }, '');
history.scrollRestoration = 'manual';

function el(tag, className = '', text = '') {
  const node = document.createElement(tag);
  node.className = className;
  if (text) node.textContent = text;
  return node;
}
function button(text, className, action) {
  const node = el('button', className, text);
  node.type = 'button';
  node.addEventListener('click', action);
  return node;
}
function showError(element, err) {
  element.textContent = err ? errorText(err) : '';
  element.hidden = !err;
}
function aborted() { return new DOMException('Stale request', 'AbortError'); }
function stale(generation) { if (generation !== epoch) throw aborted(); }
function invalidate() {
  epoch++; viewSequence++; listSequence++;
  settingsPage?.destroy(); settingsPage = null;
  invalidateSidebar();
  for (const controller of controllers) controller.abort();
  controllers.clear();
  clearTimeout(retryTimer);
  clearTimeout(searchTimer);
  retryTimer = listController = null;
  listing = saving = readingFiles = false;
  return epoch;
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
      body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal,
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
  if (!value?.user?.name || !value.csrf_token) throw Object.assign(new Error('Invalid session response'), { code: 'SESSION_RESPONSE_INVALID' });
  return value;
}
async function sessionRequest(path, options) {
  const headers = { ...options.headers, 'X-CSRF-Token': identity?.csrf_token || logoutCSRF };
  try { return await request(path, { ...options, headers }); }
  catch (err) {
    if (err.code !== 'CSRF_REJECTED') throw err;
    let value;
    try { value = validIdentity(await request('/session')); }
    catch (refreshError) {
      if (options.method === 'DELETE' && refreshError.status === 401) return request(path, { ...options, headers });
      throw refreshError;
    }
    if (identity) identity = value;
    else logoutCSRF = value.csrf_token;
    return request(path, { ...options, headers: { ...headers, 'X-CSRF-Token': value.csrf_token } });
  }
}
function closeMenus() {
  document.querySelectorAll('.menu').forEach(menu => { menu.hidden = true; });
  document.querySelectorAll('[aria-haspopup="menu"]').forEach(control => control.setAttribute('aria-expanded', 'false'));
}
function clearDrafts() {
  for (const editor of editors.values()) editor.draft.clear();
  editors.clear(); pendingWrites.clear(); failures.clear(); draft.clear();
  draftOwner = null;
}
function hidePrivate({ resume = false } = {}) {
  if (resume) {
    draftOwner ||= identity?.user.name;
    for (const [name, operation] of pendingWrites) {
      operation.busy = false;
      failures.set(name, { code: 'NOTE_SAVE_UNCONFIRMED' });
    }
    for (const editor of editors.values()) editor.suspend();
  } else clearDrafts();
  $('app').hidden = $('workspace').hidden = true;
  $('auth').hidden = false;
  for (const id of ['notes', 'tags', 'filters', 'calendar', 'draft-attachments']) $(id).replaceChildren();
  for (const id of ['content', 'q', 'attachment-input', 'username', 'password']) $(id).value = '';
  for (const id of ['save-error', 'list-error', 'sidebar-error']) showError($(id), null);
  $('empty').hidden = $('account').hidden = true;
  $('account-name').textContent = $('calendar-title').textContent = '';
  pageObserver.disconnect();
  items = []; nextToken = '';
  snapshots.clear();
  month = ''; identity = null; routeReturnKey = null;
  pageCount = 0; reloading = false; lastTimeline = { path: '/', values: filters('') };
  closeMenus();
}
function authControls({ busy = false, login = true } = {}) {
  authBusy = busy;
  $('auth').setAttribute('aria-busy', String(busy));
  $('login-form').hidden = !login;
  for (const id of ['username', 'password', 'login']) $(id).disabled = busy;
}
function expire(err) {
  invalidate(); hidePrivate({ resume: true }); logoutCSRF = null; authControls();
  showError($('auth-error'), err);
  $('username').focus();
}
function handleFailure(element, err) {
  if (err.name === 'AbortError') return;
  if (err.status === 401) expire(err);
  else showError(element, err);
}
function localToday() {
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {
      timeZone: identity?.timezone, year: 'numeric', month: '2-digit', day: '2-digit',
    }).formatToParts(new Date());
    return ['year', 'month', 'day'].map(type => parts.find(part => part.type === type).value).join('-');
  } catch { return new Date().toISOString().slice(0, 10); }
}
function formatTime(value, full = false) {
  if (!Number.isFinite(Date.parse(value))) return '';
  const options = { timeZone: identity?.timezone, month: 'long', day: 'numeric', weekday: 'short' };
  if (full) Object.assign(options, { year: 'numeric', hour: '2-digit', minute: '2-digit' });
  try { return new Intl.DateTimeFormat('zh-CN', options).format(new Date(value)); }
  catch { return value.slice(0, 10); }
}
const isTimeline = () => location.pathname === '/' || location.pathname === '/archived';
function sourceTimeline() {
  const source = notePath.test(location.pathname) ? safeFrom(new URLSearchParams(location.search).get('from')) : currentURL;
  const url = new URL(source, location.origin);
  if (!['/', '/archived'].includes(url.pathname)) return lastTimeline;
  return { path: url.pathname === '/archived' ? '/archived' : '/', values: filters(url.search) };
}
function sidebarFilter(change) {
  const source = sourceTimeline();
  navigate(timelineURL(source.path, { ...source.values, query: $('q').value, ...change }));
}
function rememberView(writeHistory = true) {
  if (!identity || !['/', '/archived'].includes(new URL(currentURL, location.origin).pathname)) return;
  const previous = snapshots.get(currentKey);
  snapshots.set(currentKey, { url: currentURL, items: [...items], nextToken, scroll: window.scrollY, pages: pageCount, dirty: previous?.dirty });
  if (writeHistory) history.replaceState({ ...history.state, scroll: window.scrollY, pages: pageCount }, '');
  if (snapshots.size > 12) snapshots.delete(snapshots.keys().next().value);
}
function navigate(url, { replace = false, returnKey = null } = {}) {
  if (url === currentURL) return;
  clearTimeout(searchTimer);
  rememberView();
  currentKey = replace ? currentKey : crypto.randomUUID();
  history[replace ? 'replaceState' : 'pushState']({ webKey: currentKey, ...(returnKey ? { returnKey } : {}) }, '', url);
  currentURL = location.pathname + location.search;
  showRoute();
}
function openNote(note) { navigate(detailURL(note.name, currentURL), { returnKey: currentKey }); }
function restoreScroll(scroll) {
  const sequence = viewSequence;
  requestAnimationFrame(() => { if (sequence === viewSequence && identity) window.scrollTo(0, scroll); });
}
function dirtySnapshots() {
  for (const snapshot of snapshots.values()) snapshot.dirty = true;
}
async function reloadTimeline(pages = pageCount || 1, scroll = window.scrollY) {
  const sequence = viewSequence;
  reloading = true;
  try {
    await loadNotes(true);
    for (let i = 1; i < pages && nextToken && identity && sequence === viewSequence && $('list-retry').hidden; i++) await loadNotes();
    if (identity && sequence === viewSequence) restoreScroll(scroll);
  } finally {
    reloading = false;
    if (identity && sequence === viewSequence && nextToken) pageObserver.observe($('page-end'));
  }
}
async function enter(value) {
  identity = validIdentity(value);
  if (draftOwner && draftOwner !== identity.user.name) clearDrafts();
  draftOwner = null;
  logoutCSRF = null;
  $('auth').hidden = true;
  $('app').hidden = $('workspace').hidden = false;
  $('password').value = '';
  $('account-name').textContent = identity.user.display_name || identity.user.name;
  $('account').hidden = false;
  month = localToday().slice(0, 7);
  $('content').value = draft.content;
  for (const editor of editors.values()) editor.update();
  updateComposer();
  await showRoute();
}
async function restore() {
  const generation = invalidate();
  hidePrivate(); authControls({ busy: true, login: false }); showError($('auth-error'), null);
  try {
    const value = await request('/session');
    stale(generation); authControls();
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
  if (logoutCSRF) { await logout(); if (logoutCSRF) return; }
  const generation = invalidate();
  authControls({ busy: true }); showError($('auth-error'), null);
  const body = { username: $('username').value, password: $('password').value };
  $('password').value = '';
  try {
    const value = await request('/session', { method: 'POST', body });
    stale(generation); authControls();
    await enter(value);
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls(); showError($('auth-error'), err); $('password').focus();
  } finally { body.password = ''; }
});
async function logout() {
  if (authBusy) return;
  logoutCSRF ||= identity?.csrf_token;
  if (!logoutCSRF) return;
  const generation = invalidate();
  hidePrivate(); authControls({ busy: true, login: false }); showError($('auth-error'), null);
  try {
    await sessionRequest('/session', { method: 'DELETE' });
    stale(generation); logoutCSRF = null; authControls(); $('username').focus();
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls(); showError($('auth-error'), err);
  }
}

function renderFilters() {
  const values = filters(location.search), box = $('filters');
  box.replaceChildren();
  const chips = [
    ...(values.query ? [{ label: `“${values.query}”`, remove: () => sidebarFilter({ query: '' }) }] : []),
    ...values.tags.map(tag => ({ label: `#${tag}`, remove: () => sidebarFilter({ tags: values.tags.filter(item => item !== tag) }) })),
    ...(values.date ? [{ label: values.date, remove: () => sidebarFilter({ date: '' }) }] : []),
  ];
  box.hidden = !chips.length;
  if (!chips.length) return;
  box.append(el('span', '', '筛选'));
  for (const chip of chips) {
    const span = el('span', 'fc', chip.label);
    const remove = button('×', '', chip.remove);
    remove.setAttribute('aria-label', `移除筛选 ${chip.label}`);
    span.append(remove); box.append(span);
  }
  box.append(button('清除全部', 'ghost', () => navigate(location.pathname)));
}
function invalidateSidebar() {
  sidebarSequence++;
  for (const cache of [sidebarTags, sidebarCalendars]) {
    for (const entry of cache.values()) entry.controller?.abort();
    cache.clear();
  }
}
function sidebarData(cache, key, path) {
  for (const [other, entry] of cache) {
    if (other !== key && entry.controller) { entry.controller.abort(); cache.delete(other); }
  }
  const cached = cache.get(key);
  if (cached && !cached.controller && Date.now() - cached.loadedAt >= 60_000) cache.delete(key);
  if (!cache.has(key)) {
    const entry = { controller: new AbortController() };
    entry.promise = request(path, { controller: entry.controller }).then(data => {
      entry.controller = null;
      entry.loadedAt = Date.now();
      return data;
    }).catch(err => {
      if (cache.get(key) === entry) cache.delete(key);
      throw err;
    });
    cache.set(key, entry);
  }
  return cache.get(key).promise;
}
function refreshSidebarOnFocus() {
  if (!identity || document.visibilityState !== 'visible') return;
  for (const cache of [sidebarTags, sidebarCalendars]) {
    for (const [key, entry] of cache) if (!entry.controller) cache.delete(key);
  }
  loadSidebar();
}
async function loadSidebar() {
  if (!identity) return;
  const sequence = ++sidebarSequence, generation = epoch;
  const { path, values } = sourceTimeline();
  const state = path === '/archived' ? 'ARCHIVED' : 'NORMAL', selectedMonth = month;
  showError($('sidebar-error'), null); $('sidebar-retry').hidden = true;
  $('tags').replaceChildren(); renderCalendar([], values.date, true);
  try {
    const [tags, calendar] = await Promise.all([
      sidebarData(sidebarTags, state, `/tags?${new URLSearchParams({ state })}`),
      sidebarData(sidebarCalendars, `${state}:${selectedMonth}`, `/calendar?${new URLSearchParams({ state, month: selectedMonth })}`),
    ]);
    if (generation !== epoch || sequence !== sidebarSequence) return;
    if (!Array.isArray(tags?.tags) || !Array.isArray(calendar?.days) || calendar.month !== selectedMonth) {
      sidebarTags.delete(state); sidebarCalendars.delete(`${state}:${selectedMonth}`);
      throw Object.assign(new Error('Invalid sidebar data'), { code: 'NOTE_LIST_INVALID' });
    }
    for (const item of tags.tags) {
      if (typeof item.tag !== 'string' || !Number.isInteger(item.count) || item.count < 0) continue;
      const on = values.tags.includes(item.tag);
      const link = button('', `ent tag-row${on ? ' on' : ''}`, () => {
        const source = sourceTimeline();
        sidebarFilter({ tags: source.values.tags.includes(item.tag)
          ? source.values.tags.filter(tag => tag !== item.tag) : [...source.values.tags, item.tag] });
      });
      link.setAttribute('aria-pressed', String(on));
      link.append(el('span', '', `#${item.tag}`), el('em', '', String(item.count)));
      $('tags').append(link);
    }
    if (!tags.tags.length) $('tags').append(el('p', 'sub', '暂无标签'));
    renderCalendar(calendar.days, values.date);
  } catch (err) {
    if (generation !== epoch || sequence !== sidebarSequence) return;
    handleFailure($('sidebar-error'), err);
    if (identity) $('sidebar-retry').hidden = false;
  }
}
function renderCalendar(days, selected, disabled = false) {
  const [year, number] = month.split('-').map(Number);
  $('calendar-title').textContent = `${year}年${number}月`;
  const calendar = $('calendar');
  calendar.replaceChildren(...Array.from('一二三四五六日', label => el('span', '', label)));
  const counts = new Map(days.map(day => [day.date, day.count]));
  const offset = (new Date(year, number - 1, 1).getDay() + 6) % 7, size = new Date(year, number, 0).getDate();
  for (let i = 0; i < offset; i++) {
    const blank = button('', 'blank', () => {}); blank.disabled = true; blank.tabIndex = -1; calendar.append(blank);
  }
  for (let day = 1; day <= size; day++) {
    const date = `${month}-${String(day).padStart(2, '0')}`, count = counts.get(date) || 0;
    const cell = button(String(day), [selected === date ? 'sel' : count > 1 ? 'h2' : count ? 'h1' : '', date === localToday() ? 'today' : ''].join(' '),
      () => sidebarFilter({ date: sourceTimeline().values.date === date ? '' : date }));
    cell.disabled = disabled;
    cell.setAttribute('aria-label', `${date} · ${count} 条笔记`);
    cell.setAttribute('aria-pressed', String(selected === date));
    calendar.append(cell);
  }
}
function changeMonth(delta) {
  const [year, number] = month.split('-').map(Number), date = new Date(year, number - 1 + delta, 1);
  if (date.getFullYear() < 1000 || date.getFullYear() > 9999) return;
  month = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}`;
  loadSidebar();
}
function validNote(note) {
  return note && noteID(note.name) && typeof note.content === 'string' &&
    ['NORMAL', 'ARCHIVED'].includes(note.state) && typeof note.source_hash === 'string' && !!note.source_hash;
}
function compareNotes(a, b) {
  return Date.parse(b.create_time) - Date.parse(a.create_time) || (a.name < b.name ? 1 : a.name > b.name ? -1 : 0);
}
function renderAttachments(note) {
  const box = el('div', 'attachment-list');
  for (const item of note.attachments || []) {
    const url = attachmentURL(note, item);
    if (!url) { box.append(el('span', 'attachment sub', `${item.filename || '附件'} · 暂时无法打开`)); continue; }
    const link = el('a', rasterImage(item.type) ? 'image-link' : 'attachment');
    link.href = url;
    if (rasterImage(item.type)) {
      link.target = '_blank'; link.rel = 'noopener noreferrer';
      const image = el('img');
      image.src = url; image.alt = item.filename || '图片'; image.loading = 'lazy';
      image.addEventListener('error', () => {
        image.remove(); link.className = 'attachment'; link.textContent = `${item.filename || '图片'} · 打开原图`;
      }, { once: true });
      link.append(image);
    } else {
      link.download = item.filename || 'attachment';
      link.textContent = item.filename || '下载附件';
      if (Number.isFinite(item.size)) link.append(el('span', 'sub', item.size < 1024 ? `${item.size} B` : `${Math.ceil(item.size / 1024)} KiB`));
    }
    box.append(link);
  }
  return box;
}
function renderNote(note, detail = false) {
  const editor = editors.get(note.name);
  if (editor) return editor.form;
  const card = el('article', `card${detail ? ' note' : ''}`);
  card.dataset.name = note.name;
  card.dataset.sourceHash = note.source_hash;
  const header = el('div', 'hd'), time = el('time', '', formatTime(note.create_time, detail));
  if (Number.isFinite(Date.parse(note.create_time))) time.dateTime = note.create_time;
  if (detail) header.append(time);
  else {
    const link = el('a', 'dlink');
    link.href = detailURL(note.name, currentURL); link.dataset.route = ''; link.append(time); header.append(link);
  }
  const menuWrap = el('div', 'mw'), menu = el('div', 'menu');
  menu.hidden = true; menu.setAttribute('role', 'menu');
  const more = button('···', 'btn s more', () => {
    const open = menu.hidden; closeMenus(); menu.hidden = !open;
    more.setAttribute('aria-expanded', String(open));
    if (open) menu.querySelector('button')?.focus();
  });
  more.setAttribute('aria-label', '更多操作');
  more.setAttribute('aria-haspopup', 'menu'); more.setAttribute('aria-expanded', 'false');
  const content = el('div', 'note-content'); content.append(renderMarkdown(note.content, note));
  const tags = el('div', 'note-tags');
  for (const tag of note.tags || []) tags.append(button(`#${tag}`, 'tag', () => {
    const source = sourceTimeline();
    sidebarFilter({ tags: [...new Set([...source.values.tags, tag])] });
  }));
  const actions = [
    ['编辑', () => startEdit(note), ''],
    ...(!detail ? [['打开笔记', () => openNote(note), '']] : []),
    [note.state === 'ARCHIVED' ? '恢复到主页' : '归档', () => changeState(note), ''],
    ['从这条漫步', () => navigate('/walk'), ''],
    ['AI 洞察这条', () => navigate('/insights'), ''],
    ['删除', () => confirmDelete(card, note), 'dg'],
  ];
  for (const [label, action, cls] of actions) {
    const control = button(label, `mi ${cls}`, () => { closeMenus(); action(); });
    control.setAttribute('role', 'menuitem'); control.disabled = pendingWrites.has(note.name); menu.append(control);
  }
  if (!detail) { menuWrap.append(more, menu); header.append(menuWrap); }
  card.append(header, content, renderAttachments(note), tags);
  if (detail) {
    const bar = el('div', 'note-actions');
    for (const [label, action, cls] of actions) {
      const control = button(label, `ghost ${cls}`, action);
      control.disabled = pendingWrites.has(note.name); bar.append(control);
    }
    card.append(bar);
  }
  const failure = failures.get(note.name);
  if (failure) appendNoteError(card, note, failure);
  return card;
}
function renderList() {
  const list = $('notes'), names = new Set(items.map(note => note.name));
  const focusedEditor = list.contains(document.activeElement) ? document.activeElement.closest('form.editor') : null;
  const existing = new Map([...list.children].map(node => [node.dataset.name, node]));
  for (const [name, node] of existing) if (!names.has(name)) node.remove();
  let cursor = list.firstElementChild;
  for (const note of items) {
    const previous = existing.get(note.name);
    const node = editors.get(note.name)?.form ||
      (previous?.dataset.sourceHash === note.source_hash ? previous : renderNote(note, notePath.test(location.pathname)));
    if (node !== cursor) {
      if (node.parentNode === list && node === focusedEditor) {
        // 移动周围卡片，保持编辑表单连接，避免输入失焦。
        const after = node.nextSibling;
        while (cursor && cursor !== node) {
          const next = cursor.nextElementSibling;
          list.insertBefore(cursor, after); cursor = next;
        }
      } else list.insertBefore(node, cursor);
    }
    if (previous && previous !== node) previous.remove();
    cursor = node.nextElementSibling;
  }
}
function replaceCard(note) {
  const card = [...$('notes').children].find(node => node.dataset.name === note.name);
  if (card) card.replaceWith(renderNote(note, notePath.test(location.pathname)));
}
function appendNoteError(card, note, err) {
  const box = el('div', 'note-error'), message = el('p', 'error');
  message.setAttribute('role', 'alert'); showError(message, err); box.append(message);
  if (pendingWrites.has(note.name)) box.append(button('重试原提交', 'ghost', () => performWrite(note)));
  box.append(button('刷新笔记', 'ghost', () => refreshNote(note))); card.append(box);
}
async function refreshNote(note) {
  const generation = epoch, sequence = viewSequence;
  try {
    const updated = await request(`/notes/${noteID(note.name)}`);
    stale(generation);
    if (sequence !== viewSequence) return;
    if (!validNote(updated)) throw Object.assign(new Error('Invalid note'), { code: 'NOTE_LIST_INVALID' });
    editors.get(note.name)?.draft.clear(); editors.delete(note.name); pendingWrites.delete(note.name); failures.delete(note.name);
    replaceCard(updated);
    items = items.map(item => item.name === note.name ? updated : item);
    if (isTimeline() && updated.state !== (location.pathname === '/archived' ? 'ARCHIVED' : 'NORMAL')) items = items.filter(item => item.name !== note.name);
    dirtySnapshots();
    if (isTimeline()) await reloadTimeline();
    else renderList();
    $('empty').hidden = !isTimeline() || !!items.length || !!nextToken; invalidateSidebar(); loadSidebar();
  } catch (err) { if (generation === epoch && sequence === viewSequence) handleFailure($('list-error'), err); }
}
function confirmDelete(card, note) {
  if (card.querySelector('.cfm') || pendingWrites.has(note.name)) return;
  const confirm = el('div', 'cfm'); confirm.setAttribute('role', 'alert');
  confirm.append(el('span', '', '删除这条笔记？删除后无法恢复。'));
  const controls = el('span', 'editor-actions');
  const cancel = button('取消', 'ghost', () => { confirm.remove(); card.querySelector('.more, .note-actions button')?.focus(); });
  const remove = button('删除', 'danger', () => {
    pendingWrites.set(note.name, { method: 'DELETE', body: Object.freeze({ source_hash: note.source_hash }), busy: false });
    performWrite(note);
  });
  controls.append(cancel, remove); confirm.append(controls);
  confirm.addEventListener('keydown', event => { if (event.key === 'Escape') { event.preventDefault(); cancel.click(); } });
  card.append(confirm); cancel.focus();
}
function changeState(note) {
  if (pendingWrites.has(note.name)) return;
  pendingWrites.set(note.name, {
    method: 'PATCH', key: crypto.randomUUID(),
    body: Object.freeze({ source_hash: note.source_hash, state: note.state === 'ARCHIVED' ? 'NORMAL' : 'ARCHIVED' }), busy: false,
  });
  performWrite(note);
}
async function performWrite(note) {
  const operation = pendingWrites.get(note.name);
  if (!operation || operation.busy) return;
  const generation = epoch;
  operation.busy = true; failures.delete(note.name);
  const editor = editors.get(note.name);
  editor?.update();
  if (!editor) replaceCard(note);
  try {
    const updated = await sessionRequest(`/notes/${noteID(note.name)}`, {
      method: operation.method, body: operation.body, headers: operation.key ? { 'Idempotency-Key': operation.key } : {},
    });
    stale(generation);
    if (operation.method !== 'DELETE' && !validNote(updated)) throw Object.assign(new Error('Invalid save response'), { code: 'NOTE_SAVE_UNCONFIRMED' });
    editor?.draft.clear(); editors.delete(note.name); pendingWrites.delete(note.name); failures.delete(note.name); dirtySnapshots();
    if (operation.method === 'DELETE') {
      items = items.filter(item => item.name !== note.name);
      if (location.pathname === `/notes/${noteID(note.name)}`) navigate(safeFrom(new URLSearchParams(location.search).get('from')));
      else if (isTimeline()) await reloadTimeline();
      else renderList();
    } else {
      items = items.map(item => item.name === note.name ? updated : item);
      if (isTimeline()) await reloadTimeline();
      else renderList();
    }
    $('empty').hidden = !isTimeline() || !!items.length || !!nextToken;
    invalidateSidebar(); loadSidebar();
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    operation.busy = false;
    if (err.status === 401) { expire(err); return; }
    failures.set(note.name, err);
    if (editor) {
      editor.draft.reject(editor.draft.pending, err.code);
      if (!editor.draft.locked) pendingWrites.delete(note.name);
      editor.update(); editor.showFailure(err);
    } else replaceCard(note);
  } finally { if (generation === epoch) { operation.busy = false; editor?.update(); } }
}
function selectedFiles(box, model, locked, update) {
  box.replaceChildren();
  model.attachments.forEach((item, index) => {
    const row = el('span', 'attachment', item.filename);
    const remove = button('×', 'dlink', () => { if (!locked()) { model.attachments.splice(index, 1); update(); } });
    remove.setAttribute('aria-label', `移除附件 ${item.filename}`); remove.disabled = locked(); row.append(remove); box.append(row);
  });
}
function startEdit(note) {
  if (pendingWrites.has(note.name)) return;
  const existing = editors.get(note.name);
  if (existing) { existing.text.focus(); return; }
  const model = new Draft(); model.content = note.content;
  const removed = new Set();
  let reading = false, readSequence = 0;
  const form = el('form', 'card composer editing editor');
  form.dataset.name = note.name; form.setAttribute('aria-label', '编辑笔记');
  const text = el('textarea'); text.value = note.content; text.rows = notePath.test(location.pathname) ? 5 : 3;
  text.setAttribute('aria-label', '编辑笔记内容');
  const retained = el('div', 'attachment-list'), added = el('div', 'attachment-list');
  const bar = el('div', 'bar'), actions = el('div', 'editor-actions'), input = el('input', 'sr');
  input.type = 'file'; input.multiple = true; input.tabIndex = -1;
  const add = button('+', 'btn', () => input.click()); add.setAttribute('aria-label', '添加附件');
  const cancel = button('取消', 'ghost', () => {
    if (pendingWrites.get(note.name)?.busy) return;
    readSequence++; model.clear(); editors.delete(note.name);
    if (!pendingWrites.has(note.name)) failures.delete(note.name);
    replaceCard(note);
  });
  const save = button('保存', 'save', () => form.requestSubmit()); save.append(el('small', '', 'Ctrl ↵'));
  const message = el('p', 'error'); message.hidden = true; message.setAttribute('role', 'alert');
  const refresh = button('刷新笔记', 'ghost', () => refreshNote(note)); refresh.hidden = true;
  function update() {
    const busy = !!pendingWrites.get(note.name)?.busy;
    text.readOnly = model.locked || busy;
    add.disabled = input.disabled = model.locked || busy || reading;
    cancel.disabled = busy;
    refresh.disabled = busy;
    save.disabled = busy || reading || (!model.locked && !model.content.trim() && !model.attachments.length && !(note.attachments || []).some(item => !removed.has(item.name)));
    form.setAttribute('aria-busy', String(busy || reading));
    selectedFiles(added, model, () => model.locked || reading, update);
    retained.replaceChildren();
    for (const item of note.attachments || []) {
      if (removed.has(item.name)) continue;
      const row = el('span', 'attachment', item.filename || '附件');
      const remove = button('×', 'dlink', () => { if (!model.locked && !busy) { removed.add(item.name); update(); } });
      remove.setAttribute('aria-label', `移除附件 ${item.filename || '附件'}`); remove.disabled = model.locked || busy;
      row.append(remove); retained.append(row);
    }
  }
  const context = {
    draft: model, form, text, update,
    suspend() { readSequence++; reading = false; update(); },
    showFailure(err) { showError(message, err); refresh.hidden = false; },
  };
  editors.set(note.name, context);
  text.addEventListener('input', () => { model.content = text.value; update(); });
  text.addEventListener('keydown', event => {
    if (event.isComposing) return;
    if (event.key === 'Escape') { event.preventDefault(); if (!cancel.disabled) cancel.click(); }
    if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); if (!save.disabled) form.requestSubmit(); }
  });
  input.addEventListener('change', async () => {
    const generation = epoch, sequence = ++readSequence, files = [...input.files]; input.value = '';
    reading = true; update();
    try {
      const attachments = await readAttachments(files, model.attachments);
      stale(generation);
      if (sequence !== readSequence || editors.get(note.name) !== context || model.locked) return;
      model.attachments.push(...attachments);
    } catch (err) {
      if (generation === epoch && sequence === readSequence) handleFailure(message, err.code ? err : { code: 'ATTACHMENT_READ' });
    } finally { if (generation === epoch && sequence === readSequence) { reading = false; update(); } }
  });
  form.addEventListener('submit', event => {
    event.preventDefault();
    if (save.disabled) return;
    let attempt;
    try {
      attempt = model.begin({ source_hash: note.source_hash, ...(removed.size ? { remove_attachments: [...removed] } : {}) },
        { retained: (note.attachments || []).filter(item => !removed.has(item.name)).length });
    } catch (err) { showError(message, err); return; }
    if (!pendingWrites.has(note.name)) pendingWrites.set(note.name, { method: 'PATCH', body: attempt.body, key: attempt.key, busy: false });
    showError(message, null); performWrite(note);
  });
  bar.append(add, input, actions); actions.append(cancel, save);
  form.append(text, retained, added, bar, message, refresh);
  update(); replaceCard(note); text.focus(); text.setSelectionRange(text.value.length, text.value.length);
}
const pageObserver = new IntersectionObserver(entries => {
  if (entries.some(entry => entry.isIntersecting) && nextToken && !listing && !reloading && isTimeline()) loadNotes();
}, { rootMargin: '200px' });
function updateListControls() {
  $('notes').setAttribute('aria-busy', String(listing));
  $('load-more').hidden = !isTimeline() || !nextToken; $('load-more').disabled = listing;
}
async function loadNotes(reset = false, retry = false) {
  if (!identity || !isTimeline() || (!reset && (listing || !nextToken))) return;
  const generation = epoch, view = viewSequence, sequence = ++listSequence;
  listController?.abort(); listController = new AbortController(); listing = true; clearTimeout(retryTimer);
  if (reset && !retry) retryDelay = 1000;
  pageObserver.disconnect(); updateListControls(); showError($('list-error'), null);
  $('list-retry').hidden = $('empty').hidden = true;
  let succeeded = false;
  try {
    const page = await request(`/notes?${listQuery(location.pathname, filters(location.search), reset ? '' : nextToken)}`, { controller: listController });
    if (generation !== epoch || view !== viewSequence || sequence !== listSequence) return;
    const state = location.pathname === '/archived' ? 'ARCHIVED' : 'NORMAL';
    if (!Array.isArray(page?.notes) || page.notes.some(note => !validNote(note) || note.state !== state)) {
      throw Object.assign(new Error('Invalid note list response'), { code: 'NOTE_LIST_INVALID' });
    }
    const merged = new Map((reset ? [] : items).map(note => [note.name, note]));
    for (const note of page.notes) merged.set(note.name, note);
    items = [...merged.values()].sort(compareNotes); renderList();
    pageCount = reset ? 1 : pageCount + 1;
    const previousToken = nextToken;
    nextToken = typeof page.next_page_token === 'string' ? page.next_page_token : '';
    if (!reset && nextToken && nextToken === previousToken) nextToken = '';
    retryDelay = 1000; $('empty').hidden = !!items.length || !!nextToken; succeeded = true;
  } catch (err) {
    if (generation !== epoch || view !== viewSequence || sequence !== listSequence) return;
    handleFailure($('list-error'), err);
    if (!identity) return;
    retryReset = reset || err.code === 'INVALID_PAGE_TOKEN';
    $('list-retry').textContent = retryReset ? '刷新主页' : '重试加载'; $('list-retry').hidden = false;
    if (err.retryable) {
      retryTimer = setTimeout(() => {
        if (generation === epoch && view === viewSequence && sequence === listSequence) loadNotes(reset, true);
      }, retryDelay);
      retryDelay = Math.min(retryDelay * 2, 30_000);
    }
  } finally {
    if (generation === epoch && view === viewSequence && sequence === listSequence) {
      listing = false; updateListControls();
      if (succeeded && nextToken && !reloading) pageObserver.observe($('page-end'));
    }
  }
}
async function showRoute() {
  if (!identity) return;
  settingsPage?.destroy(); settingsPage = null;
  const generation = epoch, sequence = ++viewSequence;
  listSequence++; listController?.abort(); clearTimeout(retryTimer); pageObserver.disconnect();
  listing = reloading = false; items = []; nextToken = ''; pageCount = 0;
  $('notes').replaceChildren(); closeMenus();
  $('notes').hidden = $('page-end').hidden = location.pathname === '/settings';
  for (const id of ['back', 'view-title', 'capability', 'empty', 'list-retry', 'filters', 'load-more']) $(id).hidden = true;
  showError($('list-error'), null); $('composer').hidden = location.pathname !== '/';
  const detail = notePath.exec(location.pathname);
  document.title = `${detail ? '笔记' : pages.get(location.pathname) || '页面不存在'} · Moerise`;
  $('q').value = sourceTimeline().values.query;
  lastTimeline = sourceTimeline();
  if (sourceTimeline().values.date) month = sourceTimeline().values.date.slice(0, 7);
  document.querySelectorAll('[data-route]').forEach(link => {
    const active = link.getAttribute('href') === location.pathname; link.classList.toggle('on', active);
    if (active) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');
  });
  loadSidebar(); updateListControls();
  if (isTimeline()) {
    if (location.pathname === '/archived') { $('view-title').hidden = false; $('view-title').textContent = '归档笔记'; }
    renderFilters();
    const snapshot = snapshots.get(currentKey);
    if (snapshot?.url === currentURL && !snapshot.dirty) {
      items = [...snapshot.items]; nextToken = snapshot.nextToken; pageCount = snapshot.pages; renderList(); $('empty').hidden = !!items.length || !!nextToken;
      updateListControls();
      if (nextToken) pageObserver.observe($('page-end'));
      restoreScroll(snapshot.scroll);
    } else {
      const saved = snapshot || history.state;
      window.scrollTo(0, 0);
      await reloadTimeline(Math.max(1, saved?.pages || 1), saved?.scroll || 0);
      snapshots.delete(currentKey);
    }
  } else if (detail) {
    const from = safeFrom(new URLSearchParams(location.search).get('from'));
    const label = pages.get(new URL(from, location.origin).pathname) || '主页';
    routeReturnKey = history.state?.returnKey || null;
    $('back').textContent = `‹ 返回${label}`; $('back').hidden = false; window.scrollTo(0, 0);
    try {
      const note = await request(`/notes/${detail[1]}`);
      if (generation !== epoch || sequence !== viewSequence) return;
      if (!validNote(note)) throw Object.assign(new Error('Invalid note'), { code: 'NOTE_LIST_INVALID' });
      items = [note]; renderList();
    } catch (err) {
      if (generation !== epoch || sequence !== viewSequence) return;
      handleFailure($('list-error'), err);
      if (identity) { $('list-retry').textContent = '重新加载笔记'; $('list-retry').hidden = false; }
    }
  } else if (location.pathname === '/settings') {
    $('view-title').textContent = '设置'; $('view-title').hidden = false;
    window.scrollTo(0, 0);
    settingsPage = createSettingsPage({
      sessionRequest, handleFailure,
      onSaved: data => {
        identity.timezone = data.settings.timezone;
        month = sourceTimeline().values.date?.slice(0, 7) || localToday().slice(0, 7);
        dirtySnapshots(); invalidateSidebar(); loadSidebar();
      },
    });
    await settingsPage.mount($('workspace'));
  } else {
    $('view-title').textContent = pages.get(location.pathname) || '页面不存在'; $('view-title').hidden = false;
    $('capability').textContent = pages.has(location.pathname) ? `${pages.get(location.pathname)}尚未开放。` : '请从侧栏打开主页。';
    $('capability').hidden = false; window.scrollTo(0, 0);
  }
}
function updateComposer() {
  $('save').disabled = saving || readingFiles || !identity || (!draft.locked && !draft.content.trim() && !draft.attachments.length);
  $('content').readOnly = saving || draft.locked;
  $('add-attachment').disabled = $('attachment-input').disabled = saving || readingFiles || draft.locked;
  $('composer').setAttribute('aria-busy', String(saving || readingFiles));
  selectedFiles($('draft-attachments'), draft, () => saving || readingFiles || draft.locked, updateComposer);
}
$('content').addEventListener('input', () => { draft.content = $('content').value; updateComposer(); });
$('content').addEventListener('keydown', event => {
  if ((event.ctrlKey || event.metaKey) && event.key === 'Enter' && !event.isComposing) {
    event.preventDefault(); if (!$('save').disabled) $('composer').requestSubmit();
  }
});
$('add-attachment').addEventListener('click', () => $('attachment-input').click());
$('attachment-input').addEventListener('change', async () => {
  const generation = epoch, files = [...$('attachment-input').files]; $('attachment-input').value = '';
  readingFiles = true; updateComposer();
  try {
    const attachments = await readAttachments(files, draft.attachments); stale(generation);
    draft.attachments.push(...attachments); showError($('save-error'), null);
  } catch (err) {
    if (generation === epoch) handleFailure($('save-error'), err.code ? err : { code: 'ATTACHMENT_READ' });
  } finally { if (generation === epoch) { readingFiles = false; updateComposer(); } }
});
$('composer').addEventListener('submit', async event => {
  event.preventDefault();
  if (saving || readingFiles || !identity) return;
  let attempt;
  try { attempt = draft.begin(); } catch (err) { showError($('save-error'), err); return; }
  const generation = epoch;
  saving = true; updateComposer(); showError($('save-error'), null);
  try {
    const note = await sessionRequest('/notes', { method: 'POST', body: attempt.body, headers: { 'Idempotency-Key': attempt.key } });
    stale(generation);
    if (!validNote(note)) throw Object.assign(new Error('Invalid create response'), { code: 'NOTE_SAVE_UNCONFIRMED' });
    draft.confirm(attempt); $('content').value = draft.content; dirtySnapshots();
    if (isTimeline()) await loadNotes(true);
    invalidateSidebar(); loadSidebar();
  } catch (err) {
    if (generation !== epoch) return;
    draft.reject(attempt, err.code); handleFailure($('save-error'), err);
  } finally { if (generation === epoch) { saving = false; updateComposer(); } }
});
$('q').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => sidebarFilter({ query: $('q').value }), 250); });
$('q').addEventListener('keydown', event => {
  if (event.key === 'Enter') { event.preventDefault(); clearTimeout(searchTimer); sidebarFilter({ query: $('q').value }); }
});
$('month-prev').addEventListener('click', () => changeMonth(-1));
$('month-next').addEventListener('click', () => changeMonth(1));
$('sidebar-retry').addEventListener('click', loadSidebar);
$('load-more').addEventListener('click', () => loadNotes());
$('list-retry').addEventListener('click', () => isTimeline() ? loadNotes(retryReset) : showRoute());
$('back').addEventListener('click', () => {
  if (routeReturnKey) history.back();
  else navigate(safeFrom(new URLSearchParams(location.search).get('from')));
});
$('logout').addEventListener('click', logout);
$('user-menu').addEventListener('click', () => {
  const open = $('account-menu').hidden; closeMenus(); $('account-menu').hidden = !open;
  $('user-menu').setAttribute('aria-expanded', String(open)); if (open) $('account-menu').querySelector('[role="menuitem"]')?.focus();
});
const extensionPaths = new Map([['每日回顾', '/review'], ['随机漫步', '/walk'], ['AI 洞察', '/insights'], ['认知地图', '/map'], ['设置', '/settings']]);
document.querySelectorAll('a[aria-disabled="true"]').forEach(link => {
  const path = extensionPaths.get(link.textContent.trim());
  if (path) { link.href = path; link.dataset.route = ''; link.removeAttribute('aria-disabled'); }
});
document.addEventListener('click', event => {
  const link = event.target.closest('a[data-route]');
  if (link && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey && event.button === 0) {
    event.preventDefault();
    if (identity) {
      const url = new URL(link.href), isDetail = notePath.test(url.pathname);
      navigate(url.pathname + url.search, { returnKey: isDetail ? currentKey : null });
    }
  }
  if (!event.target.closest('.mw, #account')) closeMenus();
});
document.addEventListener('keydown', event => {
  if (event.key === 'Escape') {
    const open = document.querySelector('[aria-haspopup="menu"][aria-expanded="true"]'); closeMenus(); open?.focus();
  }
});
window.addEventListener('popstate', () => {
  rememberView(false); clearTimeout(searchTimer);
  currentURL = location.pathname + location.search; currentKey = history.state?.webKey || crypto.randomUUID();
  if (identity) showRoute();
});
$('mobile-filters').addEventListener('click', () => {
  const open = document.querySelector('.side').classList.toggle('filters-open');
  $('mobile-filters').setAttribute('aria-expanded', String(open));
});
window.addEventListener('scroll', () => {
  if (identity && isTimeline()) history.replaceState({ ...history.state, scroll: window.scrollY, pages: pageCount }, '');
}, { passive: true });
window.addEventListener('pagehide', () => { invalidate(); hidePrivate(); });
window.addEventListener('pageshow', event => { if (event.persisted) restore(); });
window.addEventListener('focus', refreshSidebarOnFocus);
document.addEventListener('visibilitychange', refreshSidebarOnFocus);
restore();
