import { el, button } from './dom.js';
import { l, setText, setAttr, resolveText, languagePreference, setLanguage } from './i18n.js';
import { createModal } from './modal.js';
import { privacySettings } from './privacy.js';
import { snackbar } from './snackbar.js';

const fields = {
  review: ['count', 'min_age_hours', 'recent_days', 'include_archived'],
  notifications: ['enabled', 'times'],
  embedding: ['enabled', 'provider', 'protocol', 'base_url', 'model', 'dimensions', 'send_dimensions', 'batch_size', 'timeout_seconds', 'max_concurrency', 'max_retries', 'query_instruction'],
  stargazing: ['enabled'],
  channel: ['kind', 'qq_app_id', 'app_id', 'user_open_id', 'destination', 'feishu_mode', 'webhook_images', 'webhook_app_id'],
};
const secretLabels = {
  embedding_api_key: 'Embedding API Key', qq_app_secret: 'QQ AppSecret',
  app_secret: 'App Secret', verification_token: 'Verification Token', encrypt_key: 'Encrypt Key',
  webhook_url: 'Webhook URL', webhook_secret: 'Webhook Secret', webhook_app_secret: 'Webhook App Secret',
  bot_token: 'Telegram Bot Token',
};
const channels = {
  qq: [l("QQ 私聊"), 'qq_app_secret'],
  telegram: ['Telegram', 'bot_token'],
  'feishu-app': [l("飞书应用机器人"), 'app_secret'],
  'feishu-webhook': [l("飞书群 Webhook"), 'webhook_url'],
};
const destinations = [['none', l("不推送")], ...Object.entries(channels).map(([key, [label]]) => [key, label])];
const connectionLabels = {
  disabled: l("未配置"), connecting: l("连接中"), connected: l("已连接"), disconnected: l("连接已断开"), error: l("连接失败"),
  awaiting_verification: l("等待回调验证"),
  BOT_CREDENTIALS: l("请检查机器人凭据。"), BOT_CONNECTION: l("网络连接中断。"), QQ_SESSION_LOST: l("离线期间可能有消息未收到。"),
  BOT_RATE_LIMITED: l("平台限制了请求频率。"), BOT_PROTOCOL: l("平台响应异常。"), TELEGRAM_WEBHOOK: l("外部 Webhook 已占用收件。"),
  BOT_STORAGE: l("收件记录写入失败。"), STATE_KEY_REQUIRED: l("请配置状态加密密钥。"), FEISHU_CONNECTION_ERROR: l("连接飞书失败。"),
};
const deliveryLabels = { pending: l("等待发送"), running: l("发送中"), sent: l("已发送"), failed: l("发送失败") };
const same = (left, right) => JSON.stringify(left) === JSON.stringify(right);
const fieldValue = (settings, [group, key]) => (key ? settings[group]?.[key] : settings[group]) ?? '';
const readField = ({ input, type }) => type === 'checkbox' ? input.checked :
  type === 'number' ? input.valueAsNumber : input.value.trim();

function embeddingOrigin(address) {
  try {
    const url = new URL(address);
    return ['https:', 'http:'].includes(url.protocol) ? url.origin : '';
  } catch { return ''; }
}

function invalid(message) {
  return Object.assign(new Error(resolveText(message)), { code: 'SETTINGS_INPUT_INVALID', localizedMessage: message });
}

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

function select(options) {
  const control = el('select');
  for (const [value, text] of options) {
    const option = el('option', '', text); option.value = value; control.append(option);
  }
  return control;
}

function row(parent, label, control, hint = '') {
  const wrapper = el('div', 'row settings-row'), title = el('div', 'settings-label');
  const caption = el(control.id ? 'label' : 'span', '', label);
  if (control.id) caption.htmlFor = control.id;
  title.append(caption);
  if (hint) title.append(el('span', 'sub', hint));
  wrapper.append(title, control); parent.append(wrapper);
  return wrapper;
}

function switchInput(id, value) {
  const control = el('input', 'settings-switch');
  control.id = id; control.type = 'checkbox'; control.checked = value;
  control.setAttribute('role', 'switch');
  return control;
}

function linkedDraft(snapshot, draft, operations, target) {
  const cleared = secret => operations[secret]?.action === 'clear';
  if (cleared('embedding_api_key')) (draft.embedding ||= {}).enabled = false;
  if (cleared('webhook_app_secret')) (draft.channel ||= {}).webhook_images = false;
  if (cleared('verification_token') && (draft.channel?.feishu_mode ?? snapshot.settings.channel.feishu_mode) === 'callback' &&
      (snapshot.secrets.app_secret?.configured || operations.app_secret?.action === 'replace')) {
    if (!cleared('app_secret')) throw invalid(l("清空飞书回调令牌前，请明确选择清空 App Secret，以停用飞书应用机器人。"));
  }
  const kind = draft.channel?.kind ?? snapshot.settings.channel.kind;
  const disabled = target in channels && cleared(channels[target][1]);
  if ((kind === 'none' || disabled && kind === target) && snapshot.settings.notifications.enabled) {
    (draft.notifications ||= {}).enabled = false;
  }
}

