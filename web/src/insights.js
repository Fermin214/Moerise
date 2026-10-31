import { errorText } from './errors.js';
import { renderMarkdown } from './markdown.js';
import { readChatStream } from './chat-stream.js';

const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i;
const chars = value => [...(value || '')].length;
const date = (year, month, day) => `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`;
const nextDate = value => new Date(Date.parse(`${value}T12:00:00Z`) + 86400000).toISOString().slice(0, 10);
function el(tag, className = '', text = '') {
  const node = document.createElement(tag); node.className = className; node.textContent = text; return node;
}
function button(text, className, action) {
  const node = el('button', className, text); node.type = 'button'; node.addEventListener('click', action); return node;
}
function arrow(node, path) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', 'i'); svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true');
  const line = document.createElementNS(svg.namespaceURI, 'path'); line.setAttribute('d', path); svg.append(line); node.append(svg);
  return node;
}

export function createInsightsPage({ sessionRequest, csrfToken, renewCSRF, handleFailure, openNote, navigate, closeMenus, replaceURL, today, formatTime, onSaved }) {
  let root, active = false, chat = null, busy = false, materialOpen = true, picker = null;
  let selected = new Map(), pendingCreate = null, pendingUpdate = null, pendingGenerate = null, controller = null, pollTimer = null;
  let running = false, conflicted = false, sourceWarning = '', disposed = 0;
  let stopping = false, deleting = false, generationDone = null, historyDialog = null;
  const saving = new Map();
  const find = name => root.querySelector(`[data-chat="${name}"]`);
  const current = ticket => active && ticket === disposed;
  const namesEqual = (a, b) => a.length === b.length && a.every((name, index) => name === b[index]);

  function warning(text = '') {
    if (!active) return;
    const node = find('warning'); node.textContent = text; node.hidden = !text;
  }
  function failure(err) {
    if (!active || err.name === 'AbortError') return;
    const messages = {
      CHAT_MATERIAL_LIMIT: '超过字数上限，请减少所选笔记。',
      CHAT_CONTEXT_LIMIT: '完整材料与对话已超过模型上下文上限，请调整材料或开始新的会话。',
      CHAT_RESOURCE_LIMIT: '材料请求超过资源限制，请减少选择范围。',
      SOURCE_CHANGED: '所选材料已修改，请重新选择这些笔记。',
      SOURCE_NOT_FOUND: '所选材料已删除或无法访问，请调整材料。',
      CHAT_INTERRUPTED: '回复已中断，已保留收到的内容。请重新读取会话后继续。',
      CHAT_PROTOCOL: '聊天流未完整结束，请重新读取会话状态。',
      CHAT_CANCELED: '已停止生成，已保留收到的内容。',
      CHAT_BUSY: '该会话正在生成，请等待或停止后继续。',
      CHAT_CONFLICT: '会话已在其他页面更改，请重新进入后继续。',
      LLM_DISABLED: '请在设置中启用 LLM。',
      LLM_PROTOCOL: '模型返回的内容未完整结束，已保留部分回复。',
      CHAT_NOT_FOUND: '该会话已删除或无法访问。',
    };
    const text = messages[err.code] || errorText(err);
    if (err.code === 'SOURCE_CHANGED' || err.code === 'SOURCE_NOT_FOUND') sourceWarning = text;
    warning(text);
    if (err.status === 401) handleFailure(find('warning'), err);
  }
  function controls() {
    if (!active) return;
    running = !!chat?.turns?.some(turn => turn.assistant.status === 'running');
    root.setAttribute('aria-busy', String(busy || running || stopping || deleting));
    const over = [...selected.values()].reduce((sum, note) => sum + chars(note.content), 0) > 20000;
    for (const node of root.querySelectorAll('[data-generate]')) {
      node.disabled = busy || running || stopping || deleting || conflicted || !selected.size || over || (!!pendingGenerate && node.dataset.generate !== pendingGenerate.kind);
    }
    const send = find('send');
    send.textContent = running || stopping ? '停止' : '发送';
    if (running || stopping) send.disabled = stopping || deleting;
    find('ask').disabled = busy || running || stopping || deleting || conflicted;
    find('ask').readOnly = !!pendingGenerate;
    find('add').disabled = busy || running || stopping || deleting || conflicted || !!pendingGenerate;
    find('menu').disabled = deleting;
    find('delete').disabled = !chat || stopping || deleting || (busy && !running);
  }
  function renderMaterials() {
    if (!active) return;
    const count = [...selected.values()].reduce((sum, note) => sum + chars(note.content), 0);
    find('material-toggle').textContent = `材料 · ${selected.size} 条 · ${count} / 20000 字 · ${materialOpen ? '收起' : '展开'}`;
    find('materials').hidden = !materialOpen;
    find('over').hidden = count <= 20000;
    const list = find('materials'); list.replaceChildren();
    for (const note of selected.values()) {
      const chip = el('span', 'tag insight-material');
      const label = [...(note.content || note.name)].slice(0, 12).join('');
      const open = button(`${label}…`, 'chip', () => openNote(note));
      open.title = '打开笔记';
      const remove = button('×', 'btn s', async () => {
        if (busy || running || stopping || deleting || conflicted || pendingGenerate) return;
        busy = true; controls();
        const ticket = disposed;
        const previous = new Map(selected);
        selected.delete(note.name); renderMaterials();
        try { await persistSelection(); }
        catch (err) { if (current(ticket)) { selected = previous; failure(err); } }
        finally { if (current(ticket)) { busy = false; renderMaterials(); } }
      });
      remove.setAttribute('aria-label', '移除这条笔记'); remove.disabled = busy || running || stopping || deleting || conflicted || !!pendingGenerate;
      chip.append(open, remove); list.append(chip);
    }
    if (!selected.size) list.append(el('span', 'sub', '还没有添加笔记'));
    controls();
  }
  function renderTurns() {
    if (!active) return;
    const list = find('messages'); list.replaceChildren();
    const turns = chat?.turns || [];
    if (!turns.length) {
      const empty = el('div', 'insight-empty');
      empty.append(el('p', 'sub', '添加材料后，一键洞察，或直接提问。'));
      const start = button('一键洞察', 'save', () => generate('insight'));
      start.dataset.generate = 'insight'; empty.append(start); list.append(empty);
    }
    for (const turn of turns) {
      list.append(el('div', 'insight-user', turn.kind === 'insight' ? '一键洞察' : turn.user.content));
      const group = el('div', 'insight-assistant'), content = el('div', 'insight-answer note-content');
      content.append(renderMarkdown(turn.assistant.content || '', { attachments: [] }));
      const action = button(turn.assistant.saved_name ? '已保存为笔记' : '保存为笔记', 'btn s insight-save', async () => {
        if (deleting || saving.has(turn.id) || turn.assistant.saved_name || turn.assistant.status !== 'completed') return;
        if (new TextEncoder().encode(turn.assistant.content).length > 65536) {
          warning('回复超过笔记正文上限，无法完整保存。请让 AI 总结后保存总结。'); return;
        }
        const ticket = disposed, chatID = chat.id;
        action.disabled = true;
        const task = sessionRequest(`/chats/${chatID}/messages/${turn.request_id}/save`, {
          method: 'POST', body: { version: turn.assistant.version },
        });
        saving.set(turn.id, task);
        try {
          const note = await task;
          if (!current(ticket)) return;
          turn.assistant.saved_name = note.name; onSaved?.(); renderTurns();
        } catch (err) { if (current(ticket)) { failure(err); action.disabled = deleting; } }
        finally { if (saving.get(turn.id) === task) saving.delete(turn.id); }
      });
      action.disabled = deleting || turn.assistant.status !== 'completed' || !!turn.assistant.saved_name || saving.has(turn.id);
      const actions = el('div'); actions.append(action); group.append(content, actions); list.append(group);
    }
    controls();
  }
  function upsert(turn) {
    const index = chat.turns.findIndex(value => value.id === turn.id);
    if (index === -1) chat.turns.push(turn); else chat.turns[index] = turn;
    renderTurns();
  }
  function terminalWarning() {
    if (sourceWarning) { warning(sourceWarning); return; }
    const turn = chat?.turns.at(-1);
    if (turn && ['failed', 'canceled', 'interrupted'].includes(turn.assistant.status)) {
      failure({ code: turn.assistant.error_code || { canceled: 'CHAT_CANCELED', interrupted: 'CHAT_INTERRUPTED' }[turn.assistant.status] || 'CHAT_FAILED' });
    }
  }
  function setURL() {
    if (chat) replaceURL(`/insights?${new URLSearchParams({ chat: chat.id })}`);
  }
  async function writeSelection(path, method, body, ticket) {
    try { return await sessionRequest(path, { method, body }); }
    catch (err) {
      if (current(ticket) && err.status && !err.retryable && err.code !== 'CHAT_CONFLICT') {
        if (method === 'POST') pendingCreate = null; else pendingUpdate = null;
      }
      throw err;
    }
  }
  async function persistSelection() {
    const ticket = disposed, names = [...selected.keys()];
    if (!chat) {
      if (!names.length) return;
      pendingCreate ||= { request_id: crypto.randomUUID(), title: 'AI 洞察', names };
      const created = await writeSelection('/chats', 'POST', pendingCreate, ticket);
      if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
      chat = created; pendingCreate = null; sourceWarning = ''; warning(); setURL(); renderTurns();
      if (namesEqual(chat.materials.map(value => value.name), names)) return;
    }
    if (pendingUpdate) {
      const latest = await sessionRequest(`/chats/${chat.id}`);
      if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
      const applied = latest.version === pendingUpdate.version + 1 &&
        namesEqual(latest.materials.map(value => value.name), pendingUpdate.names) && latest.title === pendingUpdate.title;
      if (!applied && latest.version !== pendingUpdate.version) {
        throw Object.assign(new Error('Chat version changed'), { code: 'CHAT_CONFLICT' });
      }
      if (!applied) {
        const updated = await writeSelection(`/chats/${chat.id}`, 'PATCH', pendingUpdate, ticket);
        if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
        chat = updated;
      } else chat = latest;
      pendingUpdate = null;
    }
    pendingUpdate = { version: chat.version, title: chat.title, names };
    const updated = await writeSelection(`/chats/${chat.id}`, 'PATCH', pendingUpdate, ticket);
    if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
    chat = updated; pendingUpdate = null;
    pendingGenerate = null; sourceWarning = ''; warning(); setURL(); renderTurns();
  }
  function showStatus(value) {
    if (value.version !== chat.version) {
      conflicted = true; chat.turns = value.turns;
      warning('会话已在其他页面更改，请重新进入后继续。');
    } else chat = value;
    const pending = chat.turns.find(turn => turn.request_id === pendingGenerate?.request_id);
    if (pending && pending.assistant.status !== 'running') pendingGenerate = null;
    renderTurns();
  }
  function scheduleRefresh() {
    clearTimeout(pollTimer);
    const ticket = disposed;
    pollTimer = setTimeout(() => refreshChat().catch(err => { if (current(ticket)) failure(err); }), 1500);
  }
  async function refreshChat() {
    if (!chat || !active || stopping || deleting) return;
    const ticket = disposed;
    clearTimeout(pollTimer); pollTimer = null;
    const value = await sessionRequest(`/chats/${chat.id}`);
    if (!current(ticket) || stopping || deleting) return;
    showStatus(value);
    if (chat.turns.some(turn => turn.assistant.status === 'running')) {
      scheduleRefresh();
    } else if (!conflicted) terminalWarning();
  }
  async function cancelGeneration(ticket, expectedVersion) {
    const id = chat.id, deadline = performance.now() + 10000;
    clearTimeout(pollTimer); pollTimer = null;
    let value = await sessionRequest(`/chats/${id}`);
    if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
    if (expectedVersion !== undefined && value.version !== expectedVersion) {
      showStatus(value);
      throw Object.assign(new Error('Chat version changed'), { code: 'CHAT_CONFLICT' });
    }
    const turn = value.turns.find(turn => turn.assistant.status === 'running');
    if (turn) await sessionRequest(`/chats/${id}/messages/${turn.request_id}/cancel`, { method: 'POST', body: {} });
    if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
    // 取消已获服务端确认后再断开本地流，保留 canceled 而非 interrupted 终态。
    controller?.abort();
    await generationDone;
    for (;;) {
      if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
      value = await sessionRequest(`/chats/${id}`);
      if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
      showStatus(value);
      if (!value.turns.some(turn => turn.assistant.status === 'running')) return value;
      if (performance.now() >= deadline) throw Object.assign(new Error('停止结果尚未确认，请重试。'), { code: 'REQUEST_TIMEOUT', retryable: true });
      await new Promise(resolve => setTimeout(resolve, 150));
    }
  }
  async function stopGeneration() {
    if (stopping || deleting || !running) return;
    const ticket = disposed;
    stopping = true; controls();
    try { await cancelGeneration(ticket); if (current(ticket) && !conflicted) terminalWarning(); }
    catch (err) {
      if (!current(ticket)) return;
      try {
        const value = await sessionRequest(`/chats/${chat.id}`);
        if (!current(ticket)) return;
        showStatus(value);
        if (running) failure(err); else terminalWarning();
      } catch (readError) { if (current(ticket)) failure(readError); }
    }
    finally {
      if (current(ticket)) {
        stopping = false; renderMaterials();
        if (running) scheduleRefresh();
      }
    }
  }
  function confirmDelete() {
    closeMenus();
    if (!chat || deleting || find('delete-confirm')) return;
    const ticket = disposed, id = chat.id, version = chat.version;
    const confirm = el('div', 'cfm'); confirm.dataset.chat = 'delete-confirm'; confirm.setAttribute('role', 'alert');
    confirm.append(el('span', '', '删除这个会话？已保存的笔记会保留。'));
    const actions = el('span', 'editor-actions');
    const cancel = button('取消', 'ghost', () => { confirm.remove(); find('menu').focus(); });
    const remove = button('删除', 'danger', async () => {
      if (deleting) return;
      deleting = true; cancel.disabled = remove.disabled = true; warning(); renderTurns(); renderMaterials();
      try {
        await cancelGeneration(ticket, version);
        await Promise.allSettled([...saving.values()]);
        if (!current(ticket)) return;
        try { await sessionRequest(`/chats/${id}`, { method: 'DELETE', body: { version } }); }
        catch (err) {
          if (err.status && !err.retryable && err.code !== 'CHAT_NOT_FOUND') throw err;
          try { await sessionRequest(`/chats/${id}`); }
          catch (readError) {
            if (readError.code !== 'CHAT_NOT_FOUND') throw readError;
            if (current(ticket)) navigate('/insights?new=1', { replace: true });
            return;
          }
          throw err;
        }
        if (current(ticket)) navigate('/insights?new=1', { replace: true });
      } catch (err) {
        if (!current(ticket)) return;
        if (err.code === 'CHAT_NOT_FOUND') navigate('/insights?new=1', { replace: true });
        else failure(err);
      } finally {
        if (current(ticket)) {
          deleting = false; cancel.disabled = remove.disabled = false; renderTurns(); renderMaterials();
          if (running) scheduleRefresh();
        }
      }
    });
    actions.append(cancel, remove); confirm.append(actions); find('top').append(confirm);
    confirm.addEventListener('keydown', event => { if (event.key === 'Escape' && !deleting) { event.preventDefault(); cancel.click(); } });
    cancel.focus();
  }
  async function openHistory() {
    closeMenus();
    if (historyDialog || deleting) return;
    const ticket = disposed, abort = new AbortController();
    const overlay = el('div', 'insight-overlay'), dialog = el('div', 'card insight-picker insight-history');
    dialog.setAttribute('role', 'dialog'); dialog.setAttribute('aria-modal', 'true'); dialog.setAttribute('aria-label', '历史会话');
    const header = el('div', 'insight-between'), close = button('×', 'btn s', () => {
      abort.abort(); overlay.remove(); historyDialog = null; find('menu').focus();
    });
    close.setAttribute('aria-label', '关闭');
    header.append(el('b', '', '历史会话'), close);
    const list = el('div', 'insight-picker-area'), error = el('p', 'sub insight-warning');
    error.hidden = true;
    const seen = new Set();
    let next = '', loading = false;
    const more = button('加载更多', 'pbtn insight-done', load);
    dialog.append(header, list, error, more); overlay.append(dialog); root.append(overlay);
    historyDialog = { overlay, abort };
    async function load() {
      if (loading) return;
      loading = true; more.disabled = true; error.hidden = true;
      try {
        const query = new URLSearchParams({ page_size: '20' }); if (next) query.set('page_token', next);
        const page = await sessionRequest(`/chats?${query}`, { controller: abort });
        if (!current(ticket) || historyDialog?.overlay !== overlay) return;
        for (const value of page.chats) {
          if (seen.has(value.id)) continue;
          seen.add(value.id);
          const row = button('', `ent insight-history-row${value.id === chat?.id ? ' on' : ''}`,
            () => navigate(`/insights?${new URLSearchParams({ chat: value.id })}`));
          const label = el('span', '', value.preview || value.title || 'AI 洞察');
          label.append(el('small', 'sub insight-history-date', `${formatTime(value.updated_at)} · ${value.material_count} 条材料`));
          row.append(label); list.append(row);
        }
        if (!seen.size) list.replaceChildren(el('p', 'sub', '还没有会话'));
        next = page.next_page_token; more.hidden = !next;
      } catch (err) {
        if (current(ticket) && historyDialog?.overlay === overlay && err.name !== 'AbortError') {
          error.textContent = errorText(err); error.hidden = false;
          if (err.status === 401) handleFailure(error, err);
        }
      } finally {
        if (current(ticket) && historyDialog?.overlay === overlay) { loading = false; more.disabled = false; }
      }
    }
    overlay.addEventListener('keydown', event => {
      if (event.key === 'Escape') { event.preventDefault(); close.click(); }
      if (event.key === 'Tab') {
        const nodes = [...dialog.querySelectorAll('button:not(:disabled):not([hidden])')], first = nodes[0], last = nodes.at(-1);
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    });
    close.focus(); await load();
  }
  async function generate(kind) {
    if (busy || running || stopping || deleting || conflicted || !selected.size) return;
    if (pendingGenerate && pendingGenerate.kind !== kind) return;
    const content = pendingGenerate?.content ?? (kind === 'question' ? find('ask').value : '');
    if (kind === 'question' && !content.trim()) return;
    if (chars(content) > 8000 || new TextEncoder().encode(content).length > 32768) {
      warning('问题超过 8000 字或 32768 UTF-8 字节上限，请调整后重试。'); return;
    }
    busy = true; warning(); controls();
    const generation = disposed;
    let finish;
    const done = generationDone = new Promise(resolve => { finish = resolve; });
    try {
      await persistSelectionIfNew();
      if (!active || generation !== disposed) return;
      pendingGenerate ||= { request_id: crypto.randomUUID(), version: chat.version, kind, content };
      const input = pendingGenerate;
      controller = new AbortController();
      const timer = setTimeout(() => controller?.abort(), 125000);
      try {
        const send = () => fetch(`/api/app/v1/chats/${chat.id}/messages`, {
          method: 'POST', credentials: 'same-origin', cache: 'no-store', redirect: 'error',
          headers: { Accept: 'text/event-stream', 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken() },
          body: JSON.stringify(input), signal: controller.signal,
        });
        let response = await send();
        if (response.status === 403 && (await response.clone().json()).error?.code === 'CSRF_REJECTED') {
          await renewCSRF();
          if (!current(generation)) return;
          response = await send();
        }
        await readChatStream(response, event => {
          if (!active || generation !== disposed) throw new DOMException('Stale chat', 'AbortError');
          if (event.type === 'message' || event.type === 'complete') upsert(event.turn);
          else {
            const turn = chat.turns.find(value => value.request_id === input.request_id);
            if (!turn) throw Object.assign(new Error('Missing chat turn'), { code: 'CHAT_PROTOCOL' });
            turn.assistant.content += event.text; renderTurns();
          }
          if (event.type === 'complete') {
            pendingGenerate = null;
            if (input.kind === 'question') find('ask').value = '';
          }
        });
      } finally { clearTimeout(timer); controller = null; }
    } catch (err) {
      if (stopping || deleting || !current(generation)) return;
      if (current(generation) && err.name === 'AbortError') err = Object.assign(new Error('Chat interrupted'), { code: 'CHAT_INTERRUPTED' });
      failure(err);
      if (current(generation) && chat) {
        try {
          await refreshChat();
          if (!current(generation)) return;
          const turn = chat.turns.find(value => value.request_id === pendingGenerate?.request_id);
          if ((turn && turn.assistant.status !== 'running') || (err.status && !err.retryable)) pendingGenerate = null;
          else if (pendingGenerate && !turn) warning('请求结果尚未确认，请使用原内容重试。');
        } catch (readError) { failure(readError); }
      }
    } finally {
      finish(); if (generationDone === done) generationDone = null;
      if (active && generation === disposed) { busy = false; controls(); renderMaterials(); }
    }
  }
  async function persistSelectionIfNew() { if (!chat) await persistSelection(); }

  async function openPicker() {
    if (busy || running || stopping || deleting || conflicted || pendingGenerate || picker) return;
    const generation = disposed;
    const overlay = el('div', 'insight-overlay');
    const dialog = el('div', 'card insight-picker'); dialog.setAttribute('role', 'dialog');
    dialog.setAttribute('aria-modal', 'true'); dialog.setAttribute('aria-label', '选择笔记');
    const header = el('div', 'insight-between'), summary = el('span', 'sub');
    header.append(el('b', '', '选择笔记'), summary);
    const tabs = el('div', 'insight-tabs'), dateTab = button('按日期', 'tab on', () => setTab('date'));
    const tagTab = button('按标签 / 逐条', 'tab', () => setTab('tag'));
    tabs.append(dateTab, tagTab);
    const area = el('div', 'insight-picker-area'), error = el('p', 'sub insight-warning');
    error.hidden = true;
    const done = button('完成', 'save insight-done', async () => {
      if (busy) return;
      done.disabled = true; busy = true; controls();
      const previous = selected;
      selected = new Map(choices);
      try {
        await persistSelection();
        if (current(generation)) { renderMaterials(); closePicker(true); }
      } catch (err) {
        if (current(generation)) {
          selected = previous; failure(err); error.textContent = errorText(err); error.hidden = false; done.disabled = false;
        }
      } finally { if (current(generation)) { busy = false; renderMaterials(); } }
    });
    dialog.append(header, tabs, area, error, done); overlay.append(dialog); root.append(overlay);
    let tab = 'date', choices = new Map(selected), from = '', through = '', chosenTags = new Set();
    let year = Number(today().slice(0, 4)), month = Number(today().slice(5, 7)), years = false;
    let candidates = [], tags = [], loadSequence = 0, loading = false;
    const calendarDays = new Map();
    const abort = new AbortController(), previousFocus = document.activeElement;
    picker = { overlay, abort, previousFocus };
    function summaryText() { summary.textContent = `已选 ${choices.size} 条 · ${[...choices.values()].reduce((sum, note) => sum + chars(note.content), 0)} / 20000 字`; }
    function closePicker(force = false) {
      if (busy && !force) return;
      abort.abort(); overlay.remove(); picker = null;
      queueMicrotask(() => { if (current(generation) && !picker) previousFocus?.focus(); });
    }
    picker.close = closePicker;
    function setTab(value) { tab = value; years = false; render(); load(); }
    async function load() {
      const seq = ++loadSequence;
      error.hidden = true; candidates = []; loading = true; render();
      if (tab === 'date' && !from) { loading = false; return; }
      const query = new URLSearchParams({ page_size: '100' });
      if (from) { query.set('from', from); query.set('until', nextDate(through || from)); }
      for (const tag of chosenTags) query.append('tag', tag);
      const all = [];
      let token = '';
      try {
        do {
          if (token) query.set('page_token', token);
          const page = await sessionRequest(`/chat-materials?${query}`, { controller: abort });
          if (!active || generation !== disposed || picker?.overlay !== overlay || seq !== loadSequence) return;
          all.push(...page.notes);
          if (all.length > 10000) throw Object.assign(new Error('Selection resources'), { code: 'CHAT_RESOURCE_LIMIT' });
          token = page.next_page_token;
        } while (token);
        candidates = all; loading = false; render();
      } catch (err) {
        if (!active || picker?.overlay !== overlay || seq !== loadSequence || err.name === 'AbortError') return;
        loading = false; error.textContent = errorText(err); error.hidden = false;
        if (err.status === 401) handleFailure(error, err);
      }
    }
    function list() {
      const section = el('div', 'insight-picker-list'), count = candidates.length;
      if (loading) return section;
      const head = el('div', 'insight-between');
      head.append(el('span', 'sub', tab === 'date' ? '这段时间的笔记，勾选要用的' : `共 ${count} 条，勾选要用的`),
        button(`全选这 ${count} 条`, 'pbtn insight-select-all', () => {
          for (const note of candidates) choices.set(note.name, note);
          summaryText(); render();
        }));
      section.append(head);
      for (const note of candidates) {
        const row = el('label', 'ent insight-choice'), input = el('input');
        input.type = 'checkbox'; input.checked = choices.has(note.name);
        input.addEventListener('change', () => {
          if (input.checked) choices.set(note.name, note); else choices.delete(note.name);
          summaryText();
        });
        row.append(input, el('span', '', [...note.content || note.name].slice(0, 24).join('')),
          el('em', '', `${formatTime(note.create_time)}${note.tags?.length ? ` · #${note.tags.join(' #')}` : ''}`));
        section.append(row);
      }
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
      prev.setAttribute('aria-label', '上个月'); next.setAttribute('aria-label', '下个月');
      const title = button(`${year} 年 ${month} 月`, 'btn insight-month', () => { years = !years; render(); });
      arrow(title, 'M6 9l6 6 6-6');
      title.setAttribute('aria-label', '选择年月'); header.append(prev, title, next); section.append(header);
      if (years) {
        const row = el('div', 'insight-year');
        const older = arrow(button('', 'btn s', () => { if (year > 1000) { year--; render(); } }), 'M14 6l-6 6 6 6');
        const newer = arrow(button('', 'btn s', () => { if (year < 9999) { year++; render(); } }), 'M10 6l6 6-6 6');
        older.setAttribute('aria-label', '上一年'); newer.setAttribute('aria-label', '下一年');
        row.append(older, el('b', '', `${year} 年`), newer); section.append(row);
        const grid = el('div', 'mg');
        for (let value = 1; value <= 12; value++) grid.append(button(`${value}月`, value === month ? 'ed' : '', () => {
          month = value; years = false; render(); loadCalendar();
        }));
        section.append(grid);
      } else {
        const grid = el('div', 'pc');
        for (const day of ['一', '二', '三', '四', '五', '六', '日']) grid.append(el('span', '', day));
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
          cell.setAttribute('aria-label', value); grid.append(cell);
        }
        section.append(grid);
      }
      const range = el('div', 'insight-between insight-range');
      range.append(el('span', 'sub', !from ? '点选起止日期，筛出这段时间的笔记' : through ? `${from} – ${through} · ${candidates.length} 条` : `${from} – 请选择结束日`));
      if (from) range.append(button('清除', 'pbtn insight-select-all', () => { from = through = ''; render(); load(); }));
      section.append(range);
      if (from && candidates.length) section.append(list());
      else if (from && !loading) section.append(el('p', 'sub', '这段时间没有笔记。'));
      return section;
    }
    function render() {
      summaryText(); dateTab.classList.toggle('on', tab === 'date'); tagTab.classList.toggle('on', tab === 'tag');
      area.replaceChildren();
      if (tab === 'date') area.append(calendar());
      else {
        const filters = el('div', 'insight-tag-filters');
        filters.append(button('全部', `pbtn${chosenTags.size ? '' : ' on'}`, () => { chosenTags.clear(); load(); }));
        for (const tag of tags) filters.append(button(tag, `pbtn${chosenTags.has(tag) ? ' on' : ''}`, () => {
          if (chosenTags.has(tag)) chosenTags.delete(tag); else chosenTags.add(tag);
          load();
        }));
        area.append(filters, list());
      }
    }
    async function loadCalendar() {
      const key = date(year, month, 1).slice(0, 7);
      if (calendarDays.has(key)) return;
      try {
        const pages = await Promise.all(['NORMAL', 'ARCHIVED'].map(state =>
          sessionRequest(`/calendar?${new URLSearchParams({ month: key, state })}`, { controller: abort })));
        if (!current(generation) || picker?.overlay !== overlay) return;
        calendarDays.set(key, new Set(pages.flatMap(page => page.days.filter(day => day.count > 0).map(day => day.date))));
        render();
      } catch (err) {
        if (current(generation) && picker?.overlay === overlay && err.name !== 'AbortError') {
          error.textContent = errorText(err); error.hidden = false;
          if (err.status === 401) handleFailure(error, err);
        }
      }
    }
    overlay.addEventListener('keydown', event => {
      if (event.key === 'Escape') { event.preventDefault(); closePicker(); }
      if (event.key === 'Tab') {
        const nodes = [...dialog.querySelectorAll('button:not(:disabled),input')], first = nodes[0], last = nodes.at(-1);
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    });
    render(); dateTab.focus(); loadCalendar();
    try {
      const pages = await Promise.all(['NORMAL', 'ARCHIVED'].map(state => sessionRequest(`/tags?state=${state}`, { controller: abort })));
      if (!current(generation) || picker?.overlay !== overlay) return;
      tags = [...new Set(pages.flatMap(page => page.tags.map(value => value.tag)))].sort();
      render();
    } catch (err) {
      if (current(generation) && picker?.overlay === overlay && err.name !== 'AbortError') {
        error.textContent = errorText(err); error.hidden = false;
        if (err.status === 401) handleFailure(error, err);
      }
    }
  }

  function destroy() {
    active = false; disposed++; controller?.abort(); controller = null;
    clearTimeout(pollTimer); picker?.abort.abort(); picker?.overlay.remove(); picker = null;
    historyDialog?.abort.abort(); historyDialog?.overlay.remove(); historyDialog = null;
    root?.remove(); root = null; selected.clear(); chat = pendingCreate = pendingUpdate = pendingGenerate = null;
  }
  return {
    async mount(container) {
      active = true;
      root = el('div', 'stage insights-page');
      root.innerHTML = `<div class="insight-top" data-chat="top">
        <div class="insight-heading"><h1 class="vt">AI 洞察</h1><div class="insight-heading-right"><span class="sub">带笔记上下文的对话</span>
          <div class="mw insight-session"><button class="btn" type="button" data-chat="menu" aria-label="会话" aria-haspopup="menu" aria-expanded="false">会话 ▾</button>
            <div class="menu" data-chat="session-menu" role="menu" hidden>
              <button class="mi" type="button" role="menuitem" data-chat="new">新建会话</button>
              <button class="mi" type="button" role="menuitem" data-chat="history">历史会话</button>
              <button class="mi dg" type="button" role="menuitem" data-chat="delete">删除当前会话</button>
            </div></div></div></div>
        <div class="card insight-material-card"><div class="insight-material-header">
          <button class="btn insight-material-toggle" type="button" data-chat="material-toggle"></button>
          <button class="pbtn" type="button" data-chat="add">添加笔记</button>
        </div><div class="insight-materials" data-chat="materials"></div>
        <p class="sub insight-warning insight-over" data-chat="over" hidden>超过字数上限，请减少所选笔记。</p></div>
      </div><div class="insight-messages" data-chat="messages"></div>
      <div class="insight-bottom"><span class="sub insight-warning" role="status" data-chat="warning" hidden></span>
        <form class="insight-input" data-chat="form"><label class="sr" for="ask">向 AI 提问</label>
          <input id="ask" data-chat="ask" placeholder="围绕所选笔记提问或追问…">
          <button class="pbtn" data-chat="one" data-generate="insight" type="button">一键洞察</button>
          <button class="save" data-chat="send" data-generate="question" type="submit">发送</button>
        </form></div>`;
      container.append(root);
      find('material-toggle').addEventListener('click', () => { materialOpen = !materialOpen; renderMaterials(); });
      find('add').addEventListener('click', openPicker);
      find('one').addEventListener('click', () => generate('insight'));
      find('form').addEventListener('submit', event => { event.preventDefault(); if (running) stopGeneration(); else generate('question'); });
      find('menu').addEventListener('click', () => {
        const open = find('session-menu').hidden; closeMenus(); find('session-menu').hidden = !open;
        find('menu').setAttribute('aria-expanded', String(open));
        if (open) find('new').focus();
      });
      find('session-menu').addEventListener('keydown', event => {
        const nodes = [...find('session-menu').querySelectorAll('button:not(:disabled)')], index = nodes.indexOf(document.activeElement);
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          event.preventDefault(); nodes[(index + (event.key === 'ArrowDown' ? 1 : nodes.length - 1)) % nodes.length]?.focus();
        }
      });
      find('new').addEventListener('click', () => navigate('/insights?new=1'));
      find('history').addEventListener('click', openHistory);
      find('delete').addEventListener('click', confirmDelete);
      renderMaterials(); renderTurns();
      const generation = disposed, query = new URLSearchParams(location.search), chatID = query.get('chat'), note = query.get('note');
      busy = true; controls();
      try {
        if (note && /^memos\/[A-Za-z0-9_-]+$/.test(note)) {
          const materials = await sessionRequest('/chat-materials/resolve', { method: 'POST', body: { names: [note] } });
          if (!active || generation !== disposed) return;
          selected = new Map(materials.notes.map(value => [value.name, value]));
          await persistSelection();
        } else {
          let restored = null;
          if (chatID && uuid.test(chatID)) restored = await sessionRequest(`/chats/${chatID}`);
          else if (query.get('new') !== '1') {
            const list = await sessionRequest('/chats?page_size=1');
            if (!current(generation)) return;
            if (list.chats.length) restored = await sessionRequest(`/chats/${list.chats[0].id}`);
          }
          if (!active || generation !== disposed) return;
          chat = restored;
          if (chat) {
            setURL(); renderTurns();
            selected = new Map(chat.materials.map(value => [value.name, { name: value.name, content: '' }]));
            if (selected.size) {
              const materials = await sessionRequest('/chat-materials/resolve', { method: 'POST', body: { names: [...selected.keys()] } });
              if (!active || generation !== disposed) return;
              selected = new Map(materials.notes.map(value => [value.name, value]));
              if (materials.materials.some(value => !chat.materials.some(ref => ref.name === value.name && ref.source_hash === value.source_hash))) {
                failure({ code: 'SOURCE_CHANGED' });
              }
            }
            if (chat.turns.some(turn => turn.assistant.status === 'running')) scheduleRefresh();
            else terminalWarning();
          }
        }
      } catch (err) { failure(err); }
      finally {
        if (active && generation === disposed) {
          busy = false; renderMaterials(); renderTurns();
          if (chat?.turns.some(turn => turn.assistant.status === 'running') && !pollTimer) {
            scheduleRefresh();
          }
        }
      }
    },
    destroy,
  };
}
