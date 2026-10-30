const fields = {
  review: ['count', 'min_age_hours', 'recent_days', 'topic_limit', 'include_archived'],
  notifications: ['enabled', 'time', 'times'],
  llm: ['enabled', 'protocol', 'base_url', 'model', 'timeout_seconds', 'max_concurrency', 'max_retries', 'max_output_tokens', 'context_bytes'],
  embedding: ['enabled', 'provider', 'protocol', 'base_url', 'model', 'dimensions', 'batch_size', 'timeout_seconds', 'max_concurrency', 'max_retries', 'query_instruction'],
  channel: ['kind', 'app_id', 'user_open_id', 'destination'],
  insights: ['default_prompt'],
};
const secretLabels = {
  llm_api_key: 'LLM API Key', embedding_api_key: 'Embedding API Key',
  app_secret: 'App Secret', verification_token: 'Verification Token',
  encrypt_key: 'Encrypt Key', webhook_url: 'Webhook URL', webhook_secret: 'Webhook Secret',
};
function invalid(message) {
  return Object.assign(new Error(message), { code: 'SETTINGS_INPUT_INVALID' });
}
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

export function buildSettingsPatch(snapshot, draft, operations = {}) {
  const settings = {}, secrets = {};
  if (!Number.isSafeInteger(snapshot.version) || snapshot.version < 1) throw invalid('设置版本无效，请重新加载。');
  if (draft.timezone === null) throw invalid('设置不能是空值。');
  if (draft.timezone !== undefined && draft.timezone !== snapshot.settings.timezone) settings.timezone = draft.timezone;
  for (const [group, names] of Object.entries(fields)) {
    for (const name of names) {
      const value = draft[group]?.[name];
      if (value !== undefined && !same(value, snapshot.settings[group]?.[name])) {
        if (value === null) throw invalid('设置不能是空值。');
        (settings[group] ||= {})[name] = value;
      }
    }
  }
  for (const name of Object.keys(secretLabels)) {
    const operation = operations[name];
    if (!operation) continue;
    if (!['keep', 'replace', 'clear'].includes(operation.action)) throw invalid('请选择密钥操作。');
    if (operation.action === 'replace') {
      const value = operation.value;
      if (typeof value !== 'string' || !value.trim() || /[*•●…]/u.test(value)) {
        throw invalid(`${secretLabels[name]} 需要填写真实的新值，不能使用遮蔽字符。`);
      }
      secrets[name] = { action: 'replace', value };
    } else {
      if (operation.value !== undefined) throw invalid('保持或清空密钥不能携带新值。');
      secrets[name] = { action: operation.action };
    }
  }
  return { version: snapshot.version, settings, secrets };
}

function el(tag, className = '', text = '') {
  const node = document.createElement(tag);
  node.className = className;
  if (text) node.textContent = text;
  return node;
}
function button(text, className, action) {
  const node = el('button', className, text);
  node.type = 'button';
  node.addEventListener('click', action);
  return node;
}