function checksCandidate(snapshot, body, target) {
  if (target === 'embedding') return body.settings.embedding?.enabled ?? snapshot.settings.embedding.enabled;
  const primary = channels[target][1];
  return body.secrets[primary]?.action === 'replace' ||
    body.secrets[primary]?.action !== 'clear' && snapshot.secrets[primary]?.configured;
}

export function createSettingsPage({ sessionRequest, handleFailure, onSaved = () => {}, onDirtyChange = () => {},
  confirmDiscard = () => Promise.resolve(true), sync = async () => {}, lastSync = () => null }) {
  let root, snapshot, lifecycle, editor, pageError, reload, timer, poll, unbindPrivacy, form, indexText, indexProgress, history;
  let currentTab = 'general', queue = Promise.resolve(), pending = 0, conflict = false, reloading = null, disposed = false;
  let refreshView = () => {};
  let tabs = new Map(), panels = new Map(), summaries = new Map();
  const retrying = new Set();
  const session = new AbortController();
  const hasChanges = () => editor?.hasChanges() ?? false;

  async function requestWhile(owner, path, options) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (owner.signal.aborted) throw new DOMException('Settings view closed', 'AbortError');
    owner.signal.addEventListener('abort', abort, { once: true });
    try { return await sessionRequest(path, { ...options, controller }); }
    finally { owner.signal.removeEventListener('abort', abort); }
  }

  function failure(node, err) {
    if (err.name === 'AbortError') return;
    const messages = {
      SETTINGS_CONFLICT: l("设置已在其他页面更新。重新加载后再编辑。"),
      SETTINGS_BUSY: l("设置服务繁忙，请稍后重新保存。"),
      INVALID_ARGUMENT: l("设置不合法，请检查字段、时段和密钥配置。"),
      SETTINGS_INPUT_INVALID: err.localizedMessage,
      EMBEDDING_KEY_ORIGIN: l("API 地址更换了域名、协议或端口，请替换或清空密钥。"),
      EMBEDDING_AUTH: l("Embedding 密钥被拒绝，请检查密钥和模型权限。"),
      EMBEDDING_REJECTED: l("Embedding 请求被拒绝，请检查协议、地址、模型和维度。"),
      EMBEDDING_PROTOCOL: l("Embedding 返回格式或维度不符合配置。"),
      EMBEDDING_RATE_LIMITED: l("Embedding 调用受限，请稍后重试。"),
      EMBEDDING_UNAVAILABLE: l("Embedding 连接失败，请检查服务和地址。"),
      CHANNEL_AUTH: l("飞书拒绝了应用凭据，请检查 App ID 和 App Secret。"),
      MODEL_CHECK_TIMEOUT: l("连接检查超时，请稍后重试。"),
      CHANNEL_CHECK_TIMEOUT: l("连接检查超时，请稍后重试。"),
    };
    node.hidden = false;
    if (messages[err.code] || connectionLabels[err.code]) setText(node, messages[err.code] || connectionLabels[err.code]);
    else handleFailure(node, err);
  }

  function markConflict() {
    conflict = true;
    reload.hidden = false;
    if (editor) { editor.reload.hidden = false; editor.save.disabled = true; }
    failure(editor?.error || pageError, { code: 'SETTINGS_CONFLICT' });
  }

  function stopPoll() {
    clearTimeout(timer); timer = null;
    const request = poll; poll = null; request?.abort();
  }

  function commit(intent, { target, valid, node = pageError, saved = () => {}, lock = () => {}, deferred = false } = {}) {
    const page = lifecycle;
    const owner = valid ? page : session;
    const active = () => !disposed && (!valid || !page.signal.aborted && valid());
    pending++; stopPoll();
    const task = async () => {
      try {
        if (!active()) return;
        if (conflict) throw { code: 'SETTINGS_CONFLICT' };
        const { draft, operations = {} } = structuredClone(intent);
        linkedDraft(snapshot, draft, operations, target);
        const body = buildSettingsPatch(snapshot, draft, operations);
        if (!Object.keys(body.settings).length && !Object.keys(body.secrets).length) {
          saved(); return;
        }
        node.hidden = true;
        if (target && checksCandidate(snapshot, body, target)) {
          const endpoint = target === 'embedding' ? '/models/embedding/check' : '/channels/check';
          const candidate = {
            version: body.version,
            settings: target === 'embedding' ? { embedding: body.settings.embedding || {} } : { channel: body.settings.channel || {} },
            secrets: body.secrets,
            ...(target === 'embedding' ? {} : { target }),
          };
          const checked = await requestWhile(owner, endpoint, { method: 'POST', body: candidate });
          if (!active()) return;
          if (checked.version !== body.version) throw { code: 'SETTINGS_CONFLICT' };
        }
        const before = snapshot;
        lock();
        const data = await requestWhile(owner, '/settings', { method: 'PATCH', body });
        if (!active()) return;
        snapshot = data;
        const pushStopped = before.settings.notifications.enabled && body.settings.notifications?.enabled === false &&
          (target in channels || body.settings.channel?.kind === 'none');
        onSaved(data, before, body);
        saved();
        snackbar(pushStopped ? l("已保存，回顾推送已关闭。") : deferred ? l("已保存，次日生效。") : l("设置已保存。"));
      } catch (err) {
        if (!active()) return;
        if (err.code === 'SETTINGS_CONFLICT') {
          conflict = true;
          if (form) markConflict();
          else snackbar(l("设置已在其他页面更新。重新加载后再编辑。"));
        } else if (err.status === 401) handleFailure(node, err);
        else if (page.signal.aborted) snackbar(l("设置保存失败，请重试。"));
        else failure(node, err);
      } finally {
        pending--;
        if (!pending && form && !disposed) { refreshView(); refresh(); }
      }
    };
    queue = queue.then(task);
    return queue;
  }

  function selectTab(id, focus = false) {
    currentTab = id;
    for (const [key, tab] of tabs) {
      tab.classList.toggle('on', key === id);
      tab.setAttribute('aria-selected', String(key === id));
      tab.tabIndex = key === id ? 0 : -1;
      panels.get(key).hidden = key !== id;
    }
    moveTabSlider();
    const tab = tabs.get(id);
    if (focus) tab.focus({ preventScroll: true });
    tab.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    stopPoll(); refresh();
  }

  function moveTabSlider() {
    const tab = tabs.get(currentTab);
    const slider = form?.querySelector('.settings-tab-slider');
    if (tab && slider) slider.style.transform = `translateX(${tab.offsetLeft}px) scaleX(${tab.offsetWidth / 100})`;
  }

  function summary(parent, title, text, edit) {
    const actions = el('div', 'settings-inline-actions');
    const info = el('span', 'sub');
    actions.append(info, button(l("编辑"), 'ghost', edit));
    row(parent, title, actions);
    summaries.set(info, text);
    return info;
  }

  function editDialog(title, populate, { target, deferred } = {}) {
    if (editor) return;
    const container = el('form', 'settings-edit-form');
    const error = el('p', 'error sub');
    error.setAttribute('role', 'alert'); error.hidden = true;
    const reloadButton = button(l("重新加载设置"), 'ghost', reloadSettings);
    reloadButton.hidden = !conflict;
    const inputs = new Map(), secrets = new Map(), onRebase = [];
    let revision = 0, busy = false, closing = false, locked = [];
    let confirmEmbedding = false, confirmedOrigin = '', addressError = false;
    const requestClose = async () => {
      if (closing || busy) return;
      closing = true;
      if (await confirmDiscard(hasChanges())) modal.close(true);
      closing = false;
    };
    const modal = createModal({
      parent: root, className: 'card settings-edit', label: title,
      canClose: () => { requestClose(); return false; },
      onClose: () => { if (editor?.modal === modal) editor = null; onDirtyChange(); },
    });
    function field(path, label, { type = 'text', options, hint, min, max, step } = {}) {
      const keys = path.split('.');
      const original = fieldValue(snapshot.settings, keys);
      const id = `settings-${path.replaceAll('.', '-')}`;
      const input = type === 'checkbox' ? switchInput(id, original === true) :
        options ? select(options) : el(type === 'textarea' ? 'textarea' : 'input');
      input.id = id; input.name = path;
      if (!options && input.tagName === 'INPUT') input.type = type;
      if (type !== 'checkbox') input.value = original ?? '';
      input.autocomplete = 'off'; input.spellcheck = false;
      if (min !== undefined) input.min = min;
      if (max !== undefined) input.max = max;
      if (step !== undefined) input.step = step;
      if (type === 'number') input.required = true;
      inputs.set(path, { input, type, original, keys });
      row(container, label, input, hint);
      return input;
    }
    function secret(name) {
      const label = secretLabels[name], box = el('div', 'settings-secret');
      const action = select([['keep', l("保持")], ['replace', l("替换")], ['clear', l("清空")]]);
      setAttr(action, 'aria-label', l("{0}操作", { 0: label }));
      const input = el('input');
      input.type = 'password'; input.name = name; input.autocomplete = 'new-password'; input.hidden = true;
      setAttr(input, 'aria-label', l("{0}新值", { 0: label }));
      action.addEventListener('change', () => {
        input.hidden = action.value !== 'replace'; input.required = !input.hidden;
        if (input.hidden) input.value = '';
        else input.focus();
      });
      const status = el('span', 'sub');
      const update = () => setText(status, snapshot.secrets?.[name]?.configured ? l("已配置") : l("未配置"));
      onRebase.push(update); update();
      box.append(status, action, input);
      row(container, label, box); secrets.set(name, { action, input });
    }
    const extra = populate({ container, field, secret, onRebase }) || {};
    const save = el('button', 'save', target ? l("测试并保存") : l("保存"));
    const cancel = button(l("关闭"), 'ghost', requestClose);
    const address = inputs.get('embedding.base_url');
    const needsConfirmation = () => confirmEmbedding &&
      secrets.get('embedding_api_key').action.value === 'replace' &&
      confirmedOrigin !== (embeddingOrigin(readField(address)) || null);
    const confirmAddress = address ? button('', 'ghost settings-confirm-address', () => {
      confirmedOrigin = embeddingOrigin(readField(address));
      if (addressError) { addressError = false; error.hidden = true; }
      updateConfirmation();
      save.focus();
    }) : null;
    if (confirmAddress) container.append(confirmAddress);
    const actions = el('div', 'settings-inline-actions settings-edit-actions');
    actions.append(cancel, reloadButton, save);
    modal.dialog.append(el('h2', '', title), container);
    container.append(error, actions);
    function updateConfirmation() {
      if (!address) return;
      const origin = embeddingOrigin(readField(address));
      if (confirmedOrigin !== origin) confirmedOrigin = '';
      const required = needsConfirmation();
      confirmAddress.hidden = !required || !origin;
      extra.confirmationRequired?.(required);
      setText(confirmAddress, l("确认向 {0} 发送密钥", { 0: origin }));
    }
    const changed = () => {
      revision++; onDirtyChange();
      updateConfirmation();
      if (target === 'embedding') {
        const enabled = inputs.get('embedding.enabled').input.checked && secrets.get('embedding_api_key').action.value !== 'clear';
        setText(save, enabled ? l("测试并保存") : l("保存"));
      } else if (target in channels) {
        const primary = channels[target][1], operation = secrets.get(primary).action.value;
        setText(save, operation === 'replace' || operation === 'keep' && snapshot.secrets[primary]?.configured ?
          l("测试并保存") : l("保存"));
      }
    };
    container.addEventListener('input', changed);
    container.addEventListener('change', changed);
    container.addEventListener('invalid', event => {
      const advanced = event.target.closest('details');
      if (advanced) advanced.open = true;
    }, true);
    container.addEventListener('submit', async event => {
      event.preventDefault();
      if (busy) return;
      try {
        if (needsConfirmation()) {
          addressError = true;
          (confirmAddress.hidden ? address.input : confirmAddress).focus();
          throw invalid(l("Embedding API 地址已变化，请确认密钥的发送地址。"));
        }
        const draft = {}, operations = {};
        for (const field of inputs.values()) {
          const value = readField(field);
          if (same(field.original, value)) continue;
          const [group, key] = field.keys;
          if (key) (draft[group] ||= {})[key] = value;
          else draft[group] = value;
        }
        for (const [name, { action, input }] of secrets) {
          if (action.disabled || action.value === 'keep') continue;
          operations[name] = action.value === 'replace' ? { action: 'replace', value: input.value } : { action: 'clear' };
        }
        addressError = false;
        extra.read?.(draft);
        const stamp = revision;
        busy = true; save.disabled = true;
        await commit({ draft, operations }, {
          target, node: error, deferred, valid: () => editor?.modal === modal && revision === stamp,
          lock: () => {
            locked = [...container.querySelectorAll('input, select, textarea, button')].filter(control => !control.disabled);
            for (const control of locked) control.disabled = true;
          },
          saved: () => modal.close(true),
        });
      } catch (err) {
        failure(error, err);
      } finally {
        for (const control of locked) control.disabled = false;
        locked = [];
        if (!error.hidden && target === 'embedding') container.querySelector('details').open = true;
        busy = false; save.disabled = conflict;
      }
    });
    editor = {
      modal, error, reload: reloadButton, save,
      hasChanges: () => [...inputs.values()].some(field => !same(field.original, readField(field))) ||
        [...secrets.values()].some(({ action }) => action.value !== 'keep') || !!extra.hasChanges?.(),
      rebase() {
        const before = address && embeddingOrigin(readField(address));
        for (const field of inputs.values()) {
          const clean = same(field.original, readField(field));
          field.original = fieldValue(snapshot.settings, field.keys);
          if (clean) {
            if (field.type === 'checkbox') field.input.checked = field.original === true;
            else field.input.value = field.original;
          }
        }
        if (address && before !== embeddingOrigin(readField(address))) {
          confirmEmbedding = true; confirmedOrigin = '';
        }
        extra.rebase?.();
        for (const update of onRebase) update();
        addressError = false;
        changed();
      },
    };
    save.disabled = conflict;
    changed();
    modal.show();
  }

  function editTimezone() {
    editDialog(l("时区"), ({ container, field }) => {
      const input = field('timezone', l("时区"), { hint: l("使用 IANA 名称，例如 Asia/Hong_Kong。") });
      const list = el('datalist'); list.id = 'settings-timezones'; input.setAttribute('list', list.id);
      const system = Intl.DateTimeFormat().resolvedOptions().timeZone;
      for (const zone of new Set([system, 'UTC', ...(Intl.supportedValuesOf?.('timeZone') || [])])) {
        const option = el('option'); option.value = zone; list.append(option);
      }
      container.append(list, button(l("采用设备时区"), 'ghost', () => {
        input.value = system; input.dispatchEvent(new Event('input', { bubbles: true }));
      }));
      return { read: () => {
        try { new Intl.DateTimeFormat('en', { timeZone: input.value.trim() }); }
        catch { throw invalid(l("请填写有效的 IANA 时区，例如 Asia/Hong_Kong。")); }
      } };
    }, { deferred: true });
  }

  function editEmbedding() {
    editDialog(l("向量模型"), ({ container, field, secret, onRebase }) => {
      field('embedding.enabled', l("启用 Embedding"), { type: 'checkbox' });
      const protocol = field('embedding.protocol', l("接口协议"), {
        options: [['dashscope', 'DashScope'], ['openai-compatible', l("OpenAI 兼容 Embedding")]],
      });
      field('embedding.base_url', l("API 地址"), { type: 'url' });
      field('embedding.model', l("模型名称"));
      secret('embedding_api_key');
      const advanced = el('details', 'settings-advanced');
      advanced.append(el('summary', '', l("高级参数"))); container.append(advanced);
      for (const [key, label, min, max, step] of [
        ['dimensions', l("向量维度"), 1, 16384], ['timeout_seconds', l("请求超时（秒）"), 1, 120, 'any'],
        ['max_concurrency', l("并发数"), 1, 8], ['max_retries', l("重试次数"), 0, 3], ['batch_size', l("批量大小"), 1, 20],
      ]) advanced.append(field(`embedding.${key}`, label, { type: 'number', min, max, step }).closest('.settings-row'));
      const send = field('embedding.send_dimensions', l("发送维度参数"), { type: 'checkbox',
        hint: l("让模型按你填写的维度生成向量。模型只支持固定维度时可以关闭。") });
      const instruction = field('embedding.query_instruction', l("查询指令"), { type: 'textarea',
        hint: l("在搜索词前加一句任务提示，帮助模型理解你想找什么。") });
      advanced.append(send.closest('.settings-row'), instruction.closest('.settings-row'));
      const update = () => {
        const generic = protocol.value === 'openai-compatible';
        send.closest('.settings-row').hidden = !generic;
        instruction.closest('.settings-row').hidden = generic;
      };
      protocol.addEventListener('change', update); onRebase.push(update); update();
      let check;
      if (snapshot.settings.embedding.enabled) {
        check = button(l("检查 Embedding 连接"), 'ghost', async () => {
          const ticket = snapshot.version;
          check.disabled = true;
          try {
            const result = await requestWhile(lifecycle, '/models/embedding/check', { method: 'POST', body: { version: ticket } });
            if (check.isConnected && snapshot.version === result.version) snackbar(l("连接正常 · {0} 毫秒", { 0: result.duration_ms }));
          } catch (err) {
            if (check.isConnected) {
              if (err.code === 'SETTINGS_CONFLICT') markConflict();
              else failure(editor.error, err);
            }
          } finally {
            check.disabled = false;
          }
        });
        container.append(check);
      }
      return {
        confirmationRequired(required) { if (check) check.hidden = required; },
        read(draft) {
          if (!draft.embedding) return;
          const embedding = draft.embedding, generic = protocol.value === 'openai-compatible';
          if (embedding.protocol) embedding.provider = generic ? 'openai-compatible' : 'bailian';
          if (generic) embedding.query_instruction = '';
          else embedding.send_dimensions = false;
        },
      };
    }, { target: 'embedding' });
  }

  function editReview() {
    editDialog(l("每日回顾"), ({ field }) => {
      field('review.count', l("每次数量（条）"), { type: 'number', min: 1, max: 20 });
      field('review.min_age_hours', l("等待期（小时）"), { type: 'number', min: 0, max: 87600 });
      field('review.recent_days', l("重复排除期（天）"), { type: 'number', min: 0, max: 3650 });
      field('review.include_archived', l("包含归档笔记"), { type: 'checkbox' });
    }, { deferred: true });
  }

  function editTimes() {
    editDialog(l("推送时段"), ({ container }) => {
      const list = el('div', 'settings-time-list');
      const read = () => [...list.querySelectorAll('input')].map(input => input.value);
      const update = () => {
        const rows = [...list.children];
        rows.forEach((line, index) => {
          setAttr(line.firstChild, 'aria-label', l("推送时段 {0}", { 0: index + 1 }));
          line.lastChild.disabled = rows.length === 1;
        });
        add.disabled = rows.length >= 24;
        container.dispatchEvent(new Event('input'));
      };
      const append = value => {
        const line = el('div', 'settings-time'), input = el('input');
        input.type = 'time'; input.required = true; input.value = value;
        line.append(input, button(l("移除"), 'ghost', () => { line.remove(); update(); }));
        list.append(line); update();
      };
      const add = button(l("添加时段"), 'ghost', () => append(''));
      let original = snapshot.settings.notifications.times;
      for (const clock of original) append(clock);
      container.append(list, add);
      const hasChanges = () => !same(original, read());
      return {
        hasChanges,
        rebase() {
          const edited = hasChanges();
          original = snapshot.settings.notifications.times;
          if (!edited && hasChanges()) {
            list.replaceChildren();
            for (const clock of original) append(clock);
          }
        },
        read(draft) {
          const clocks = read();
          if (!clocks.length || new Set(clocks).size !== clocks.length) throw invalid(l("请填写至少一个有效推送时段，时段不能重复。"));
          if (hasChanges()) draft.notifications = { times: clocks };
        },
      };
    }, { deferred: true });
  }

  function editChannel(target) {
    editDialog(channels[target][0], ({ container, field, secret, onRebase }) => {
      function conditional(trigger, predicate, populate) {
        const anchor = container.lastChild;
        populate();
        const block = el('div'); container.append(block);
        while (anchor.nextSibling !== block) block.append(anchor.nextSibling);
        const update = () => {
          block.hidden = !predicate();
          for (const control of block.querySelectorAll('input,select')) control.disabled = block.hidden;
        };
        trigger.addEventListener('change', update); onRebase.push(update); update();
      }
      if (target === 'qq') {
        container.append(el('p', 'sub', l("支持随时私聊速记与查回顾。")));
        field('channel.qq_app_id', 'QQ AppID').inputMode = 'numeric';
        secret('qq_app_secret');
      } else if (target === 'telegram') {
        secret('bot_token');
        field('channel.destination', l("Telegram 接收会话"), {
          hint: l("留空使用本人私聊（支持随时速记与查回顾），也可填写群或频道的 Chat ID。"),
        });
      } else if (target === 'feishu-app') {
        const mode = field('channel.feishu_mode', l("接收方式"), { options: [['websocket', l("长连接")], ['callback', l("公网回调")]] });
        field('channel.app_id', 'App ID');
        secret('app_secret');
        field('channel.user_open_id', l("用户 Open ID"), {
          hint: l("私人应用可留空以绑定首个私聊，支持随时私聊速记与查回顾。多人应用请指定用户。"),
        });
        conditional(mode, () => mode.value === 'callback', () => {
          secret('verification_token'); secret('encrypt_key');
        });
      } else {
        secret('webhook_url'); secret('webhook_secret');
        const images = field('channel.webhook_images', l("推送图片"), { type: 'checkbox' });
        conditional(images, () => images.checked || snapshot.secrets.webhook_app_secret?.configured, () => {
          field('channel.webhook_app_id', 'Webhook App ID'); secret('webhook_app_secret');
        });
      }
    }, { target });
  }

  function showHistory(items) {
    const rows = items || [];
    history.replaceChildren();
    if (!rows.length) history.append(el('p', 'sub', l("暂无发送记录")));
    for (const item of rows) {
      const line = el('div', 'row settings-row');
      line.append(el('span', '', () => [item.date, item.clock, resolveText(channels[item.channel_kind]?.[0]),
        resolveText(deliveryLabels[item.status] || item.status), item.last_error].filter(Boolean).join(' · ')));
      if (item.status === 'failed') {
        const retry = button(l("重试"), 'ghost', async () => {
          if (retrying.has(item.id)) return;
          const page = lifecycle;
          retrying.add(item.id); retry.disabled = true;
          try {
            await requestWhile(page, `/admin/notifications/${encodeURIComponent(item.id)}/retry`, { method: 'POST', body: {} });
          } catch (err) {
            if (!page.signal.aborted) failure(pageError, err);
          } finally {
            retrying.delete(item.id);
            if (!page.signal.aborted) { stopPoll(); refresh(); }
          }
        });
        retry.disabled = retrying.has(item.id);
        line.append(retry);
      }
      history.append(line);
    }
  }

  async function refresh() {
    if (!form || pending || reloading || conflict || poll || document.hidden || !['search', 'review'].includes(currentTab)) return;
    clearTimeout(timer);
    const request = poll = new AbortController();
    const search = currentTab === 'search';
    try {
      if (search) {
        const data = await requestWhile(request, '/models');
        if (poll !== request) return;
        if (data.version !== snapshot.version) { markConflict(); return; }
        const labels = { disabled: l("语义索引已关闭"), waiting: l("语义索引等待同步或构建"),
          building: l("语义索引构建中"), ready: l("语义索引已就绪"), unavailable: l("语义索引构建失败，等待重试") };
        const generation = data.vector_index?.building;
        indexProgress.hidden = !generation || !['building', 'unavailable'].includes(data.index_status);
        if (!indexProgress.hidden) {
          indexProgress.max = Math.max(1, generation.chunks);
          indexProgress.value = generation.completed_chunks;
        }
        setText(indexText, () => [resolveText(labels[data.index_status] || l("索引状态不可用")),
          !indexProgress.hidden && resolveText(l("{0}/{1} 个片段", { 0: generation.completed_chunks, 1: generation.chunks })),
          !indexProgress.hidden && generation.last_error].filter(Boolean).join(' · '));
      } else {
        const [data, deliveries] = await Promise.all([
          requestWhile(request, '/settings'),
          requestWhile(request, '/admin/notifications?kind=review'),
        ]);
        if (poll !== request) return;
        if (data.version !== snapshot.version) { markConflict(); return; }
        snapshot.channel_status = data.channel_status;
        refreshView(); showHistory(deliveries.notifications);
      }
    } catch (err) {
      if (poll === request) failure(search ? indexText : pageError, err);
    } finally {
      if (poll === request) {
        poll = null; request.abort();
        if (!conflict && !document.hidden) timer = setTimeout(refresh, 5000);
      }
    }
  }

  function render() {
    unbindPrivacy?.();
    tabs = new Map(); panels = new Map(); summaries = new Map();
    form = el('div', 'settings-form'); setAttr(form, 'aria-label', l("产品设置"));
    const tablist = el('div', 'settings-tabs'), slider = el('span', 'settings-tab-slider');
    slider.setAttribute('aria-hidden', 'true'); tablist.append(slider);
    tablist.setAttribute('role', 'tablist'); setAttr(tablist, 'aria-label', l("设置模块"));
    tablist.addEventListener('keydown', event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
      const keys = [...tabs.keys()], index = keys.indexOf(currentTab);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? keys.length - 1 :
        (index + (event.key === 'ArrowRight' ? 1 : -1) + keys.length) % keys.length;
      event.preventDefault(); selectTab(keys[next], true);
    });
    form.append(tablist);
    function section(id, title) {
      const card = el('section', 'card settings-card'), tab = button(title, 'tab', () => selectTab(id));
      card.id = `settings-panel-${id}`; tab.id = `settings-tab-${id}`;
      card.setAttribute('role', 'tabpanel'); card.setAttribute('aria-labelledby', tab.id); card.tabIndex = 0;
      tab.setAttribute('role', 'tab'); tab.setAttribute('aria-controls', card.id);
      tabs.set(id, tab); panels.set(id, card); tablist.append(tab); form.append(card);
      return card;
    }
    pageError = el('p', 'error sub'); pageError.hidden = true; pageError.setAttribute('role', 'alert');
    reload = button(l("重新加载设置"), 'ghost', reloadSettings); reload.hidden = true;
    form.append(pageError, reload);

    const general = section('general', l("通用"));
    const language = select([['auto', l("跟随浏览器")], ['zh-CN', '简体中文'], ['en', 'English']]);
    language.id = 'settings-language'; language.dataset.language = ''; language.value = languagePreference();
    language.addEventListener('change', () => { setLanguage(language.value, { persist: true }); moveTabSlider(); });
    row(general, l("语言"), language);
    summary(general, l("时区"), () => snapshot.settings.timezone, editTimezone);
    const syncActions = el('div', 'settings-inline-actions'), synced = el('span', 'sub');
    const syncButton = button(l("立即同步"), 'ghost', async () => {
      const page = lifecycle;
      syncButton.disabled = true;
      try { await sync(); } finally {
        if (!page.signal.aborted) syncButton.disabled = false;
      }
    });
    syncActions.append(synced, syncButton); row(general, l("上次同步"), syncActions);
    summaries.set(synced, () => lastSync() ? new Date(lastSync()).toLocaleString() : l("尚未同步"));
    window.addEventListener('moerisesync', () => { if (synced.isConnected) setText(synced, summaries.get(synced)()); }, { signal: lifecycle.signal });

    const search = section('search', l("检索与观星"));
    const embeddingStatus = summary(search, l("向量模型"), () => snapshot.settings.embedding.enabled ?
      snapshot.settings.embedding.model || l("已启用 · 已配置") : l("已关闭"), editEmbedding);
    embeddingStatus.id = 'settings-embedding-status';
    indexText = el('p', 'sub'); indexText.id = 'settings-index-status'; indexText.setAttribute('role', 'status');
    indexProgress = el('progress'); indexProgress.hidden = true; setAttr(indexProgress, 'aria-label', l("语义索引进度"));
    search.append(indexText, indexProgress);
    const stargazing = switchInput('settings-stargazing-enabled', snapshot.settings.stargazing.enabled);
    stargazing.name = 'stargazing.enabled';
    stargazing.addEventListener('change', () => commit({ draft: { stargazing: { enabled: stargazing.checked } } }));
    row(search, l("启用观星"), stargazing);
    const rebuild = button(l("重新生成星图"), 'ghost', async () => {
      const page = lifecycle;
      rebuild.disabled = true;
      try {
        await requestWhile(page, '/map/rebuild', { method: 'POST', body: {} });
        if (!page.signal.aborted) snackbar(l("已请求重新生成星图。"));
      } catch (err) {
        if (!page.signal.aborted) failure(pageError, err);
      } finally { rebuild.disabled = false; }
    });
    rebuild.id = 'settings-rebuild-map';
    const rebuildRow = row(search, l("星图更新"), rebuild, l("每日 03:00 更新"));

    const review = section('review', l("回顾与推送"));
    summary(review, l("每日回顾"), () => l("{0} 条", { 0: snapshot.settings.review.count }), editReview);
    const enabled = switchInput('settings-notifications-enabled', snapshot.settings.notifications.enabled);
    enabled.name = 'notifications.enabled';
    enabled.addEventListener('change', () => commit({ draft: { notifications: { enabled: enabled.checked } } }));
    row(review, l("开启回顾推送"), enabled);
    const destination = select(destinations); destination.id = 'settings-channel-kind'; destination.name = 'channel.kind';
    destination.addEventListener('change', () => commit({ draft: { channel: { kind: destination.value } } }));
    row(review, l("定时回顾发往"), destination);
    const destinationHint = el('p', 'sub settings-hint'); review.append(destinationHint);
    summary(review, l("推送时段"), () => snapshot.settings.notifications.times.join(' · '), editTimes);
    review.append(el('h3', '', l("消息渠道")));
    const telegramLink = el('a', 'dlink', l("打开机器人")); telegramLink.target = '_blank'; telegramLink.rel = 'noreferrer';
    for (const [kind, [label, secret]] of Object.entries(channels)) {
      summary(review, label, () => {
        const { state, detail, peer } = snapshot.channel_status?.[kind] || {};
        return [resolveText(connectionLabels[state] || (snapshot.secrets[secret]?.configured ? l("已配置") : l("未配置"))),
          resolveText(connectionLabels[detail] || detail), peer].filter(Boolean).join(' · ');
      }, () => editChannel(kind));
      if (kind === 'telegram') review.append(telegramLink);
    }
    history = el('div');
    review.append(el('h3', '', l("最近发送记录")), history);
    unbindPrivacy = privacySettings(section('privacy', l("隐私")));

    refreshView = () => {
      const value = snapshot.settings, kind = value.channel.kind;
      stargazing.checked = value.stargazing.enabled;
      rebuildRow.hidden = !(value.stargazing.enabled && value.embedding.enabled);
      enabled.checked = value.notifications.enabled; enabled.disabled = kind === 'none';
      destination.value = kind;
      const peer = (kind === 'telegram' ? value.channel.destination : kind === 'feishu-app' ? value.channel.user_open_id : '') ||
        snapshot.channel_status?.[kind]?.peer;
      setText(destinationHint, kind === 'none' ? l("请先选择定时推送渠道。") : peer ? l("接收会话：{0}", { 0: peer }) :
        kind !== 'feishu-webhook' ? l("等待建立私聊") : '');
      const link = snapshot.channel_status?.telegram?.link || '';
      telegramLink.hidden = !/^https:\/\/t\.me\/[a-z\d_]+(?:\?start=[a-z\d_-]{1,64})?$/i.test(link);
      if (telegramLink.hidden) telegramLink.removeAttribute('href');
      else telegramLink.href = link;
      for (const [node, text] of summaries) setText(node, text());
    };
    root.prepend(form);
    refreshView(); selectTab(currentTab);
  }

  async function reloadSettings() {
    if (pending || reloading) return;
    const owner = lifecycle;
    reloading = owner;
    const triggers = [reload, editor?.reload].filter(Boolean);
    for (const trigger of triggers) trigger.disabled = true;
    stopPoll();
    try {
      const data = await requestWhile(owner, '/settings');
      if (owner.signal.aborted) return;
      if (!data?.settings?.channel || !Number.isSafeInteger(data.version)) throw invalid(l("服务返回的设置不完整，请重新加载。"));
      const previous = snapshot;
      if (previous && data.version < previous.version) {
        if (!form) { root.replaceChildren(); render(); }
        return;
      }
      snapshot = data; conflict = false;
      if (previous) onSaved(data, previous);
      if (!form) { root.replaceChildren(); render(); }
      else {
        pageError.hidden = reload.hidden = true;
        if (editor) {
          editor.rebase();
          editor.error.hidden = editor.reload.hidden = true;
          editor.save.disabled = false;
        }
        refreshView(); refresh();
      }
    } catch (err) {
      if (owner.signal.aborted) return;
      if (!form) {
        pageError = el('p', 'error sub');
        reload = button(l("重新加载设置"), 'ghost', reloadSettings);
        root.replaceChildren(pageError, reload);
      }
      failure(editor?.error || pageError, err);
    } finally {
      if (reloading === owner) {
        reloading = null;
        for (const trigger of triggers) trigger.disabled = false;
        if (!owner.signal.aborted) refresh();
      }
    }
  }
  return {
    async mount(container) {
      const owner = lifecycle = new AbortController();
      currentTab = 'general';
      root = el('div', 'settings-page'); root.id = 'settings-page';
      root.append(el('p', 'sub', l("正在加载设置…"))); container.append(root);
      document.addEventListener('visibilitychange', () => document.hidden ? stopPoll() : refresh(), { signal: lifecycle.signal });
      window.addEventListener('resize', moveTabSlider, { signal: lifecycle.signal });
      if (pending) await queue;
      if (!owner.signal.aborted) return reloadSettings();
    },
    destroy() {
      lifecycle?.abort(); reloading = null; stopPoll(); editor?.modal.destroy(); unbindPrivacy?.();
      root?.remove(); root = form = pageError = reload = history = indexText = indexProgress = unbindPrivacy = null;
      tabs.clear(); panels.clear(); summaries.clear();
      refreshView = () => {};
    },
    dispose() { disposed = true; session.abort(); this.destroy(); },
    hasChanges,
  };
}
