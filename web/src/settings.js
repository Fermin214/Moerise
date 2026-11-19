import { el, button } from './dom.js';
import { formSignature } from './forms.js';
import { l, setText, setAttr, resolveText, languagePreference, setLanguage } from './i18n.js';
const fields = {
  review: ['count', 'min_age_hours', 'recent_days', 'include_archived'],
  notifications: ['enabled', 'times'],
  llm: ['enabled', 'protocol', 'base_url', 'model', 'timeout_seconds', 'max_concurrency', 'max_retries', 'max_output_tokens'],
  embedding: ['enabled', 'provider', 'protocol', 'base_url', 'model', 'dimensions', 'send_dimensions', 'batch_size', 'timeout_seconds', 'max_concurrency', 'max_retries', 'query_instruction'],
  stargazing: ['enabled'],
  channel: ['kind', 'qq_app_id', 'app_id', 'user_open_id', 'destination', 'feishu_mode', 'webhook_images', 'webhook_app_id'],
  insights: ['default_prompt'],
};
const secretLabels = {
  llm_api_key: 'LLM API Key', embedding_api_key: 'Embedding API Key',
  app_secret: 'App Secret', verification_token: 'Verification Token',
  encrypt_key: 'Encrypt Key', webhook_url: 'Webhook URL', webhook_secret: 'Webhook Secret',
  bot_token: 'Telegram Bot Token', qq_app_secret: 'QQ AppSecret', webhook_app_secret: 'Webhook App Secret',
};
const destinations = [
  ['none', l("不推送")], ['qq', l("QQ 私聊")], ['telegram', 'Telegram'],
  ['feishu-app', l("飞书应用机器人")], ['feishu-webhook', l("飞书群 Webhook")],
];
const connectionLabels = {
  disabled: l("未配置"), connecting: l("连接中"), connected: l("已连接"), disconnected: l("连接已断开"), error: l("连接失败"),
  awaiting_verification: l("等待回调验证"),
  BOT_CREDENTIALS: l("请检查机器人凭据。"), BOT_CONNECTION: l("网络连接中断。"), QQ_SESSION_LOST: l("离线期间可能有消息未收到。"),
  BOT_RATE_LIMITED: l("平台限制了请求频率。"), BOT_PROTOCOL: l("平台响应异常。"), TELEGRAM_WEBHOOK: l("外部 Webhook 已占用收件。"),
  BOT_STORAGE: l("收件记录写入失败。"),
  STATE_KEY_REQUIRED: l("请配置状态加密密钥。"), FEISHU_CONNECTION_ERROR: l("连接飞书失败。"),
};
const deliveryLabels = {
  pending: l("等待发送"), running: l("发送中"), sent: l("已发送"), unknown: l("送达结果不明"), failed: l("发送失败"), skipped: l("已跳过"),
};
function invalid(message, field) {
  return Object.assign(new Error(resolveText(message)), { code: 'SETTINGS_INPUT_INVALID', localizedMessage: message, field });
}
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

export function buildSettingsPatch(snapshot, draft, operations = {}) {
  const settings = {}, secrets = {};
  if (!Number.isSafeInteger(snapshot.version) || snapshot.version < 1) throw invalid(l("设置版本无效，请重新加载。"));
  if (draft.timezone === null) throw invalid(l("设置不能是空值。"));
  if (draft.timezone !== undefined && draft.timezone !== snapshot.settings.timezone) settings.timezone = draft.timezone;
  for (const [group, names] of Object.entries(fields)) {
    for (const name of names) {
      const value = draft[group]?.[name];
      if (value !== undefined && !same(value, snapshot.settings[group]?.[name])) {
        if (value === null) throw invalid(l("设置不能是空值。"));
        (settings[group] ||= {})[name] = value;
      }
    }
  }
  for (const name of Object.keys(secretLabels)) {
    const operation = operations[name];
    if (!operation) continue;
    if (!['keep', 'replace', 'clear'].includes(operation.action)) throw invalid(l("请选择密钥操作。"));
    if (operation.action === 'replace') {
      const value = operation.value;
      if (typeof value !== 'string' || !value.trim() || /[*•●…]/u.test(value)) {
        throw invalid(l("{0} 需要填写真实的新值，不能使用遮蔽字符。", { 0: secretLabels[name] }));
      }
      secrets[name] = { action: 'replace', value };
    } else {
      if (operation.value !== undefined) throw invalid(l("保持或清空密钥不能携带新值。"));
      secrets[name] = { action: operation.action };
    }
  }
  return { version: snapshot.version, settings, secrets };
}

import { privacySettings } from './privacy.js';