export function createSettingsPage({ sessionRequest, handleFailure, onSaved = () => {} }) {
  let root, snapshot, form, error, status, reload, save, controller, pendingBody;
  let sequence = 0, active = false, busy = false, conflict = false;
  let controls = new Map(), secretControls = new Map(), times = [];

  function clearSecrets() {
    for (const { action, input } of secretControls.values()) {
      input.value = ''; input.required = false; input.hidden = true; action.value = 'keep';
    }
    if (pendingBody) for (const operation of Object.values(pendingBody.secrets)) delete operation.value;
    pendingBody = null;
  }
  function destroy() {
    active = false; sequence++; controller?.abort(); clearSecrets();
    root?.remove(); root = snapshot = form = controller = null;
    controls.clear(); secretControls.clear(); times = [];
  }
  function message(text = '') {
    error.textContent = text; error.hidden = !text;
  }
  function failure(err) {
    if (err.name === 'AbortError') return;
    const messages = {
      SETTINGS_CONFLICT: '设置已在其他页面更新。重新加载后再编辑；重新加载会丢弃表单中的修改。',
      SETTINGS_BUSY: '设置服务繁忙，请稍后重新保存。',
      INVALID_ARGUMENT: '设置不合法，请检查字段、时段和密钥配置。',
      SETTINGS_INPUT_INVALID: err.message,
    };
    if (messages[err.code]) message(messages[err.code]);
    else handleFailure(error, err);
  }
  function section(title, target = form) {
    const card = el('section', 'card settings-card');
    card.append(el('h2', '', title)); target.append(card);
    return card;
  }
  function row(parent, label, control, hint = '') {
    const wrapper = el('div', 'row settings-row'), title = el('div', 'settings-label');
    const caption = el('label', '', label);
    caption.htmlFor = control.id; title.append(caption);
    if (hint) title.append(el('span', 'sub', hint));
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
        const option = el('option', '', value || '未配置'); option.value = value ?? '';
        input.append(option); input.value = value ?? '';
      }
    }
    if (min !== undefined) input.min = min;
    if (max !== undefined) input.max = max;
    if (step !== undefined) input.step = step;
    if (type === 'number') input.required = true;
    if (type === 'text') { input.autocomplete = 'off'; input.spellcheck = false; }
    controls.set(path, { input, type, original: value });
    row(parent, label, input, hint);
    return input;
  }
  function secret(parent, name) {
    const label = secretLabels[name], box = el('div', 'settings-secret');
    const configured = snapshot.secrets?.[name]?.configured === true;
    box.append(el('span', 'sub', configured ? '已配置' : '未配置'));
    const action = el('select'); action.id = `settings-secret-${name}`; action.setAttribute('aria-label', `${label}操作`);
    for (const [value, text] of [['keep', '保持'], ['replace', '替换'], ['clear', '清空']]) {
      const option = el('option', '', text); option.value = value; action.append(option);
    }
    const input = el('input'); input.type = 'password'; input.hidden = true;
    input.autocomplete = 'new-password'; input.spellcheck = false; input.setAttribute('aria-label', `${label}新值`);
    input.placeholder = '填写真实的新值';
    action.addEventListener('change', () => {
      input.value = ''; input.hidden = action.value !== 'replace'; input.required = !input.hidden;
      if (!input.hidden) input.focus();
    });
    box.id = `settings-${name}-controls`; box.append(action, input);
    secretControls.set(name, { action, input });
    row(parent, label, box);
    // 可见标签关联密钥操作选择框。
    box.parentElement.querySelector('label').htmlFor = action.id;
  }
  function readDraft() {
    const draft = {};
    for (const [path, { input, type, original }] of controls) {
      let value = type === 'checkbox' ? input.checked : type === 'number' ? input.valueAsNumber : input.value;
      if (type === 'number' && !Number.isFinite(value)) throw invalid('请填写有效的数字。');
      if (value === '' && original == null) continue;
      const [group, name] = path.split('.');
      if (name) (draft[group] ||= {})[name] = value;
      else draft[group] = value;
    }
    try { new Intl.DateTimeFormat('zh-CN', { timeZone: draft.timezone }).format(); }
    catch { throw invalid('请填写有效的 IANA 时区，例如 Asia/Hong_Kong。'); }
    const clocks = times.map(input => input.value);
    if (!clocks.length || clocks.some(clock => !/^(?:[01]\d|2[0-3]):[0-5]\d$/.test(clock)) ||
        new Set(clocks).size !== clocks.length) throw invalid('请填写至少一个有效推送时段，时段不能重复。');
    const previous = snapshot.settings.notifications;
    const previousClocks = previous.times?.length ? previous.times : [previous.time];
    if (!same(clocks, previousClocks)) {
      draft.notifications.times = clocks;
      draft.notifications.time = clocks[0];
    }
    return draft;
  }
  function renderTimes(parent) {
    const box = el('div', 'settings-times'), list = el('div', 'settings-time-list');
    function add(value = '') {
      const line = el('div', 'settings-time'), input = el('input');
      input.type = 'time'; input.required = true; input.value = value;
      const remove = button('移除', 'ghost', () => {
        times = times.filter(item => item !== input); line.remove(); update();
      });
      line.append(input, remove); list.append(line); times.push(input); update();
    }
    function update() {
      times.forEach((input, index) => {
        input.setAttribute('aria-label', `推送时段 ${index + 1}`);
        input.nextElementSibling.disabled = times.length === 1;
      });
      append.disabled = times.length >= 24;
    }
    const append = button('添加时段', 'ghost', () => add());
    const { time, times: clocks } = snapshot.settings.notifications;
    for (const clock of clocks?.length ? clocks : [time || '09:00']) add(clock);
    box.append(list, append, el('span', 'sub', '时段数即每日推送次数；每个时段分配不同笔记。'));
    parent.append(box);
  }
  function model(parent, group, title) {
    const block = el('div', 'settings-model'); block.append(el('h3', '', title)); parent.append(block);
    field(block, `${group}.enabled`, `启用 ${title}`, { type: 'checkbox' });
    if (group === 'embedding') field(block, 'embedding.provider', '供应商', { options: [['bailian', '百炼']] });
    const protocols = group === 'llm'
      ? [['openai-compatible', 'OpenAI Chat Completions'], ['openai-responses', 'OpenAI Responses'], ['anthropic', 'Anthropic']]
      : [['dashscope', 'DashScope']];
    field(block, `${group}.protocol`, '接口协议', { options: protocols });
    field(block, `${group}.base_url`, 'API 地址', { type: 'url' });
    field(block, `${group}.model`, '模型名称');
    secret(block, `${group}_api_key`);
    const advanced = el('details', 'settings-advanced'); advanced.append(el('summary', '', '高级参数')); block.append(advanced);
    field(advanced, `${group}.timeout_seconds`, '请求超时（秒）', { type: 'number', min: 1, max: group === 'llm' ? 110 : 120, step: 'any' });
    field(advanced, `${group}.max_concurrency`, '并发数', { type: 'number', min: 1, max: 8 });
    field(advanced, `${group}.max_retries`, '重试次数', { type: 'number', min: 0, max: 3 });
    if (group === 'llm') {
      field(advanced, 'llm.max_output_tokens', '最大输出 Token', { type: 'number', min: 32, max: 16384 });
      field(advanced, 'llm.context_bytes', '上下文大小（字节）', { type: 'number', min: 1024, max: 262144 });
    } else {
      field(advanced, 'embedding.dimensions', '向量维度', { type: 'number', min: 1 });
      field(advanced, 'embedding.batch_size', '批量大小', { type: 'number', min: 1, max: 20 });
      field(advanced, 'embedding.query_instruction', '查询指令', { type: 'textarea' });
    }
  }
  function render() {
    controls.clear(); secretControls.clear(); times = [];
    form = el('form', 'settings-form'); form.setAttribute('aria-label', '产品设置'); form.autocomplete = 'off';
    form.addEventListener('submit', submit);
    const review = section('每日回顾');
    field(review, 'timezone', '时区', { hint: '使用 IANA 名称，例如 Asia/Hong_Kong。' });
    field(review, 'review.count', '每次数量（条）', { type: 'number', min: 1, max: 20 });
    field(review, 'review.min_age_hours', '等待期（小时）', { type: 'number', min: 0, max: 87600 });
    field(review, 'review.recent_days', '重复排除期（天）', { type: 'number', min: 0, max: 3650 });
    field(review, 'review.include_archived', '包含归档笔记', { type: 'checkbox' });
    field(review, 'notifications.enabled', '开启回顾推送', { type: 'checkbox' });
    const clocks = el('div', 'settings-clocks'); clocks.append(el('h3', '', '推送时段')); review.append(clocks); renderTimes(clocks);
    review.append(el('p', 'sub settings-hint', '回顾参数、时段和时区从后续日期生效，已固定的当天计划保持不变。'));
    const models = section('模型'); model(models, 'llm', 'LLM'); model(models, 'embedding', 'Embedding');
    const insights = section('默认洞察 Prompt');
    const prompt = field(insights, 'insights.default_prompt', '一键洞察使用的 Prompt', { type: 'textarea' });
    prompt.rows = 7;
    const restore = button('恢复默认', 'ghost', () => {
      prompt.value = snapshot.default_insight_prompt; status.textContent = '已恢复默认 Prompt，保存后生效。';
    });
    restore.disabled = typeof snapshot.default_insight_prompt !== 'string'; insights.append(restore);
    const channel = section('消息渠道');
    field(channel, 'channel.kind', '发送方式', { options: [['feishu-app', '飞书应用机器人'], ['feishu-webhook', '飞书 Webhook']] });
    const capabilities = el('div', 'settings-capabilities');
    for (const [key, text] of [['text', '文字推送'], ['images', '图文推送'], ['record', '双向记录']]) {
      const enabled = snapshot.channel_capabilities?.[key] === true;
      capabilities.append(el('span', enabled ? 'tag' : 'sub', `${text}：${enabled ? '可用' : '不可用'}`));
    }
    channel.append(capabilities, el('p', 'sub settings-hint', '仅配置 Webhook URL 可发送文字。图文还需 App ID 和 App Secret；双向记录还需 Verification Token 和用户 Open ID。能力在保存后更新。'));
    field(channel, 'channel.app_id', 'App ID');
    secret(channel, 'app_secret'); secret(channel, 'verification_token'); secret(channel, 'encrypt_key');
    field(channel, 'channel.user_open_id', '用户 Open ID');
    field(channel, 'channel.destination', '推送目标');
    secret(channel, 'webhook_url'); secret(channel, 'webhook_secret');
    const footer = el('div', 'settings-footer');
    error = el('p', 'error sub'); error.setAttribute('role', 'alert'); error.hidden = true;
    status = el('p', 'sub'); status.setAttribute('role', 'status');
    save = el('button', 'save', '保存设置'); save.type = 'submit';
    reload = button('重新加载设置', 'ghost', load);
    footer.append(error, status, reload, save); form.append(footer);
    root.replaceChildren(form); busy = conflict = false;
  }
  async function load() {
    const ticket = ++sequence; controller?.abort(); clearSecrets(); controller = new AbortController();
    busy = true; conflict = false;
    root.replaceChildren(el('p', 'sub', '正在加载设置…'));
    root.setAttribute('aria-busy', 'true');
    try {
      const data = await sessionRequest('/settings', { controller });
      if (!active || sequence !== ticket) return;
      if (!data?.settings || !Number.isSafeInteger(data.version) || !data.settings.review || !data.settings.notifications ||
          !data.settings.llm || !data.settings.embedding || !data.settings.channel || !data.settings.insights) {
        throw invalid('服务返回的设置不完整，请重新加载。');
      }
      snapshot = data; render();
    } catch (err) {
      if (!active || sequence !== ticket) return;
      error = el('p', 'error sub'); error.setAttribute('role', 'alert');
      root.replaceChildren(error, button('重新加载设置', 'ghost', load)); failure(err);
    } finally {
      if (active && sequence === ticket) { busy = false; root.setAttribute('aria-busy', 'false'); }
    }
  }
  async function submit(event) {
    event.preventDefault();
    if (busy || conflict) return;
    message(); status.textContent = '';
    try {
      const operations = Object.fromEntries([...secretControls].map(([name, { action, input }]) =>
        [name, { action: action.value, ...(action.value === 'replace' ? { value: input.value } : {}) }]));
      pendingBody = buildSettingsPatch(snapshot, readDraft(), operations);
      if (!Object.keys(pendingBody.settings).length && !Object.values(pendingBody.secrets).some(op => op.action !== 'keep')) {
        status.textContent = '没有需要保存的修改。'; pendingBody = null; return;
      }
    } catch (err) { failure(err); return; }
    const ticket = ++sequence; controller = new AbortController();
    const replaced = Object.values(pendingBody.secrets).some(op => op.action === 'replace');
    busy = true; save.disabled = reload.disabled = true; form.setAttribute('aria-busy', 'true');
    // 应用设置时锁定表单，避免提交期间修改内容。
    const locked = [...form.querySelectorAll('input, select, textarea, button')].filter(input => !input.disabled);
    for (const input of locked) input.disabled = true;
    try {
      const data = await sessionRequest('/settings', { method: 'PATCH', body: pendingBody, controller });
      if (!active || sequence !== ticket) return;
      clearSecrets(); snapshot = data; render();
      status.textContent = '设置已保存。'; onSaved(data);
    } catch (err) {
      if (!active || sequence !== ticket) return;
      clearSecrets();
      conflict = err.code === 'SETTINGS_CONFLICT'; failure(err);
      if (active && replaced) status.textContent = '新密钥已从表单移除；再次替换时请重新填写。';
    } finally {
      if (active && sequence === ticket) {
        clearSecrets(); busy = false;
        for (const input of locked) input.disabled = false;
        form.setAttribute('aria-busy', 'false'); save.disabled = conflict; reload.disabled = false;
      }
    }
  }
  return {
    mount(container) {
      destroy(); active = true;
      root = el('div', 'settings-page'); root.id = 'settings-page'; container.append(root);
      return load();
    },
    destroy,
  };
}
