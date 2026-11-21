import { l, setText, setAttr, locale } from './i18n.js';
import { el, button } from './dom.js';
import { createModal } from './modal.js';
import { errorMessage } from './errors.js';
import { usage, resultOf } from './privacy.js';

const bytes = text => new TextEncoder().encode(text).length;
const key = (request, role) => `${request}:${role}`;
const storageKeyFor = (owner, chatID) => `moerise.chat-save.v1:${JSON.stringify([owner, chatID])}`;

export function forgetChatSave(owner, chatID) {
  try { sessionStorage.removeItem(storageKeyFor(owner, chatID)); } catch {}
}

export function openChatSave({ parent, owner, chat, sessionRequest, onSaved, onFailure, onClose }) {
  const storageKey = storageKeyFor(owner, chat.id);
  const picked = new Set(), entries = [], rows = [];
  let alive = true, busy = false, frozen, confirmation = false, exceeded = false;
  try { frozen = JSON.parse(sessionStorage.getItem(storageKey)); } catch {}
  function forget(body) {
    try {
      if (JSON.parse(sessionStorage.getItem(storageKey))?.operation_key === body.operation_key) sessionStorage.removeItem(storageKey);
    } catch {}
  }
  const started = performance.now();
  function recordSave(selected, result) {
    const elapsed = performance.now() - started;
    if (selected.length > 1) usage('save_multiple', result, elapsed);
    else if (selected[0]?.role === 'assistant') usage('save_answer', result, elapsed);
    if (selected.some(entry => entry.role === 'user')) usage('save_question', result, elapsed);
  }
  const modal = createModal({ parent, className: 'card insight-picker', label: l("保存为笔记"),
    canClose: () => !busy, onClose: () => { alive = false; onClose(); } });
  const header = el('div', 'insight-between'), close = button('×', 'btn s', () => modal.close());
  setAttr(close, 'aria-label', l("关闭")); header.append(el('h2', '', l("保存为笔记")), close);
  const list = el('div', 'insight-picker-area'), status = el('span', 'sub'), warning = el('p', 'sub insight-warning');
  const footer = el('div', 'material-footer'), save = button('', 'save', submit);
  for (const turn of chat.turns) {
    for (const role of ['user', 'assistant']) {
      if (role === 'user' && turn.kind !== 'question') continue;
      const message = turn[role], stopped = role === 'assistant' && (message.status === 'canceled' || message.status === 'truncated');
      const ok = !!message.content && (role === 'user' || message.status === 'completed' || stopped);
      const entry = { request_id: turn.request_id, role, message, stopped, enabled: ok, id: key(turn.request_id, role) };
      entries.push(entry);
      const row = el('label', 'ent save-choice'), check = el('input'); check.type = 'checkbox';
      const label = el('span'), state = role === 'user' || message.status === 'completed' ? l("已完成") :
        message.status === 'truncated' ? l("已截断（内容不完整）") : stopped ? l("已停止（内容不完整）") : message.status === 'running' ? l("正在生成") : l("未完成");
      label.append(el('b', '', role === 'user' ? l("我") : 'Moerise'), el('small', 'sub', state),
        el('span', '', [...message.content || ''].slice(0, 140).join('')));
      row.append(check, label); list.append(row); rows.push(check);
      check.addEventListener('change', () => {
        if (check.checked) picked.add(entry.id); else picked.delete(entry.id);
        confirmation = false; refresh();
      });
    }
  }
  if (frozen) for (const ref of frozen.messages) picked.add(key(ref.request_id, ref.role));
  else {
    const latest = entries.findLast(entry => entry.role === 'assistant' && entry.message.status === 'completed');
    if (latest) picked.add(latest.id);
  }
  footer.append(status, save); modal.dialog.append(header, list, warning, footer);
  function selection() { return entries.filter(entry => picked.has(entry.id)); }
  function refresh() {
    const selected = selection(), language = frozen?.language || locale();
    const content = selected.length === 1 ? selected[0].message.content : selected.map(entry =>
      `**${entry.role === 'assistant' ? 'Moerise' : language === 'en' ? 'Me' : '我'}**\n\n${entry.message.content}`).join('\n\n');
    const size = bytes(content);
    if (size > 65536 && !exceeded) recordSave(selected, 'over_limit');
    exceeded = size > 65536;
    for (const [index, entry] of entries.entries()) {
      rows[index].checked = picked.has(entry.id);
      rows[index].disabled = !entry.enabled || busy || !!frozen;
    }
    setText(status, l("{0} 条消息 · {1} 字节", { 0: selected.length, 1: size }));
    const message = size > 65536 ? l("正文超过 65,536 字节，请减少消息。") : confirmation ?
      l("包含已停止的回答，内容不完整。仍要保存吗？") : '';
    setText(warning, message); warning.hidden = !message;
    setText(save, busy ? l("正在保存…") : frozen ? l("重试保存") : confirmation ? l("确认保存") : l("保存为笔记"));
    save.disabled = busy || (!frozen && (!selected.length || size > 65536));
    close.disabled = busy;
  }
  async function submit() {
    if (save.disabled) return;
    const selected = selection();
    if (!frozen && selected.some(entry => entry.stopped) && !confirmation) { confirmation = true; refresh(); return; }
    const body = frozen || { operation_key: crypto.randomUUID(), language: locale(), confirm_incomplete: confirmation,
      messages: selected.map(entry => ({ request_id: entry.request_id, role: entry.role, version: entry.message.version })) };
    try { sessionStorage.setItem(storageKey, JSON.stringify(body)); }
    catch {
      setText(warning, l("浏览器无法暂存操作，请允许此站点使用会话存储后重试。")); warning.hidden = false;
      return;
    }
    busy = true; refresh();
    try {
      const note = await sessionRequest(`/chats/${chat.id}/save-selection`, { method: 'POST', body });
      if (!note?.name) throw Object.assign(new Error(), { code: 'NOTE_SAVE_UNCONFIRMED' });
      forget(body);
      recordSave(selected, 'success');
      if (alive) { onSaved(note); modal.close(true); }
    } catch (err) {
      const uncertain = !err.status || err.status >= 500 || err.retryable || (frozen && [401, 403].includes(err.status));
      if (!uncertain) forget(body);
      if (!alive) return;
      recordSave(selected, resultOf(err));
      frozen = uncertain ? body : null;
      busy = false; refresh(); setText(warning, errorMessage(err)); warning.hidden = false;
      if (err.status === 401) onFailure(err);
    }
  }
  refresh(); modal.show();
  return modal;
}
