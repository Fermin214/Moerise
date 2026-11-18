import { l, setText, setAttr, resolveText, locale, monthLabel, weekdays } from './i18n.js';
import { errorMessage } from './errors.js';
import { el, button, icon } from './dom.js';
import { createModal } from './modal.js';

const chars = value => [...(value || '')].length;
const date = (year, month, day) => `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`;
const nextDate = value => new Date(Date.parse(`${value}T12:00:00Z`) + 86400000).toISOString().slice(0, 10);
function arrow(node, path) { node.append(icon(path)); return node; }

export function createMaterialPicker({ parent, selected, today, formatTime, sessionRequest, characterLimit,
  onLimit, onSubmit, onFailure, onClose, onDirtyChange }) {
  let alive = true, busy = false, tab = 'date', choices = new Map(selected), from = '', through = '', chosenTags = new Set();
  let year = Number(today().slice(0, 4)), month = Number(today().slice(5, 7)), years = false;
  let candidates = [], tags = [], loadSequence = 0, loading = false, next = '', candidateController = null;
  let limit = characterLimit;
  const calendarDays = new Map(), tokens = new Set(), abort = new AbortController();
  const modal = createModal({ parent, className: 'card insight-picker', label: l("选择笔记"),
    canClose: () => !busy,
    onClose: () => {
      alive = false; abort.abort(); candidateController?.abort(); onClose(); onDirtyChange();
    } });
  const dialog = modal.dialog, header = el('div', 'insight-between'), summary = el('span', 'sub');
  summary.setAttribute('role', 'status');
  header.append(el('h2', '', l("选择笔记")), summary);
  const tabs = el('div', 'insight-tabs'), dateTab = button(l("按日期"), 'tab on', () => setTab('date'));
  const tagTab = button(l("按标签 / 逐条"), 'tab', () => setTab('tag'));
  tabs.append(dateTab, tagTab);
  const area = el('div', 'insight-picker-area'), error = el('p', 'sub insight-warning');
  error.setAttribute('role', 'alert'); error.hidden = true;
  const done = button(l("完成"), 'save insight-done', async () => {
    if (busy) return;
    busy = true; done.disabled = true; dialog.setAttribute('aria-busy', 'true');
    setText(done, l("正在保存…"));
    const locked = [...dialog.querySelectorAll('button,input')].filter(node => !node.disabled);
    for (const node of locked) node.disabled = true;
    try {
      await onSubmit(new Map(choices));
      if (alive) modal.close(true);
    } catch (err) { if (alive) fail(err); }
    finally {
      if (alive) {
        busy = false;
        for (const node of locked) if (node.isConnected) node.disabled = false;
        dialog.setAttribute('aria-busy', 'false'); summaryText(); setText(done, l("完成"));
      }
    }
  });
  dialog.append(header, tabs, area, error, done);
  function fail(err) {
    if (!alive || err.name === 'AbortError') return;
    setText(error, errorMessage(err)); error.hidden = false;
    onFailure(err);
  }
  function setLimit(value) {
    if (!Number.isSafeInteger(value) || value <= 0) return;
    limit = value; onLimit(value);
  }
  function summaryText() {
    const count = [...choices.values()].reduce((sum, note) => sum + chars(note.content), 0);
    setText(summary, l("已选 {0} 条 · {1} / {2} 字", { 0: choices.size, 1: count, 2: limit }));
    done.disabled = busy;
  }
  function setTab(value) { if (busy) return; tab = value; years = false; load(); }
  async function load(reset = true) {
    if (!alive || busy || (!reset && (loading || !next))) return;
    if (reset) {
      candidateController?.abort(); loadSequence++; candidates = []; next = ''; tokens.clear();
    }
    const seq = loadSequence;
    error.hidden = true; loading = true; render();
    if (tab === 'date' && !from) { loading = false; render(); return; }
    const query = new URLSearchParams({ page_size: '100' });
    if (from) { query.set('from', from); query.set('until', nextDate(through || from)); }
    for (const tag of chosenTags) query.append('tag', tag);
    if (next) query.set('page_token', next);
    const requestController = candidateController = new AbortController();
    try {
      const page = await sessionRequest(`/chat-materials?${query}`, { controller: requestController });
      if (!alive || seq !== loadSequence) return;
      setLimit(page.character_limit);
      const merged = new Map(candidates.map(note => [note.name, note]));
      for (const note of page.notes) merged.set(note.name, note);
      candidates = [...merged.values()];
      const token = typeof page.next_page_token === 'string' ? page.next_page_token : '';
      next = tokens.has(token) ? '' : token;
      if (token) tokens.add(token);
    } catch (err) { if (seq === loadSequence) fail(err); }
    finally {
      if (alive && seq === loadSequence) {
        candidateController = null; loading = false; render();
      }
    }
  }
  function list() {
    const section = el('div', 'insight-picker-list'), count = candidates.length;
    section.setAttribute('aria-busy', String(loading));
    const head = el('div', 'insight-between');
    head.append(el('span', 'sub', tab === 'date' ? l("这段时间的笔记，勾选要用的") : l("共 {0} 条，勾选要用的", { 0: count })),
      button(l("全选这 {0} 条", { 0: count }), 'pbtn insight-select-all', () => {
        for (const note of candidates) choices.set(note.name, note);
        summaryText(); render(); onDirtyChange();
      }));
    if (count) section.append(head);
    for (const note of candidates) {
      const row = el('label', 'ent insight-choice'), input = el('input');
      input.type = 'checkbox'; input.checked = choices.has(note.name);
      input.addEventListener('change', () => {
        if (input.checked) choices.set(note.name, note); else choices.delete(note.name);
        summaryText(); onDirtyChange();
      });
      row.append(input, el('span', '', [...note.content || note.name].slice(0, 24).join('')),
        el('em', '', () => `${resolveText(formatTime(note.create_time))}${note.tags?.length ? ` · #${note.tags.join(' #')}` : ''}`));
      section.append(row);
    }
    if (loading) section.append(el('p', 'sub', l("正在加载笔记…")));
    const more = button(l("加载更多"), 'pbtn insight-more', () => load(!count));
    more.disabled = loading;
    if (next || (!count && !error.hidden)) section.append(more);
    return section;
  }
  function calendar() {
    const section = el('div', 'insight-picker-area'), header = el('div', 'insight-between');
    const shift = delta => {
      const value = new Date(Date.UTC(year, month - 1 + delta, 1));
      if (value.getUTCFullYear() < 1000 || value.getUTCFullYear() > 9999) return;
      year = value.getUTCFullYear(); month = value.getUTCMonth() + 1; render(); loadCalendar();
    };
    const prev = arrow(button('', 'btn s', () => shift(-1)), 'M14 6l-6 6 6 6');
    const next = arrow(button('', 'btn s', () => shift(1)), 'M10 6l6 6-6 6');
    setAttr(prev, 'aria-label', l("上个月")); setAttr(next, 'aria-label', l("下个月"));
    const title = button(monthLabel(year, month), 'btn insight-month', () => { years = !years; render(); });
    arrow(title, 'M6 9l6 6 6-6');
    setAttr(title, 'aria-label', l("选择年月")); header.append(prev, title, next); section.append(header);
    if (years) {
      const row = el('div', 'insight-year');
      const older = arrow(button('', 'btn s', () => { if (year > 1000) { year--; render(); } }), 'M14 6l-6 6 6 6');
      const newer = arrow(button('', 'btn s', () => { if (year < 9999) { year++; render(); } }), 'M10 6l6 6-6 6');
      setAttr(older, 'aria-label', l("上一年")); setAttr(newer, 'aria-label', l("下一年"));
      row.append(older, el('b', '', l("{0} 年", { 0: year })), newer); section.append(row);
      const grid = el('div', 'mg');
      for (let value = 1; value <= 12; value++) grid.append(button(() => new Intl.DateTimeFormat(locale(), { month: 'short', timeZone: 'UTC' }).format(new Date(Date.UTC(year, value - 1, 1))), value === month ? 'ed' : '', () => {
        month = value; years = false; render(); loadCalendar();
      }));
      section.append(grid);
    } else {
      const grid = el('div', 'pc');
      for (const day of weekdays) grid.append(el('span', '', day));
      const offset = (new Date(Date.UTC(year, month - 1, 1)).getUTCDay() + 6) % 7;
      for (let i = 0; i < offset; i++) { const empty = button('', 'blank', () => {}); empty.disabled = true; grid.append(empty); }
      const days = new Date(Date.UTC(year, month, 0)).getUTCDate();
      for (let day = 1; day <= days; day++) {
        const value = date(year, month, day);
        const cls = [calendarDays.get(date(year, month, 1).slice(0, 7))?.has(value) ? 'has' : '',
          value === from || value === through ? 'ed' : from && through && value > from && value < through ? 'rg' : ''].join(' ');
        const cell = button(String(day), cls, () => {
          if (!from || through) { from = value; through = ''; }
          else { through = value < from ? from : value; from = value < from ? value : from; }
          render(); load();
        });
        setAttr(cell, 'aria-label', value); grid.append(cell);
      }
      section.append(grid);
    }
    const range = el('div', 'insight-between insight-range');
    range.append(el('span', 'sub', !from ? l("点选起止日期，筛出这段时间的笔记") : through ? l("{0} – {1} · {2} 条", { 0: from, 1: through, 2: candidates.length }) : l("{0} – 请选择结束日", { 0: from })));
    if (from) range.append(button(l("清除"), 'pbtn insight-select-all', () => { from = through = ''; render(); load(); }));
    section.append(range);
    if (from && (candidates.length || loading || !error.hidden)) section.append(list());
    else if (from && !loading) section.append(el('p', 'sub', l("这段时间没有笔记。")));
    return section;
  }
  function render() {
    const focused = area.contains(document.activeElement) ? document.activeElement : null;
    const caption = focused?.getAttribute('aria-label') || focused?.textContent;
    const top = area.scrollTop;
    summaryText(); dateTab.classList.toggle('on', tab === 'date'); tagTab.classList.toggle('on', tab === 'tag');
    area.replaceChildren();
    if (tab === 'date') area.append(calendar());
    else {
      const filters = el('div', 'insight-tag-filters');
      filters.append(button(l("全部"), `pbtn${chosenTags.size ? '' : ' on'}`, () => { chosenTags.clear(); load(); }));
      for (const tag of tags) filters.append(button(tag, `pbtn${chosenTags.has(tag) ? ' on' : ''}`, () => {
        if (chosenTags.has(tag)) chosenTags.delete(tag); else chosenTags.add(tag);
        load();
      }));
      area.append(filters, list());
    }
    area.scrollTop = top;
    if (caption) [...area.querySelectorAll('button')].find(node =>
      (node.getAttribute('aria-label') || node.textContent) === caption)?.focus({ preventScroll: true });
  }

  async function loadCalendar() {
    const key = date(year, month, 1).slice(0, 7);
    if (calendarDays.has(key)) return;
    try {
      const pages = await Promise.all(['NORMAL', 'ARCHIVED'].map(state =>
        sessionRequest(`/calendar?${new URLSearchParams({ month: key, state })}`, { controller: abort })));
      if (!alive) return;
      calendarDays.set(key, new Set(pages.flatMap(page => page.days.filter(day => day.count > 0).map(day => day.date))));
      if (!busy) render();
    } catch (err) { fail(err); }
  }
  dateTab.autofocus = true; render(); modal.show(); loadCalendar();
  Promise.all(['NORMAL', 'ARCHIVED'].map(state => sessionRequest(`/tags?state=${state}`, { controller: abort })))
    .then(pages => {
      if (!alive) return;
      tags = [...new Set(pages.flatMap(page => page.tags.map(value => value.tag)))].sort();
      if (!busy) render();
    }).catch(fail);
  return {
    destroy: () => modal.destroy(),
    hasChanges: () => choices.size !== selected.size || [...choices.keys()].some(name => !selected.has(name)),
  };
}
