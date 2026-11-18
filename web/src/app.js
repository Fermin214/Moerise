import { l, setText, setAttr, resolveText, locale, monthLabel, weekdays, initI18n } from './i18n.js';
import { Draft, readAttachments } from './draft.js';
import { errorMessage as errorText } from './errors.js';
import { pages, notePath, filters, timelineURL, listQuery, enhancedSearch, searchBody, safeFrom, detailURL, noteID, attachmentURL, rasterImage } from './routes.js';
import { el, button } from './dom.js';
import { validNote, compareNotes, renderNoteCard, renderReadingCard } from './notes.js';
import { createClient, validIdentity } from './client.js';
import { bindMenu, closeMenus } from './menus.js';
import { bindSearchInput } from './search-input.js';
import { createLeaveGuard } from './leave-guard.js';
import { createRouter } from './router.js';
import { clearMedia } from './media.js';
import { initShell } from './shell.js';

const $ = id => document.getElementById(id);
const shell = initShell();
const draft = new Draft(), snapshots = new Map();
const editors = new Map(), pendingWrites = new Map(), failures = new Map();
const noteSizes = new Map();
const measuredCards = new Map();
let cardMeasureFrame = null;
const cardObserver = new ResizeObserver(entries => {
  for (const entry of entries) {
    const card = entry.target;
    if (!card.isConnected || card.skipped) continue;
    const height = entry.borderBoxSize?.[0]?.blockSize || card.getBoundingClientRect().height;
    const width = entry.borderBoxSize?.[0]?.inlineSize || card.clientWidth;
    const key = `${card.dataset.name}:${card.dataset.sourceHash}:${Math.round(width)}`;
    noteSizes.delete(key); noteSizes.set(key, height);
    measuredCards.set(card, height);
  }
  while (noteSizes.size > 512) noteSizes.delete(noteSizes.keys().next().value);
  if (cardMeasureFrame === null) cardMeasureFrame = requestAnimationFrame(() => {
    cardMeasureFrame = null;
    for (const [card, height] of measuredCards) {
      if (!card.isConnected) continue;
      const size = `auto ${height}px`;
      if (card.style.containIntrinsicBlockSize !== size) card.style.containIntrinsicBlockSize = size;
      if ([...card.parentElement.children].indexOf(card) >= 6 && card.getBoundingClientRect().top >= innerHeight) card.classList.add('timeline-deferred');
    }
    measuredCards.clear();
  });
});
const sidebarTags = new Map(), sidebarCalendars = new Map();
let sidebarTagState = '', sidebarCalendarKey = '';
let sidebarFocusTimer = null, sidebarFocusRefresh = null;
let epoch = 0, viewSequence = 0, listSequence = 0, sidebarSequence = 0;
let identity = null, logoutCSRF = null, authBusy = false, saving = false, readingFiles = false, listing = false;
let draftOwner = null;
let items = [], nextToken = '', listController = null, retryReset = true;
let retryTimer = null, retryDelay = 1000;
let month = '', routeReturnKey = null;
let pageCount = 0, reloading = false, moduleReload = false;
let searchStatus = '';
let lastTimeline = { path: '/', values: filters('') };
let settingsPage = null;
let insightsPage = null;
let mapPage = null, mapView = null, mapSnapshot = null;
let discoveryPage = null, discovery = null;
const client = createClient({
  generation: () => epoch, session: () => identity, logoutToken: () => logoutCSRF,
  onSession: value => { if (identity) { identity = value; updateStargazing(); } else logoutCSRF = value.csrf_token; },
});
const { request, sessionRequest } = client;
const searchInput = bindSearchInput($('q'), query => sidebarFilter({ query }));
const discoveryCard = (note, review = false, className) => renderReadingCard(note, { review, className, formatTime, openNote });
const pageDirty = () => settingsPage?.hasChanges() || insightsPage?.hasChanges();
const leaveGuard = createLeaveGuard(() => !!identity && (pageDirty() || draft.locked || readingFiles ||
  !!draft.content.trim() || !!draft.attachments.length || [...editors.values()].some(editor => editor.hasChanges())));
