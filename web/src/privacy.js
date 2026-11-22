import { el, button } from './dom.js';
import { l, setAttr } from './i18n.js';
import { createModal } from './modal.js';

const choiceKey = 'moerise.privacy.v1', batchKey = 'moerise.aggregate.v1', quotaKey = 'moerise.sent.v1';
const ownerKey = 'moerise.aggregate-owner.v1';
const day = 86400000;
const operations = new Set('create edit enqueue save_ack sync search_keyword search_semantic search_hybrid review walk stars insight material_date material_tags material_manual save_answer save_multiple save_question startup client_error request_error crash anr'.split(' '));
const results = new Set('success failure canceled conflict unknown timeout over_limit'.split(' '));
const sources = new Set(['user', 'prefetch', 'background']);
const codes = new Set('SOURCE_CHANGED WRITE_CONFLICT IDEMPOTENCY_CONFLICT CHAT_MATERIAL_CHANGED WRITE_OUTCOME_UNKNOWN NETWORK_ERROR REQUEST_TIMEOUT SESSION_UNAUTHORIZED CORE_UNAVAILABLE UPSTREAM_UNAVAILABLE RATE_LIMITED SEARCH_RESOURCE_LIMIT'.split(' '));
const consentLabel = l("分享使用与故障统计");
const explanation = l("开启后，发送功能使用次数、等待时间和错误类型等汇总，帮助改进 Moerise。不含笔记、搜索词、提问和回答正文。");
let account = '', api, allowed = false, coreVersion = 'unknown', revision = 0, welcome, diagnostic, sending;
let memoryChoice = '', timer;
function get(key) { try { return localStorage.getItem(key); } catch { return null; } }
function put(key, value) {
  try { if (value == null) localStorage.removeItem(key); else localStorage.setItem(key, value); return true; }
  catch { return false; }
}
function version(value) { return /^(?:0|[1-9]\d{0,2})(?:\.(?:0|[1-9]\d{0,2})){2}$/.test(value || '') ? value : 'unknown'; }
function choice() { return get(choiceKey) || memoryChoice; }
function enabled() { return !!account && get(ownerKey) === account && allowed && choice() === 'on'; }
function discardBatch() {
  sending?.abort(); clearTimeout(timer); timer = null;
  put(ownerKey, null); put(batchKey, null);
}
function choose(on) {
  memoryChoice = on && allowed ? 'on' : 'off';
  put(choiceKey, memoryChoice);
  if (memoryChoice === 'on') put(ownerKey, account);
  else discardBatch();
  window.dispatchEvent(new Event('moeriseprivacy'));
}
function batch() {
  try { return JSON.parse(get(batchKey)) || {}; } catch { return {}; }
}
function duration(ms) {
  if (!Number.isFinite(ms) || ms < 0) return 'unknown';
  return ms < 300 ? 'lt300ms' : ms < 1000 ? 'lt1s' : ms < 3000 ? 'lt3s' : ms < 10000 ? 'lt10s' : 'ge10s';
}
function major() {
  const match = ['Edg', 'Firefox', 'Chrome', 'Version'].map(brand =>
    navigator.userAgent.match(new RegExp(`${brand}/(\\d{1,3})(?:\\.| )`))).find(Boolean);
  return match?.[1] || 'unknown';
}
function metadata() {
  return { schema: 1, platform: 'web', client_version: version(document.querySelector('meta[name="moerise-version"]')?.content),
    core_version: coreVersion, system_major: major() };
}
export function resultOf(error) {
  if (error?.name === 'AbortError') return 'canceled';
  if (error?.status === 409 || /CONFLICT|SOURCE_CHANGED/.test(error?.code || '')) return 'conflict';
  if (error?.code === 'REQUEST_TIMEOUT') return 'timeout';
  if (/NETWORK_ERROR|UNCONFIRMED|OUTCOME_UNKNOWN/.test(error?.code || '')) return 'unknown';
  return 'failure';
}
export function diagnose(error) {
  if (!account || error?.name === 'AbortError') return;
  diagnostic = { time: new Date().toISOString(), ...metadata(), operation: 'request_error', result: resultOf(error),
    code: codes.has(error?.code) ? error.code : 'unknown' };
  if (/^(?:[a-f0-9]{16,32}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12})$/i.test(error?.requestId || '')) {
    diagnostic.request_id = error.requestId;
  }
  usage('request_error', diagnostic.result);
}
export function usage(operation, result, elapsed, source = 'user') {
  if (!enabled() || !operations.has(operation) || !results.has(result) || !sources.has(source)) return;
  const data = batch(), now = Date.now(), meta = metadata();
  if (data.owner !== account) return;
  const rows = (data.rows || []).filter(row => now - row.since < 2 * day && row.since <= now);
  const bucket = duration(elapsed);
  const same = rows.find(row => row.operation === operation && row.result === result && row.duration === bucket && row.source === source);
  if (same) same.count = Math.min(65535, same.count + 1);
  else if (rows.length < 256) rows.push({ operation, result, duration: bucket, source, count: 1, since: now });
  put(batchKey, JSON.stringify({ owner: account, meta, rows }));
  if (!timer && !document.hidden) timer = setTimeout(flush, 1000);
}
async function flush() {
  clearTimeout(timer); timer = null;
  const stored = batch();
  if (!enabled() || document.hidden || sending || stored.owner !== account || !stored.rows?.length ||
      Date.now() - Number(get(quotaKey) || 0) < day) return;
  const stamp = revision;
  try {
    const cap = await api('/capabilities');
    if (stamp !== revision || !enabled() || document.hidden) return;
    if (cap?.telemetry?.allowed !== true || cap.telemetry.schema !== 1) {
      allowed = false; discardBatch(); window.dispatchEvent(new Event('moeriseprivacy')); return;
    }
    const now = Date.now(), data = batch();
    if (data.owner !== account) return;
    const rows = (data.rows || []).filter(row => row.since <= now && now - row.since < 2 * day);
    if (!rows.length || now - Number(get(quotaKey) || 0) < day) return;
    if (!put(quotaKey, String(now))) return;
    put(batchKey, JSON.stringify({ owner: account, rows: [] }));
    const payload = { ...data.meta, rows: rows.map(({ since, ...row }) => row) };
    sending = new AbortController();
    await api('/telemetry', { method: 'POST', body: payload, controller: sending });
  } catch {} finally { sending = null; }
}
function prepareBatch() {
  if (!enabled()) return;
  const data = batch();
  const meta = metadata(), now = Date.now();
  const rows = data.owner === account && JSON.stringify(data.meta) === JSON.stringify(meta)
    ? (data.rows || []).filter(row => row.since <= now && now - row.since < 2 * day) : [];
  put(batchKey, JSON.stringify({ owner: account, meta, rows }));
}
export function disconnectPrivacy(clear = false) {
  revision++; sending?.abort(); clearTimeout(timer); timer = null;
  if (clear && account && get(ownerKey) === account) discardBatch();
  account = ''; allowed = false; diagnostic = null;
  welcome?.destroy();
}
export async function connectPrivacy(identity, request, editorReadyAt, readCapabilities) {
  const stamp = ++revision;
  api = request;
  const name = `${identity.instance_id || ''}\0${identity.user.name}`;
  let caps;
  try { caps = await readCapabilities(); } catch {}
  if (stamp !== revision) return;
  coreVersion = version(caps?.version);
  allowed = caps?.telemetry?.schema === 1 && caps.telemetry.allowed === true;
  try {
    const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(name));
    if (stamp !== revision) return;
    account = Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('');
  } catch { if (stamp !== revision) return; allowed = false; }
  if (account && allowed && choice() === 'on') {
    if (batch().owner !== account) put(batchKey, null);
    put(ownerKey, account);
  } else discardBatch();
  prepareBatch();
  if (enabled()) flush();
  if (editorReadyAt != null) usage('startup', 'success', editorReadyAt);
  if (!choice()) showWelcome();
  window.dispatchEvent(new Event('moeriseprivacy'));
}
function issues() {
  const link = el('a', 'dlink', l("反馈问题"));
  link.href = 'https://github.com/Fermin214/Moerise/issues'; link.target = '_blank'; link.rel = 'noopener noreferrer';
  return link;
}
function toggle(onChange) {
  const label = el('label', 'privacy-switch'), control = el('input');
  control.type = 'checkbox'; control.setAttribute('role', 'switch'); control.dataset.privacy = '';
  control.checked = allowed && choice() === 'on'; control.disabled = !allowed;
  control.addEventListener('change', () => onChange?.(control.checked));
  label.append(el('span', '', consentLabel), control);
  return { label, control };
}
function information(parent) {
  parent.append(el('p', '', l("Moerise 项目维护者接收按日汇总的计数，用于改善功能、等待时间和可靠性。")),
    el('p', '', l("字段示例：Web、客户端版本、Core 版本、浏览器大版本、保存成功、耗时小于 1 秒、用户操作、3 次。")),
    el('p', '', l("本机最多缓存 48 小时，每 24 小时最多发送一次。接收端保留 30 天的日汇总。关闭后清除未发送的计数。")));
}
function preview() {
  const modal = createModal({ parent: document.body, className: 'card privacy-dialog', label: l("诊断摘要") });
  const area = el('textarea'); area.rows = 9; setAttr(area, 'aria-label', l("诊断摘要"));
  const recent = diagnostic && Date.now() - Date.parse(diagnostic.time) < day;
  area.value = JSON.stringify(recent ? diagnostic : metadata(), null, 2);
  const copy = button(l("复制诊断摘要"), 'save', async () => {
    try { await navigator.clipboard.writeText(area.value); }
    catch { area.focus(); area.select(); }
  });
  modal.dialog.append(el('h2', '', l("诊断摘要")), el('p', '', l("公开 GitHub Issue 的内容会被其他人看到。请先检查，可删减后复制。")),
    area, copy, issues(), button(l("关闭"), 'ghost', () => modal.close()));
  modal.show();
}
function showWelcome() {
  const modal = welcome = createModal({ parent: document.body, className: 'card privacy-dialog', label: l("欢迎使用 Moerise"),
    onClose: () => { if (!choice()) choose(false); welcome = null; } });
  const { label, control } = toggle();
  const details = button(l("查看统计说明"), 'dlink', () => {
    const info = createModal({ parent: document.body, className: 'card privacy-dialog', label: l("统计说明") });
    info.dialog.append(el('h2', '', l("统计说明"))); information(info.dialog);
    info.dialog.append(button(l("关闭"), 'ghost', () => info.close())); info.show();
  });
  const enter = button(l("进入 Moerise"), 'save', () => { choose(control.checked); prepareBatch(); modal.close(); });
  modal.dialog.append(el('h2', '', l("欢迎使用 Moerise")), el('p', '', l("Moerise 还在持续完善中，欢迎反馈问题和建议。")),
    label, el('p', 'sub', explanation));
  if (!allowed) modal.dialog.append(el('p', 'sub', l("此实例无法发送统计。")));
  const links = el('div', 'privacy-links'); links.append(issues(), details);
  modal.dialog.append(enter, links); modal.show(); enter.focus();
}
export function privacySettings(parent) {
  const { label, control } = toggle(on => { choose(on); prepareBatch(); });
  const unavailable = el('p', 'sub', l("此实例无法发送统计。"));
  function update() {
    control.checked = allowed && choice() === 'on'; control.disabled = !allowed; unavailable.hidden = allowed;
  }
  window.addEventListener('moeriseprivacy', update); update();
  parent.append(label, unavailable, el('p', '', explanation)); information(parent);
  const links = el('div', 'privacy-links');
  links.append(button(l("预览诊断摘要"), 'ghost', preview), issues()); parent.append(links);
  return () => window.removeEventListener('moeriseprivacy', update);
}
if (typeof window !== 'undefined') {
  window.addEventListener('storage', event => {
    if (event.key !== choiceKey && event.key !== null) return;
    memoryChoice = '';
    if (choice() !== 'on') sending?.abort();
    if (choice()) welcome?.close();
    window.dispatchEvent(new Event('moeriseprivacy'));
  });
  window.addEventListener('error', () => usage('client_error', 'failure'));
  window.addEventListener('unhandledrejection', () => usage('client_error', 'failure'));
  document.addEventListener('visibilitychange', () => { if (!document.hidden) { prepareBatch(); flush(); } });
}
