import { l, setText, setAttr, monthLabel, weekdays } from './i18n.js';
import { errorMessage } from './errors.js';
import { el, button, icon } from './dom.js';
import { createModal } from './modal.js';
import { resultOf, usage } from './privacy.js';

export const materialSize = note => note.characters ?? [...(note.content || '')].length;
const date = (year, month, day) => `${year}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`;
const nextDate = value => new Date(Date.parse(`${value}T12:00:00Z`) + 86400000).toISOString().slice(0, 10);

export function createMaterialPicker({ parent, selected, today, sessionRequest, readTags, readCalendar,
  characterLimit, existing, previous, changedNames = [], onLimit, onSubmit, onFailure, onClose, onDirtyChange }) {
  const choices = new Map(selected), automatic = new Set(previous?.automatic), omitted = new Set(previous?.omitted);
  const initial = new Map(selected), candidates = new Map(), rows = new Map(), tokens = new Set();
  const tags = new Set(), calendar = new Map(), changed = new Set(changedNames), manual = new Map();
  const started = performance.now();
  let operation = 'material_manual', submitted = false, overLimit = false, baseOmitted;
  let range = previous?.range || '', start = '', end = '', tab = 'date', calendarOpen = false, selectedView = false;
  let batch = '', baseline, baseAuto, loading = false, complete = false, failed = false, blocked = false;
  let alive = true, busy = false, next = '', controller, sequence = 0, limit = characterLimit, searching = false;
  let sourceLimit = 1000, availableTags, year = Number(today().slice(0, 4)), month = Number(today().slice(5, 7));
  const modal = createModal({ parent, className: 'card insight-picker material-dialog', label: l("选择笔记"),
    canClose: () => !busy, onClose: () => {
      if (!submitted) usage(operation, 'canceled', performance.now() - started);
      alive = false; controller?.abort(); observer?.disconnect(); onClose(); onDirtyChange();
    } });
  const dialog = modal.dialog, header = el('div', 'insight-between');
  const close = button('×', 'btn s', () => modal.close()); setAttr(close, 'aria-label', l("关闭"));
  header.append(el('h2', '', l("选择笔记")), close);
  const tabs = el('div', 'insight-tabs'), dateTab = button(l("按日期"), 'tab', () => setTab('date'));
  const tagTab = button(l("按标签"), 'tab', () => setTab('tag'));
  const view = button(l("查看已选"), 'pbtn', () => {
    selectedView = !selectedView; calendarOpen = false; controls(); displayRows();
  });
  tabs.append(dateTab, tagTab);
  const nav = el('div', 'insight-between'); nav.append(tabs, view);
  const filters = el('div', 'insight-tag-filters'), dateEntry = button('', 'pbtn', () => {
    [start = '', end = ''] = range.split('/'); calendarOpen = true;
    if (start) { year = +start.slice(0, 4); month = +start.slice(5, 7); }
    controls(); showCalendar(); datePane.querySelector('button:not(:disabled)')?.focus();
  });
  const datePane = el('div', 'material-calendar'), body = el('div', 'material-body');
  const scroll = el('div', 'insight-picker-area material-scroll'), list = el('div', 'insight-picker-list');
  const tools = el('div', 'material-tools');
  const select = button(l("全选"), 'pbtn', () => beginBatch('select'));
  const clear = button(l("取消全选"), 'pbtn', () => beginBatch('clear'));
  const stop = button(l("停止批量选择，改为逐条勾选"), 'pbtn', () => {
    abandonBatch(); operation = 'material_manual'; controls(); displayRows();
    if (!candidates.size && !complete && !blocked) load();
  });
  const status = el('p', 'sub'), error = el('p', 'sub insight-warning'); error.setAttribute('role', 'alert');
  const more = button(l("加载更多"), 'pbtn insight-more', () => load());
  const footer = el('div', 'material-footer'), summary = el('span', 'sub'), done = button('', 'save insight-done', submit);
  tools.append(select, clear, stop); scroll.append(tools, list, status, error, more); body.append(datePane, scroll);
  footer.append(summary, done); dialog.append(header, nav, dateEntry, filters, body, footer);
  const observer = typeof IntersectionObserver === 'function' && new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting) && !failed && !blocked && !calendarOpen && !selectedView) load();
  }, { root: scroll });

  function controls() {
    const count = [...choices.values()].reduce((sum, note) => sum + materialSize(note), 0);
    const exceeded = count > limit || choices.size > sourceLimit || blocked;
    if (exceeded && !overLimit) usage(operation, 'over_limit', performance.now() - started);
    overLimit = exceeded;
    const inList = [...candidates.keys()].filter(name => choices.has(name)).length;
    setText(summary, l("已选 {0} 条 · {1} / {2} 字", { 0: choices.size, 1: count, 2: limit }));
    setText(done, busy ? l("正在保存…") : !choices.size && existing ? l("清空材料") : l("应用所选 {0} 条", { 0: choices.size }));
    done.disabled = busy || calendarOpen || !!batch || changed.size > 0 || (!choices.size && !existing) || count > limit || choices.size > sourceLimit;
    dialog.setAttribute('aria-busy', String(busy)); header.inert = nav.inert = body.inert = filters.inert = busy;
    dateEntry.disabled = busy; summary.classList.toggle('insight-warning', count > limit);
    dateTab.classList.toggle('on', !selectedView && tab === 'date'); tagTab.classList.toggle('on', !selectedView && tab === 'tag');
    dateTab.setAttribute('aria-pressed', String(!selectedView && tab === 'date')); tagTab.setAttribute('aria-pressed', String(!selectedView && tab === 'tag'));
    setText(view, selectedView ? l("返回清单") : l("查看已选")); view.setAttribute('aria-pressed', String(selectedView));
    filters.hidden = selectedView || tab !== 'tag'; dateEntry.hidden = selectedView || tab !== 'date' || calendarOpen;
    setText(dateEntry, range ? range.split('/').filter(Boolean).join(' – ') : l("选择日期"));
    dialog.classList.toggle('calendar-open', calendarOpen); datePane.hidden = !calendarOpen;
    const hasMatches = candidates.size > 0 || !complete;
    select.hidden = !!batch || !hasMatches || (complete && inList === candidates.size);
    clear.hidden = !!batch || !hasMatches || (!inList && (complete || !choices.size));
    stop.hidden = !batch; tools.hidden = selectedView || calendarOpen || (select.hidden && clear.hidden && stop.hidden);
    setText(status, batch ? l("正在统计") : loading && !candidates.size ? l("正在加载笔记…") :
      !(selectedView ? choices.size : candidates.size) && complete ? l("没有笔记") : '');
    status.hidden = !status.textContent;
    more.hidden = selectedView || calendarOpen || blocked || complete || (!failed && !next);
    more.disabled = loading || busy; setText(more, failed ? l("重试") : l("加载更多"));
    observer?.unobserve(more);
    if (!more.hidden && !failed && !loading && !batch) observer?.observe(more);
  }
  function showError(text) { setText(error, text); error.hidden = !text; }
  function updateChoice(name, checked, note) {
    if (checked) choices.set(name, note); else choices.delete(name);
    automatic.delete(name);
    if (checked) omitted.delete(name); else if (range) omitted.add(name);
    changed.delete(name);
    if (batch) manual.set(name, checked ? note : null);
  }
  function displayRows() {
    const visible = selectedView ? choices : candidates;
    for (const [name, entry] of rows) if (!visible.has(name)) { entry.row.remove(); rows.delete(name); }
    for (const [name, note] of visible) {
      let entry = rows.get(name);
      if (!entry) {
        const row = el('div', 'ent insight-choice'), label = el('label'), input = el('input'), text = el('span');
        const reselect = button(l("重新选择"), 'pbtn', async () => {
          reselect.disabled = true;
          try {
            const result = await sessionRequest('/chat-materials/resolve', { method: 'POST', body: { names: [name] } });
            if (!alive || !changed.has(name)) return;
            const latest = result.notes[0]; candidates.set(name, latest); updateChoice(name, true, latest);
            showError(changed.size ? l("所选材料已修改，请重新选择这些笔记。") : ''); controls(); displayRows();
          } catch (err) { if (alive && err.name !== 'AbortError') showError(errorMessage(err)); }
          finally { reselect.disabled = false; }
        });
        input.type = 'checkbox'; label.append(input, text); row.append(label, reselect);
        entry = { row, input, text, reselect }; rows.set(name, entry); list.append(row);
        input.addEventListener('change', () => {
          const fallback = selectedView && !input.checked ? row.nextElementSibling?.querySelector('input') || row.previousElementSibling?.querySelector('input') || view : null;
          updateChoice(name, input.checked, candidates.get(name) || choices.get(name));
          controls(); if (selectedView) displayRows(); onDirtyChange();
          fallback?.focus({ preventScroll: true });
        });
      }
      entry.input.checked = choices.has(name);
      entry.reselect.hidden = !changed.has(name);
      const value = (note.preview ?? note.content ?? name).slice(0, 180) || name;
      const caption = changed.has(name) ? l("{0} · 来源已更改，请重新勾选", { 0: value }) : value;
      if (entry.text.textContent !== String(caption)) setText(entry.text, caption);
    }
  }
  function abandonBatch() {
    if (!batch) return;
    controller?.abort(); sequence++; loading = false;
    choices.clear(); automatic.clear(); omitted.clear();
    for (const [name, note] of baseline) choices.set(name, note);
    for (const name of baseAuto) automatic.add(name);
    for (const name of baseOmitted) omitted.add(name);
    batch = '';
    for (const [name, note] of manual) updateChoice(name, !!note, note);
    manual.clear(); failed = false; showError(''); onDirtyChange();
  }
  function beginBatch(mode, resume = false) {
    if (busy) return;
    if (batch) abandonBatch();
    baseline = new Map(choices); baseAuto = new Set(automatic); baseOmitted = new Set(omitted); manual.clear();
    operation = tab === 'date' && range ? 'material_date' : tab === 'tag' && tags.size ? 'material_tags' : 'material_manual';
    batch = mode;
    if (!resume && (mode === 'select' || mode === 'clear')) for (const name of candidates.keys()) omitted.delete(name);
    applyBatch(candidates.values());
    if (complete) finishBatch(); else if (!loading && !blocked) load();
    controls(); displayRows(); onDirtyChange();
  }
  function applyBatch(items) {
    for (const note of items) {
      if (manual.has(note.name) || changed.has(note.name)) continue;
      if (batch === 'clear') {
        choices.delete(note.name); automatic.delete(note.name); omitted.add(note.name);
      } else if (batch && (batch !== 'date' || !omitted.has(note.name))) {
        if (batch === 'date' && !choices.has(note.name)) automatic.add(note.name);
        if (batch === 'select') { automatic.delete(note.name); omitted.delete(note.name); }
        if (!choices.has(note.name)) choices.set(note.name, note);
      }
    }
  }
  function finishBatch() { batch = ''; baseline = baseAuto = null; manual.clear(); }
  function resetList() {
    controller?.abort(); sequence++; loading = failed = complete = blocked = false; next = '';
    candidates.clear(); tokens.clear(); rows.clear(); list.replaceChildren(); scroll.scrollTop = 0; showError('');
  }
  async function load() {
    if (!alive || loading || busy || complete || blocked || selectedView && !batch) return;
    const ticket = sequence, request = controller = new AbortController();
    loading = true; failed = false; showError(''); controls();
    const query = new URLSearchParams({ page_size: '100', format: 'compact', tag_match: 'any' });
    if (tab === 'date' && range) {
      const [from, through] = range.split('/'); query.set('from', from); query.set('until', nextDate(through || from));
    }
    if (tab === 'tag') for (const tag of tags) query.append('tag', tag);
    if (next) query.set('page_token', next);
    try {
      const page = await sessionRequest(`/chat-materials?${query}`, { controller: request });
      if (!alive || sequence !== ticket) return;
      if (page.character_limit > 0) { limit = page.character_limit; onLimit(limit); }
      sourceLimit = page.source_limit || sourceLimit;
      const items = page.candidates || page.notes, fresh = [];
      for (const note of items) {
        if (!candidates.has(note.name) && candidates.size >= sourceLimit) { blocked = true; break; }
        candidates.set(note.name, note); fresh.push(note);
      }
      applyBatch(fresh);
      next = page.next_page_token || ''; complete = !next && !blocked;
      if (next && candidates.size >= sourceLimit) blocked = true;
      if (blocked) showError(l("超过 {0} 条来源，请缩小范围或逐条勾选。", { 0: sourceLimit }));
      else if (next && tokens.has(next)) { blocked = true; showError(l("分页无法继续，请缩小范围或逐条勾选。")); }
      else if (next) tokens.add(next);
      if (batch && complete) finishBatch();
    } catch (err) {
      if (!alive || sequence !== ticket || err.name === 'AbortError') return;
      failed = true;
      blocked = err.code === 'CHAT_RESOURCE_LIMIT';
      showError(blocked ? l("超过 {0} 条来源，请缩小范围或逐条勾选。", { 0: sourceLimit }) : errorMessage(err));
      if (err.status === 401) onFailure(err);
    } finally {
      if (alive && sequence === ticket) {
        loading = false; controls(); displayRows();
        if (batch && !failed && !blocked && !complete) load();
      }
    }
  }
  function setTab(value) {
    if (tab === value && !selectedView && !calendarOpen) return;
    if (tab !== value) {
      abandonBatch(); tab = value; resetList();
    }
    calendarOpen = selectedView = false; controls(); displayRows();
    if (!complete && !candidates.size && !loading && !failed) load();
    if (tab === 'tag') loadTags();
  }
  function drawTags() {
    filters.replaceChildren();
    for (const value of ['', ...(availableTags || [])]) {
      const pressed = value ? tags.has(value) : !tags.size;
      const chip = button(value || l("全部"), `pbtn${pressed ? ' on' : ''}`, () => {
        abandonBatch();
        if (!value) tags.clear(); else if (tags.has(value)) tags.delete(value);
        else tags.add(value);
        resetList(); drawTags(); controls(); load();
      });
      chip.setAttribute('aria-pressed', String(pressed)); filters.append(chip);
    }
  }
  async function loadTags() {
    if (searching || availableTags) return;
    searching = true;
    try {
      const pages = await Promise.all(['NORMAL', 'ARCHIVED'].map(state => readTags(state)));
      if (!alive) return;
      availableTags = [...new Set(pages.flatMap(page => page.tags.map(value => value.tag)))].sort(); drawTags();
    } catch (err) { if (alive && err.name !== 'AbortError') { showError(errorMessage(err)); if (err.status === 401) onFailure(err); } }
    finally { searching = false; }
  }
  function showCalendar() {
    const key = date(year, month, 1).slice(0, 7), counts = calendar.get(key);
    const activeDay = datePane.contains(document.activeElement) && document.activeElement.dataset.day;
    const header = el('div', 'insight-between'), grid = el('div', 'pc');
    const shift = delta => {
      const value = new Date(Date.UTC(year, month - 1 + delta, 1));
      if (value.getUTCFullYear() < 1000 || value.getUTCFullYear() > 9999) return;
      year = value.getUTCFullYear(); month = value.getUTCMonth() + 1; showCalendar();
      datePane.querySelector(delta > 0 ? '.next' : '.previous').focus();
    };
    const prev = button('', 'btn s previous', () => shift(-1)), following = button('', 'btn s next', () => shift(1));
    prev.append(icon('M14 6l-6 6 6 6')); following.append(icon('M10 6l6 6-6 6'));
    setAttr(prev, 'aria-label', l("上个月")); setAttr(following, 'aria-label', l("下个月"));
    header.append(prev, el('b', '', monthLabel(year, month)), following);
    for (const day of weekdays) grid.append(el('span', '', day));
    const offset = (new Date(Date.UTC(year, month - 1, 1)).getUTCDay() + 6) % 7;
    for (let index = 0; index < offset; index++) grid.append(el('span'));
    const days = new Date(Date.UTC(year, month, 0)).getUTCDate();
    for (let day = 1; day <= days; day++) {
      const value = date(year, month, day), count = counts?.get(value) || 0;
      const chosen = !!start && value >= start && value <= (end || start);
      const cell = button(String(day), `${!counts ? 'unknown' : count > 1 ? 'many' : count ? 'has' : ''}${chosen ? ' in-range' : ''}${value === start || value === end ? ' endpoint' : ''}`, () => {
        if (!start || end) { start = value; end = ''; }
        else [start, end] = [start, value].sort();
        showCalendar();
      });
      cell.dataset.day = value; cell.setAttribute('aria-pressed', String(chosen));
      setAttr(cell, 'aria-label', counts ? l("{0} · {1} 条笔记", { 0: value, 1: count }) : l("{0} · 数量未知", { 0: value }));
      grid.append(cell);
    }
    const apply = button(l("应用日期"), 'save', () => {
      const newRange = `${start}/${end || start}`, same = range === newRange;
      abandonBatch();
      if (!same) { for (const name of automatic) choices.delete(name); automatic.clear(); omitted.clear(); }
      range = newRange; calendarOpen = false; resetList(); controls();
      beginBatch('date', true);
      dateEntry.focus(); onDirtyChange();
    });
    apply.disabled = !start;
    const actions = el('div', 'material-tools');
    actions.append(button(l("返回清单"), 'ghost', () => { calendarOpen = false; controls(); dateEntry.focus(); }), apply);
    datePane.replaceChildren(header, grid, actions);
    if (activeDay) datePane.querySelector(`[data-day="${activeDay}"]`)?.focus({ preventScroll: true });
    if (!calendar.has(key)) {
      calendar.set(key, null);
      Promise.all(['NORMAL', 'ARCHIVED'].map(state => readCalendar(state, key))).then(pages => {
        if (!alive) return;
        const days = new Map();
        for (const page of pages) for (const { date, count } of page.days) days.set(date, (days.get(date) || 0) + count);
        calendar.set(key, days);
        if (calendarOpen && key === date(year, month, 1).slice(0, 7)) showCalendar();
      }).catch(err => {
        calendar.delete(key);
        if (alive && err.status === 401) onFailure(err);
      });
    }
  }
  async function submit() {
    if (done.disabled) return;
    busy = true; controls();
    try {
      await onSubmit(new Map(choices), { range, automatic: new Set(automatic), omitted: new Set(omitted) });
      submitted = true; usage(operation, 'success', performance.now() - started);
      if (alive) modal.close(true);
    } catch (err) {
      if (!alive || err.name === 'AbortError') return;
      usage(operation, resultOf(err), performance.now() - started);
      if (err.changed_names?.length) {
        for (const name of err.changed_names) if (choices.has(name)) changed.add(name);
        tab = 'date'; selectedView = true; calendarOpen = false; resetList();
        showError(l("所选材料已修改，请重新选择这些笔记。"));
      } else showError(errorMessage(err));
      if (err.status === 401) onFailure(err);
    } finally { if (alive) { busy = false; controls(); displayRows(); } }
  }
  selectedView = changed.size > 0;
  dateTab.autofocus = true; drawTags(); showError(changed.size ? l("所选材料已修改，请重新选择这些笔记。") : '');
  controls(); displayRows(); modal.show(); load();
  return { destroy: () => modal.destroy(),
    hasChanges: () => choices.size !== initial.size || [...choices].some(([name, note]) => initial.get(name)?.source_hash !== note.source_hash) };
}
