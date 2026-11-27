import { l, setText, setAttr, resolveText, locale, monthLabel, weekdays, initI18n, localize } from './i18n.js';
import { Draft, readAttachments } from './draft.js';
import { errorMessage as errorText } from './errors.js';
import { pages, notePath, filters, timelineURL, safeFrom, detailURL, noteID, attachmentURL, rasterImage } from './routes.js';
import { el, button } from './dom.js';
import { validNote, observeFoldedBodies, renderNoteCard, renderReadingCard } from './notes.js';
import { createTimeline, readTimeline, storeTimelineSnapshot } from './timeline.js';
import { createClient, validIdentity, sessionOwner } from './client.js';
import { closeMenus } from './menus.js';
import { bindSearchInput } from './search-input.js';
import { createLeaveGuard } from './leave-guard.js';
import { createRouter } from './router.js';
import { clearMedia } from './media.js';
import { initShell } from './shell.js';
import { connectPrivacy, disconnectPrivacy, diagnose, resultOf, usage } from './privacy.js';
import { syncStatus } from './sync-status.js';
import { dismissSnackbar, snackbar } from './snackbar.js';
import { animateMotion, motionPositions, moveFrom } from './motion.js';
import { serviceDate, nextServiceDay, timelineTime } from './timeline-time.js';
import { initTheme } from './theme.js';