export function createSettingsPage({ sessionRequest, handleFailure, onSaved = () => {}, onDirtyChange = () => {},
  confirmDiscard = () => Promise.resolve(true) }) {
  let root, snapshot, form, error, status, reload, save, controller, pendingBody;
  let baseline = '';
  let unbindPrivacy;
  let sequence = 0, active = false, busy = false, conflict = false;
  let controls = new Map(), secretControls = new Map(), times = [];
  let modelViews = new Map(), modelController, modelTimer, indexText, indexProgress;
  let activeTab = 'general', tabs = new Map(), panels = new Map();
  let channelViews = new Map(), destinationHint, telegramLink, channelTimer, channelController, history, historySignature;
  let deliveryRequests = new Set();
  const checks = new Map();
  let lifecycle = null;
  const hasChanges = () => !!form && !!baseline && formSignature(form) !== baseline;
  const reloadSettings = async () => { if (await confirmDiscard(hasChanges())) return load(); };

  function abortModels() {
    clearTimeout(modelTimer); modelTimer = null;
    const pending = modelController; modelController = null; pending?.abort();
    for (const controller of checks.values()) controller.abort();
    checks.clear();
  }
  function clearSecrets() {
    for (const { action, input, direct } of secretControls.values()) {
      input.value = ''; input.required = false; input.hidden = !direct; action.value = 'keep';
    }
    if (pendingBody) for (const operation of Object.values(pendingBody.secrets)) delete operation.value;
    pendingBody = null;
  }
  function destroy() {
    unbindPrivacy?.();
    active = false; sequence++; lifecycle?.abort(); controller?.abort(); abortModels(); stopChannelPoll(); clearSecrets();
    deliveryRequests = new Set();
    root?.remove(); root = snapshot = form = controller = null;
    controls.clear(); secretControls.clear(); modelViews.clear(); times = []; baseline = ''; onDirtyChange();
  }
  function message(text = '') {
    setText(error, text); error.hidden = !text;
  }
  function failure(err) {
    if (err.name === 'AbortError') return;
    const messages = {
      SETTINGS_CONFLICT: l("设置已在其他页面更新。重新加载后再编辑；重新加载会丢弃表单中的修改。"),
      SETTINGS_BUSY: l("设置服务繁忙，请稍后重新保存。"),
      INVALID_ARGUMENT: l("设置不合法，请检查字段、时段和密钥配置。"),
      SETTINGS_INPUT_INVALID: err.localizedMessage || err.message,
    };
    if (messages[err.code]) message(messages[err.code]);
    else handleFailure(error, err);
    if (!active || !err.field) return;
    const panel = err.field.closest('[role="tabpanel"]');
    if (panel) selectTab([...panels].find(([, node]) => node === panel)[0]);
    error.id = 'settings-error';
    err.field.setAttribute('aria-invalid', 'true');
    const described = err.field.getAttribute('aria-describedby') || '';
    err.field.setAttribute('aria-describedby', `${described} ${error.id}`.trim());
    err.field.focus();
    err.field.addEventListener('input', () => {
      err.field.removeAttribute('aria-invalid');
      if (described) err.field.setAttribute('aria-describedby', described);
      else err.field.removeAttribute('aria-describedby');
    }, { once: true });
  }
  function selectTab(id, focus = false) {
    activeTab = id;
    for (const [key, tab] of tabs) {
      tab.classList.toggle('on', key === id);
      tab.setAttribute('aria-selected', String(key === id)); tab.tabIndex = key === id ? 0 : -1;
      panels.get(key).hidden = key !== id;
    }
    if (focus) {
      tabs.get(id).focus({ preventScroll: true });
      tabs.get(id).scrollIntoView({ block: 'nearest', inline: 'nearest' });
    }
    stopChannelPoll();
    if (form.querySelector('.settings-footer')) form.querySelector('.settings-footer').hidden = id === 'privacy';
    if (active && !busy && !conflict) refreshChannel();
  }
  function section(id, title, caption = title) {
    const card = el('section', 'card settings-card');
    card.id = `settings-panel-${id}`; card.setAttribute('role', 'tabpanel');
    card.setAttribute('aria-labelledby', `settings-tab-${id}`); card.tabIndex = 0;
    const tab = button(caption, 'tab', () => selectTab(id));
    tab.id = `settings-tab-${id}`; tab.setAttribute('role', 'tab');
    tab.setAttribute('aria-controls', card.id);
    tabs.set(id, tab); panels.set(id, card);
    form.querySelector('[role="tablist"]').append(tab);
    card.append(el('h2', '', title)); form.append(card);
    return card;
  }
  function row(parent, label, control, hint = '') {
    const wrapper = el('div', 'row settings-row'), title = el('div', 'settings-label');
    const caption = el('label', '', label);
    caption.htmlFor = control.id; title.append(caption);
    if (hint) {
      const description = el('span', 'sub', hint); description.id = `${control.id}-hint`;
      control.setAttribute('aria-describedby', description.id); title.append(description);
    }
    wrapper.append(title, control); parent.append(wrapper);
  }
  function field(parent, path, label, { type = 'text', options, min, max, step, hint = '' } = {}) {
    let input;
    if (options) {
      input = el('select');
      for (const [value, text] of options) {
        const option = el('option', '', text); option.value = value; input.append(option);
      }
    } else if (type === 'textarea') input = el('textarea');
    else { input = el('input'); input.type = type; }
    input.id = `settings-${path.replaceAll('.', '-')}`; input.name = path;
    const [group, key] = path.split('.');
    const value = key ? snapshot.settings[group]?.[key] : snapshot.settings[group];
    if (type === 'checkbox') input.checked = value === true;
    else {
      input.value = value ?? '';
      if (options && !options.some(([candidate]) => candidate === input.value)) {
        const option = el('option', '', value || l("未配置")); option.value = value ?? '';
        input.append(option); input.value = value ?? '';
      }
    }
    if (min !== undefined) input.min = min;
    if (max !== undefined) input.max = max;
    if (step !== undefined) input.step = step;
    if (type === 'number') input.required = true;
    if (type === 'text') { input.autocomplete = 'off'; input.spellcheck = false; input.autocapitalize = 'none'; }
    controls.set(path, { input, type, original: value });
    row(parent, label, input, hint);
    return input;
  }
  function secret(parent, name) {
    const label = secretLabels[name], box = el('div', 'settings-secret');
    const configured = snapshot.secrets?.[name]?.configured === true;
    const direct = !configured && !name.endsWith('_api_key');
    if (!direct) box.append(el('span', 'sub', configured ? l("已配置") : l("未配置")));
    const action = el('select'); action.id = `settings-secret-${name}`; setAttr(action, 'aria-label', l("{0}操作", { 0: label }));
    for (const [value, text] of [['keep', l("保持")], ['replace', l("替换")], ['clear', l("清空")]]) {
      const option = el('option', '', text); option.value = value; action.append(option);
    }
    action.hidden = direct;
    const input = el('input'); input.type = 'password'; input.hidden = !direct;
    input.id = `settings-secret-${name}-value`; input.name = name;
    input.autocomplete = 'new-password'; input.spellcheck = false; setAttr(input, 'aria-label', l("{0}新值", { 0: label }));
    setAttr(input, 'placeholder', l("填写真实的新值"));
    action.addEventListener('change', () => {
      input.value = ''; input.hidden = action.value !== 'replace'; input.required = !input.hidden;
      if (!input.hidden) input.focus();
    });
    box.id = `settings-${name}-controls`; box.append(action, input);
    secretControls.set(name, { action, input, direct });
    row(parent, label, box);
    box.parentElement.querySelector('label').htmlFor = direct ? input.id : action.id;
  }
  function readDraft() {
    const draft = {};
    for (const [path, { input, type, original }] of controls) {
      if (input.disabled && input.closest('[data-channel-fields]')) continue;
      let value = type === 'checkbox' ? input.checked : type === 'number' ? input.valueAsNumber : input.value;
      if (type === 'number' && !Number.isFinite(value)) throw invalid(l("请填写有效的数字。"));
      if (value === '' && original == null) continue;
      const [group, name] = path.split('.');
      if (name) (draft[group] ||= {})[name] = value;
      else draft[group] = value;
    }
    try { new Intl.DateTimeFormat('zh-CN', { timeZone: draft.timezone }).format(); }
    catch { throw invalid(l("请填写有效的 IANA 时区，例如 Asia/Hong_Kong。"), controls.get('timezone').input); }
    const clocks = times.map(input => input.value);
    if (!clocks.length || clocks.some(clock => !/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(clock)) ||
        new Set(clocks).size !== clocks.length) throw invalid(l("请填写至少一个有效推送时段，时段不能重复。"), times[0]);
    const previous = snapshot.settings.notifications;
    const previousClocks = previous.times;
    if (!same(clocks, previousClocks)) {
      draft.notifications.times = clocks;
    }
    return draft;
  }
  function renderTimes(parent) {
    const box = el('div', 'settings-times'), list = el('div', 'settings-time-list');
    function add(value = '') {
      const line = el('div', 'settings-time'), input = el('input');
      input.type = 'time'; input.required = true; input.value = value;
      const remove = button(l("移除"), 'ghost', () => {
        times = times.filter(item => item !== input); line.remove(); update(); onDirtyChange();
      });
      line.append(input, remove); list.append(line); times.push(input); update();
    }
    function update() {
      times.forEach((input, index) => {
        setAttr(input, 'aria-label', l("推送时段 {0}", { 0: index + 1 }));
        input.nextElementSibling.disabled = times.length === 1;
      });
      append.disabled = times.length >= 24;
    }
    const append = button(l("添加时段"), 'ghost', () => { add(); onDirtyChange(); });
    const clocks = snapshot.settings.notifications.times;
    for (const clock of clocks) add(clock);
    box.append(list, append, el('span', 'sub', l("时段数即每日推送次数；每个时段分配不同笔记。")));
    parent.append(box);
  }
  function modelDirty(group) {
    for (const [path, { input, type, original }] of controls) {
      if (!path.startsWith(`${group}.`)) continue;
      const value = type === 'checkbox' ? input.checked : type === 'number' ? input.valueAsNumber : input.value;
      if (!same(value, original ?? (type === 'checkbox' ? false : ''))) return true;
    }
    return secretControls.get(`${group}_api_key`).action.value !== 'keep';
  }
  function stopChannelPoll() {
    clearTimeout(channelTimer);
    channelController?.abort(); channelController = null;
  }
  function showChannelStatus(statuses = {}) {
    for (const [kind, view] of channelViews) {
      const { state, detail, peer } = statuses[kind] || {};
      setText(view, () => [resolveText(connectionLabels[state] || connectionLabels.disabled),
        resolveText(connectionLabels[detail] || detail), peer].filter(Boolean).join(' · '));
    }
    const link = statuses.telegram?.link || '';
    telegramLink.hidden = !/^https:\/\/t\.me\/[a-z\d_]+(?:\?start=[a-z\d_-]{1,64})?$/i.test(link);
    if (telegramLink.hidden) telegramLink.removeAttribute('href');
    else telegramLink.href = link;
    const kind = controls.get('channel.kind').input.value;
    const configuredTarget = kind === 'telegram' && controls.get('channel.destination').input.value.trim() ||
      kind === 'feishu-app' && controls.get('channel.user_open_id').input.value.trim();
    const target = configuredTarget || statuses[kind]?.peer;
    setText(destinationHint, target ? l("接收会话：{0}", { 0: target }) :
      kind !== 'none' && kind !== 'feishu-webhook' ? l("等待建立私聊") : '');
  }
  async function refreshChannel() {
    if (!['review', 'channel', 'history'].includes(activeTab) || !active || !form || busy || conflict || document.hidden || channelController) return;
    const request = new AbortController();
    channelController = request;
    try {
      if (activeTab === 'history') {
        const data = await sessionRequest('/admin/notifications?kind=review', { controller: request });
        if (channelController === request) showHistory(data.notifications);
      } else {
        const data = await sessionRequest('/settings', { controller: request });
        if (channelController !== request) return;
        if (data.version !== snapshot.version) { versionConflict(); return; }
        snapshot.channel_status = data.channel_status;
        showChannelStatus(snapshot.channel_status);
      }
    } catch (err) {
      if (channelController !== request || err.name === 'AbortError') return;
      if (activeTab === 'history') { historySignature = ''; handleFailure(history, err); }
      else for (const view of channelViews.values()) setText(view, l("连接状态不可用"));
    } finally {
      if (channelController === request) {
        channelController = null;
        if (active && !conflict && !document.hidden) channelTimer = setTimeout(refreshChannel, 5000);
      }
    }
  }
  function showHistory(items) {
    const reviews = items || [];
    const signature = JSON.stringify(reviews);
    if (historySignature === signature) return;
    historySignature = signature;
    const receipts = new Map([...history.querySelectorAll('[data-delivery-receipt]')].map(input => [input.dataset.deliveryReceipt, input.value]));
    history.replaceChildren();
    if (!reviews.length) history.append(el('p', 'sub', l("暂无发送记录")));
    for (const item of reviews) {
      const line = el('div', 'row settings-row');
      const kind = destinations.find(([key]) => key === item.channel_kind)?.[1];
      const status = deliveryLabels[item.status] || item.status;
      line.append(el('span', '', () => [item.date, item.clock, resolveText(kind), resolveText(status), item.last_error].filter(Boolean).join(' · ')));
      if (item.status === 'unknown') {
        const actions = el('div', 'settings-delivery-actions');
        let receipt;
        if (item.channel_kind === 'feishu-app') {
          receipt = el('input'); receipt.type = 'text'; receipt.maxLength = 256;
          receipt.autocomplete = 'off'; receipt.dataset.deliveryReceipt = item.id;
          receipt.value = receipts.get(item.id) || '';
          setAttr(receipt, 'placeholder', l("飞书消息 ID（可选）"));
          setAttr(receipt, 'aria-label', l("飞书消息 ID（可选）"));
          receipt.addEventListener('keydown', event => { if (event.key === 'Enter') event.preventDefault(); });
          actions.append(receipt);
        }
        async function resolveDelivery(decision) {
          const requests = deliveryRequests;
          if (requests.has(item.id)) return;
          requests.add(item.id);
          const body = { decision };
          if (decision === 'sent' && receipt?.value.trim()) body.receipt = receipt.value.trim();
          confirm.disabled = retry.disabled = true;
          try {
            await sessionRequest(`/admin/notifications/${encodeURIComponent(item.id)}/resolve`, {
              method: 'POST', body,
            });
          } catch (err) {
            if (requests === deliveryRequests && form?.isConnected) failure(err);
          } finally {
            requests.delete(item.id);
            if (requests === deliveryRequests && active) {
              historySignature = '';
              if (activeTab === 'history') { stopChannelPoll(); refreshChannel(); }
            }
          }
        }
        const confirm = button(l("确认已送达"), 'ghost', () => resolveDelivery('sent'));
        const retry = button(l("补发"), 'ghost', () => resolveDelivery('not_sent'));
        confirm.disabled = retry.disabled = deliveryRequests.has(item.id);
        actions.append(confirm, retry); line.append(actions);
      }
      history.append(line);
    }
  }
  function channelFields(parent, title) {
    const block = el('fieldset', 'settings-channel');
    block.append(el('legend', '', title)); parent.append(block);
    return block;
  }
  function showWhen(block, input, visible) {
    block.dataset.channelFields = '';
    const sync = () => {
      block.hidden = !visible();
      for (const control of block.querySelectorAll('input, select')) control.disabled = block.hidden;
    };
    input.addEventListener('change', sync); sync();
  }
  function updateChecks() {
    for (const [group, view] of modelViews) {
      view.check.disabled = busy || conflict || checks.has(group) || modelDirty(group) || !snapshot.settings[group].enabled;
    }
  }
  function modelError(node, err) {
    const messages = {
      EMBEDDING_DISABLED: l("请先启用并保存 Embedding。"), LLM_DISABLED: l("请先启用并保存 LLM。"),
      EMBEDDING_AUTH: l("Embedding 密钥被拒绝，请检查密钥和模型权限。"),
      LLM_AUTH: l("LLM 密钥被拒绝，请检查密钥和模型权限。"),
      EMBEDDING_REJECTED: l("Embedding 请求被拒绝，请检查协议、地址、模型和维度。"),
      LLM_REJECTED: l("LLM 请求被拒绝，请检查协议、地址和模型。"),
      EMBEDDING_PROTOCOL: l("Embedding 返回格式或维度不符合配置。"),
      LLM_PROTOCOL: l("LLM 返回格式不符合所选协议。"),
      EMBEDDING_RATE_LIMITED: l("Embedding 调用受限，请稍后重试。"),
      LLM_RATE_LIMITED: l("LLM 调用受限，请稍后重试。"),
      EMBEDDING_UNAVAILABLE: l("Embedding 连接失败，请检查服务和地址。"),
      LLM_UNAVAILABLE: l("LLM 连接失败，请检查服务和地址。"),
      MODEL_CHECK_TIMEOUT: l("连接检查超时，请稍后重试。"),
      UPSTREAM_AUTH_UNAVAILABLE: l("Memos 连接失败，请恢复笔记服务后重试。"),
      UPSTREAM_UNAVAILABLE: l("Memos 连接失败，请恢复笔记服务后重试。"),
    };
    node.hidden = false;
    if (messages[err.code]) setText(node, messages[err.code]);
    else handleFailure(node, err);
  }
  function versionConflict() {
    abortModels(); stopChannelPoll(); conflict = true; save.disabled = true; updateChecks();
    for (const view of modelViews.values()) {
      setText(view.config, l("设置已变更，请重新加载。"));
      setText(view.result, '');
    }
    setText(indexText, l("索引状态已失效，请重新加载设置。"));
    indexProgress.hidden = true; indexProgress.value = 0;
    failure({ code: 'SETTINGS_CONFLICT' });
  }
  async function refreshModels() {
    if (!active || busy || conflict || modelController || document.hidden) return;
    clearTimeout(modelTimer); modelTimer = null;
    const ticket = sequence, requestController = new AbortController();
    modelController = requestController;
    try {
      const data = await sessionRequest('/models', { controller: requestController });
      if (!active || sequence !== ticket || modelController !== requestController) return;
      if (data.version !== snapshot.version) { versionConflict(); return; }
      for (const [group, view] of modelViews) {
        setText(view.config, !data[group].enabled ? l("已关闭") : data[group].configured ? l("已启用 · 已配置") : l("配置不完整"));
      }
      const labels = { disabled: l("语义索引已关闭"), waiting: l("语义索引等待同步或构建"),
        building: l("语义索引构建中"), ready: l("语义索引已就绪"), unavailable: l("语义索引构建失败，等待重试") };
      const generation = data.vector_index?.building;
      const label = labels[data.index_status] || l("索引状态不可用");
      indexProgress.hidden = !generation || !['building', 'unavailable'].includes(data.index_status);
      if (!indexProgress.hidden) {
        indexProgress.max = Math.max(1, generation.chunks);
        indexProgress.value = generation.completed_chunks;

      }
      setText(indexText, () => resolveText(label) + (indexProgress.hidden ? '' :
        resolveText(l(" · {0}/{1} 个片段", { 0: generation.completed_chunks, 1: generation.chunks })) +
        (generation.last_error ? ` · ${generation.last_error}` : '')));
    } catch (err) {
      if (!active || sequence !== ticket || modelController !== requestController || err.name === 'AbortError') return;
      modelError(indexText, err);
    } finally {
      if (modelController === requestController) {
        modelController = null;
        if (active && sequence === ticket && !conflict && !document.hidden) modelTimer = setTimeout(refreshModels, 5000);
      }
    }
  }
  async function checkModel(group) {
    const view = modelViews.get(group);
    if (view.check.disabled) return;
    const ticket = sequence, checkController = new AbortController();
    checks.set(group, checkController); updateChecks();
    setText(view.result, l("正在检查已保存配置…"));
    try {
      const result = await sessionRequest(`/models/${group}/check`, {
        method: 'POST', body: { version: snapshot.version }, controller: checkController,
      });
      if (!active || sequence !== ticket || checks.get(group) !== checkController) return;
      if (result.version !== snapshot.version) { versionConflict(); return; }
      setText(view.result, l("连接正常 · {0} 毫秒", { 0: result.duration_ms }));
    } catch (err) {
      if (!active || sequence !== ticket || checks.get(group) !== checkController || err.name === 'AbortError') return;
      if (err.code === 'SETTINGS_CONFLICT') versionConflict();
      else modelError(view.result, err);
    } finally {
      if (active && sequence === ticket && checks.get(group) === checkController) {
        checks.delete(group); updateChecks();
      }
    }
  }
  function model(parent, group, title) {
    const block = el('div', 'settings-model'); block.append(el('h3', '', title)); parent.append(block);
    field(block, `${group}.enabled`, l("启用 {0}", { 0: title }), { type: 'checkbox' });
    let provider;
    if (group === 'embedding') provider = field(block, 'embedding.provider', l("供应商"), { options: [['bailian', l("百炼")], ['openai-compatible', l("OpenAI 兼容")]] });
    const protocols = group === 'llm'
      ? [['openai-compatible', 'OpenAI Chat Completions'], ['openai-responses', 'OpenAI Responses'], ['anthropic', 'Anthropic']]
      : [['dashscope', 'DashScope'], ['openai-compatible', l("OpenAI 兼容 Embedding")]];
    const protocol = field(block, `${group}.protocol`, l("接口协议"), { options: protocols });
    field(block, `${group}.base_url`, l("API 地址"), { type: 'url' });
    field(block, `${group}.model`, l("模型名称"));
    secret(block, `${group}_api_key`);
    const advanced = el('div', 'settings-advanced'); advanced.append(el('h4', 'sub', l("高级参数"))); block.append(advanced);
    field(advanced, `${group}.timeout_seconds`, l("请求超时（秒）"), { type: 'number', min: 1, max: group === 'llm' ? 110 : 120, step: 'any' });
    field(advanced, `${group}.max_concurrency`, l("并发数"), { type: 'number', min: 1, max: 8 });
    field(advanced, `${group}.max_retries`, l("重试次数"), { type: 'number', min: 0, max: 3 });
    if (group === 'llm') {
      field(advanced, 'llm.max_output_tokens', l("最大输出 Token"), { type: 'number', min: 32, max: 16384 });
    } else {
      field(advanced, 'embedding.dimensions', l("向量维度"), { type: 'number', min: 1, max: 16384 });
      const send = field(advanced, 'embedding.send_dimensions', l("发送维度参数"), { type: 'checkbox',
        hint: l("通用协议按需开启；固定维度模型可关闭，返回维度仍须一致。") });
      field(advanced, 'embedding.batch_size', l("批量大小"), { type: 'number', min: 1, max: 20 });
      const instruction = field(advanced, 'embedding.query_instruction', l("查询指令"), { type: 'textarea', hint: l("仅百炼 DashScope 支持。") });
      const syncProtocol = () => {
        const generic = protocol.value === 'openai-compatible';
        instruction.disabled = generic; send.disabled = !generic;
        if (generic) instruction.value = '';
        else send.checked = false;
      };
      provider.addEventListener('change', () => { protocol.value = provider.value === 'bailian' ? 'dashscope' : 'openai-compatible'; syncProtocol(); });
      protocol.addEventListener('change', () => { provider.value = protocol.value === 'dashscope' ? 'bailian' : 'openai-compatible'; syncProtocol(); });
      syncProtocol();
    }
    const config = el('p', 'sub', l("正在读取模型状态…")), result = el('p', 'sub');
    const check = button(l("检查 {0} 连接", { 0: title }), 'ghost', () => checkModel(group));
    result.setAttribute('role', 'status'); result.id = `settings-${group}-check-result`;
    config.id = `settings-${group}-status`;
    block.append(config, check, result, el('p', 'sub', l("连接检查使用已保存配置，会产生少量模型用量。")));
    modelViews.set(group, { config, check, result });
    block.addEventListener('input', changed); block.addEventListener('change', changed);
    function changed() {
      checks.get(group)?.abort(); checks.delete(group);
      setText(result, modelDirty(group) ? l("模型设置有修改，请保存后检查。") : '');
      updateChecks();
    }
  }
  function render() {
    abortModels(); stopChannelPoll(); controls.clear(); secretControls.clear(); modelViews.clear(); channelViews.clear(); times = [];
    tabs.clear(); panels.clear();
    form = el('form', 'settings-form'); setAttr(form, 'aria-label', l("产品设置")); form.autocomplete = 'off';
    form.noValidate = true;
    form.addEventListener('submit', submit);
    const tablist = el('div', 'settings-tabs');
    tablist.setAttribute('role', 'tablist'); setAttr(tablist, 'aria-label', l("设置模块"));
    tablist.addEventListener('keydown', event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      const keys = [...tabs.keys()], index = keys.indexOf(activeTab);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? keys.length - 1 :
        (index + (event.key === 'ArrowRight' ? 1 : -1) + keys.length) % keys.length;
      event.preventDefault(); selectTab(keys[next], true);
    });
    form.append(tablist);
    const general = section('general', l("通用"));
    const language = el('select'); language.id = 'settings-language'; language.dataset.language = '';
    for (const [value, title] of [['auto', l("跟随浏览器")], ['zh-CN', '简体中文'], ['en', 'English']]) {
      const option = el('option', '', title); option.value = value; language.append(option);
    }
    language.value = languagePreference();
    language.addEventListener('change', () => setLanguage(language.value, { persist: true }));
    row(general, l("语言"), language);
    field(general, 'stargazing.enabled', l("启用观星"), { type: 'checkbox' });
    const review = section('review', l("回顾"));
    field(review, 'timezone', l("时区"), { hint: l("使用 IANA 名称，例如 Asia/Hong_Kong。") });
    field(review, 'review.count', l("每次数量（条）"), { type: 'number', min: 1, max: 20 });
    field(review, 'review.min_age_hours', l("等待期（小时）"), { type: 'number', min: 0, max: 87600 });
    field(review, 'review.recent_days', l("重复排除期（天）"), { type: 'number', min: 0, max: 3650 });
    field(review, 'review.include_archived', l("包含归档笔记"), { type: 'checkbox' });
    const enable = field(review, 'notifications.enabled', l("开启回顾推送"), { type: 'checkbox' });
    const channelKind = field(review, 'channel.kind', l("定时回顾发往"), { options: destinations });
    destinationHint = el('p', 'sub settings-hint'); review.append(destinationHint);
    channelKind.addEventListener('change', () => {
      if (channelKind.value === 'none') enable.checked = false;
      showChannelStatus(snapshot.channel_status);
    });
    const clocks = el('div', 'settings-clocks'); clocks.append(el('h3', '', l("推送时段"))); review.append(clocks); renderTimes(clocks);
    review.append(el('p', 'sub settings-hint', l("回顾参数、时段和时区从后续日期生效，已固定的当天计划保持不变。")));
    const models = section('models', l("模型")); model(models, 'llm', 'LLM'); model(models, 'embedding', 'Embedding');
    indexText = el('p', 'sub', l("正在读取索引状态…")); indexText.id = 'settings-index-status'; indexText.setAttribute('role', 'status');
    indexProgress = el('progress'); indexProgress.hidden = true; setAttr(indexProgress, 'aria-label', l("语义索引进度"));
    models.append(indexText, indexProgress);
    const insights = section('insights', l("默认洞察 Prompt"), l("洞察"));
    const prompt = field(insights, 'insights.default_prompt', l("一键洞察使用的 Prompt"), { type: 'textarea' });
    prompt.rows = 7;
    const restore = button(l("恢复默认"), 'ghost', () => {
      prompt.value = snapshot.default_insight_prompt; setText(status, l("已恢复默认 Prompt，保存后生效。"));
      onDirtyChange();
    });
    restore.disabled = typeof snapshot.default_insight_prompt !== 'string'; insights.append(restore);
    historySignature = ''; history = el('div'); section('history', l("发送记录")).append(history);
    const channel = section('channel', l("消息渠道"));
    const qq = channelFields(channel, l("QQ 私聊"));
    field(qq, 'channel.qq_app_id', 'QQ AppID').inputMode = 'numeric'; secret(qq, 'qq_app_secret');
    const telegram = channelFields(channel, 'Telegram');
    secret(telegram, 'bot_token');
    field(telegram, 'channel.destination', l("Telegram 接收会话"), { hint: l("留空使用本人私聊，也可填写群或频道的 Chat ID。") })
      .addEventListener('input', () => showChannelStatus(snapshot.channel_status));
    telegramLink = el('a', '', l("打开机器人")); telegramLink.target = '_blank'; telegramLink.rel = 'noreferrer';
    telegram.append(telegramLink);
    const feishu = channelFields(channel, l("飞书应用机器人"));
    const mode = field(feishu, 'channel.feishu_mode', l("接收方式"), { options: [['websocket', l("长连接")], ['callback', l("公网回调")]] });
    field(feishu, 'channel.app_id', 'App ID');
    secret(feishu, 'app_secret');
    field(feishu, 'channel.user_open_id', l("用户 Open ID"), { hint: l("私人应用可留空以绑定首个私聊。多人应用请指定用户。") })
      .addEventListener('input', () => showChannelStatus(snapshot.channel_status));
    const callback = el('div'); feishu.append(callback);
    secret(callback, 'verification_token'); secret(callback, 'encrypt_key');
    showWhen(callback, mode, () => mode.value === 'callback');
    for (const [key, block] of [['qq', qq], ['telegram', telegram], ['feishu-app', feishu]]) {
      const status = el('p', 'sub'); status.setAttribute('role', 'status'); block.append(status); channelViews.set(key, status);
    }
    const webhook = channelFields(channel, l("飞书群 Webhook"));
    secret(webhook, 'webhook_url'); secret(webhook, 'webhook_secret');
    const images = field(webhook, 'channel.webhook_images', l("推送图片"), { type: 'checkbox' });
    const upload = el('div'); webhook.append(upload);
    field(upload, 'channel.webhook_app_id', 'Webhook App ID');
    showWhen(upload, images, () => images.checked);
    const uploadSecret = el('div'); webhook.append(uploadSecret); secret(uploadSecret, 'webhook_app_secret');
    showWhen(uploadSecret, images, () => images.checked || snapshot.secrets?.webhook_app_secret?.configured === true);
    const uploadEdit = secretControls.get('webhook_app_secret');
    const replaceOption = uploadEdit.action.querySelector('[value="replace"]');
    const updateUpload = () => {
      replaceOption.hidden = replaceOption.disabled = !images.checked;
      if (!images.checked) {
        uploadEdit.input.value = '';
        if (uploadEdit.action.value === 'replace') {
          uploadEdit.action.value = 'keep'; uploadEdit.input.hidden = true; uploadEdit.input.required = false;
        }
      }
    };
    images.addEventListener('change', updateUpload); updateUpload();
    showChannelStatus(snapshot.channel_status);
    unbindPrivacy?.();
    unbindPrivacy = privacySettings(section('privacy', l("隐私")));
    const footer = el('div', 'settings-footer');
    error = el('p', 'error sub'); error.setAttribute('role', 'alert'); error.hidden = true;
    status = el('p', 'sub'); status.setAttribute('role', 'status');
    save = el('button', 'save', l("保存设置")); save.type = 'submit';
    reload = button(l("重新加载设置"), 'ghost', reloadSettings);
    footer.append(error, status, reload, save); form.append(footer);
    root.replaceChildren(form); busy = conflict = false; baseline = formSignature(form); onDirtyChange();
    selectTab(activeTab);
    updateChecks();
  }
  async function load() {
    const ticket = ++sequence; controller?.abort(); abortModels(); stopChannelPoll(); clearSecrets(); controller = new AbortController();
    form = null; baseline = ''; onDirtyChange();
    busy = true; conflict = false;
    root.replaceChildren(el('p', 'sub', l("正在加载设置…")));
    root.setAttribute('aria-busy', 'true');
    try {
      const data = await sessionRequest('/settings', { controller });
      if (!active || sequence !== ticket) return;
      if (!data?.settings || !Number.isSafeInteger(data.version) || !data.settings.review || !data.settings.notifications ||
          !data.settings.llm || !data.settings.embedding || !data.settings.stargazing ||
          !data.settings.channel || !data.settings.insights) {
        throw invalid(l("服务返回的设置不完整，请重新加载。"));
      }
      snapshot = data; render();
    } catch (err) {
      if (!active || sequence !== ticket) return;
      error = el('p', 'error sub'); error.setAttribute('role', 'alert');
      root.replaceChildren(error, button(l("重新加载设置"), 'ghost', reloadSettings)); failure(err);
    } finally {
      if (active && sequence === ticket) {
        busy = false; root.setAttribute('aria-busy', 'false');
        if (form?.parentElement === root) { updateChecks(); refreshModels(); }
      }
    }
  }
  async function submit(event) {
    event.preventDefault();
    if (busy || conflict) return;
    const invalidInput = form.querySelector(':invalid');
    if (invalidInput) {
      const panel = invalidInput.closest('[role="tabpanel"]');
      if (panel) selectTab([...panels].find(([, node]) => node === panel)[0]);
      invalidInput.reportValidity(); return;
    }
    message(); setText(status, '');
    try {
      const operations = {};
      for (const [name, { action, input, direct }] of secretControls) {
        if (action.disabled) continue;
        const edit = direct && input.value ? 'replace' : action.value;
        if (edit !== 'keep') operations[name] = { action: edit, ...(edit === 'replace' ? { value: input.value } : {}) };
      }
      pendingBody = buildSettingsPatch(snapshot, readDraft(), operations);
      if (!Object.keys(pendingBody.settings).length && !Object.values(pendingBody.secrets).some(op => op.action !== 'keep')) {
        setText(status, l("没有需要保存的修改。")); pendingBody = null; return;
      }
    } catch (err) { failure(err); return; }
    const ticket = ++sequence; abortModels(); stopChannelPoll(); controller = new AbortController();
    const replaced = Object.values(pendingBody.secrets).some(op => op.action === 'replace');
    busy = true; save.disabled = reload.disabled = true; form.setAttribute('aria-busy', 'true');
    // 应用设置时锁定表单，避免提交期间修改内容。
    const locked = [...form.querySelectorAll('input, select, textarea, button')].filter(input => !input.disabled);
    for (const input of locked) input.disabled = true;
    try {
      const data = await sessionRequest('/settings', { method: 'PATCH', body: pendingBody, controller });
      if (!active || sequence !== ticket) return;
      clearSecrets(); snapshot = data; render();
      setText(status, l("设置已保存。")); onSaved(data);
    } catch (err) {
      if (!active || sequence !== ticket) return;
      clearSecrets();
      if (err.code === 'SETTINGS_CONFLICT') versionConflict();
      else failure(err);
      if (active && replaced) setText(status, l("新密钥已从表单移除；再次替换时请重新填写。"));
    } finally {
      if (active && sequence === ticket) {
        clearSecrets(); busy = false;
        for (const input of locked) input.disabled = false;
        form.setAttribute('aria-busy', 'false'); save.disabled = conflict; reload.disabled = false;
        updateChecks();
        onDirtyChange();
        if (!conflict) { refreshModels(); refreshChannel(); }
      }
    }
  }
  return {
    mount(container) {
      destroy(); active = true;
      lifecycle = new AbortController();
      document.addEventListener('visibilitychange', () => {
        if (document.hidden) { clearTimeout(modelTimer); modelTimer = null; stopChannelPoll(); }
        else { refreshModels(); refreshChannel(); }
      }, { signal: lifecycle.signal });
      root = el('div', 'settings-page'); root.id = 'settings-page'; container.append(root);
      return load();
    },
    destroy,
    hasChanges,
  };
}
