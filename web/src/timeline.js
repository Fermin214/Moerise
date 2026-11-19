import { l, setText } from './i18n.js';
import { validNote, compareNotes } from './notes.js';
import { filters, listQuery, enhancedSearch, searchBody } from './routes.js';
import { usage, resultOf } from './privacy.js';

export function createTimeline({ key, url, snapshots, request, sessionRequest, render, showError, handleFailure }) {
  const path = location.pathname, values = filters(location.search);
  const enhanced = enhancedSearch(values), state = path === '/archived' ? 'ARCHIVED' : 'NORMAL';
  const element = id => document.getElementById(id);
  let items = [], nextToken = '', pages = 0, status = '';
  let active = null, closed = false, retryOptions = null;
  let firstSearch = !!values.query;
  const cursors = new Set();
  let scrollTimer = null, scrollFrame = null;
  const observer = new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting)) load();
  }, { rootMargin: '200px' });

  function remember(writeHistory = true, clean = false) {
    if (!pages) return;
    const previous = snapshots.get(key);
    snapshots.delete(key);
    const focusedName = document.activeElement?.closest('article[data-name]')?.dataset.name || previous?.focusedName;
    snapshots.set(key, { url, items: [...items], nextToken, searchStatus: status, scroll: window.scrollY,
      pages, focusedName, dirty: clean ? false : previous?.dirty });
    if (writeHistory) saveScroll();
    if (snapshots.size > 12) snapshots.delete(snapshots.keys().next().value);
  }
  function saveScroll() {
    history.replaceState({ ...history.state, scroll: window.scrollY, pages }, '');
  }
  function onScroll() {
    if (scrollTimer !== null) return;
    scrollTimer = setTimeout(() => { scrollTimer = null; saveScroll(); }, 150);
  }
  function restoreScroll(value) {
    cancelAnimationFrame(scrollFrame);
    scrollFrame = requestAnimationFrame(() => window.scrollTo(0, value));
  }
  function controls() {
    element('notes').setAttribute('aria-busy', String(!!active));
    element('load-more').hidden = !nextToken; element('load-more').disabled = !!active;
  }
  function display(anchor = false) {
    const cards = anchor && window.scrollY > 0 ? [...element('notes').children] : [];
    const first = cards.findIndex(card => card.getBoundingClientRect().bottom > 0);
    const nearby = first < 0 ? [] : [cards[first], cards[first + 1], cards[first - 1]].filter(Boolean)
      .map(card => [card.dataset.name, card.getBoundingClientRect().top]);
    render(items);
    setText(element('capability'), status); element('capability').hidden = !status;
    element('empty').hidden = !!items.length || !!nextToken;
    for (const [name, top] of nearby) {
      const card = [...element('notes').children].find(node => node.dataset.name === name);
      if (card) { window.scrollBy(0, card.getBoundingClientRect().top - top); break; }
    }
  }
  async function fetchPage(owner, token) {
    const page = enhanced
      ? await sessionRequest('/search', { method: 'POST', body: searchBody(path, values, token), controller: owner })
      : await request(`/notes?${listQuery(path, values, token)}`, { controller: owner });
    if (!Array.isArray(page?.notes) || page.notes.some(note => !validNote(note) || note.state !== state) ||
        (enhanced && (!['keyword', values.mode].includes(page.mode) ||
          typeof page.semantic?.used !== 'boolean' || typeof page.semantic?.reason !== 'string' ||
          (page.mode === 'keyword' && (values.mode !== 'hybrid' || page.semantic.used)) ||
          (page.mode !== 'keyword' && !page.semantic.used)))) {
      throw Object.assign(new Error('Invalid note list response'), { code: 'NOTE_LIST_INVALID' });
    }
    return page;
  }
  async function load({ reset = false, targetPages = 1, scroll } = {}) {
    if (closed || (!reset && (active || !nextToken))) return;
    active?.abort();
    const owner = active = new AbortController();
    let measure = firstSearch && reset;
    firstSearch = false;
    const started = performance.now();
    retryOptions = null;
    observer.disconnect(); controls(); showError(element('list-error'), null);
    element('list-retry').hidden = element('empty').hidden = true;
    try {
      const merged = new Map((reset ? [] : items).map(note => [note.name, note]));
      let token = reset ? '' : nextToken, loaded = 0, page;
      const seenTokens = reset ? new Set() : new Set(cursors);
      const tail = reset && pages > 1 ? items.at(-1) : null;
      do {
        page = await fetchPage(owner, token);
        if (owner !== active) return;
        if (measure) { usage(`search_${values.mode}`, 'success', performance.now() - started); measure = false; }
        for (const note of page.notes) merged.set(note.name, note);
        token = typeof page.next_page_token === 'string' ? page.next_page_token : '';
        if (seenTokens.has(token)) token = '';
        else if (token) seenTokens.add(token);
        loaded++;
      } while (reset && token && (loaded < targetPages ||
        (!enhanced && tail && page.notes.length && compareNotes(page.notes.at(-1), tail) < 0)));
      items = [...merged.values()];
      if (!enhanced) items.sort(compareNotes);
      status = enhanced && page.mode === 'keyword'
        ? l("语义检索暂不可用，当前显示按相关度排列的关键词结果。")
        : enhanced ? l("搜索结果按相关度排列。") : '';
      pages = reset ? loaded : pages + loaded; nextToken = token;
      cursors.clear(); for (const cursor of seenTokens) cursors.add(cursor);
      display(true); remember(false, true);
      if (scroll != null) restoreScroll(scroll);
      if (nextToken) observer.observe(element('page-end'));
    } catch (error) {
      if (measure) usage(`search_${values.mode}`, resultOf(error), performance.now() - started);
      if (owner !== active) return;
      handleFailure(element('list-error'), error.code === 'SOURCE_CHANGED' ? { ...error, code: 'SEARCH_SOURCE_CHANGED' } : error);
      if (closed) return;
      const restart = reset || ['INVALID_PAGE_TOKEN', 'SOURCE_CHANGED'].includes(error.code);
      retryOptions = { reset: restart, targetPages, scroll };
      setText(element('list-retry'), restart ? l("刷新主页") : l("重试加载")); element('list-retry').hidden = false;
    } finally {
      if (owner === active) { active = null; controls(); }
    }
  }
  window.addEventListener('scroll', onScroll, { passive: true });
  return {
    remember,
    async open(returning) {
      const previous = snapshots.get(key);
      const snapshot = previous?.url === url ? previous :
        [...snapshots.values()].reverse().find(entry => entry.url === url && !entry.dirty);
      if (snapshot) {
        if (firstSearch) { usage(`search_${values.mode}`, 'success'); firstSearch = false; }
        items = [...snapshot.items]; nextToken = snapshot.nextToken; pages = snapshot.pages; status = snapshot.searchStatus;
        display(); controls(); remember(false, true); restoreScroll(snapshot.scroll);
        await load({ reset: true, targetPages: Math.max(1, pages) });
      } else {
        const saved = snapshot || history.state;
        window.scrollTo(0, 0);
        await load({ reset: true, targetPages: Math.max(1, saved?.pages || 1), scroll: saved?.scroll || 0 });
      }
      if (!closed && returning && snapshot?.focusedName) {
        [...element('notes').children].find(card => card.dataset.name === snapshot.focusedName)?.querySelector('.dlink')?.focus({ preventScroll: true });
      }
    },
    loadMore: () => load(),
    refresh: () => load({ reset: true, targetPages: pages || 1 }),
    retry: () => retryOptions?.reset
      ? load({ reset: true, targetPages: pages || 1 }) : load(),
    async update(name, note) {
      active?.abort(); active = null;
      const filtered = values.query || values.date || values.tags.length;
      if (!filtered || items.some(item => item.name === name)) {
        items = items.filter(item => item.name !== name);
        if (note?.state === state) items.push(note);
        if (!enhanced) items.sort(compareNotes);
        display(true); controls(); remember(false, !filtered);
      }
      if (filtered || !pages) await load({ reset: true, targetPages: pages || 1 });
      else if (nextToken) observer.observe(element('page-end'));
    },
    destroy() {
      closed = true; active?.abort(); active = null;
      observer.disconnect();
      window.removeEventListener('scroll', onScroll); clearTimeout(scrollTimer); cancelAnimationFrame(scrollFrame);
    },
  };
}