const $ = id => document.getElementById(id);
const logoutKey = 'moerise.logout-pending';
let logoutCSRF = null;
try { logoutCSRF = sessionStorage.getItem(logoutKey); } catch {}
function setLogoutCSRF(token) {
  logoutCSRF = token;
  try {
    if (token) sessionStorage.setItem(logoutKey, token);
    else sessionStorage.removeItem(logoutKey);
  } catch {}
}
const shell = initShell(loadSidebar);
const draft = new Draft(), snapshots = new Map();
const editors = new Map(), pendingWrites = new Map(), failures = new Map();
const refreshSync = Symbol('refresh');
const noteSizes = new Map();
const expandedNotes = new Set();
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
const sidebarTags = new Map(), sidebarCalendars = new Map(), sidebarCommits = new Map();
let preloadController = null, preloadTimer = null, serviceDay = '', serviceDayTimer = null;
let sidebarTagState = '', sidebarCalendarKey = '';
let sidebarFocusTimer = null, sidebarFocusRefresh = null;
let epoch = 0, viewSequence = 0, sidebarTagSequence = 0, sidebarCalendarSequence = 0;
let identity = null, authBusy = false, saving = false, readingFiles = false;
let loginAutofillTimer;
let draftOwner = null;
let timeline = null;
let month = '', routeReturnKey = null;
let monthDirection = 0, lastSyncAt = null;
let moduleReload = false;
let lastTimeline = { key: null, url: '/', path: '/', values: filters('') };
let projectionRevision = 0, timelineRefresh = null;
let settingsPage = null, sessionSettings = null;
let settingsAccount = null;
let mapPage = null, mapView = null, mapSnapshot = null;
let discoveryPage = null, discovery = null;
const client = createClient({
  generation: () => epoch, session: () => identity,
  onSession: value => {
    const timezoneChanged = identity.timezone !== value.timezone;
    identity = value; updateStargazing();
    if (timezoneChanged) {
      refreshServiceDay(true); month = sidebarDate().slice(0, 7) || serviceDay.slice(0, 7);
      dirtySnapshots(); invalidateSidebar(); loadSidebar();
    }
  },
  onOwnerChange: () => expire({ code: 'SESSION_UNAUTHORIZED' }, false),
});
const { request, sessionRequest } = client;
let capabilitiesRequest;
function readCapabilities() {
  if (!capabilitiesRequest) {
    const pending = request('/capabilities').catch(error => {
      if (capabilitiesRequest === pending) capabilitiesRequest = null;
      throw error;
    });
    capabilitiesRequest = pending;
  }
  return capabilitiesRequest;
}
const searchInput = bindSearchInput($('q'), query => sidebarFilter({ query }));
const discoveryCard = (note, review = false, className, bottomTags = true) => renderReadingCard(note, {
  review, className, formatTime, formatDate, openNote, bottomTags,
  onTag: tag => navigate(timelineURL('/', { tags: [tag] })),
});
const pageDirty = () => settingsPage?.hasChanges();
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
  epoch++; viewSequence++;
  syncStatus.reset();
  dismissSnackbar(); lastSyncAt = null;
  capabilitiesRequest = null;
  stopPreload();
  timeline?.destroy(); timeline = null;
  sessionSettings?.dispose(); settingsPage = sessionSettings = null;
  settingsAccount?.remove(); settingsAccount = null;
  mapPage?.destroy(); mapPage = null;
  discoveryPage?.destroy(); discoveryPage = null;
  invalidateSidebar();
  client.cancel();
  searchInput.cancel();
  saving = readingFiles = false;
  return epoch;
}
function updateStargazing(enabled = identity?.stargazing_enabled !== false) {
  if (identity) identity.stargazing_enabled = enabled;
  document.querySelectorAll('a[data-route][href="/map"]').forEach(link => { link.hidden = !enabled; });
  if (enabled) return;
  mapView = mapSnapshot = null;
  if (identity && location.pathname === '/map') {
    mapPage?.destroy(); mapPage = null;
    if (history.state?.mapView || history.state?.mapReader) {
      history.replaceState({ ...history.state, mapView: null, mapReader: null }, '');
    }
    setText($('capability'), l("观星已关闭。")); $('capability').hidden = false;
  }
}
function clearDrafts() {
  for (const editor of editors.values()) editor.draft.clear();
  editors.clear(); pendingWrites.clear(); failures.clear(); draft.clear();
  draftOwner = null;
}
function hidePrivate({ resume = false } = {}) {
  shell.reset();
  if (resume) {
    draftOwner ||= sessionOwner(identity);
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
  cardObserver.disconnect(); observeFoldedBodies($('notes')); noteSizes.clear(); expandedNotes.clear(); measuredCards.clear(); clearMedia();
  $('search-mode').value = 'keyword';
  snapshots.clear();
  discovery?.clear();
  mapView = mapSnapshot = null;
  clearTimeout(serviceDayTimer);
  month = serviceDay = ''; identity = null; routeReturnKey = null;
  lastTimeline = { key: null, url: '/', path: '/', values: filters('') };
  closeMenus(); leaveGuard.update();
}
function loginReady() { return !!$('username').value.trim() && !!$('password').value; }
function updateLoginButton() { $('login').disabled = authBusy || !loginReady(); }
function monitorLoginAutofill() {
  clearInterval(loginAutofillTimer);
  updateLoginButton();
  if (!authBusy && !$('auth').hidden && !$('login-form').hidden && !document.hidden) {
    loginAutofillTimer = setInterval(updateLoginButton, 150);
  }
}
function authControls({ busy = false, login = true } = {}) {
  authBusy = busy;
  $('auth').setAttribute('aria-busy', String(busy));
  $('login-form').hidden = !login;
  $('retry-logout').hidden = !logoutCSRF || busy;
  for (const id of ['username', 'password']) $(id).disabled = busy;
  $('auth').querySelector('[data-language]').disabled = busy;
  monitorLoginAutofill();
}
function expire(err, resume = true) {
  disconnectPrivacy(true);
  leaveGuard.cancel(); router.cancel();
  invalidate(); hidePrivate({ resume }); authControls();
  showError($('auth-error'), err);
  $('username').focus();
}
function handleFailure(element, err) {
  if (err.name === 'AbortError') return;
  diagnose(err);
  if (err.status === 401) expire(err);
  else showError(element, err);
}
function localToday() {
  const now = Date.now();
  try { return serviceDate(identity?.timezone, now); }
  catch { return serviceDate('UTC', now); }
}
function refreshServiceDay(timezoneChanged = false) {
  clearTimeout(serviceDayTimer);
  if (!identity) return;
  const now = Date.now();
  let timezone = identity.timezone, day;
  try { day = serviceDate(timezone, now); }
  catch { timezone = 'UTC'; day = serviceDate(timezone, now); }
  const changed = serviceDay !== day;
  const previous = serviceDay;
  if (changed) {
    serviceDay = day;
    const calendar = $('calendar');
    calendar.querySelector('.today')?.classList.remove('today');
    calendar.querySelector(`[data-date="${day}"]`)?.classList.add('today');
    if (!timezoneChanged && month === previous.slice(0, 7) && month && month !== day.slice(0, 7) &&
        !sidebarDate() && !calendar.contains(document.activeElement)) {
      month = day.slice(0, 7);
      loadSidebar(null, { calendarOnly: true });
    }
  }
  if (changed || timezoneChanged) $('notes').querySelectorAll('.note-colophon time').forEach(localize);
  if (!document.hidden) serviceDayTimer = setTimeout(refreshServiceDay,
    Math.max(1, nextServiceDay(timezone, now, day) - Date.now()));
}
function formatTimelineTime(value) {
  return () => timelineTime(value, identity?.timezone, serviceDay, locale() === 'en');
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
function formatDate(value) {
  if (!Number.isFinite(Date.parse(value))) return '';
  return () => {
    try {
      const { year, month, day } = Object.fromEntries(new Intl.DateTimeFormat('en-CA-u-ca-gregory-nu-latn', {
        timeZone: identity?.timezone, year: 'numeric', month: '2-digit', day: '2-digit',
      }).formatToParts(new Date(value)).map(part => [part.type, part.value]));
      return locale() === 'en' ? `${year}-${month}-${day}` : `${year}年${Number(month)}月${Number(day)}日`;
    } catch { return ''; }
  };
}
const isTimeline = () => location.pathname === '/' || location.pathname === '/archived';
function sourceTimeline() {
  const detail = notePath.test(location.pathname);
  const source = detail ? safeFrom(new URLSearchParams(location.search).get('from')) : router.url;
  const url = new URL(source, location.origin);
  if (!['/', '/archived'].includes(url.pathname)) return lastTimeline;
  return { key: detail ? history.state?.returnKey : router.key, url: url.pathname + url.search,
    path: url.pathname, values: filters(url.search) };
}
function sidebarFilter(change) {
  const source = sourceTimeline();
  navigate(timelineURL(source.path, { ...source.values, query: $('q').value, ...change }));
}
function sidebarTagContext() {
  if (isTimeline()) return { path: location.pathname, values: filters(location.search) };
  if (notePath.test(location.pathname)) {
    const from = new URL(safeFrom(new URLSearchParams(location.search).get('from')), location.origin);
    if (from.pathname === '/' || from.pathname === '/archived') return { path: from.pathname, values: filters(from.search) };
  }
  return { path: '/', values: filters('') };
}
function setSidebarTagScope(path) {
  const state = path === '/archived' ? 'ARCHIVED' : 'NORMAL';
  if (sidebarTagState !== state) {
    sidebarTagSequence++;
    const host = $('tags');
    sidebarCommits.delete(host);
    host.replaceChildren();
    sidebarTagState = state;
  }
  return state;
}
const sidebarDate = () => location.pathname === '/' ? filters(location.search).date : '';
function recordSync() {
  lastSyncAt = Date.now();
  window.dispatchEvent(new Event('moerisesync'));
}
function rememberView(writeHistory = true) {
  if (writeHistory && identity && (mapPage || notePath.test(location.pathname))) {
    const state = mapPage ? { mapView } : { returnKey: routeReturnKey };
    history.replaceState({ ...history.state, ...state }, '');
  }
  timeline?.remember(writeHistory);
}
function navigate(url, options) { return router.navigate(url, options); }
function openNote(note) { navigate(detailURL(note.name, router.url), { returnKey: router.key }); }
function dirtySnapshots() {
  projectionRevision++;
  for (const snapshot of snapshots.values()) snapshot.dirty = true;
  discovery?.invalidate(); mapSnapshot = null;
  discoveryPage?.refresh();
  mapPage?.refresh();
}
async function enter(value, restored = false) {
  identity = validIdentity(value);
  updateStargazing();
  if (draftOwner && draftOwner !== sessionOwner(identity)) clearDrafts();
  draftOwner = null;
  $('auth').hidden = true;
  $('app').hidden = $('workspace').hidden = false;
  $('password').value = '';
  monitorLoginAutofill();
  setText($('account-name'), identity.user.display_name?.trim() || identity.user.name.replace(/^users\//, ''));
  $('account').hidden = false;
  refreshServiceDay(); month = serviceDay.slice(0, 7);
  $('content').value = draft.content;
  for (const editor of editors.values()) editor.update();
  updateComposer();
  connectPrivacy(identity, sessionRequest, restored && location.pathname === '/' ? performance.now() : null, readCapabilities);
  await showRoute();
}
async function restore() {
  if (logoutCSRF) {
    authBusy = false;
    return logout();
  }
  const generation = invalidate();
  hidePrivate(); authControls({ busy: true, login: false }); showError($('auth-error'), null);
  try {
    const value = await request('/session');
    stale(generation); authControls();
    await enter(value, true);
  } catch (err) {
    if (generation !== epoch || err.name === 'AbortError') return;
    authControls();
    showError($('auth-error'), err.status === 401 ? null : err);
  }
}
$('login-form').addEventListener('input', updateLoginButton);
$('login-form').addEventListener('change', updateLoginButton);
$('login-form').addEventListener('keydown', event => {
  if (event.key === 'Enter') updateLoginButton();
});
$('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  if (authBusy || logoutCSRF || !loginReady()) return;
  const generation = invalidate();
  authControls({ busy: true }); showError($('auth-error'), null);
  const body = { username: $('username').value.trim(), password: $('password').value };
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
  if (authBusy) return;
  const token = logoutCSRF || identity?.csrf_token;
  if (!token) return;
  setLogoutCSRF(token);
  disconnectPrivacy(true);
  history.replaceState({ ...history.state, mapView: null }, '');
  const generation = invalidate();
  hidePrivate(); authControls({ busy: true, login: false }); showError($('auth-error'), null);
  try {
    await request('/session', { method: 'DELETE', headers: { 'X-CSRF-Token': token } });
  } catch (err) {
    if (generation !== epoch) return;
    if (err.status !== 403 || err.code !== 'CSRF_REJECTED') {
      authControls({ login: false });
      showError($('auth-error'), err);
      $('retry-logout').focus();
      return;
    }
  }
  setLogoutCSRF(null); authControls(); $('username').focus();
}

function renderFilters() {
  const values = filters(location.search), box = $('filters');
  const previous = [...box.querySelectorAll('.fc')];
  box.hidden = false;
  const positions = motionPositions(previous);
  box.replaceChildren();
  const chips = [
    ...values.tags.map(tag => ({ label: `#${tag}`, remove: () => sidebarFilter({ tags: values.tags.filter(item => item !== tag) }) })),
    ...(values.date ? [{ label: values.date, remove: () => sidebarFilter({ date: '' }) }] : []),
  ];
  box.hidden = !chips.length;
  if (chips.length) box.append(el('span', '', l("筛选")));
  for (const chip of chips) {
    const span = el('span', 'fc', chip.label);
    span.dataset.name = chip.label;
    const remove = button('×', '', chip.remove);
    setAttr(remove, 'aria-label', l("移除筛选 {0}", { 0: chip.label }));
    span.append(remove); box.append(span);
  }
  if (chips.length) box.append(button(l("清除全部"), 'ghost', () => sidebarFilter({ tags: [], date: '' })));
  const next = box.querySelectorAll('.fc');
  moveFrom(next, positions);
  for (const chip of next) if (!previous.some(node => node.dataset.name === chip.dataset.name)) {
    animateMotion(chip, [{ transform: 'scale(.92)', opacity: 0 }, { transform: 'none', opacity: 1 }], 'tactile');
  }
  for (const chip of previous) {
    const position = positions.get(chip.dataset.name);
    if (!position || [...next].some(node => node.dataset.name === chip.dataset.name)) continue;
    chip.inert = true;
    Object.assign(chip.style, { position: 'fixed', inset: 'auto', top: `${position.top}px`, left: `${position.left}px`, zIndex: '11' });
    document.body.append(chip);
    animateMotion(chip, [{ transform: 'none', opacity: 1 }, { transform: 'scale(.92)', opacity: 0 }], 'tactile')
      .finished.catch(() => {}).then(() => chip.remove());
  }
}
function invalidateSidebar() {
  sidebarTagSequence++; sidebarCalendarSequence++;
  sidebarCommits.clear();
  clearTimeout(sidebarFocusTimer);
  sidebarFocusTimer = sidebarFocusRefresh = null;
  for (const cache of [sidebarTags, sidebarCalendars]) {
    for (const entry of cache.values()) entry.controller?.abort();
    cache.clear();
  }
}
function discard(cache, key) {
  cache.get(key)?.controller?.abort();
  cache.delete(key);
}
function readTags(state, owner) { return sidebarData(sidebarTags, state, `/tags?${new URLSearchParams({ state })}`, owner); }
function readCalendar(month, owner) {
  return sidebarData(sidebarCalendars, `NORMAL:${month}`, `/calendar?${new URLSearchParams({ state: 'NORMAL', month })}`, owner);
}
function sidebarChanged(before, after) {
  const dayOf = note => note && resolveText(formatTime(note.create_time)).slice(0, 10);
  const oldDay = dayOf(before), newDay = dayOf(after);
  const changedState = before?.state !== after?.state;
  const tagsOf = note => JSON.stringify([...(note?.tags || [])].sort());
  for (const [note, day] of [[before, oldDay], [after, newDay]]) {
    if (!note) continue;
    if (note.state === 'NORMAL' && (changedState || oldDay !== newDay)) discard(sidebarCalendars, `NORMAL:${day.slice(0, 7)}`);
    if ((changedState && note.tags?.length) || tagsOf(before) !== tagsOf(after)) discard(sidebarTags, note.state);
  }
  loadSidebar();
}
function sidebarData(cache, key, path, owner) {
  const cached = cache.get(key);
  if (owner?.signal?.aborted) return Promise.reject(aborted());
  if (cached && !cached.controller && Date.now() - cached.loadedAt >= 60_000) cache.delete(key);
  if (!cache.has(key)) {
    const entry = { controller: new AbortController(), owner };
    const cancel = () => { if (entry.owner === owner && cache.get(key) === entry) discard(cache, key); };
    owner?.signal?.addEventListener('abort', cancel, { once: true });
    entry.promise = request(path, { controller: entry.controller }).then(data => {
      entry.controller = null;
      entry.loadedAt = Date.now();
      return data;
    }).catch(err => {
      if (cache.get(key) === entry) cache.delete(key);
      throw err;
    }).finally(() => owner?.signal?.removeEventListener('abort', cancel));
    cache.set(key, entry);
    if (cache.size > 36) discard(cache, cache.keys().next().value);
  } else if (!owner) cache.get(key).owner = null;
  return cache.get(key).promise;
}
function stopPreload() {
  clearTimeout(preloadTimer); preloadController?.abort(); preloadController = null;
}
function schedulePreload() {
  stopPreload();
  if (!identity || !isTimeline() || document.hidden) return;
  const generation = epoch;
  preloadTimer = setTimeout(async () => {
    const owner = preloadController = new AbortController();
    try {
      await loadSidebar(owner);
      if (owner.signal.aborted || document.hidden) return;
      if (owner.signal.aborted || document.hidden || mapSnapshot || identity?.stargazing_enabled === false) return;
      const started = performance.now();
      const snapshot = await request('/map', { controller: owner, maxBytes: 8 * 1024 * 1024 }).then(value => {
        usage('stars', 'success', performance.now() - started, 'prefetch'); return value;
      }, error => { usage('stars', resultOf(error), performance.now() - started, 'prefetch'); throw error; });
      if (!owner.signal.aborted && generation === epoch && identity?.stargazing_enabled !== false &&
          ['ready', 'empty'].includes(snapshot?.status)) mapSnapshot = snapshot;
    } catch (error) { if (generation === epoch && error.status === 401) expire(error); }
    finally { if (preloadController === owner) preloadController = null; }
  }, 250);
}
function refreshSidebarOnFocus() {
  refreshServiceDay();
  if (document.hidden) { stopPreload(); return; }
  if (!identity || sidebarFocusRefresh) return;
  clearTimeout(sidebarFocusTimer);
  sidebarFocusTimer = setTimeout(() => {
    sidebarFocusTimer = null;
    if (!identity || document.visibilityState !== 'visible') return;
    const pending = loadSidebar(null, { preserveFocus: true });
    sidebarFocusRefresh = pending;
    pending.finally(() => { if (sidebarFocusRefresh === pending) sidebarFocusRefresh = null; });
  }, 100);
}
function commitSidebar(host, preserveFocus, current, render) {
  const commit = () => { if (current()) render(); };
  if (preserveFocus && host.contains(document.activeElement)) sidebarCommits.set(host, commit);
  else {
    sidebarCommits.delete(host);
    commit();
  }
}
async function loadSidebar(owner, { calendarOnly = false, preserveFocus = false } = {}) {
  if (!(owner instanceof AbortController)) owner = null;
  if (!identity) return;
  const generation = epoch;
  const { path } = sidebarTagContext();
  const state = setSidebarTagScope(path), selectedMonth = month;
  const calendarKey = `NORMAL:${selectedMonth}`;
  const tagsHost = $('tags'), calendarHost = $('calendar');
  calendarOnly = calendarOnly && tagsHost.hasChildNodes();
  const tagSequence = calendarOnly ? null : ++sidebarTagSequence;
  const calendarSequence = ++sidebarCalendarSequence;
  const contextCurrent = () => generation === epoch && sidebarTagContext().path === path;
  const tagsCurrent = () => tagSequence === sidebarTagSequence && contextCurrent();
  const calendarCurrent = () => calendarSequence === sidebarCalendarSequence && selectedMonth === month && generation === epoch;
  showError($('sidebar-error'), null); $('sidebar-retry').hidden = true;
  if (sidebarCalendarKey !== calendarKey && (!preserveFocus || !calendarHost.contains(document.activeElement))) {
    renderCalendar([], sidebarDate(), true);
    sidebarCalendarKey = calendarKey;
  }
  async function receive(read, current, publish) {
    if (!current()) return;
    try {
      const data = await read();
      if (current()) publish(data);
    } catch (err) {
      if (!current() || err.name === 'AbortError') return;
      handleFailure($('sidebar-error'), err);
      if (identity) $('sidebar-retry').hidden = false;
    }
  }
  const invalidData = () => Object.assign(new Error('Invalid sidebar data'), { code: 'NOTE_LIST_INVALID' });
  const tags = () => receive(() => readTags(state, owner), tagsCurrent, data => {
    if (!Array.isArray(data?.tags)) { sidebarTags.delete(state); throw invalidData(); }
    commitSidebar(tagsHost, preserveFocus, tagsCurrent, () => renderTags(data.tags, sidebarTagContext().values.tags));
  });
  const calendar = () => receive(() => readCalendar(selectedMonth, owner), calendarCurrent, data => {
    if (!Array.isArray(data?.days) || data.month !== selectedMonth) { sidebarCalendars.delete(calendarKey); throw invalidData(); }
    commitSidebar(calendarHost, preserveFocus, calendarCurrent, () => {
      renderCalendar(data.days, sidebarDate());
      sidebarCalendarKey = calendarKey;
    });
  });
  if (calendarOnly) await calendar();
  else if (owner) {
    await tags();
    await calendar();
  } else {
    await Promise.all([tags(), calendar()]);
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
      const { path, values } = sidebarTagContext();
      const tags = values.tags.includes(item.tag) ? values.tags.filter(tag => tag !== item.tag) : [...values.tags, item.tag];
      navigate(timelineURL(path, { ...values, query: isTimeline() ? $('q').value : values.query, tags }));
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
  const today = serviceDay;
  for (let day = 1; day <= size; day++) {
    const date = `${month}-${String(day).padStart(2, '0')}`, count = counts.get(date) || 0;
    const cell = button(String(day), [selected === date ? 'sel' : count > 1 ? 'h2' : count ? 'h1' : '', date === today ? 'today' : ''].join(' '),
      () => navigate(timelineURL('/', location.pathname === '/'
        ? { ...filters(location.search), query: $('q').value, date: sidebarDate() === date ? '' : date }
        : { date })));
    cell.disabled = disabled;
    cell.dataset.date = date;
    setAttr(cell, 'aria-label', l("{0}，{1} 条笔记", { 0: date, 1: count }));
    cell.setAttribute('aria-pressed', String(selected === date));
    calendar.append(cell);
  }
  if (monthDirection) {
    animateMotion(calendar, [{ transform: `translateX(${monthDirection * 12}px)`, opacity: .45 }, { transform: 'none', opacity: 1 }]);
    monthDirection = 0;
  }
}
function changeMonth(delta) {
  const [year, number] = month.split('-').map(Number), date = new Date(year, number - 1 + delta, 1);
  if (date.getFullYear() < 1000 || date.getFullYear() > 9999) return;
  if (sidebarCalendars.get(sidebarCalendarKey)?.controller) discard(sidebarCalendars, sidebarCalendarKey);
  month = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}`;
  monthDirection = delta;
  loadSidebar();
}
function renderNote(note, detail = false) {
  const editor = editors.get(note.name);
  if (editor) return editor.form;
  const actions = [
    [l("编辑"), () => startEdit(note), ''],
    ...(!detail ? [[l("打开笔记"), () => openNote(note), '']] : []),
    [note.state === 'ARCHIVED' ? l("恢复") : l("归档"), () => changeState(note), ''],
    ...(note.state === 'NORMAL' ? [[l("从这条漫步"), () => startFromNote(note), '']] : []),
    [l("删除"), () => confirmDelete(card, note), 'dg'],
  ];
  const card = renderNoteCard(note, {
    detail, formatTime, formatTimelineTime, href: detailURL(note.name, router.url), actions,
    pending: pendingWrites.has(note.name),
    expandedNotes: !detail && !sourceTimeline().values.query.trim() && !pendingWrites.has(note.name) ? expandedNotes : null,
    onTag: tag => {
      const source = sourceTimeline();
      sidebarFilter({ tags: [...new Set([...source.values.tags, tag])] });
    },
  });
  const failure = failures.get(note.name);
  if (failure) appendNoteError(card, note, failure);
  return card;
}
function renderList(items, entering) {
  const list = $('notes'), names = new Set(items.map(note => note.name));
  const positions = entering ? motionPositions(list.children) : null;
  cardObserver.disconnect();
  const focusedEditor = list.contains(document.activeElement) ? document.activeElement.closest('form.editor') : null;
  const existing = new Map([...list.children].map(node => [node.dataset.name, node]));
  for (const [name, node] of existing) if (!names.has(name)) node.remove();
  let cursor = list.firstElementChild;
  for (const note of items) {
    const previous = existing.get(note.name);
    const node = editors.get(note.name)?.form ||
      (previous?.matches('article') && previous.dataset.sourceHash === note.source_hash ? previous : renderNote(note, notePath.test(location.pathname)));
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
      if (!card.hasAttribute('data-visibility-bound')) {
        card.dataset.visibilityBound = '';
        card.addEventListener('contentvisibilityautostatechange', event => { card.skipped = event.skipped; });
      }
      cardObserver.observe(card);
    });
  }
  observeFoldedBodies(list);
  if (positions) {
    moveFrom(list.children, positions);
    const created = [...list.children].find(node => node.dataset.name === entering);
    if (created && !positions.has(entering)) animateMotion(created, [
      { transform: 'translateY(-18px) scale(.985)', opacity: .4 }, { transform: 'none', opacity: 1 },
    ], 'spatial');
  }
}
function replaceCard(note) {
  const card = [...$('notes').children].find(node => node.dataset.name === note.name);
  if (card) {
    const next = renderNote(note, notePath.test(location.pathname));
    if (card === next) return;
    card.replaceWith(next);
    observeFoldedBodies($('notes'));
    if (card.matches('.editor') !== next.matches('.editor')) animateMotion(next, [
      { transform: 'scale(.99)', opacity: .7 }, { transform: 'none', opacity: 1 },
    ]);
  }
}
function appendNoteError(card, note, err) {
  const box = el('div', 'note-error'), message = el('p', 'error');
  message.setAttribute('role', 'alert'); showError(message, err); box.append(message);
  if (pendingWrites.has(note.name)) box.append(button(l("重试原提交"), 'ghost', () => performWrite(note)));
  box.append(button(l("刷新笔记"), 'ghost', () => refreshNote(note))); card.append(box);
}
async function refreshNote(note) {
  const generation = epoch, sequence = viewSequence;
  const operation = pendingWrites.get(note.name);
  try {
    const updated = await request(`/notes/${noteID(note.name)}`);
    stale(generation);
    if (sequence !== viewSequence || operation !== pendingWrites.get(note.name) || operation?.busy) return;
    if (!validNote(updated)) throw Object.assign(new Error('Invalid note'), { code: 'NOTE_LIST_INVALID' });
    editors.get(note.name)?.draft.clear(); editors.delete(note.name); pendingWrites.delete(note.name); failures.delete(note.name);
    syncStatus.clear(note.name);
    replaceCard(updated);
    dirtySnapshots();
    if (timeline) await timeline.update(note.name, updated);
    else renderList([updated]);
    sidebarChanged(note, updated);
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
  animateMotion(confirm, [{ transform: 'scaleY(.92)', opacity: 0 }, { transform: 'none', opacity: 1 }]);
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
  const started = performance.now();
  const transfer = syncStatus.start(note.name, { write: true });
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
    transfer('success');
    if (operation.body.state === 'ARCHIVED' && updated.state === 'ARCHIVED') snackbar(l("笔记已归档。"), {
      label: l("撤销"),
      action: () => {
        if (generation !== epoch || pendingWrites.has(note.name)) return;
        changeState(updated);
      },
    });
    else if (operation.body.state === 'NORMAL') snackbar(l("笔记已恢复。"));
    if (editor) { usage('edit', 'success'); usage('save_ack', 'success', performance.now() - started); }
    editor?.draft.clear(); editors.delete(note.name); pendingWrites.delete(note.name); failures.delete(note.name); dirtySnapshots();
    if (timeline) await timeline.update(note.name, updated);
    else if (location.pathname === `/notes/${noteID(note.name)}`) {
      if (operation.method === 'DELETE') navigate(safeFrom(new URLSearchParams(location.search).get('from')));
      else renderList([updated]);
    }
    sidebarChanged(note, updated);
  } catch (err) {
    transfer(resultOf(err));
    if (editor && generation === epoch) { usage('edit', resultOf(err)); usage('save_ack', resultOf(err), performance.now() - started); }
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
  const cancel = button(l("取消"), 'secondary', () => {
    if (pendingWrites.get(note.name)?.busy) return;
    readSequence++; model.clear(); editors.delete(note.name);
    if (!pendingWrites.has(note.name)) {
      failures.delete(note.name);
      syncStatus.clear(note.name);
    }
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
  setSidebarTagScope(sidebarTagContext().path);
  timelineRefresh?.abort();
  stopPreload();
  shell.closeNavigation();
  timeline?.destroy(); timeline = null;
  settingsPage?.destroy(); settingsPage = null;
  settingsAccount?.remove(); settingsAccount = null;
  mapPage?.destroy(); mapPage = null;
  discoveryPage?.destroy(); discoveryPage = null;
  cardObserver.disconnect();
  const generation = epoch, sequence = ++viewSequence;
  $('main').dataset.page = location.pathname.slice(1) || 'home';
  moduleReload = false;
  $('notes').setAttribute('aria-busy', 'false');
  $('notes').replaceChildren(); closeMenus();
  observeFoldedBodies($('notes'));
  $('notes').hidden = $('page-end').hidden = ['/settings', '/review', '/walk', '/map'].includes(location.pathname);
  for (const id of ['back', 'view-title', 'capability', 'empty', 'list-retry', 'filters', 'load-more']) $(id).hidden = true;
  showError($('list-error'), null); $('composer').hidden = location.pathname !== '/';
  const detail = notePath.exec(location.pathname);
  const title = detail ? l("笔记") : pages.get(location.pathname) || l("页面不存在");
  setText(document.querySelector('title'), () => `${resolveText(title)} - Moerise`);
  setAttr($('main'), 'aria-label', title);
  $('view-title').classList.toggle('sr', location.pathname === '/' || !!detail);
  if (location.pathname === '/' || detail) { setText($('view-title'), title); $('view-title').hidden = false; }
  if (focus) $('main').focus({ preventScroll: true });
  if ($('q').value !== sourceTimeline().values.query) $('q').value = sourceTimeline().values.query;
  $('search-mode').value = sourceTimeline().values.mode;
  lastTimeline = sourceTimeline();
  if (sidebarDate()) month = sidebarDate().slice(0, 7);
  document.querySelectorAll('[data-route]').forEach(link => {
    const active = link.getAttribute('href') === location.pathname; link.classList.toggle('on', active);
    if (active) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');
  });
  if (!isTimeline()) loadSidebar();
  if (isTimeline()) {
    if (location.pathname === '/archived') { $('view-title').hidden = false; setText($('view-title'), l("归档")); }
    renderFilters();
    timeline = createTimeline({ key: router.key, url: router.url, snapshots, request, sessionRequest,
      render: renderList, showError, handleFailure, onSynced: recordSync });
    await timeline.open(returning);
    if (sequence === viewSequence) schedulePreload();
  } else if (detail) {
    const from = safeFrom(new URLSearchParams(location.search).get('from'));
    const label = pages.get(new URL(from, location.origin).pathname) || l("主页");
    routeReturnKey = history.state?.returnKey || null;
    setText($('back'), l("‹ 返回{0}", { 0: label })); $('back').hidden = false; window.scrollTo(0, 0);
    try {
      const note = await request(`/notes/${detail[1]}`);
      if (generation !== epoch || sequence !== viewSequence) return;
      if (!validNote(note)) throw Object.assign(new Error('Invalid note'), { code: 'NOTE_LIST_INVALID' });
      renderList([note]);
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
      localToday, formatDate, discoveryCard, startFromNote, expire });
    discoveryPage = discovery.createPage(location.pathname === '/review' ? 'review' : 'walk', {
      key: router.key, seedName: new URLSearchParams(location.search).get('name'),
    });
    await discoveryPage.mount($('workspace'));
  } else if (location.pathname === '/map') {
    setText($('view-title'), l("观星")); $('view-title').hidden = false;
    window.scrollTo(0, 0);
    if (identity.stargazing_enabled === false) {
      setText($('capability'), l("观星已关闭。")); $('capability').hidden = false;
      return;
    }
    const module = await routeModule(() => import('./map.js'), generation, sequence);
    if (!module || identity.stargazing_enabled === false) return;
    const owner = sessionOwner(identity);
    const saved = history.state?.mapView?.owner === owner ? history.state.mapView :
      mapView?.owner === owner ? mapView : null;
    mapPage = module.createMapPage({
      request: sessionRequest, handleFailure, validNote, renderNote: note => discoveryCard(note, false, 'card map-note', false),
      initialView: saved, pushLayer: router.pushLayer,
      initialData: mapSnapshot,
      onData: value => {
        if (identity && generation === epoch && sequence === viewSequence) {
          mapSnapshot = value;
          if (value.disabled_reason === 'stargazing') updateStargazing(false);
        }
      },
      onViewChange: value => {
        if (!identity || generation !== epoch || sequence !== viewSequence) return;
        mapView = { ...value, owner };
        history.replaceState({ ...history.state, mapView }, '');
      },
    });
    await mapPage.mount($('workspace'));
  } else if (location.pathname === '/settings') {
    setText($('view-title'), l("设置")); $('view-title').hidden = false;
    window.scrollTo(0, 0);
    const account = settingsAccount = el('section', 'card settings-card settings-account');
    account.id = 'settings-account';
    const signOut = button(l("退出登录"), 'secondary', logout); signOut.id = 'logout';
    account.append(el('span', '', $('account-name').textContent), signOut);
    $('workspace').append(account);
    const module = await routeModule(() => import('./settings.js'), generation, sequence);
    if (!module) return;
    settingsPage = sessionSettings ||= module.createSettingsPage({
      sessionRequest, handleFailure, onDirtyChange: () => leaveGuard.update(),
      confirmDiscard: dirty => leaveGuard.confirm(dirty),
      sync: synchronize, lastSync: () => lastSyncAt,
      onSaved: (data, previous, body) => {
        const before = previous.settings;
        identity.timezone = data.settings.timezone;
        updateStargazing(data.settings.stargazing.enabled);
        if (body?.secrets.embedding_api_key || JSON.stringify(before.embedding) !== JSON.stringify(data.settings.embedding) ||
            JSON.stringify(previous.secrets.embedding_api_key) !== JSON.stringify(data.secrets.embedding_api_key)) {
          discovery?.invalidate({ settings: true });
        }
        if (before.timezone !== data.settings.timezone) {
          refreshServiceDay(true);
          month = sourceTimeline().values.date?.slice(0, 7) || localToday().slice(0, 7);
          dirtySnapshots(); invalidateSidebar(); loadSidebar();
        }
      },
    });
    await settingsPage.mount($('workspace'), account);
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
async function synchronize() {
  const trigger = $('sync-trigger');
  if (!identity || trigger.disabled) return;
  const generation = epoch, view = viewSequence;
  trigger.disabled = true;
  const transfer = syncStatus.start(refreshSync);
  try {
    dirtySnapshots(); invalidateSidebar();
    const [, refreshed] = await Promise.all([loadSidebar(), timeline ? timeline.refresh({ record: false }) : refreshTimelineSnapshot()]);
    stale(generation);
    if (view !== viewSequence || !refreshed) { transfer(view === viewSequence ? 'failure' : 'canceled'); return; }
    transfer('success');
    recordSync();
    snackbar(l("已从 Core 刷新笔记。"));
  } catch (err) {
    transfer(resultOf(err));
    if (generation === epoch && (view === viewSequence || err.status === 401)) handleFailure($('list-error'), err);
  } finally {
    trigger.disabled = false;
  }
}

async function refreshTimelineSnapshot() {
  const source = sourceTimeline(), revision = projectionRevision;
  const controller = timelineRefresh = new AbortController();
  const matching = [...snapshots].reverse().find(([, snapshot]) => snapshot.url === source.url);
  const key = source.key || matching?.[0] || crypto.randomUUID();
  const snapshot = snapshots.get(key);
  const previous = snapshot?.url === source.url ? snapshot : matching?.[1];
  try {
    const data = await readTimeline({ ...source, request, sessionRequest, controller, previous });
    if (controller.signal.aborted || revision !== projectionRevision) return false;
    storeTimelineSnapshot(snapshots, key, { ...previous, ...data, url: source.url, scroll: previous?.scroll ?? 0, dirty: false });
    return true;
  } finally {
    if (timelineRefresh === controller) timelineRefresh = null;
  }
}
$('sync-trigger').addEventListener('click', synchronize);
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
  const started = performance.now(), transfer = syncStatus.start(draft, { write: true });
  const returnFocus = document.activeElement === $('content') || document.activeElement === $('save');
  saving = true; updateComposer(); showError($('save-error'), null);
  try {
    const note = await sessionRequest('/notes', { method: 'POST', body: attempt.body, headers: { 'Idempotency-Key': attempt.key } });
    stale(generation);
    if (!validNote(note)) throw Object.assign(new Error('Invalid create response'), { code: 'NOTE_SAVE_UNCONFIRMED' });
    transfer('success'); usage('create', 'success'); usage('save_ack', 'success', performance.now() - started);
    draft.confirm(attempt); $('content').value = draft.content; dirtySnapshots();
    timeline?.update(note.name, note, true);
    sidebarChanged(null, note);
    if (returnFocus && [document.body, $('content'), $('save')].includes(document.activeElement) && !$('composer').hidden) $('content').focus({ preventScroll: true });
  } catch (err) {
    transfer(resultOf(err));
    if (generation !== epoch) return;
    usage('create', resultOf(err)); usage('save_ack', resultOf(err), performance.now() - started);
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
$('load-more').addEventListener('click', () => timeline?.loadMore());
$('list-retry').addEventListener('click', () => {
  if (moduleReload) location.reload();
  else if (timeline) timeline.retry();
  else showRoute();
});
$('back').addEventListener('click', () => {
  if (routeReturnKey) history.back();
  else navigate(safeFrom(new URLSearchParams(location.search).get('from')));
});
$('retry-logout').addEventListener('click', logout);
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
for (const host of [$('tags'), $('calendar')]) host.addEventListener('focusout', event => {
  if (host.contains(event.relatedTarget)) return;
  const commit = sidebarCommits.get(host);
  sidebarCommits.delete(host);
  commit?.();
});
window.addEventListener('pagehide', () => { clearInterval(loginAutofillTimer); disconnectPrivacy(); invalidate(); hidePrivate(); });
window.addEventListener('pageshow', event => { if (event.persisted) restore(); });
window.addEventListener('focus', refreshSidebarOnFocus);
document.addEventListener('visibilitychange', refreshSidebarOnFocus);
document.addEventListener('visibilitychange', monitorLoginAutofill);
initTheme();
initI18n();
restore();
