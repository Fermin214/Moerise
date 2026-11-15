import { l, setText, setAttr, resolveText, locale, monthLabel, weekdays, initI18n } from './i18n.js';
import { Draft, readAttachments } from './draft.js';
import { errorMessage as errorText } from './errors.js';
import { pages, notePath, filters, timelineURL, listQuery, enhancedSearch, searchBody, safeFrom, detailURL, noteID, attachmentURL, rasterImage } from './routes.js';
import { renderMarkdown } from './markdown.js';
import { initShell } from './shell.js';

const $ = id => document.getElementById(id);
const shell = initShell();
const draft = new Draft(), controllers = new Set(), snapshots = new Map();
const editors = new Map(), pendingWrites = new Map(), failures = new Map();
const sidebarTags = new Map(), sidebarCalendars = new Map();
let sidebarTagState = '', sidebarCalendarKey = '';
let sidebarFocusTimer = null, sidebarFocusRefresh = null;
let epoch = 0, viewSequence = 0, listSequence = 0, sidebarSequence = 0;
let identity = null, logoutCSRF = null, authBusy = false, saving = false, readingFiles = false, listing = false;
let draftOwner = null;
let items = [], nextToken = '', listController = null, retryReset = true;
let retryTimer = null, retryDelay = 1000, searchTimer = null;
let currentURL = location.pathname + location.search;
let currentKey = history.state?.webKey || crypto.randomUUID();
let month = '', routeReturnKey = null;
let pageCount = 0, reloading = false, moduleReload = false;
let searchStatus = '';
let lastTimeline = { path: '/', values: filters('') };
let settingsPage = null;
let insightsPage = null;
let mapPage = null, mapView = null, mapSnapshot = null;
let discoveryPage = null, reviewView = null, walkView = null;
let walkConfiguration = null, walkFloaters = null;
history.replaceState({ ...history.state, webKey: currentKey }, '');
history.scrollRestoration = 'manual';