const router = createRouter({
  isDirty: pageDirty, confirmLeave: () => leaveGuard.confirm(),
  beforeNavigate: (writeHistory = true) => { shell.closeNavigation(); searchInput.cancel(); rememberView(writeHistory); },
  onChange: kind => { if (identity) showRoute({ focus: kind !== 'filter', returning: kind === 'return' }); },
});

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
  client.cancel();
  clearTimeout(retryTimer);
  searchInput.cancel();
  retryTimer = listController = null;
  listing = saving = readingFiles = false;
  return epoch;
}
function updateStargazing(enabled = identity?.stargazing_enabled !== false) {
  if (identity) identity.stargazing_enabled = enabled;
  document.querySelectorAll('a[data-route][href="/map"]').forEach(link => { link.hidden = !enabled; });
  if (!enabled) mapView = mapSnapshot = null;
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
  cardObserver.disconnect(); noteSizes.clear(); measuredCards.clear(); clearMedia();
  items = []; nextToken = '';
  searchStatus = ''; $('search-mode').value = 'keyword';
  snapshots.clear();
  discovery?.clear();
  mapView = mapSnapshot = null;
  month = ''; identity = null; routeReturnKey = null;
  pageCount = 0; reloading = false; lastTimeline = { path: '/', values: filters('') };
  closeMenus(); leaveGuard.update();
}
function authControls({ busy = false, login = true } = {}) {
  authBusy = busy;
  $('auth').setAttribute('aria-busy', String(busy));
  $('login-form').hidden = !login;
  for (const id of ['username', 'password', 'login']) $(id).disabled = busy;
}
function expire(err) {
  leaveGuard.cancel(); router.cancel();
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
  const source = notePath.test(location.pathname) ? safeFrom(new URLSearchParams(location.search).get('from')) : router.url;
  const url = new URL(source, location.origin);
  if (!['/', '/archived'].includes(url.pathname)) return lastTimeline;
  return { path: url.pathname === '/archived' ? '/archived' : '/', values: filters(url.search) };
}
function sidebarFilter(change) {
  const source = sourceTimeline();
  navigate(timelineURL(source.path, { ...source.values, query: $('q').value, ...change }));
}
function rememberView(writeHistory = true, clean = false) {
  if (writeHistory && identity && (mapPage || notePath.test(location.pathname))) {
    const state = mapPage ? { mapView } : { returnKey: routeReturnKey };
    history.replaceState({ ...history.state, ...state }, '');
  }
  if (!identity || !pageCount || !['/', '/archived'].includes(new URL(router.url, location.origin).pathname)) return;
  const previous = snapshots.get(router.key);
  snapshots.delete(router.key);
  const focusedName = document.activeElement?.closest('article[data-name]')?.dataset.name || previous?.focusedName;
  snapshots.set(router.key, { url: router.url, items: [...items], nextToken, searchStatus, scroll: window.scrollY,
    pages: pageCount, focusedName, dirty: clean ? false : previous?.dirty });
  if (writeHistory) history.replaceState({ ...history.state, scroll: window.scrollY, pages: pageCount }, '');
  if (snapshots.size > 12) snapshots.delete(snapshots.keys().next().value);
}
function timelineSnapshot() {
  const saved = snapshots.get(router.key);
  if (saved?.url === router.url) return saved;
  return [...snapshots.values()].reverse().find(value => value.url === router.url && !value.dirty);
}
function navigate(url, options) { return router.navigate(url, options); }
function openNote(note) { navigate(detailURL(note.name, router.url), { returnKey: router.key }); }
function restoreScroll(scroll) {
  const sequence = viewSequence;
  requestAnimationFrame(() => { if (sequence === viewSequence && identity) window.scrollTo(0, scroll); });
}
function dirtySnapshots() {
  for (const snapshot of snapshots.values()) snapshot.dirty = true;
  discovery?.invalidate(); mapSnapshot = null;
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
  if (identity && !(await leaveGuard.confirm())) return;
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
function renderNote(note, detail = false) {
  const editor = editors.get(note.name);
  if (editor) return editor.form;
  const actions = [
    [l("编辑"), () => startEdit(note), ''],
    ...(!detail ? [[l("打开笔记"), () => openNote(note), '']] : []),
    [note.state === 'ARCHIVED' ? l("恢复到主页") : l("归档"), () => changeState(note), ''],
    ...(note.state === 'NORMAL' ? [[l("从这条漫步"), () => startFromNote(note), '']] : []),
    [l("洞察这条"), () => navigate(`/insights?${new URLSearchParams({ note: note.name })}`), ''],
    [l("删除"), () => confirmDelete(card, note), 'dg'],
  ];
  const card = renderNoteCard(note, {
    detail, formatTime, href: detailURL(note.name, router.url), actions,
    pending: pendingWrites.has(note.name),
    onTag: tag => {
      const source = sourceTimeline();
      sidebarFilter({ tags: [...new Set([...source.values.tags, tag])] });
    },
  });
  const failure = failures.get(note.name);
  if (failure) appendNoteError(card, note, failure);
  return card;
}
function renderList() {
  const list = $('notes'), names = new Set(items.map(note => note.name));
  cardObserver.disconnect();
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
  if (isTimeline()) {
    const width = Math.round(list.getBoundingClientRect().width);
    [...list.children].forEach((card, index) => {
      if (card.matches('.editor')) return;
      const height = noteSizes.get(`${card.dataset.name}:${card.dataset.sourceHash}:${width}`);
      if (index >= 6 && height && card.getBoundingClientRect().top >= innerHeight) {
        card.style.containIntrinsicBlockSize = `auto ${height}px`; card.classList.add('timeline-deferred');
      }
      if (!card.dataset.visibilityBound) {
        card.dataset.visibilityBound = '';
        card.addEventListener('contentvisibilityautostatechange', event => { card.skipped = event.skipped; });
      }
      cardObserver.observe(card);
    });
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
    replaceCard(note); leaveGuard.update();
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
    leaveGuard.update();
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
    hasChanges: () => model.locked || reading || model.content !== note.content || !!model.attachments.length || !!removed.size,
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
async function showRoute({ focus = false, returning = false } = {}) {
  if (!identity) return;
  shell.closeNavigation();
  settingsPage?.destroy(); settingsPage = null;
  insightsPage?.destroy(); insightsPage = null;
  mapPage?.destroy(); mapPage = null;
  discoveryPage?.destroy(); discoveryPage = null;
  cardObserver.disconnect();
  const generation = epoch, sequence = ++viewSequence;
  $('main').dataset.page = location.pathname.slice(1) || 'home';
  listSequence++; listController?.abort(); clearTimeout(retryTimer); pageObserver.disconnect();
  listing = reloading = moduleReload = false; items = []; nextToken = ''; pageCount = 0; searchStatus = '';
  $('notes').replaceChildren(); closeMenus();
  $('notes').hidden = $('page-end').hidden = ['/settings', '/review', '/walk', '/insights', '/map'].includes(location.pathname);
  for (const id of ['back', 'view-title', 'capability', 'empty', 'list-retry', 'filters', 'load-more']) $(id).hidden = true;
  showError($('list-error'), null); $('composer').hidden = location.pathname !== '/';
  const detail = notePath.exec(location.pathname);
  const title = detail ? l("笔记") : pages.get(location.pathname) || l("页面不存在");
  setText(document.querySelector('title'), () => `${resolveText(title)} · Moerise`);
  setAttr($('main'), 'aria-label', title);
  $('view-title').classList.toggle('sr', location.pathname === '/' || !!detail);
  if (location.pathname === '/' || detail) { setText($('view-title'), title); $('view-title').hidden = false; }
  if (focus) $('main').focus({ preventScroll: true });
  if ($('q').value !== sourceTimeline().values.query) $('q').value = sourceTimeline().values.query;
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
    if (snapshot?.url === router.url && !snapshot.dirty) {
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
    if (returning && generation === epoch && sequence === viewSequence) {
      const name = snapshot?.focusedName;
      [...$('notes').children].find(card => card.dataset.name === name)?.querySelector('.dlink')?.focus({ preventScroll: true });
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
    const module = await routeModule(() => import('./discovery.js'), generation, sequence);
    if (!module) return;
    discovery ||= module.createDiscovery({ request, sessionRequest, getIdentity: () => identity,
      localToday, formatTime, discoveryCard, navigate, startFromNote, expire });
    discoveryPage = discovery.createPage(location.pathname === '/review' ? 'review' : 'walk', {
      key: router.key, seedName: new URLSearchParams(location.search).get('name'),
    });
    await discoveryPage.mount($('workspace'));
  } else if (location.pathname === '/insights') {
    window.scrollTo(0, 0);
    const module = await routeModule(() => import('./insights.js'), generation, sequence);
    if (!module) return;
    insightsPage = module.createInsightsPage({
      sessionRequest, csrfToken: () => identity?.csrf_token, handleFailure, openNote,
      navigate: async (url, options) => {
        if (url === router.url) { if (await leaveGuard.confirm(pageDirty())) showRoute({ focus: true }); }
        else navigate(url, options);
      },
      closeMenus,
      renewCSRF: async () => { identity = validIdentity(await request('/session')); updateStargazing(); },
      replaceURL: url => router.replaceURL(url),
      today: localToday, formatTime,
      onDirtyChange: () => leaveGuard.update(),
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
      sessionRequest, handleFailure, onDirtyChange: () => leaveGuard.update(),
      confirmDiscard: dirty => leaveGuard.confirm(dirty),
      onSaved: data => {
        identity.timezone = data.settings.timezone;
        updateStargazing(data.settings.stargazing.enabled);
        discovery?.invalidate({ settings: true });
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
  leaveGuard.update();
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
$('search-mode').addEventListener('change', () => {
  searchInput.cancel();
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
bindMenu($('user-menu'), $('account-menu'));
document.addEventListener('click', event => {
  const link = event.target.closest('a[data-route]');
  if (link && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey && event.button === 0) {
    event.preventDefault();
    if (identity) {
      const url = new URL(link.href), isDetail = notePath.test(url.pathname);
      navigate(url.pathname + url.search, { returnKey: isDetail ? router.key : null });
    }
  }
});
$('navigation-dialog').addEventListener('close', closeMenus);
let scrollTimer = null;
window.addEventListener('scroll', () => {
  if (scrollTimer !== null || !identity || !isTimeline()) return;
  const key = router.key;
  scrollTimer = setTimeout(() => {
    scrollTimer = null;
    if (identity && isTimeline() && key === router.key) {
      history.replaceState({ ...history.state, scroll: window.scrollY, pages: pageCount }, '');
    }
  }, 150);
}, { passive: true });
window.addEventListener('pagehide', () => { invalidate(); hidePrivate(); });
window.addEventListener('pageshow', event => { if (event.persisted) restore(); });
window.addEventListener('focus', refreshSidebarOnFocus);
document.addEventListener('visibilitychange', refreshSidebarOnFocus);
initI18n();
restore();
