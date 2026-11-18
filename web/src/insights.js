import { l, setText, setAttr, resolveText, locale, localize } from './i18n.js';
import { errorMessage as errorText } from './errors.js';
import { createStreamRenderer } from './stream-markdown.js';
import { readChatStream } from './chat-stream.js';
import { el, button } from './dom.js';
import { createModal } from './modal.js';
import { bindMenu } from './menus.js';
import { createMaterialPicker } from './material-picker.js';

const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i;
const chars = value => [...(value || '')].length;

export function createInsightsPage({ sessionRequest, csrfToken, renewCSRF, handleFailure, openNote, navigate, closeMenus, replaceURL, today, formatTime, onSaved, onDirtyChange = () => {} }) {
  let root, active = false, chat = null, busy = false, picker = null;
  let selected = new Map(), pendingCreate = null, pendingUpdate = null, pendingGenerate = null, controller = null, selectionController = null, pollTimer = null;
  let running = false, conflicted = false, sourceWarning = '', disposed = 0;
  let stopping = false, deleting = false, generationDone = null, historyDialog = null;
  let renderTimer = null, lifecycle = null;
  let characterLimit = 20000;
  const saving = new Map(), turnNodes = new Map();
  const find = name => root.querySelector(`[data-chat="${name}"]`);
  const current = ticket => active && ticket === disposed;
  const namesEqual = (a, b) => a.length === b.length && a.every((name, index) => name === b[index]);
  function hasChanges() {
    if (!root) return false;
    const acknowledged = pendingGenerate && chat?.turns.some(turn => turn.request_id === pendingGenerate.request_id);
    const questionSaved = acknowledged && pendingGenerate.kind === 'question' && find('ask').value === pendingGenerate.content;
    return (!!find('ask').value.trim() && !questionSaved) || (!!pendingGenerate && !acknowledged) || !!picker?.hasChanges();
  }

  function warning(text = '') {
    if (!active) return;
    const node = find('warning'); setText(node, text); node.hidden = !text;
  }
  function failure(err) {
    if (!active || err.name === 'AbortError') return;
    const messages = {
      CHAT_MATERIAL_LIMIT: l("超过字数上限，请减少所选笔记。"),
      CHAT_RESOURCE_LIMIT: l("材料请求超过资源限制，请减少选择范围。"),
      SOURCE_CHANGED: l("所选材料已修改，请重新选择这些笔记。"),
      SOURCE_NOT_FOUND: l("所选材料已删除或无法访问，请调整材料。"),
      CHAT_INTERRUPTED: l("回复已中断，已保留收到的内容。请重新读取会话后继续。"),
      CHAT_PROTOCOL: l("聊天流未完整结束，请重新读取会话状态。"),
      CHAT_CANCELED: l("已停止生成，已保留收到的内容。"),
      CHAT_BUSY: l("该会话正在生成，请等待或停止后继续。"),
      CHAT_CONFLICT: l("会话已在其他页面更改，请重新进入后继续。"),
      LLM_DISABLED: l("请在设置中启用 LLM。"),
      LLM_PROTOCOL: l("模型返回的内容未完整结束，已保留部分回复。"),
      CHAT_NOT_FOUND: l("该会话已删除或无法访问。"),
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
    const over = [...selected.values()].reduce((sum, note) => sum + chars(note.content), 0) > characterLimit;
    const changed = chat?.materials.some(ref => selected.get(ref.name)?.source_hash !== ref.source_hash);
    for (const node of root.querySelectorAll('[data-generate]')) {
      node.disabled = busy || running || stopping || deleting || conflicted || !selected.size || over || changed || (!!pendingGenerate && node.dataset.generate !== pendingGenerate.kind);
    }
    const send = find('send');
    setText(send, running || stopping ? l("停止") : l("发送"));
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
    setText(find('material-toggle'), l("材料 · {0} 条 · {1} / {2} 字", { 0: selected.size, 1: count, 2: characterLimit }));
    find('over').hidden = count <= characterLimit;
    const list = find('materials'); list.replaceChildren();
    for (const note of selected.values()) {
      const chip = el('span', 'tag insight-material');
      const label = [...(note.content || note.name)].slice(0, 12).join('');
      const open = button(`${label}…`, 'chip', () => openNote(note));
      setAttr(open, 'title', l("打开笔记"));
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
      setAttr(remove, 'aria-label', l("移除这条笔记")); remove.disabled = busy || running || stopping || deleting || conflicted || !!pendingGenerate;
      chip.append(open, remove); list.append(chip);
    }
    controls();
  }
  function renderTurns() {
    if (!active) return;
    if (renderTimer !== null) { clearTimeout(renderTimer); renderTimer = null; }
    const list = find('messages');
    const turns = chat?.turns || [];
    if (!turns.length) {
      turnNodes.clear(); list.replaceChildren();
      const empty = el('div', 'insight-empty');
      const start = button(l("一键洞察"), 'save', () => generate('insight'));
      start.dataset.generate = 'insight'; empty.append(start); list.append(empty);
      controls(); return;
    }
    list.querySelector('.insight-empty')?.remove();
    const names = new Set(turns.map(turn => turn.id));
    for (const [id, entry] of turnNodes) {
      if (!names.has(id)) { entry.user.remove(); entry.group.remove(); turnNodes.delete(id); }
    }
    for (const turn of turns) {
      let entry = turnNodes.get(turn.id);
      if (!entry) {
        const user = el('div', 'insight-user'), group = el('div', 'insight-assistant');
        const content = el('div', 'insight-answer note-content');
        const action = button('', 'btn s insight-save', async () => {
          const turn = entry.turn;
          if (deleting || saving.has(turn.id) || turn.assistant.saved_name || turn.assistant.status !== 'completed') return;
          if (new TextEncoder().encode(turn.assistant.content).length > 65536) {
            warning(l("回复超过笔记正文上限，无法完整保存。请让 AI 总结后保存总结。")); return;
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
            entry.turn.assistant.saved_name = note.name; onSaved?.(); renderTurns();
          } catch (err) { if (current(ticket)) { failure(err); action.disabled = deleting; } }
          finally { if (saving.get(turn.id) === task) saving.delete(turn.id); }
        });
        const actions = el('div'); actions.append(action); group.append(content, actions);
        entry = { user, group, content, action, turn, text: null, status: '', markdown: createStreamRenderer(content) };
        turnNodes.set(turn.id, entry);
      }
      entry.turn = turn;
      const question = turn.kind === 'insight' ? l("一键洞察") : turn.user.content;
      if (entry.user.textContent !== question) setText(entry.user, question);
      const content = turn.assistant.content || '';
      if (entry.text !== content || entry.status !== turn.assistant.status) {
        entry.markdown.render(content, turn.assistant.status === 'running');
        entry.text = content; entry.status = turn.assistant.status;
      }
      const label = turn.assistant.saved_name ? l("已保存为笔记") : l("保存为笔记");
      if (entry.action.textContent !== label) setText(entry.action, label);
      entry.action.disabled = deleting || turn.assistant.status !== 'completed' || !!turn.assistant.saved_name || saving.has(turn.id);
    }
    let cursor = list.firstElementChild;
    for (const turn of turns) {
      const entry = turnNodes.get(turn.id);
      for (const node of [entry.user, entry.group]) {
        if (node !== cursor) list.insertBefore(node, cursor);
        cursor = node.nextElementSibling;
      }
    }
    controls();
  }
  function scheduleTurns() {
    if (renderTimer === null) renderTimer = setTimeout(() => { renderTimer = null; renderTurns(); }, 100);
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
    const ticket = disposed;
    if (selected.size) {
      const resolving = selectionController = new AbortController();
      try {
        const materials = await sessionRequest('/chat-materials/resolve', {
          method: 'POST', body: { names: [...selected.keys()] }, controller: resolving,
        });
        if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
        if (Number.isSafeInteger(materials.limit) && materials.limit > 0) characterLimit = materials.limit;
        selected = new Map(materials.notes.map(note => [note.name, note]));
      } finally { if (selectionController === resolving) selectionController = null; }
    }
    await persistSelectionReferences();
    if (current(ticket) && chat?.materials.some(ref => selected.get(ref.name)?.source_hash !== ref.source_hash)) failure({ code: 'SOURCE_CHANGED' });
  }
  async function persistSelectionReferences() {
    const ticket = disposed, names = [...selected.keys()];
    if (!chat) {
      if (!names.length) return;
      pendingCreate ||= { request_id: crypto.randomUUID(), title: resolveText(l("洞察")), names };
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
      warning(l("会话已在其他页面更改，请重新进入后继续。"));
    } else chat = value;
    const pending = chat.turns.find(turn => turn.request_id === pendingGenerate?.request_id);
    if (pending && pending.assistant.status !== 'running') pendingGenerate = null;
    renderTurns();
  }
  function scheduleRefresh(delay = 1500) {
    clearTimeout(pollTimer); pollTimer = null;
    if (document.hidden) return;
    const ticket = disposed;
    pollTimer = setTimeout(() => refreshChat(true).catch(err => {
      if (!current(ticket) || stopping || deleting) return;
      failure(err);
      if (current(ticket) && err.retryable && running) scheduleRefresh(Math.min(delay * 2, 10000));
    }), delay);
  }
  async function refreshChat(polling = false) {
    if (!chat || !active || stopping || deleting || (polling && document.hidden)) return;
    const ticket = disposed;
    clearTimeout(pollTimer); pollTimer = null;
    const value = await sessionRequest(`/chats/${chat.id}`);
    if (!current(ticket) || stopping || deleting) return;
    showStatus(value);
    if (polling && !conflicted) warning(sourceWarning);
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
      if (performance.now() >= deadline) throw Object.assign(new Error(l("停止结果尚未确认，请重试。")), { code: 'REQUEST_TIMEOUT', retryable: true });
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
    confirm.append(el('span', '', l("删除这个会话？已保存的笔记会保留。")));
    const actions = el('span', 'editor-actions');
    const cancel = button(l("取消"), 'ghost', () => { confirm.remove(); find('menu').focus(); });
    const remove = button(l("删除"), 'danger', async () => {
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
    const modal = createModal({ parent: root, className: 'card insight-picker insight-history', label: l("历史会话"),
      onClose: () => { abort.abort(); historyDialog = null; } });
    const dialog = modal.dialog, overlay = dialog;
    const header = el('div', 'insight-between'), close = button('×', 'btn s', () => modal.close());
    close.autofocus = true;
    setAttr(close, 'aria-label', l("关闭"));
    header.append(el('h2', '', l("历史会话")), close);
    const list = el('div', 'insight-picker-area'), error = el('p', 'sub insight-warning');
    error.hidden = true;
    const seen = new Set();
    let next = '', loading = false;
    const more = button(l("加载更多"), 'pbtn insight-done', load);
    dialog.append(header, list, error, more);
    historyDialog = { overlay, abort, modal }; modal.show();
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
          const label = el('span', '', value.preview_kind === 'insight' ? l("一键洞察") : value.preview || value.title || l("洞察"));
          label.append(el('small', 'sub insight-history-date', l("{0} · {1} 条材料", { 0: formatTime(value.updated_at), 1: value.material_count })));
          row.append(label); list.append(row);
        }
        if (!seen.size) list.replaceChildren(el('p', 'sub', l("还没有会话")));
        next = page.next_page_token; more.hidden = !next;
      } catch (err) {
        if (current(ticket) && historyDialog?.overlay === overlay && err.name !== 'AbortError') {
          setText(error, errorText(err)); error.hidden = false;
          if (err.status === 401) handleFailure(error, err);
        }
      } finally {
        if (current(ticket) && historyDialog?.overlay === overlay) { loading = false; more.disabled = false; }
      }
    }
    await load();
  }
  async function generate(kind) {
    if (busy || running || stopping || deleting || conflicted || !selected.size) return;
    if (pendingGenerate && pendingGenerate.kind !== kind) return;
    const content = pendingGenerate?.content ?? (kind === 'question' ? find('ask').value : '');
    if (kind === 'question' && !content.trim()) return;
    busy = true; warning(); controls();
    const generation = disposed;
    let finish;
    const done = generationDone = new Promise(resolve => { finish = resolve; });
    try {
      await persistSelectionIfNew();
      if (!active || generation !== disposed) return;
      pendingGenerate ||= { request_id: crypto.randomUUID(), version: chat.version, kind, content, language: locale() };
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
            turn.assistant.content += event.text; scheduleTurns();
          }
          if (event.type === 'complete') {
            pendingGenerate = null;
            if (input.kind === 'question') find('ask').value = '';
            onDirtyChange();
          }
          if (event.type === 'message') onDirtyChange();
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
          else if (pendingGenerate && !turn) warning(l("请求结果尚未确认，请使用原内容重试。"));
        } catch (readError) {
          failure(readError);
          if (current(generation) && readError.retryable && running) scheduleRefresh();
        }
      }
    } finally {
      finish(); if (generationDone === done) generationDone = null;
      if (active && generation === disposed) { busy = false; controls(); renderMaterials(); onDirtyChange(); }
    }
  }
  async function persistSelectionIfNew() { if (!chat) await persistSelection(); }

  function openPicker() {
    if (busy || running || stopping || deleting || conflicted || pendingGenerate || picker) return;
    const ticket = disposed;
    picker = createMaterialPicker({ parent: root, selected, today, formatTime, sessionRequest,
      characterLimit, onLimit: value => { characterLimit = value; renderMaterials(); },
      onFailure: failure, onClose: () => { picker = null; }, onDirtyChange,
      onSubmit: async choices => {
        const previous = selected; selected = choices; busy = true; controls();
        try {
          await persistSelection();
          if (current(ticket)) renderMaterials();
        } catch (err) { if (current(ticket)) selected = previous; throw err; }
        finally { if (current(ticket)) { busy = false; renderMaterials(); onDirtyChange(); } }
      },
    });
  }

  function destroy() {
    active = false; disposed++; lifecycle?.abort(); controller?.abort(); controller = null;
    if (renderTimer !== null) clearTimeout(renderTimer);
    renderTimer = null; turnNodes.clear();
    selectionController?.abort(); selectionController = null;
    clearTimeout(pollTimer); picker?.destroy(); picker = null;
    historyDialog?.abort.abort(); historyDialog?.modal.destroy(); historyDialog = null;
    root?.remove(); root = null; selected.clear(); chat = pendingCreate = pendingUpdate = pendingGenerate = null;
  }
  return {
    async mount(container) {
      active = true; lifecycle = new AbortController();
      document.addEventListener('visibilitychange', () => {
        if (document.hidden) { clearTimeout(pollTimer); pollTimer = null; }
        else if (chat && running && !controller && !busy && !stopping && !deleting) {
          refreshChat(true).catch(err => { failure(err); if (err.retryable && running) scheduleRefresh(); });
        }
      }, { signal: lifecycle.signal });
      root = el('div', 'stage insights-page');
      root.innerHTML = `<div class="insight-top" data-chat="top">
        <div class="insight-heading"><h1 class="vt" data-i18n="洞察">洞察</h1>
          <div class="mw insight-session"><button class="btn" type="button" data-chat="menu" aria-label="会话" aria-haspopup="menu" aria-expanded="false" data-i18n-aria-label="会话"><svg class="i" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16"></path></svg></button>
            <div class="menu" data-chat="session-menu" role="menu" hidden>
              <button class="mi" type="button" role="menuitem" data-chat="new" data-i18n="新建会话">新建会话</button>
              <button class="mi" type="button" role="menuitem" data-chat="history" data-i18n="历史会话">历史会话</button>
              <button class="mi dg" type="button" role="menuitem" data-chat="delete" data-i18n="删除当前会话">删除当前会话</button>
            </div></div></div>
        <div class="card insight-material-card"><div class="insight-material-header">
          <span class="btn insight-material-toggle" data-chat="material-toggle"></span>
          <button class="pbtn" type="button" data-chat="add" data-i18n="添加笔记">添加笔记</button>
        </div><div class="insight-materials" data-chat="materials"></div>
        <p class="sub insight-warning insight-over" data-chat="over" hidden data-i18n="超过字数上限，请减少所选笔记。">超过字数上限，请减少所选笔记。</p></div>
      </div><div class="insight-messages" data-chat="messages"></div>
      <div class="insight-bottom"><span class="sub insight-warning" role="status" data-chat="warning" hidden></span>
        <form class="insight-input" data-chat="form"><label class="sr" for="ask" data-i18n="向 AI 提问">向 AI 提问</label>
          <input id="ask" data-chat="ask" placeholder="围绕所选笔记提问或追问…" data-i18n-placeholder="围绕所选笔记提问或追问…">
          <button class="pbtn" data-chat="one" data-generate="insight" type="button" data-i18n="一键洞察">一键洞察</button>
          <button class="save" data-chat="send" data-generate="question" type="submit" data-i18n="发送">发送</button>
        </form></div>`;
      localize(root);
      container.append(root);
      find('add').addEventListener('click', openPicker);
      find('one').addEventListener('click', () => generate('insight'));
      find('form').addEventListener('submit', event => { event.preventDefault(); if (running) stopGeneration(); else generate('question'); });
      bindMenu(find('menu'), find('session-menu'), { signal: lifecycle.signal });
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
          if (Number.isSafeInteger(materials.limit) && materials.limit > 0) characterLimit = materials.limit;
          selected = new Map(materials.notes.map(value => [value.name, value]));
          await persistSelection();
        } else {
          let restored = null;
          if (chatID && uuid.test(chatID)) restored = await sessionRequest(`/chats/${chatID}`);
          else if (query.get('new') !== '1') {
            const list = await sessionRequest('/chats?page_size=1');
            if (!current(generation)) return;
            if (Number.isSafeInteger(list.character_limit) && list.character_limit > 0) characterLimit = list.character_limit;
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
              if (Number.isSafeInteger(materials.limit) && materials.limit > 0) characterLimit = materials.limit;
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
    hasChanges,
  };
}