function el(tag, className = '', text = '') {
  const node = document.createElement(tag);
  node.className = className;
  if (text) setText(node, text);
  return node;
}
function button(text, className, action) {
  const node = el('button', className, text);
  node.type = 'button';
  node.addEventListener('click', action);
  return node;
}
function showError(element, err) {
  setText(element, err ? errorText(err) : '');
  element.hidden = !err;
}
function aborted() { return new DOMException('Stale request', 'AbortError'); }
function stale(generation) { if (generation !== epoch) throw aborted(); }
function invalidate() {
  epoch++; viewSequence++; listSequence++;
  settingsPage?.destroy(); settingsPage = null;
  insightsPage?.destroy(); insightsPage = null;
  mapPage?.destroy(); mapPage = null;
  discoveryPage?.destroy(); discoveryPage = null;
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
      throw Object.assign(new Error(failure.message || l("请求失败，请重试。")), {
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
  if (value.stargazing_enabled !== undefined && typeof value.stargazing_enabled !== 'boolean') {
    throw Object.assign(new Error('Invalid stargazing configuration'), { code: 'SESSION_RESPONSE_INVALID' });
  }
  return value;
}
function updateStargazing(enabled = identity?.stargazing_enabled !== false) {
  if (identity) identity.stargazing_enabled = enabled;
  document.querySelectorAll('a[data-route][href="/map"]').forEach(link => { link.hidden = !enabled; });
  if (!enabled) mapView = mapSnapshot = null;
}
async function sessionRequest(path, options = {}) {
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
    if (identity) { identity = value; updateStargazing(); }
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
  shell.reset();
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
  sidebarTagState = sidebarCalendarKey = '';
  for (const id of ['content', 'q', 'attachment-input', 'username', 'password']) $(id).value = '';
  for (const id of ['save-error', 'list-error', 'sidebar-error']) showError($(id), null);
  $('empty').hidden = $('account').hidden = true;
  setText($('account-name'), setText($('calendar-title'), ''));
  pageObserver.disconnect();
  items = []; nextToken = '';
  searchStatus = ''; $('search-mode').value = 'keyword';
  snapshots.clear();
  reviewView = walkView = null;
  walkConfiguration = walkFloaters = null;
  mapView = mapSnapshot = null;
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
function formatTime(value) {
  if (!Number.isFinite(Date.parse(value))) return '';
  return () => {
    try {
      const parts = Object.fromEntries(new Intl.DateTimeFormat('en-CA-u-ca-gregory-nu-latn', {
        timeZone: identity?.timezone, year: 'numeric', month: '2-digit', day: '2-digit',
        hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
      }).formatToParts(new Date(value)).map(part => [part.type, part.value]));
      return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}`;
    } catch { return ''; }
  };
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
function rememberView(writeHistory = true, clean = false) {
  if (!identity || !pageCount || !['/', '/archived'].includes(new URL(currentURL, location.origin).pathname)) return;
  const previous = snapshots.get(currentKey);
  snapshots.delete(currentKey);
  snapshots.set(currentKey, { url: currentURL, items: [...items], nextToken, searchStatus, scroll: window.scrollY, pages: pageCount, dirty: clean ? false : previous?.dirty });
  if (writeHistory) history.replaceState({ ...history.state, scroll: window.scrollY, pages: pageCount }, '');
  if (snapshots.size > 12) snapshots.delete(snapshots.keys().next().value);
}
function timelineSnapshot() {
  const saved = snapshots.get(currentKey);
  if (saved?.url === currentURL) return saved;
  return [...snapshots.values()].reverse().find(value => value.url === currentURL && !value.dirty);
}
function navigate(url, { replace = false, returnKey = null } = {}) {
  shell.closeNavigation();
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
  reviewView = walkView = walkFloaters = mapSnapshot = null;
  discoveryPage?.refresh();
  mapPage?.refresh();
}
async function reloadTimeline(pages = pageCount || 1, scroll = window.scrollY) {
  const sequence = viewSequence;
  reloading = true;
  try {
    await loadNotes(true, false, { pages, scroll });
  } finally {
    if (identity && sequence === viewSequence) {
      reloading = false;
      if (nextToken && $('list-retry').hidden) pageObserver.observe($('page-end'));
    }
  }
}
async function enter(value) {
  identity = validIdentity(value);
  updateStargazing();
  if (draftOwner && draftOwner !== identity.user.name) clearDrafts();
  draftOwner = null;
  logoutCSRF = null;
  $('auth').hidden = true;
  $('app').hidden = $('workspace').hidden = false;
  $('password').value = '';
  setText($('account-name'), identity.user.display_name?.trim() || identity.user.name.replace(/^users\//, ''));
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
  history.replaceState({ ...history.state, mapView: null }, '');
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
    ...values.tags.map(tag => ({ label: `#${tag}`, remove: () => sidebarFilter({ tags: values.tags.filter(item => item !== tag) }) })),
    ...(values.date ? [{ label: values.date, remove: () => sidebarFilter({ date: '' }) }] : []),
  ];
  box.hidden = !chips.length;
  if (!chips.length) return;
  box.append(el('span', '', l("筛选")));
  for (const chip of chips) {
    const span = el('span', 'fc', chip.label);
    const remove = button('×', '', chip.remove);
    setAttr(remove, 'aria-label', l("移除筛选 {0}", { 0: chip.label }));
    span.append(remove); box.append(span);
  }
  box.append(button(l("清除全部"), 'ghost', () => sidebarFilter({ tags: [], date: '' })));
}
function invalidateSidebar() {
  sidebarSequence++;
  clearTimeout(sidebarFocusTimer);
  sidebarFocusTimer = sidebarFocusRefresh = null;
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
  if (!identity || document.visibilityState !== 'visible' || sidebarFocusRefresh) return;
  clearTimeout(sidebarFocusTimer);
  sidebarFocusTimer = setTimeout(() => {
    sidebarFocusTimer = null;
    if (!identity || document.visibilityState !== 'visible') return;
    for (const cache of [sidebarTags, sidebarCalendars]) {
      for (const [key, entry] of cache) if (!entry.controller) cache.delete(key);
    }
    const pending = loadSidebar();
    sidebarFocusRefresh = pending;
    pending.finally(() => { if (sidebarFocusRefresh === pending) sidebarFocusRefresh = null; });
  }, 100);
}
async function loadSidebar() {
  if (!identity) return;
  const sequence = ++sidebarSequence, generation = epoch;
  const { path, values } = sourceTimeline();
  const state = path === '/archived' ? 'ARCHIVED' : 'NORMAL', selectedMonth = month;
  const calendarKey = `${state}:${selectedMonth}`;
  showError($('sidebar-error'), null); $('sidebar-retry').hidden = true;
  if (sidebarTagState !== state) {
    $('tags').replaceChildren();
    sidebarTagState = state;
  }
  if (sidebarCalendarKey !== calendarKey) {
    renderCalendar([], values.date, true);
    sidebarCalendarKey = calendarKey;
  }
  try {
    const [tags, calendar] = await Promise.all([
      sidebarData(sidebarTags, state, `/tags?${new URLSearchParams({ state })}`),
      sidebarData(sidebarCalendars, calendarKey, `/calendar?${new URLSearchParams({ state, month: selectedMonth })}`),
    ]);
    if (generation !== epoch || sequence !== sidebarSequence) return;
    if (!Array.isArray(tags?.tags) || !Array.isArray(calendar?.days) || calendar.month !== selectedMonth) {
      sidebarTags.delete(state); sidebarCalendars.delete(calendarKey);
      throw Object.assign(new Error('Invalid sidebar data'), { code: 'NOTE_LIST_INVALID' });
    }
    renderTags(tags.tags, values.tags);
    renderCalendar(calendar.days, values.date);
  } catch (err) {
    if (generation !== epoch || sequence !== sidebarSequence) return;
    handleFailure($('sidebar-error'), err);
    if (identity) $('sidebar-retry').hidden = false;
  }
}
function renderTags(items, selectedTags) {
  const selected = new Set(selectedTags);
  const matches = items.filter(item => typeof item?.tag === 'string' && Number.isInteger(item.count) && item.count >= 0)
    .sort((a, b) => Number(selected.has(b.tag)) - Number(selected.has(a.tag)) || b.count - a.count || a.tag.localeCompare(b.tag));
  const fragment = document.createDocumentFragment();
  for (const item of matches) {
    const on = selected.has(item.tag);
    const link = button('', `ent tag-row${on ? ' on' : ''}`, () => {
      const source = sourceTimeline();
      sidebarFilter({ tags: source.values.tags.includes(item.tag)
        ? source.values.tags.filter(tag => tag !== item.tag) : [...source.values.tags, item.tag] });
    });
    link.setAttribute('aria-pressed', String(on)); link.title = `#${item.tag}`;
    link.append(el('span', '', `#${item.tag}`), el('em', '', String(item.count)));
    fragment.append(link);
  }
  if (!matches.length) fragment.append(el('p', 'sub', l("暂无标签")));
  $('tags').replaceChildren(fragment);
}
function renderCalendar(days, selected, disabled = false) {
  const [year, number] = month.split('-').map(Number);
  setText($('calendar-title'), monthLabel(year, number, true));
  const calendar = $('calendar');
  calendar.replaceChildren(...weekdays.map(label => el('span', '', label)));
  const counts = new Map(days.map(day => [day.date, day.count]));
  const offset = (new Date(year, number - 1, 1).getDay() + 6) % 7, size = new Date(year, number, 0).getDate();
  for (let i = 0; i < offset; i++) {
    const blank = button('', 'blank', () => {}); blank.disabled = true; blank.tabIndex = -1; calendar.append(blank);
  }
  const today = localToday();
  for (let day = 1; day <= size; day++) {
    const date = `${month}-${String(day).padStart(2, '0')}`, count = counts.get(date) || 0;
    const cell = button(String(day), [selected === date ? 'sel' : count > 1 ? 'h2' : count ? 'h1' : '', date === today ? 'today' : ''].join(' '),
      () => sidebarFilter({ date: sourceTimeline().values.date === date ? '' : date }));
    cell.disabled = disabled;
    setAttr(cell, 'aria-label', l("{0} · {1} 条笔记", { 0: date, 1: count }));
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
    if (!url) { box.append(el('span', 'attachment sub', l("{0} · 暂时无法打开", { 0: item.filename || l("附件") }))); continue; }
    const link = el('a', rasterImage(item.type) ? 'image-link' : 'attachment');
    link.href = url;
    if (rasterImage(item.type)) {
      link.target = '_blank'; link.rel = 'noopener noreferrer';
      const image = el('img');
      image.src = url; setAttr(image, 'alt', item.filename || l("图片")); image.loading = 'lazy';
      image.addEventListener('error', () => {
        image.remove(); link.className = 'attachment'; setText(link, l("{0} · 打开原图", { 0: item.filename || l("图片") }));
      }, { once: true });
      link.append(image);
    } else {
      link.download = item.filename || 'attachment';
      setText(link, item.filename || l("下载附件"));
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
  const header = el('div', 'hd'), time = el('time', '', formatTime(note.create_time));
  if (Number.isFinite(Date.parse(note.create_time))) time.dateTime = note.create_time;
  if (detail) header.append(time);
  else {
    const link = el('a', 'dlink');
    link.href = detailURL(note.name, currentURL); link.dataset.route = ''; link.append(time); header.append(link);
  }
  const menuWrap = el('div', 'mw'), menu = el('div', 'menu');
  menu.hidden = true; menu.setAttribute('role', 'menu');
  const more = button('', 'btn s more', () => {
    const open = menu.hidden; closeMenus(); menu.hidden = !open;
    more.setAttribute('aria-expanded', String(open));
    if (open) menu.querySelector('button')?.focus();
  });
  const dots = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  dots.setAttribute('class', 'i dots'); dots.setAttribute('viewBox', '0 0 24 24'); dots.setAttribute('aria-hidden', 'true');
  const points = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  points.setAttribute('d', 'M5 12h.01M12 12h.01M19 12h.01'); dots.append(points); more.append(dots);
  setAttr(more, 'aria-label', l("更多操作"));
  more.setAttribute('aria-haspopup', 'menu'); more.setAttribute('aria-expanded', 'false');
  const content = el('div', 'note-content'); content.append(renderMarkdown(note.content, note));
  const tags = el('div', 'note-tags');
  for (const tag of new Set(note.tags || [])) tags.append(button(`#${tag}`, 'tag', () => {
    const source = sourceTimeline();
    sidebarFilter({ tags: [...new Set([...source.values.tags, tag])] });
  }));
  const actions = [
    [l("编辑"), () => startEdit(note), ''],
    ...(!detail ? [[l("打开笔记"), () => openNote(note), '']] : []),
    [note.state === 'ARCHIVED' ? l("恢复到主页") : l("归档"), () => changeState(note), ''],
    ...(note.state === 'NORMAL' ? [[l("从这条漫步"), () => startFromNote(note), '']] : []),
    [l("洞察这条"), () => navigate(`/insights?${new URLSearchParams({ note: note.name })}`), ''],
    [l("删除"), () => confirmDelete(card, note), 'dg'],
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
  if (pendingWrites.has(note.name)) box.append(button(l("重试原提交"), 'ghost', () => performWrite(note)));
  box.append(button(l("刷新笔记"), 'ghost', () => refreshNote(note))); card.append(box);
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
  confirm.append(el('span', '', l("删除这条笔记？删除后无法恢复。")));
  const controls = el('span', 'editor-actions');
  const cancel = button(l("取消"), 'ghost', () => { confirm.remove(); card.querySelector('.more, .note-actions button')?.focus(); });
  const remove = button(l("删除"), 'danger', () => {
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
    setAttr(remove, 'aria-label', l("移除附件 {0}", { 0: item.filename })); remove.disabled = locked(); row.append(remove); box.append(row);
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
  form.dataset.name = note.name; setAttr(form, 'aria-label', l("编辑笔记"));
  const text = el('textarea'); text.value = note.content; text.rows = notePath.test(location.pathname) ? 5 : 3;
  setAttr(text, 'aria-label', l("编辑笔记内容"));
  setAttr(text, 'placeholder', l("今天想到了什么……"));
  const retained = el('div', 'attachment-list'), added = el('div', 'attachment-list');
  const bar = el('div', 'bar'), actions = el('div', 'editor-actions'), input = el('input', 'sr');
  input.type = 'file'; input.multiple = true; input.tabIndex = -1;
  const add = button('+', 'btn', () => input.click()); setAttr(add, 'aria-label', l("添加附件"));
  const cancel = button(l("取消"), 'ghost', () => {
    if (pendingWrites.get(note.name)?.busy) return;
    readSequence++; model.clear(); editors.delete(note.name);
    if (!pendingWrites.has(note.name)) failures.delete(note.name);
    replaceCard(note);
  });
  const save = button(l("保存"), 'save', () => form.requestSubmit()); save.append(el('small', '', 'Ctrl ↵'));
  const message = el('p', 'error'); message.hidden = true; message.setAttribute('role', 'alert');
  const refresh = button(l("刷新笔记"), 'ghost', () => refreshNote(note)); refresh.hidden = true;
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
      const row = el('span', 'attachment', item.filename || l("附件"));
      const remove = button('×', 'dlink', () => { if (!model.locked && !busy) { removed.add(item.name); update(); } });
      setAttr(remove, 'aria-label', l("移除附件 {0}", { 0: item.filename || l("附件") })); remove.disabled = model.locked || busy;
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
async function loadNotes(reset = false, retry = false, { pages = 1, scroll } = {}) {
  if (!identity || !isTimeline() || (!reset && (listing || !nextToken))) return;
  const generation = epoch, view = viewSequence, sequence = ++listSequence;
  listController?.abort(); listController = new AbortController(); listing = true; clearTimeout(retryTimer);
  if (reset && !retry) retryDelay = 1000;
  pageObserver.disconnect(); updateListControls(); showError($('list-error'), null);
  $('list-retry').hidden = $('empty').hidden = true;
  let succeeded = false;
  try {
    const values = filters(location.search), enhanced = enhancedSearch(values);
    const state = location.pathname === '/archived' ? 'ARCHIVED' : 'NORMAL';
    const merged = new Map((reset ? [] : items).map(note => [note.name, note]));
    let token = reset ? '' : nextToken, loaded = 0, page;
    const seenTokens = new Set(token ? [token] : []);
    do {
      page = enhanced
        ? await sessionRequest('/search', { method: 'POST', body: searchBody(location.pathname, values, token), controller: listController })
        : await request(`/notes?${listQuery(location.pathname, values, token)}`, { controller: listController });
      if (generation !== epoch || view !== viewSequence || sequence !== listSequence) return;
      if (!Array.isArray(page?.notes) || page.notes.some(note => !validNote(note) || note.state !== state) ||
          (enhanced && (!['keyword', values.mode].includes(page.mode) ||
            typeof page.semantic?.used !== 'boolean' || typeof page.semantic?.reason !== 'string' ||
            (page.mode === 'keyword' && (values.mode !== 'hybrid' || page.semantic.used)) ||
            (page.mode !== 'keyword' && !page.semantic.used)))) {
        throw Object.assign(new Error('Invalid note list response'), { code: 'NOTE_LIST_INVALID' });
      }
      for (const note of page.notes) merged.set(note.name, note);
      token = typeof page.next_page_token === 'string' ? page.next_page_token : '';
      if (seenTokens.has(token)) token = '';
      else if (token) seenTokens.add(token);
      loaded++;
    } while (reset && loaded < pages && token);
    items = [...merged.values()];
    if (!enhanced) items.sort(compareNotes);
    renderList();
    searchStatus = enhanced && page.mode === 'keyword'
      ? l("语义检索暂不可用，当前显示按相关度排列的关键词结果。")
      : enhanced ? l("搜索结果按相关度排列。") : '';
    setText($('capability'), searchStatus); $('capability').hidden = !searchStatus;
    pageCount = reset ? loaded : pageCount + loaded;
    nextToken = token;
    retryDelay = 1000; $('empty').hidden = !!items.length || !!nextToken; succeeded = true;
    rememberView(false, true);
    if (scroll != null) restoreScroll(scroll);
  } catch (err) {
    if (generation !== epoch || view !== viewSequence || sequence !== listSequence) return;
    handleFailure($('list-error'), err.code === 'SOURCE_CHANGED' ? { ...err, code: 'SEARCH_SOURCE_CHANGED' } : err);
    if (!identity) return;
    retryReset = reset || ['INVALID_PAGE_TOKEN', 'SOURCE_CHANGED'].includes(err.code);
    setText($('list-retry'), retryReset ? l("刷新主页") : l("重试加载")); $('list-retry').hidden = false;
    if (err.retryable) {
      retryTimer = setTimeout(() => {
        if (generation === epoch && view === viewSequence && sequence === listSequence) loadNotes(retryReset, true, { pages, scroll });
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
function startFromNote(note) {
  navigate(`/walk?${new URLSearchParams({ name: note.name })}`);
}
function discoveryText(value) {
  const code = typeof value === 'string' ? value : value?.code;
  return {
    EMBEDDING_DISABLED: l("请在设置中启用 Embedding。"),
    EMBEDDING_UNAVAILABLE: l("Embedding 暂时无法连接，请重试或在设置中检查连接。"),
    EMBEDDING_RATE_LIMITED: l("Embedding 调用受限，请稍后重试。"),
    EMBEDDING_PROTOCOL: l("Embedding 返回格式或维度与配置不符，请在设置中检查连接。"),
    INDEX_NOT_READY: l("语义索引尚未就绪，请在设置中查看构建进度后重试。"),
    NO_SOURCE_VECTORS: l("这条笔记没有可用的正文向量，请从其他笔记开始。"),
    SEMANTIC_UNAVAILABLE: l("语义索引暂时不可用，请稍后重试。"),
    WALK_RESOURCE_LIMIT: l("漫步暂时达到技术资源限制，请重试。"),
    WALK_ARCHIVED: l("归档笔记不参与漫步，请选择其他笔记。"),
    RESOURCE_LIMIT: l("漫步暂时达到技术资源限制，请重试。"),
    WALK_RESPONSE_INVALID: l("服务返回的漫步内容不完整，请重试。"),
    REVIEW_RESPONSE_INVALID: l("服务返回的回顾内容不完整，请重试。"),
    REQUEST_TIMEOUT: l("读取超时，请重试。"),
  }[code] || (typeof value === 'string' ? l("服务提示：{0}", { 0: value }) : errorText(value));
}
function discoveryCard(note, review = false, className = 'card big discovery-note') {
  const card = el('article', `${className}${review ? ' rv' : ''}`);
  card.dataset.name = note.name;
  const header = el('div', 'hd'), time = el('time', '', formatTime(note.create_time));
  time.dateTime = note.create_time;
  const open = button('', 'btn s', () => openNote(note));
  setAttr(open, 'aria-label', l("打开笔记")); setAttr(open, 'title', l("打开笔记"));
  const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  icon.setAttribute('class', 'i'); icon.setAttribute('viewBox', '0 0 24 24'); icon.setAttribute('aria-hidden', 'true');
  const arrow = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  arrow.setAttribute('d', 'M9 6h9v9M18 6L7 17'); icon.append(arrow); open.append(icon);
  const metadata = el('div', 'discovery-meta');
  if (review && Number.isFinite(Date.parse(note.create_time))) {
    const days = Math.max(0, Math.floor((Date.now() - Date.parse(note.create_time)) / 86_400_000));
    header.append(el('span', 'pill', days ? l("{0} 天前", { 0: days }) : l("今天"))); metadata.append(time);
  } else header.append(time);
  for (const tag of new Set(note.tags || [])) metadata.append(el('span', 'tag', `#${tag}`));
  metadata.append(open); header.append(metadata);
  const content = el('div', 'note-content'); content.append(renderMarkdown(note.content, note));
  card.append(header, content, renderAttachments(note));
  return card;
}
function createDiscoveryPage(kind) {
  let alive = true, sequence = 0, controller = null, busy = false, failure = null, retry = null;
  let modelHint = '', configured = walkConfiguration?.enabled === true, modelController = null;
  let floaters = walkFloaters || [], floaterController, floaterFailure, animatedNote, preparedNext = null;
  const pageKey = currentKey, seedName = new URLSearchParams(location.search).get('name');
  let seedPending = kind === 'walk' && !!seedName && walkView?.key !== pageKey;
  const root = el('div', `stage discovery-page ${kind}-page`);
  root.id = 'discovery-page';
  const heading = el('header', 'discovery-heading');
  heading.append(el('h1', 'vt', kind === 'review' ? l("回顾") : l("漫步")));
  const content = el('div', 'discovery-content'), controls = el('div', 'discovery-controls');
  root.append(heading, content, controls);
  const active = ticket => alive && ticket === sequence;
  function discardNext() {
    preparedNext?.controller.abort(); preparedNext = null;
  }
  function cancel() {
    sequence++; controller?.abort(); controller = null; busy = false; discardNext();
  }
  function message(text, error = false) {
    const node = el('p', error ? 'error' : 'sub', text);
    node.setAttribute('role', error ? 'alert' : 'status'); content.append(node);
  }
  function updateBusy() {
    root.setAttribute('aria-busy', String(kind === 'review' ? busy && !reviewView :
      busy || (!!modelController && !walkConfiguration)));
  }
  function render() {
    if (!alive) return;
    content.replaceChildren(); controls.replaceChildren();
    updateBusy();
    if (kind === 'review') renderReview();
    else renderWalk();
    if (failure) {
      message(discoveryText(failure), true);
      content.append(button(l("重试"), 'ghost discovery-retry', () => retry?.()));
    }
  }
  function renderReview() {
    const batch = reviewView?.batch;
    if (!batch) {
      if (busy) message(l("正在加载回顾…"));
      return;
    }
    if (!batch.items.length) { message(l("今天没有可回顾的旧笔记。")); return; }
    const selected = reviewView.position;
    const stack = el('div', 'review-stack');
    for (let i = 0; i < Math.min(2, batch.items.length - selected - 1); i++) {
      const peek = el('div', `peek peek-${i + 1}`); peek.setAttribute('aria-hidden', 'true'); stack.append(peek);
    }
    batch.items.forEach((item, index) => {
      let card;
      if (item.unavailable) {
        card = el('article', 'card big rv discovery-note');
        card.append(el('p', 'sub', l("这条笔记已不存在或无法访问。")));
      } else card = discoveryCard(item.note, true);
      if (index === selected && animatedNote !== `${batch.id}:${selected}`) card.classList.add(selected % 2 ? 'wb' : 'wa');
      card.hidden = index !== selected; card.dataset.reviewIndex = String(index); stack.append(card);
    });
    animatedNote = `${batch.id}:${selected}`;
    content.append(stack);
    const positions = el('div', 'pg review-positions');
    positions.setAttribute('role', 'group'); setAttr(positions, 'aria-label', l("回顾条目"));
    batch.items.forEach((_, index) => {
      const dot = button('', `dt${index === selected ? ' on' : ''}`, () => {
        reviewView.position = index; render();
        root.querySelectorAll('.review-positions button')[index].focus({ preventScroll: true });
      });
      setAttr(dot, 'aria-label', l("第 {0} / {1} 条", { 0: index + 1, 1: batch.items.length }));
      dot.setAttribute('aria-pressed', String(index === selected)); positions.append(dot);
    });
    const actions = el('div', 'discovery-actions'), current = batch.items[selected];
    if (!current.unavailable) {
      if (current.note.state === 'NORMAL') actions.append(button(l("从这条漫步"), 'ghost', () => startFromNote(current.note)));
      actions.append(button(l("洞察这条"), 'ghost', () => navigate(`/insights?${new URLSearchParams({ note: current.note.name })}`)));
    }
    actions.append(button(selected + 1 < batch.items.length ? l("下一条") : l("再看一遍"), 'save', () => {
      reviewView.position = (selected + 1) % batch.items.length; render();
      root.querySelector('.discovery-actions .save').focus({ preventScroll: true });
    }));
    controls.append(positions, actions);
  }
  async function loadReview() {
    cancel();
    const ticket = sequence;
    if (reviewView?.batch.date !== localToday() || reviewView?.batch.timezone !== identity?.timezone) reviewView = null;
    controller = new AbortController(); busy = true; failure = null; retry = loadReview; render();
    let changed = false;
    try {
      const batch = await request('/reviews/daily', { controller });
      if (!active(ticket)) return;
      if (!batch?.id || !/^\d{4}-\d{2}-\d{2}$/.test(batch.date) || !batch.timezone || !Array.isArray(batch.items) ||
          batch.items.some(item => !item || !noteID(item.name) || typeof item.unavailable !== 'boolean' ||
            (!item.unavailable && (!validNote(item.note) || item.note.name !== item.name)))) {
        throw Object.assign(new Error('Invalid daily review'), { code: 'REVIEW_RESPONSE_INVALID' });
      }
      changed = JSON.stringify(batch) !== JSON.stringify(reviewView?.batch);
      const position = reviewView?.batch.id === batch.id ? reviewView.position : 0;
      reviewView = { batch, position: Math.min(position, Math.max(0, batch.items.length - 1)) };
    } catch (err) {
      if (!active(ticket) || err.name === 'AbortError') return;
      if (err.status === 401) { expire(err); return; }
      failure = err;
    } finally {
      if (active(ticket)) {
        controller = null; busy = false;
        if (changed || failure || !reviewView) render();
        else root.setAttribute('aria-busy', 'false');
      }
    }
  }
  function stopWalk() {
    cancel(); seedPending = false; walkView = { key: pageKey, result: null, previous: null };
    animatedNote = null; failure = retry = null; render();
  }
  function renderWalk() {
    if (modelHint) {
      message(modelHint);
      const settings = el('a', 'dlink', l("打开设置")); settings.href = '/settings'; settings.dataset.route = ''; content.append(settings);
    }
    const result = walkView?.result;
    if (result?.item) {
      const notes = el('div', 'walk-cards');
      if (walkView.previous) {
        const previous = el('div', 'card walk-trail');
        previous.append(el('span', 'sub', formatTime(walkView.previous.create_time)),
          el('div', 'walk-trail-text', walkView.previous.content));
        previous.setAttribute('aria-hidden', 'true'); notes.append(previous);
      }
      const current = discoveryCard(result.item.note);
      if (animatedNote !== result.item.note.name) current.classList.add(result.path.length % 2 ? 'wa' : 'wb');
      animatedNote = result.item.note.name;
      notes.append(current); content.append(notes);
    } else if (!result?.exhausted) {
      const start = el('div', 'walk-start');
      floaters.forEach(note => {
        const preview = [...note.content.replace(/\s+/gu, ' ').trim()].slice(0, 64).join('') || note.attachments?.[0]?.filename || l("笔记");
        const floater = button('', 'flc fl walk-floater', () => walkRequest('/walks/start', { name: note.name }));
        floater.append(el('span', '', preview));
        floater.dataset.name = note.name;
        setAttr(floater, 'aria-label', l("从这条笔记开始：{0}", { 0: preview }));
        floater.disabled = busy || !configured;
        start.append(floater);
      });
      const control = button(busy || (modelController && !walkConfiguration) ? l("翻找中…") : l("随机开始"), 'save', () => walkRequest('/walks/start', {}));
      control.disabled = busy || !configured; start.append(control); content.append(start);
      if (floaterFailure) {
        message(errorText(floaterFailure), true);
        content.append(button(l("重试加载笔记"), 'ghost discovery-retry', loadFloaters));
      }
    }
    for (const warning of walkView?.warnings || []) message(discoveryText(warning));
    if (result?.exhausted) message(result.item ? l("能走的路都走完了。") : l("没有可漫步的笔记。"));
    if (result || ((busy || failure) && retry !== loadWalk)) {
      const actions = el('div', 'discovery-actions');
      actions.append(button(l("停止"), 'pbtn', stopWalk));
      if (result?.item && !result.exhausted) {
        const next = button(busy ? l("翻找中…") : l("继续漫步"), 'save', () => walkRequest('/walks/next', {
          name: result.item.note.name, path: [...result.path],
        }));
        next.disabled = busy || !configured; actions.append(next);
      }
      controls.append(actions);
    }
  }
  async function loadFloaters() {
    floaterController?.abort();
    const pending = new AbortController(); floaterController = pending; floaterFailure = null;
    try {
      const page = await request(`/notes?${listQuery('/', { tags: [] })}`, { controller: pending });
      if (!alive || floaterController !== pending) return;
      if (!Array.isArray(page?.notes) || page.notes.some(note => !validNote(note) || note.state !== 'NORMAL')) {
        throw Object.assign(new Error('Invalid notes'), { code: 'NOTE_LIST_INVALID' });
      }
      walkFloaters = floaters = page.notes.slice(0, 6);
    } catch (err) {
      if (!alive || floaterController !== pending || err.name === 'AbortError') return;
      if (err.status === 401) { expire(err); return; }
      floaterFailure = err;
    } finally {
      if (alive && floaterController === pending) {
        floaterController = null;
        if (!walkView?.result) render();
      }
    }
  }
  async function fetchWalk(path, body, pending) {
    const result = await sessionRequest(path, { method: 'POST', body, controller: pending });
    const validPath = Array.isArray(result?.path) && result.path.every(name => typeof name === 'string' && noteID(name)) &&
      new Set(result.path).size === result.path.length;
    const prior = path === '/walks/next' ? body.path : [];
    if (!validPath || typeof result.mode !== 'string' || typeof result.exhausted !== 'boolean' ||
        !Array.isArray(result.warnings) || result.warnings.some(value => typeof value !== 'string') ||
        prior.some((name, index) => result.path[index] !== name) ||
        (result.item ? !validNote(result.item.note) || result.item.note.state !== 'NORMAL' || result.path.at(-1) !== result.item.note.name ||
          result.path.length !== prior.length + 1 || (body.name && !prior.length && result.item.note.name !== body.name)
          : result.path.length !== prior.length)) {
      throw Object.assign(new Error('Invalid walk response'), { code: 'WALK_RESPONSE_INVALID' });
    }
    return result;
  }
  function prepareNext() {
    const source = walkView?.result;
    if (!alive || busy || !configured || !source?.item || source.exhausted || preparedNext?.source === source) return;
    discardNext();
    const body = { name: source.item.note.name, path: [...source.path] };
    const pending = { source, key: JSON.stringify(body), controller: new AbortController(), outcome: null, loadedAt: 0 };
    preparedNext = pending;
    pending.promise = fetchWalk('/walks/next', body, pending.controller).then(result => {
      pending.loadedAt = Date.now();
      return pending.outcome = { result };
    }, error => {
      if (alive && preparedNext === pending && error.status === 401) expire(error);
      return pending.outcome = { error };
    });
  }
  async function walkRequest(path, body) {
    if (busy || !configured) return;
    let pending = path === '/walks/next' && preparedNext?.source === walkView?.result &&
      preparedNext.key === JSON.stringify(body) ? preparedNext : null;
    if (pending?.outcome?.error || (pending?.loadedAt && Date.now() - pending.loadedAt >= 60_000)) pending = null;
    if (pending) preparedNext = null;
    else discardNext();
    const ticket = ++sequence;
    controller = pending?.controller || new AbortController(); busy = true; failure = null;
    retry = () => walkRequest(path, body); walkView.warnings = [];
    if (!pending?.outcome?.result) render();
    try {
      const outcome = pending ? pending.outcome || await pending.promise : { result: await fetchWalk(path, body, controller) };
      if (!active(ticket)) return;
      if (outcome.error) throw outcome.error;
      const result = outcome.result;
      walkView.warnings = result.warnings;
      if (!result.item && !result.exhausted) {
        failure = { code: 'WALK_RESOURCE_LIMIT' };
      } else {
        if (result.item) {
          walkView.previous = walkView.result?.item?.note || null; walkView.result = result;
        } else walkView.result = { ...result, item: walkView.result?.item };
      }
    } catch (err) {
      if (!active(ticket) || err.name === 'AbortError') return;
      if (err.status === 401) { expire(err); return; }
      failure = err;
    } finally {
      if (active(ticket)) {
        controller = null; busy = false; render();
        if (!failure) prepareNext();
      }
    }
  }
  function startSeed() {
    if (!seedPending || !configured || busy) return;
    seedPending = false;
    if (!noteID(seedName)) { failure = { code: 'SOURCE_NOT_FOUND' }; render(); return; }
    return walkRequest('/walks/start', { name: seedName });
  }
  async function loadWalk() {
    const ticket = sequence, pending = new AbortController();
    let redraw = !walkConfiguration;
    modelController?.abort(); modelController = pending;
    if (!walkConfiguration) { failure = null; retry = loadWalk; }
    render();
    try {
      const models = await sessionRequest('/models', { controller: pending });
      if (!alive || modelController !== pending) return;
      if (typeof models?.embedding?.enabled !== 'boolean') {
        throw Object.assign(new Error('Invalid models response'), { code: 'WALK_RESPONSE_INVALID' });
      }
      redraw ||= configured !== models.embedding.enabled;
      walkConfiguration = { enabled: models.embedding.enabled };
      configured = models.embedding?.enabled === true;
      modelHint = configured ? '' : l("请在设置中启用 Embedding，再开始漫步。");
      if (!configured) discardNext();
      else { startSeed(); prepareNext(); }
    } catch (err) {
      if (!alive || modelController !== pending || err.name === 'AbortError') return;
      if (err.status === 401) { expire(err); return; }
      if (active(ticket)) { failure = err; retry = loadWalk; redraw = true; }
    } finally {
      if (alive && modelController === pending) {
        modelController = null;
        if (redraw || !walkView?.result) render();
        else updateBusy();
      }
    }
  }
  return {
    async mount(parent) {
      parent.append(root);
      if (kind === 'review') await loadReview();
      else {
        walkView ||= { key: null, result: null, previous: null };
        if (seedPending) walkView = { key: pageKey, result: null, previous: null };
        if (walkConfiguration) {
          modelHint = configured ? '' : l("请在设置中启用 Embedding，再开始漫步。");
          startSeed(); prepareNext();
        }
        await Promise.all([loadWalk(), loadFloaters()]);
      }
    },
    refresh() {
      cancel(); floaterController?.abort(); floaters = [];
      if (kind === 'review') loadReview();
      else {
        walkView = { key: pageKey, result: null, previous: null };
        animatedNote = null; failure = retry = null; render();
        loadWalk(); loadFloaters();
      }
    },
    destroy() { alive = false; cancel(); modelController?.abort(); floaterController?.abort(); root.remove(); },
  };
}
async function routeModule(load, generation, sequence) {
  try {
    const module = await load();
    return identity && generation === epoch && sequence === viewSequence ? module : null;
  } catch {
    if (identity && generation === epoch && sequence === viewSequence) {
      moduleReload = true;
      handleFailure($('list-error'), { code: 'WEB_MODULE_UNAVAILABLE' });
      setText($('list-retry'), l("刷新页面")); $('list-retry').hidden = false;
    }
    return null;
  }
}
async function showRoute() {
  if (!identity) return;
  shell.closeNavigation();
  settingsPage?.destroy(); settingsPage = null;
  insightsPage?.destroy(); insightsPage = null;
  mapPage?.destroy(); mapPage = null;
  discoveryPage?.destroy(); discoveryPage = null;
  const generation = epoch, sequence = ++viewSequence;
  $('main').dataset.page = location.pathname.slice(1) || 'home';
  listSequence++; listController?.abort(); clearTimeout(retryTimer); pageObserver.disconnect();
  listing = reloading = moduleReload = false; items = []; nextToken = ''; pageCount = 0; searchStatus = '';
  $('notes').replaceChildren(); closeMenus();
  $('notes').hidden = $('page-end').hidden = ['/settings', '/review', '/walk', '/insights', '/map'].includes(location.pathname);
  for (const id of ['back', 'view-title', 'capability', 'empty', 'list-retry', 'filters', 'load-more']) $(id).hidden = true;
  showError($('list-error'), null); $('composer').hidden = location.pathname !== '/';
  const detail = notePath.exec(location.pathname);
  setText(document.querySelector('title'), () => `${resolveText(detail ? l("笔记") : pages.get(location.pathname) || l("页面不存在"))} · Moerise`);
  $('q').value = sourceTimeline().values.query;
  $('search-mode').value = sourceTimeline().values.mode;
  lastTimeline = sourceTimeline();
  if (sourceTimeline().values.date) month = sourceTimeline().values.date.slice(0, 7);
  document.querySelectorAll('[data-route]').forEach(link => {
    const active = link.getAttribute('href') === location.pathname; link.classList.toggle('on', active);
    if (active) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');
  });
  loadSidebar(); updateListControls();
  if (isTimeline()) {
    if (location.pathname === '/archived') { $('view-title').hidden = false; setText($('view-title'), l("归档笔记")); }
    renderFilters();
    const snapshot = timelineSnapshot();
    if (snapshot?.url === currentURL && !snapshot.dirty) {
      items = [...snapshot.items]; nextToken = snapshot.nextToken; pageCount = snapshot.pages; renderList(); $('empty').hidden = !!items.length || !!nextToken;
      searchStatus = snapshot.searchStatus || '';
      setText($('capability'), searchStatus); $('capability').hidden = !searchStatus;
      updateListControls();
      rememberView(false, true);
      restoreScroll(snapshot.scroll);
      await reloadTimeline(Math.max(1, snapshot.pages), null);
    } else {
      const saved = snapshot || history.state;
      window.scrollTo(0, 0);
      await reloadTimeline(Math.max(1, saved?.pages || 1), saved?.scroll || 0);
    }
  } else if (detail) {
    const from = safeFrom(new URLSearchParams(location.search).get('from'));
    const label = pages.get(new URL(from, location.origin).pathname) || l("主页");
    routeReturnKey = history.state?.returnKey || null;
    setText($('back'), l("‹ 返回{0}", { 0: label })); $('back').hidden = false; window.scrollTo(0, 0);
    try {
      const note = await request(`/notes/${detail[1]}`);
      if (generation !== epoch || sequence !== viewSequence) return;
      if (!validNote(note)) throw Object.assign(new Error('Invalid note'), { code: 'NOTE_LIST_INVALID' });
      items = [note]; renderList();
    } catch (err) {
      if (generation !== epoch || sequence !== viewSequence) return;
      handleFailure($('list-error'), err);
      if (identity) { setText($('list-retry'), l("重新加载笔记")); $('list-retry').hidden = false; }
    }
  } else if (location.pathname === '/review' || location.pathname === '/walk') {
    window.scrollTo(0, 0);
    discoveryPage = createDiscoveryPage(location.pathname === '/review' ? 'review' : 'walk');
    await discoveryPage.mount($('workspace'));
  } else if (location.pathname === '/insights') {
    window.scrollTo(0, 0);
    const module = await routeModule(() => import('./insights.js'), generation, sequence);
    if (!module) return;
    insightsPage = module.createInsightsPage({
      sessionRequest, csrfToken: () => identity?.csrf_token, handleFailure, openNote,
      navigate: (url, options) => { if (url === currentURL) showRoute(); else navigate(url, options); },
      closeMenus,
      renewCSRF: async () => { identity = validIdentity(await request('/session')); updateStargazing(); },
      replaceURL: url => {
        history.replaceState({ ...history.state }, '', url);
        currentURL = location.pathname + location.search;
      },
      today: localToday, formatTime,
      onSaved: () => { dirtySnapshots(); invalidateSidebar(); loadSidebar(); },
    });
    await insightsPage.mount($('workspace'));
  } else if (location.pathname === '/map') {
    setText($('view-title'), l("观星")); $('view-title').hidden = false;
    window.scrollTo(0, 0);
    if (identity.stargazing_enabled === false) {
      setText($('capability'), l("观星已关闭。")); $('capability').hidden = false;
      return;
    }
    const module = await routeModule(() => import('./map.js'), generation, sequence);
    if (!module) return;
    const saved = history.state?.mapView?.owner === identity.user.name ? history.state.mapView :
      mapView?.owner === identity.user.name ? mapView : null;
    mapPage = module.createMapPage({
      request: sessionRequest, handleFailure, validNote, renderNote: note => discoveryCard(note, false, 'card map-note'),
      initialView: saved,
      initialData: mapSnapshot,
      onData: value => {
        if (identity && generation === epoch && sequence === viewSequence) {
          mapSnapshot = value;
          if (value.disabled_reason === 'stargazing') updateStargazing(false);
        }
      },
      onViewChange: value => {
        if (!identity || generation !== epoch || sequence !== viewSequence) return;
        mapView = { ...value, owner: identity.user.name };
        history.replaceState({ ...history.state, mapView }, '');
      },
    });
    await mapPage.mount($('workspace'));
  } else if (location.pathname === '/settings') {
    setText($('view-title'), l("设置")); $('view-title').hidden = false;
    window.scrollTo(0, 0);
    const module = await routeModule(() => import('./settings.js'), generation, sequence);
    if (!module) return;
    settingsPage = module.createSettingsPage({
      sessionRequest, handleFailure,
      onSaved: data => {
        identity.timezone = data.settings.timezone;
        updateStargazing(data.settings.stargazing.enabled);
        walkConfiguration = null;
        month = sourceTimeline().values.date?.slice(0, 7) || localToday().slice(0, 7);
        dirtySnapshots(); invalidateSidebar(); loadSidebar();
      },
    });
    await settingsPage.mount($('workspace'));
  } else {
    setText($('view-title'), pages.get(location.pathname) || l("页面不存在")); $('view-title').hidden = false;
    setText($('capability'), pages.has(location.pathname) ? l("{0}尚未开放。", { 0: pages.get(location.pathname) }) : l("请从侧栏打开主页。"));
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
$('search-mode').addEventListener('change', () => {
  clearTimeout(searchTimer);
  sidebarFilter({ mode: $('search-mode').value });
});
$('month-prev').addEventListener('click', () => changeMonth(-1));
$('month-next').addEventListener('click', () => changeMonth(1));
$('sidebar-retry').addEventListener('click', loadSidebar);
$('load-more').addEventListener('click', () => loadNotes());
$('list-retry').addEventListener('click', () => {
  if (moduleReload) location.reload();
  else if (isTimeline()) {
    if (retryReset) reloadTimeline();
    else loadNotes();
  }
  else showRoute();
});
$('back').addEventListener('click', () => {
  if (routeReturnKey) history.back();
  else navigate(safeFrom(new URLSearchParams(location.search).get('from')));
});
$('logout').addEventListener('click', logout);
$('user-menu').addEventListener('click', () => {
  const open = $('account-menu').hidden; closeMenus(); $('account-menu').hidden = !open;
  $('user-menu').setAttribute('aria-expanded', String(open)); if (open) $('account-menu').querySelector('[role="menuitem"]')?.focus();
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
    const open = document.querySelector('[aria-haspopup="menu"][aria-expanded="true"]');
    if (open) { event.preventDefault(); closeMenus(); open.focus(); }
  }
});
window.addEventListener('popstate', () => {
  rememberView(false); clearTimeout(searchTimer);
  currentURL = location.pathname + location.search; currentKey = history.state?.webKey || crypto.randomUUID();
  if (identity) showRoute();
});
$('navigation-dialog').addEventListener('close', closeMenus);
window.addEventListener('scroll', () => {
  if (identity && isTimeline()) history.replaceState({ ...history.state, scroll: window.scrollY, pages: pageCount }, '');
}, { passive: true });
window.addEventListener('pagehide', () => { invalidate(); hidePrivate(); });
window.addEventListener('pageshow', event => { if (event.persisted) restore(); });
window.addEventListener('focus', refreshSidebarOnFocus);
document.addEventListener('visibilitychange', refreshSidebarOnFocus);
initI18n();
restore();
