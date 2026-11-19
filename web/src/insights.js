import { l, setText, setAttr, resolveText, locale, localize } from './i18n.js';
import { errorMessage as errorText } from './errors.js';
import { createStreamRenderer } from './stream-markdown.js';
import { readChatStream } from './chat-stream.js';
import { el, button } from './dom.js';
import { createModal } from './modal.js';
import { bindMenu } from './menus.js';
import { createMaterialPicker, materialSize } from './material-picker.js';
import { forgetChatSave, openChatSave } from './chat-save.js';
import { resultOf, usage } from './privacy.js';

const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i;
export function createInsightsPage({ owner, sessionRequest, csrfToken, renewCSRF, handleFailure, navigate, closeMenus, replaceURL,
  today, formatTime, onSaved, readTags, readCalendar, readHistory, readCapabilities, invalidateHistory = () => {}, onDirtyChange = () => {} }) {
  let root, active = false, chat = null, busy = false, picker = null;
  let selected = new Map(), pendingCreate = null, pendingUpdate = null, pendingGenerate = null, controller = null, selectionController = null, pollTimer = null;
  let running = false, conflicted = false, sourceWarning = '', disposed = 0;
  let stopping = false, deleting = false, generationDone = null, historyDialog = null;
  let renderTimer = null, lifecycle = null;
  let characterLimit = 20000, capabilities = {}, provenance, following = true, saveDialog, deleteDialog;
  let retryTurn, uncertain = false, checking = false, streaming = false;
  const turnNodes = new Map();
  const changedNames = new Set();
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
    const over = [...selected.values()].reduce((sum, note) => sum + materialSize(note), 0) > characterLimit;
    const changed = !!sourceWarning || changedNames.size > 0;
    for (const node of root.querySelectorAll('[data-generate]')) {
      node.disabled = busy || running || stopping || deleting || conflicted || uncertain || !selected.size || over || changed;
    }
    const send = find('send');
    setText(send, streaming || running || stopping ? l("停止") : l("发送"));
    if (streaming || running || stopping) send.disabled = stopping || deleting;
    find('ask').readOnly = busy || running || stopping || deleting || conflicted || uncertain || !!pendingGenerate;
    if (pendingGenerate && !streaming && !running) send.disabled = true;
    const conversation = !!chat?.turns.length || streaming || uncertain || !!pendingGenerate;
    find('one').hidden = find('initial').hidden = conversation;
    find('change').hidden = !conversation;
    find('add').disabled = find('change').disabled = busy || running || stopping || deleting || conflicted || uncertain;
    find('save').disabled = !chat?.turns.length || deleting;
    find('check').hidden = !uncertain; find('check').disabled = checking || busy;
    find('retry').hidden = uncertain || !retryTurn || running || busy || conflicted;
    setText(find('retry'), pendingGenerate ? l("再次发送") : l("重新生成"));
    find('server').hidden = !conflicted;
    find('menu').disabled = deleting;
    find('delete').disabled = !chat || stopping || deleting || (busy && !running);
  }
  function renderMaterials() {
    if (!active) return;
    const count = [...selected.values()].reduce((sum, note) => sum + materialSize(note), 0);
    setText(find('material-toggle'), count > characterLimit ?
      l("已选 {0} 条 · {1} / {2} 字", { 0: selected.size, 1: count, 2: characterLimit }) :
      l("已选 {0} 条 · {1} 字", { 0: selected.size, 1: () => count.toLocaleString(locale()) }));
    controls();
  }
  function renderTurns() {
    if (!active) return;
    if (renderTimer !== null) { clearTimeout(renderTimer); renderTimer = null; }
    const list = find('messages');
    const turns = chat?.turns || [];
    const ordered = [];
    const names = new Set(turns.map(turn => turn.id));
    for (const [id, entry] of turnNodes) {
      if (!names.has(id)) { entry.notice.remove(); entry.user.remove(); entry.group.remove(); turnNodes.delete(id); }
    }
    for (const turn of turns) {
      let entry = turnNodes.get(turn.id);
      if (!entry) {
        const user = el('div', 'insight-user'), group = el('div', 'insight-assistant');
        const content = el('div', 'insight-answer note-content');
        const action = button(l("复制正文"), 'btn s insight-save', async () => {
          try { await navigator.clipboard.writeText(entry.turn.assistant.content); }
          catch (err) { failure(err); }
        });
        const wait = el('span', 'sub', l("正在洞察……")), notice = el('small', 'sub', l("材料已更新"));
        group.append(wait, content, action);
        entry = { user, group, content, action, wait, notice, turn, text: null, status: '', markdown: createStreamRenderer(content) };
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
      entry.action.hidden = !content || !['canceled', 'interrupted', 'failed'].includes(turn.assistant.status);
      entry.wait.hidden = !!content || turn.assistant.status !== 'running';
      const before = turns[turns.indexOf(turn) - 1];
      entry.notice.hidden = !before || !turn.material_version || before.material_version === turn.material_version;
      ordered.push(entry.notice, entry.user, entry.group);
    }
    const latest = turns.at(-1);
    find('updated').hidden = !latest || !chat.material_version || chat.material_version === latest.material_version;
    const acknowledged = turns.some(turn => turn.request_id === pendingGenerate?.request_id);
    find('pending').hidden = !streaming || acknowledged;
    if (!find('pending').hidden) {
      setText(find('pending-question'), pendingGenerate?.kind === 'question' ? pendingGenerate.content : l("一键洞察"));
    }
    ordered.push(find('updated'), find('pending'));
    let cursor = list.firstElementChild;
    for (const node of ordered) {
      if (node !== cursor) list.insertBefore(node, cursor);
      cursor = node.nextElementSibling;
    }
    controls();
    if (following) list.scrollTop = list.scrollHeight;
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
    const turn = chat?.turns.at(-1);
    if (!uncertain && turn && ['failed', 'interrupted'].includes(turn.assistant.status)) retryTurn = turn;
    if (sourceWarning) { warning(sourceWarning); return; }
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
    if (selected.size && !capabilities.material_selection) {
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
    const refs = capabilities.material_selection ? { materials: [...selected.values()].map(({ name, source_hash }) => ({ name, source_hash })) } : { names };
    if (!chat) {
      if (!names.length) return;
      pendingCreate ||= { request_id: crypto.randomUUID(), title: resolveText(l("洞察")), ...refs };
      const created = await writeSelection('/chats', 'POST', pendingCreate, ticket);
      if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
      chat = created; pendingCreate = null; sourceWarning = ''; warning(); setURL(); invalidateHistory(); renderTurns();
      if (namesEqual(chat.materials.map(value => value.name), names) && chat.materials.every(ref => selected.get(ref.name)?.source_hash === ref.source_hash)) return;
    }
    if (pendingUpdate) {
      const latest = await sessionRequest(`/chats/${chat.id}`);
      if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
      const applied = latest.version === pendingUpdate.version + 1 &&
        namesEqual(latest.materials.map(value => value.name), pendingUpdate.names || pendingUpdate.materials.map(ref => ref.name)) &&
        (!pendingUpdate.materials || pendingUpdate.materials.every((ref, index) => latest.materials[index].source_hash === ref.source_hash)) &&
        latest.title === pendingUpdate.title;
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
    pendingUpdate = { version: chat.version, title: chat.title, ...refs };
    const updated = await writeSelection(`/chats/${chat.id}`, 'PATCH', pendingUpdate, ticket);
    if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
    chat = updated; pendingUpdate = null;
    pendingGenerate = null; sourceWarning = ''; warning(); setURL(); invalidateHistory(); renderTurns();
  }
  function showStatus(value) {
    if (value.version !== chat.version) {
      conflicted = true; chat.turns = value.turns;
      warning(l("会话已在其他页面更改，请重新进入后继续。"));
    } else chat = value;
    const pending = chat.turns.find(turn => turn.request_id === pendingGenerate?.request_id);
    if (pending) {
      uncertain = false;
      if (pending.kind === 'question' && find('ask').value === pendingGenerate.content) find('ask').value = '';
      if (pending.assistant.status !== 'running') pendingGenerate = null;
    }
    if (uncertain && !pending && pendingGenerate) {
      retryTurn = { kind: pendingGenerate.kind, user: { content: pendingGenerate.content } };
      uncertain = false;
    } else {
      const last = value.turns.at(-1);
      retryTurn = last && ['failed', 'interrupted'].includes(last.assistant.status) ? last : null;
    }
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
    while (current(ticket) && streaming && pendingGenerate && !value.turns.some(turn => turn.request_id === pendingGenerate.request_id)) {
      if (performance.now() >= deadline) throw Object.assign(new Error(l("停止结果尚未确认，请重试。")), { code: 'REQUEST_TIMEOUT', retryable: true });
      await new Promise(resolve => setTimeout(resolve, 150));
      if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
      value = await sessionRequest(`/chats/${id}`);
    }
    if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
    if (expectedVersion !== undefined && value.version !== expectedVersion) {
      showStatus(value);
      throw Object.assign(new Error('Chat version changed'), { code: 'CHAT_CONFLICT' });
    }
    const turn = value.turns.find(turn => turn.assistant.status === 'running');
    if (turn) await sessionRequest(`/chats/${id}/messages/${turn.request_id}/cancel`, { method: 'POST', body: {} });
    if (!current(ticket)) throw new DOMException('Stale chat', 'AbortError');
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
    if (stopping || deleting || (!streaming && !running)) return;
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
    if (!chat || deleting || deleteDialog) return;
    const ticket = disposed, id = chat.id, version = chat.version;
    function deleted() {
      forgetChatSave(owner, id);
      if (current(ticket)) navigate('/insights?new=1', { replace: true });
    }
    const modal = deleteDialog = createModal({ parent: root, className: 'card insight-picker chat-confirm',
      label: l("删除对话"), canClose: () => !deleting, onClose: () => { deleteDialog = null; } });
    const confirm = modal.dialog; confirm.dataset.chat = 'delete-confirm';
    confirm.append(el('h2', '', l("删除对话")), el('p', '', l("已保存的笔记会保留。")));
    const actions = el('span', 'editor-actions');
    const cancel = button(l("取消"), 'ghost', () => modal.close()); cancel.autofocus = true;
    const problem = el('p', 'sub insight-warning'); problem.hidden = true;
    const remove = button(l("删除对话"), 'danger', async () => {
      if (deleting) return;
      deleting = true; cancel.disabled = remove.disabled = true; warning(); renderTurns(); renderMaterials();
      try {
        await cancelGeneration(ticket, version);
        invalidateHistory();
        if (!current(ticket)) return;
        try { await sessionRequest(`/chats/${id}`, { method: 'DELETE', body: { version } }); }
        catch (err) {
          if (err.status && !err.retryable && err.code !== 'CHAT_NOT_FOUND') throw err;
          try { await sessionRequest(`/chats/${id}`); }
          catch (readError) {
            if (readError.code !== 'CHAT_NOT_FOUND') throw readError;
            deleted();
            return;
          }
          throw err;
        }
        deleted();
      } catch (err) {
        if (!current(ticket)) return;
        if (err.code === 'CHAT_NOT_FOUND') deleted();
        else { setText(problem, err.code === 'CHAT_CONFLICT' ? l("会话已在其他页面更改，请重新进入后继续。") : errorText(err)); problem.hidden = false; failure(err); }
      } finally {
        if (current(ticket)) {
          deleting = false; cancel.disabled = remove.disabled = false; renderTurns(); renderMaterials();
          if (running) scheduleRefresh();
        }
      }
    });
    actions.append(cancel, remove); confirm.append(problem, actions); modal.show();
  }
  async function openHistory() {
    closeMenus();
    if (historyDialog || deleting) return;
    const ticket = disposed, abort = new AbortController(); let observer;
    const modal = createModal({ parent: root, className: 'card insight-picker insight-history', label: l("历史会话"),
      onClose: () => { abort.abort(); observer?.disconnect(); historyDialog = null; } });
    const dialog = modal.dialog, overlay = dialog;
    const header = el('div', 'insight-between'), close = button('×', 'btn s', () => modal.close());
    close.autofocus = true;
    setAttr(close, 'aria-label', l("关闭"));
    header.append(el('h2', '', l("历史会话")), close);
    const list = el('div', 'insight-picker-area'), error = el('p', 'sub insight-warning');
    error.hidden = true;
    const seen = new Set(), tokens = new Set();
    let next = '', loading = false, failed = false;
    const more = button(l("加载更多"), 'pbtn insight-done', load);
    list.append(more); dialog.append(header, list, error);
    historyDialog = { overlay, abort, modal }; modal.show();
    if (typeof IntersectionObserver === 'function') observer = new IntersectionObserver(entries => {
      if (next && !failed && entries.some(entry => entry.isIntersecting)) load();
    }, { root: list });
    async function load() {
      if (loading) return;
      loading = true; more.disabled = true; error.hidden = true; failed = false; observer?.unobserve(more);
      try {
        const query = new URLSearchParams({ page_size: '20' }); if (next) query.set('page_token', next);
        const page = !next && readHistory ? await readHistory() : await sessionRequest(`/chats?${query}`, { controller: abort });
        if (!current(ticket) || historyDialog?.overlay !== overlay) return;
        for (const value of page.chats) {
          if (seen.has(value.id)) continue;
          seen.add(value.id);
          const row = button('', `ent insight-history-row${value.id === chat?.id ? ' on' : ''}`,
            () => navigate(`/insights?${new URLSearchParams({ chat: value.id })}`));
          const label = el('span', '', value.preview_kind === 'insight' ? l("一键洞察") : value.preview || value.title || l("洞察"));
          label.append(el('small', 'sub insight-history-date', l("{0} · {1} 条材料", { 0: formatTime(value.updated_at), 1: value.material_count })));
          row.append(label); list.insertBefore(row, more);
        }
        next = page.next_page_token;
        if (tokens.has(next)) next = '';
        if (!seen.size && !next) list.insertBefore(el('p', 'sub', l("还没有会话")), more);
        if (next) tokens.add(next); more.hidden = !next;
        setText(more, l("加载更多")); if (next) observer?.observe(more);
      } catch (err) {
        if (current(ticket) && historyDialog?.overlay === overlay && err.name !== 'AbortError') {
          failed = true; setText(error, errorText(err)); error.hidden = false; setText(more, l("重试"));
          if (err.status === 401) handleFailure(error, err);
        }
      } finally {
        if (current(ticket) && historyDialog?.overlay === overlay) { loading = false; more.disabled = false; }
      }
    }
    await load();
  }
  async function generate(kind, retry = false) {
    if (busy || running || stopping || deleting || conflicted || !selected.size) return;
    if (pendingGenerate && pendingGenerate.kind !== kind) return;
    const content = pendingGenerate?.content ?? (retry ? retryTurn.user.content : kind === 'question' ? find('ask').value : '');
    if (kind === 'question' && !content.trim()) return;
    busy = streaming = true; retryTurn = null; warning();
    const started = performance.now();
    let measured = false;
    pendingGenerate ||= { request_id: crypto.randomUUID(), version: chat?.version, kind, content, language: locale() };
    renderTurns();
    const generation = disposed;
    let finish;
    const done = generationDone = new Promise(resolve => { finish = resolve; });
    try {
      await persistSelectionIfNew();
      if (!active || generation !== disposed) return;
      pendingGenerate.version = chat.version;
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
          if (event.type === 'message' || event.type === 'complete') {
            upsert(event.turn);
            if (input.kind === 'question' && find('ask').value === input.content) find('ask').value = '';
          }
          else {
            const turn = chat.turns.find(value => value.request_id === input.request_id);
            if (!turn) throw Object.assign(new Error('Missing chat turn'), { code: 'CHAT_PROTOCOL' });
            if (!measured && event.text) { usage('insight', 'success', performance.now() - started); measured = true; }
            turn.assistant.content += event.text; scheduleTurns();
          }
          if (event.type === 'complete') {
            if (!measured && event.turn.assistant.content) { usage('insight', 'success', performance.now() - started); measured = true; }
            pendingGenerate = null;
            invalidateHistory();
            onDirtyChange();
          }
          if (event.type === 'message') onDirtyChange();
        });
      } finally { clearTimeout(timer); controller = null; }
    } catch (err) {
      if (!measured) usage('insight', stopping || deleting ? 'canceled' : resultOf(err), performance.now() - started);
      if (stopping || deleting || !current(generation)) return;
      if (current(generation) && err.name === 'AbortError') err = Object.assign(new Error('Chat interrupted'), { code: 'CHAT_INTERRUPTED' });
      failure(err); uncertain = true;
      if (current(generation) && chat) {
        try {
          await refreshChat();
          if (!current(generation)) return;
          const turn = chat.turns.find(value => value.request_id === pendingGenerate?.request_id);
          if ((turn && turn.assistant.status !== 'running') || (err.status && !err.retryable)) pendingGenerate = null;
          else if (pendingGenerate && !turn) warning(l("未找到此请求的回复。"));
        } catch (readError) {
          failure(readError);
          if (current(generation) && readError.retryable && running) scheduleRefresh();
        }
      }
    } finally {
      finish(); if (generationDone === done) generationDone = null;
      if (active && generation === disposed) { streaming = busy = false; renderTurns(); renderMaterials(); onDirtyChange(); }
    }
  }
  async function persistSelectionIfNew() { if (!chat) await persistSelection(); }

  async function restoreMaterials(generation) {
    selected = new Map(chat.materials.map(ref => [ref.name, { ...ref }]));
    if (!selected.size) return;
    let names = [...selected.keys()];
    while (names.length && current(generation)) {
      try {
        const resolved = await sessionRequest('/chat-materials/resolve', { method: 'POST', body: { names } });
        if (!current(generation)) return;
        if (resolved.limit > 0) characterLimit = resolved.limit;
        for (const note of resolved.notes) {
          const original = selected.get(note.name);
          if (note.source_hash !== original.source_hash) changedNames.add(note.name);
          selected.set(note.name, { ...note, source_hash: original.source_hash });
        }
        if (changedNames.size && !sourceWarning) failure({ code: 'SOURCE_CHANGED' });
        return;
      } catch (err) {
        if (!current(generation)) return;
        if (err.code !== 'SOURCE_NOT_FOUND' || !err.changed_names?.length) throw err;
        const remaining = names.filter(name => !err.changed_names.includes(name));
        if (remaining.length === names.length) throw err;
        for (const name of err.changed_names) changedNames.add(name);
        failure(err); names = remaining;
      }
    }
  }

  function openPicker() {
    if (busy || running || stopping || deleting || conflicted || pendingGenerate || picker) return;
    const ticket = disposed;
    picker = createMaterialPicker({ parent: root, selected, today, sessionRequest, previous: provenance, changedNames,
      readTags: readTags || (state => sessionRequest(`/tags?state=${state}`)),
      readCalendar: readCalendar || ((state, month) => sessionRequest(`/calendar?${new URLSearchParams({ month, state })}`)),
      modern: !!capabilities.material_selection, existing: !!chat?.materials.length,
      characterLimit, onLimit: value => { characterLimit = value; renderMaterials(); },
      onFailure: failure, onClose: () => { picker = null; }, onDirtyChange,
      onSubmit: async (choices, groups) => {
        const previous = selected; selected = choices; busy = true; controls();
        try {
          await persistSelection();
          if (current(ticket)) { changedNames.clear(); provenance = groups; renderMaterials(); }
        } catch (err) {
          if (current(ticket)) {
            selected = previous;
            if (err.code === 'CHAT_CONFLICT') {
              const latest = await sessionRequest(`/chats/${chat.id}`);
              if (current(ticket)) showStatus(latest);
            }
          }
          throw err;
        }
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
    saveDialog?.destroy(); deleteDialog?.destroy();
    root?.remove(); root = null; selected.clear(); chat = pendingCreate = pendingUpdate = pendingGenerate = null;
  }
  return {
    async mount(container) {
      active = true; lifecycle = new AbortController();
      document.addEventListener('visibilitychange', () => {
        if (document.hidden) { clearTimeout(pollTimer); pollTimer = null; }
        else if (chat && (running || uncertain) && !controller && !busy && !stopping && !deleting) {
          refreshChat(true).catch(err => { failure(err); if (err.retryable && running) scheduleRefresh(); });
        }
      }, { signal: lifecycle.signal });
      root = el('div', 'stage insights-page');
      root.innerHTML = `<div class="insight-top" data-chat="top">
        <div class="insight-heading"><h1 class="vt" data-i18n="洞察">洞察</h1>
          <div class="mw insight-session"><button class="btn" type="button" data-chat="menu" aria-label="更多" aria-haspopup="menu" aria-expanded="false" data-i18n-aria-label="更多"><svg class="i" viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/></svg></button>
            <div class="menu" data-chat="session-menu" role="menu" hidden>
              <button class="mi" type="button" role="menuitem" data-chat="new" data-i18n="新建会话">新建会话</button>
              <button class="mi" type="button" role="menuitem" data-chat="history" data-i18n="历史会话">历史会话</button>
              <button class="mi" type="button" role="menuitem" data-chat="change" data-i18n="修改材料">修改材料</button>
              <button class="mi" type="button" role="menuitem" data-chat="save" data-i18n="保存为笔记">保存为笔记</button>
              <button class="mi dg" type="button" role="menuitem" data-chat="delete" data-i18n="删除对话">删除对话</button>
            </div></div></div>
        <div class="insight-material-header" data-chat="initial">
          <span class="sub" data-chat="material-toggle"></span>
          <button class="pbtn" type="button" data-chat="add" data-i18n="添加笔记">添加笔记</button>
        </div>
      </div><div class="insight-messages" data-chat="messages" tabindex="0" role="region" aria-label="对话消息" data-i18n-aria-label="对话消息">
        <small class="sub" data-chat="updated" data-i18n="材料已更新" hidden>材料已更新</small>
        <div data-chat="pending" hidden><div class="insight-user" data-chat="pending-question"></div><span class="sub" role="status" data-i18n="正在洞察……">正在洞察……</span></div>
      </div>
      <div class="insight-bottom"><span class="sub insight-warning" role="status" data-chat="warning" hidden></span>
        <div class="chat-tools"><button type="button" class="pbtn" data-chat="latest" data-i18n="回到最新" hidden>回到最新</button>
        <button type="button" class="pbtn" data-chat="check" data-i18n="检查回复" hidden>检查回复</button>
        <button type="button" class="pbtn" data-chat="retry" data-i18n="重新生成" hidden>重新生成</button>
        <button type="button" class="pbtn" data-chat="server" data-i18n="使用服务器版本" hidden>使用服务器版本</button></div>
        <form class="insight-input" data-chat="form"><label class="sr" for="ask" data-i18n="向 AI 提问">向 AI 提问</label>
          <textarea id="ask" rows="2" data-chat="ask" placeholder="围绕所选笔记提问或追问…" data-i18n-placeholder="围绕所选笔记提问或追问…"></textarea>
          <button class="pbtn" data-chat="one" data-generate="insight" type="button" data-i18n="一键洞察">一键洞察</button>
          <button class="save" data-chat="send" data-generate="question" type="submit" data-i18n="发送">发送</button>
        </form></div>`;
      localize(root);
      container.append(root);
      const viewport = window.visualViewport;
      if (viewport) {
        const resize = () => { root.closest('.main')?.style.setProperty('--chat-viewport', `${viewport.height}px`); };
        resize(); viewport.addEventListener('resize', resize, { signal: lifecycle.signal });
        lifecycle.signal.addEventListener('abort', () => root?.closest('.main')?.style.removeProperty('--chat-viewport'), { once: true });
      }
      find('add').addEventListener('click', openPicker);
      find('change').addEventListener('click', openPicker);
      find('save').addEventListener('click', () => {
        if (saveDialog || !chat) return;
        saveDialog = openChatSave({ parent: root, owner, chat, modern: !!capabilities.save_selection, sessionRequest,
          onSaved: note => { onSaved?.(note); warning(l("已保存为笔记")); },
          onFailure: failure, onClose: () => { saveDialog = null; } });
      });
      const messages = find('messages');
      messages.addEventListener('scroll', () => {
        following = messages.scrollHeight - messages.scrollTop - messages.clientHeight < 28; find('latest').hidden = following;
      }, { passive: true });
      find('latest').addEventListener('click', () => { messages.scrollTop = messages.scrollHeight; });
      find('check').addEventListener('click', checkResult);
      window.addEventListener('online', () => { if (uncertain) checkResult(); }, { signal: lifecycle.signal });
      async function checkResult() {
        if (checking || !active) return;
        checking = true; controls();
        try { await refreshChat(); } catch (err) { failure(err); }
        finally { checking = false; controls(); }
      }
      find('retry').addEventListener('click', () => retryTurn && generate(retryTurn.kind, true));
      find('server').addEventListener('click', () => {
        const dialog = createModal({ parent: root, className: 'card insight-picker chat-confirm', label: l("使用服务器版本") });
        const cancel = button(l("取消"), 'ghost', () => dialog.close()); cancel.autofocus = true;
        dialog.dialog.append(el('p', '', l("使用服务器版本会舍弃未发送的提问和材料修改。")), cancel,
          button(l("使用服务器版本"), 'save', () => {
            dialog.close(); find('ask').value = ''; pendingUpdate = pendingGenerate = null; onDirtyChange();
            location.reload();
          }));
        dialog.show();
      });
      find('one').addEventListener('click', () => generate('insight'));
      find('ask').addEventListener('keydown', event => {
        if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
          event.preventDefault(); if (!find('ask').readOnly) generate('question');
        }
      });
      find('form').addEventListener('submit', event => { event.preventDefault(); if (streaming || running) stopGeneration(); else generate('question'); });
      bindMenu(find('menu'), find('session-menu'), { signal: lifecycle.signal });
      find('new').addEventListener('click', () => navigate('/insights?new=1'));
      find('history').addEventListener('click', openHistory);
      find('delete').addEventListener('click', confirmDelete);
      renderMaterials(); renderTurns();
      const generation = disposed, query = new URLSearchParams(location.search), chatID = query.get('chat'), note = query.get('note');
      busy = true; controls();
      try {
        try { capabilities = (await (readCapabilities ? readCapabilities() : sessionRequest('/capabilities'))).chat || {}; }
        catch (err) { if (err.status !== 404) throw err; }
        if (!current(generation)) return;
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
            const list = readHistory ? await readHistory() : await sessionRequest('/chats?page_size=20');
            if (!current(generation)) return;
            if (Number.isSafeInteger(list.character_limit) && list.character_limit > 0) characterLimit = list.character_limit;
            if (list.chats.length) restored = await sessionRequest(`/chats/${list.chats[0].id}`);
          }
          if (!active || generation !== disposed) return;
          chat = restored;
          if (chat) {
            setURL(); renderTurns();
            await restoreMaterials(generation);
            if (!current(generation)) return;
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
