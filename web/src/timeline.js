import { l, setText } from './i18n.js';
import { validNote, compareNotes } from './notes.js';
import { filters, listQuery, enhancedSearch, searchBody } from './routes.js';

export function createTimeline({ key, url, snapshots, request, sessionRequest, render, showError, handleFailure }) {
  const path = location.pathname, values = filters(location.search);
  const enhanced = enhancedSearch(values), state = path === '/archived' ? 'ARCHIVED' : 'NORMAL';
  const element = id => document.getElementById(id);
  let items = [], nextToken = '', pages = 0, status = '';
  let active = null, closed = false, retryTimer = null, retryDelay = 1000, retryOptions = null;
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
  function display() {
    render(items);
    setText(element('capability'), status); element('capability').hidden = !status;
    element('empty').hidden = !!items.length || !!nextToken;
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
  async function load({ reset = false, targetPages = 1, scroll } = {}, automatic = false) {
    if (closed || (!reset && (active || !nextToken))) return;
    active?.abort();
    const owner = active = new AbortController();
    clearTimeout(retryTimer); retryOptions = null;
    if (!automatic && reset) retryDelay = 1000;
    observer.disconnect(); controls(); showError(element('list-error'), null);
    element('list-retry').hidden = element('empty').hidden = true;
    try {
      const merged = new Map((reset ? [] : items).map(note => [note.name, note]));
      let token = reset ? '' : nextToken, loaded = 0, page;
      const seenTokens = new Set(token ? [token] : []);
      do {
        page = await fetchPage(owner, token);
        if (owner !== active) return;
        for (const note of page.notes) merged.set(note.name, note);
        token = typeof page.next_page_token === 'string' ? page.next_page_token : '';
        if (seenTokens.has(token)) token = '';
        else if (token) seenTokens.add(token);
        loaded++;
      } while (reset && loaded < targetPages && token);
      items = [...merged.values()];
      if (!enhanced) items.sort(compareNotes);
      status = enhanced && page.mode === 'keyword'
        ? l("语义检索暂不可用，当前显示按相关度排列的关键词结果。")
        : enhanced ? l("搜索结果按相关度排列。") : '';
      pages = reset ? loaded : pages + loaded; nextToken = token; retryDelay = 1000;
      display(); remember(false, true);
      if (scroll != null) restoreScroll(scroll);
      if (nextToken) observer.observe(element('page-end'));
    } catch (error) {
      if (owner !== active) return;
      handleFailure(element('list-error'), error.code === 'SOURCE_CHANGED' ? { ...error, code: 'SEARCH_SOURCE_CHANGED' } : error);
      if (closed) return;
      const restart = reset || ['INVALID_PAGE_TOKEN', 'SOURCE_CHANGED'].includes(error.code);
      retryOptions = { reset: restart, targetPages, scroll };
      setText(element('list-retry'), restart ? l("刷新主页") : l("重试加载")); element('list-retry').hidden = false;
      if (error.retryable) {
        retryTimer = setTimeout(() => load(retryOptions, true), retryDelay);
        retryDelay = Math.min(retryDelay * 2, 30_000);
      }
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
      if (snapshot && !snapshot.dirty) {
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
    refresh: () => load({ reset: true }),
    retry: () => retryOptions?.reset
      ? load({ reset: true, targetPages: pages || 1, scroll: window.scrollY }) : load(),
    async update(name, note) {
      items = note?.state === state ? items.map(item => item.name === name ? note : item) : items.filter(item => item.name !== name);
      display();
      await load({ reset: true, targetPages: pages || 1, scroll: window.scrollY });
    },
    destroy() {
      closed = true; active?.abort(); active = null;
      observer.disconnect(); clearTimeout(retryTimer);
      window.removeEventListener('scroll', onScroll); clearTimeout(scrollTimer); cancelAnimationFrame(scrollFrame);
    },
  };
}
