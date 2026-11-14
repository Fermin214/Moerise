import { StarScene } from './scene.js?v=3';

const $ = selector => document.querySelector(selector);
const methods = ['umap', 'pacmap', 'pca'];
const names = { pca: 'PCA', umap: 'UMAP', pacmap: 'PaCMAP' };
const palette = ['#b4d1e9', '#d4c7ad', '#b7bad9', '#a8c8c8', '#cbb7c4', '#bcc8b3', '#9cb9d6', '#c0b5d1', '#cdbfb6', '#a8c5c9', '#c4cbdc', '#c8c4a9'];
const field = $('#field'), sky = $('#sky'), scene = new StarScene(field), context = scene.ctx;
let bundle, scale, stage = 'frozen', method = 'umap', tab = 'topics', visualMode = 'photo';
let preset = 'balanced', embedding = new URLSearchParams(location.search).get('model') || 'minilm384';
let points = [], selected = -1, topicFilter = null, view = { x: 0, y: 0, scale: 1 }, fitScale = 1;
let width = 0, height = 0, revision = '', raf = 0, hovered = -1;
const pointers = new Map();
let dragged = false, origin, picking = -1;
const number = value => value.toLocaleString('zh-CN');
const pct = value => value == null ? '—' : `${(value * 100).toFixed(1)}%`;
const text = (selector, value) => { $(selector).textContent = value; };
const element = (tag, className, value) => { const node = document.createElement(tag); node.className = className; if (value != null) node.textContent = value; return node; };
const color = label => label < 0 ? '#8e9ba9' : palette[label % palette.length];
const layout = () => method === 'umap' ? layoutForPreset() : scale?.layouts[method];
const embeddingData = () => scale?.embedding_variants?.[embedding];
const peers = () => embeddingData()?.neighbors || scale?.neighbors || [];
const embeddingInfo = () => bundle?.embedding_models?.find(item => item.id === embedding)
  || { id: 'minilm384', label: 'MiniLM · 384', dimensions: bundle?.source.dimensions, model: bundle?.source.model };
const data = () => layout()?.stages[stage];
const coordinates = () => data()?.coordinates || [];
const note = local => bundle.notes[scale.indices[local]];
const topic = local => scale.topics.find(value => value.id === scale.labels[local]);
const topicName = local => topic(local)?.label || '独立笔记';
const project = point => ({ x: width / 2 + (point[0] - view.x) * view.scale, y: height / 2 + (point[1] - view.y) * view.scale });

function requestDraw() {
  if (!raf) raf = requestAnimationFrame(() => { raf = 0; draw(); });
}
function resize() {
  const previous = fitScale;
  width = sky.clientWidth; height = sky.clientHeight;
  const ratio = Math.min(devicePixelRatio || 1, 2);
  field.width = Math.round(width * ratio); field.height = Math.round(height * ratio);
  context.setTransform(ratio, 0, 0, ratio, 0, 0);
  if (points.length && Math.abs(view.scale / previous - 1) < .03) fit();
  else requestDraw();
}
new ResizeObserver(resize).observe(sky);

function fit(members = null) {
  const source = members || points;
  if (!source.length) return;
  const xs = source.map(point => point[0]).sort((a, b) => a - b), ys = source.map(point => point[1]).sort((a, b) => a - b);
  const quantile = (values, p) => values[Math.round((values.length - 1) * p)];
  const frame = trim => {
    const left = quantile(xs, trim), right = quantile(xs, 1 - trim);
    const top = quantile(ys, trim), bottom = quantile(ys, 1 - trim);
    return { x: (left + right) / 2, y: (top + bottom) / 2,
      scale: Math.max(1, Math.min((width - 90) / Math.max(.25, right - left), (height - 150) / Math.max(.25, bottom - top))) };
  };
  const full = frame(0);
  view = full;
  if (!members && source.length >= 100 && $('#framing').value === 'subject') {
    const subject = frame(.015); subject.scale = Math.min(subject.scale, full.scale * 1.12);
    const inside = point => Math.abs(point[0] - subject.x) * subject.scale < width / 2 - 20
      && Math.abs(point[1] - subject.y) * subject.scale < height / 2 - 45;
    const coverage = source.filter(inside).length / source.length;
    const groupsPresent = scale.topics.every(group => {
      const members = points.filter((_, i) => scale.labels[i] === group.id);
      return !members.length || members.filter(inside).length / members.length >= .8;
    });
    if (coverage >= .97 && groupsPresent) view = subject;
  }
  if (!members) fitScale = view.scale;
  requestDraw();
}
function zoom(factor, x = width / 2, y = height / 2) {
  const next = Math.max(fitScale * .2, Math.min(fitScale * 50, view.scale * factor));
  view.x += (x - width / 2) / view.scale - (x - width / 2) / next;
  view.y += (y - height / 2) / view.scale - (y - height / 2) / next;
  view.scale = next; requestDraw();
}
function draw() {
  if (!points.length) return;
  const zoomLevel = view.scale / fitScale;
  const neighbors = selected < 0 ? [] : peers()[selected].map(peer => scale.indices.indexOf(peer.index)).filter(i => i < points.length);
  scene.draw(view, {
    width, height, fitScale, mode: visualMode, labels: $('#labels').checked, colors: $('#colors').checked,
    selected, hovered, filter: topicFilter, groups: scale.topics, subtopics: scale.subtopics,
    groupLabels: scale.labels, palette, getNote: note, neighbors,
    base: layout().stages.base.coordinates, movement: $('#movement').checked && stage !== 'base',
    additions: $('#additions').checked && !document.body.classList.contains('immersive'),
  });
  const independent = scale.labels.slice(0, points.length).filter(label => label === -1).length;
  text('#visible-info', `${number(points.length)} 颗星 · ${scale.topics.length} 个主题 · ${independent} 条独立笔记`);
  text('#sky-source', `${names[method]}　${embeddingInfo().label}　${number(points.length)} 条${bundle.source.kind === 'demo' ? '演示' : ''}笔记`);
  text('#zoom-level', `${Math.round(zoomLevel * 100)}%`);
  field.dataset.layoutPreset = method === 'umap' ? preset : method;
  field.dataset.embedding = embedding;
  field.dataset.camera = JSON.stringify(view);
}

function thumbnails() {
  for (const name of methods) {
    const canvas = $(`[data-method="${name}"] canvas`), rect = canvas.getBoundingClientRect();
    canvas.width = rect.width * 2; canvas.height = rect.height * 2;
    const ctx = canvas.getContext('2d'); ctx.scale(2, 2);
    const coords = (name === 'umap' ? layoutForPreset() : scale.layouts[name]).stages[stage].coordinates;
    const bound = Math.max(.1, ...coords.flat().map(Math.abs));
    const unit = Math.min(rect.width - 20, rect.height - 14) / (2 * bound);
    coords.forEach((point, i) => {
      ctx.fillStyle = $('#colors').checked ? color(scale.labels[i]) : '#bad8ef';
      ctx.globalAlpha = .72; ctx.beginPath(); ctx.arc(rect.width / 2 + point[0] * unit, rect.height / 2 + point[1] * unit, .8, 0, 2 * Math.PI); ctx.fill();
    });
  }
}
function layoutForPreset(id = preset, model = embedding) {
  return scale?.embedding_variants?.[model]?.umap_variants?.[id] || scale?.umap_variants?.[id] || scale?.layouts.umap;
}

function metrics() {
  const value = data().metrics, node = $('#metrics');
  node.replaceChildren();
  const values = [
    [pct(value.recall), `语义近邻保持 @${value.k}`, '原始向量余弦近邻，与二维欧氏近邻的重合比例'],
    [pct(value.trustworthiness), '近邻可信度', 'Trustworthiness：二维近邻在高维空间中的排名惩罚，越高越好'],
    [pct(value.same_topic), '同群近邻', '已归类笔记的二维近邻中，属于同一高维主题的比例'],
    [pct(value.drift_p95), '旧点位移 P95', '相似变换对齐后，旧点位移相对于基础布局 P90 半径的比例，越低越稳定'],
  ];
  for (const [score, label, explanation] of values) {
    const cell = element('div', 'metric'); cell.title = explanation;
    cell.append(element('strong', '', score), element('span', '', label)); node.append(cell);
  }
  text('#layout-name', method === 'umap' ? `UMAP · ${bundle.umap_presets?.find(item => item.id === preset)?.label || '均衡'}` : names[method]);
  const extra = scale.added_count;
  text('#stage-caption', stage === 'base' ? `${scale.base_count} 条基础笔记，留出 ${extra} 条用于新增实验。`
    : `${scale.base_count} 条基础笔记 + ${extra} 条新增笔记。`);
  const detail = {
    pca: 'PCA 是线性基线。主题间距有投影含义，局部语义可能重叠。',
    umap: 'UMAP 优先保留局部语义。岛屿间距不宜直接解释为主题距离。',
    pacmap: 'PaCMAP 同时约束近邻与中远距离。关注主题的连续过渡。',
  };
  text('#method-caption', layout().parameters.reason || detail[method]);
}

function change({ reset = false } = {}) {
  if (!scale) return;
  if (method !== 'umap') embedding = 'minilm384';
  points = coordinates();
  scene.setData(points, scale.star_attributes, scale.labels);
  $('#stage').value = stage;
  $('#preview-algorithm').value = method;
  $('#preview-embedding').value = embedding;
  text('#source-info', `${number(bundle.source.vector_notes)} 条已有向量 / ${embeddingInfo().dimensions} 维`);
  $('#source-info').title = embeddingInfo().model || '';
  if (selected >= points.length) closeReader();
  for (const button of document.querySelectorAll('[data-method]')) button.setAttribute('aria-pressed', String(button.dataset.method === method));
  for (const button of document.querySelectorAll('[data-preset]')) {
    button.disabled = !scale.umap_variants?.[button.dataset.preset] && button.dataset.preset !== 'balanced';
    button.setAttribute('aria-pressed', String(method === 'umap' && button.dataset.preset === preset));
  }
  metrics(); renderTopics(); renderNotes(); renderComparison(); thumbnails();
  if (selected >= 0) read(selected, false);
  if (reset) fit(); else requestDraw();
}

function renderTopics() {
  const node = $('#topics'); node.replaceChildren();
  function row(label, count, id, representative = '') {
    const button = element('button', `topic${topicFilter === id ? ' active' : ''}`), line = element('span', 'topic-line');
    line.append(element('i', ''), element('strong', '', label), element('span', '', number(count)));
    if (id != null) line.firstElementChild.style.backgroundColor = color(id);
    button.append(line); if (representative) button.append(element('small', '', representative));
    button.addEventListener('click', () => {
      topicFilter = topicFilter === id ? null : id;
      renderTopics();
      if (topicFilter == null) fit();
      else fit(points.filter((point, i) => scale.labels[i] === topicFilter));
    });
    node.append(button);
  }
  row('全部主题', points.length, null);
  for (const group of scale.topics) row(group.label, scale.labels.slice(0, points.length).filter(value => value === group.id).length,
    group.id, bundle.notes[group.representatives[0]].title);
  if (scale.outliers) row('独立笔记', scale.labels.slice(0, points.length).filter(value => value === -1).length, -1);
}
function renderNotes() {
  if (!scale) return;
  const query = $('#search').value.trim().toLocaleLowerCase(), node = $('#note-list');
  node.replaceChildren();
  const found = points.map((_, i) => i).filter(i => !query || `${note(i).content} ${note(i).tags.join(' ')}`.toLocaleLowerCase().includes(query));
  text('#note-count', `${number(found.length)} 条笔记`);
  for (const i of found.slice(0, 100)) {
    const button = element('button', 'note-row', note(i).title);
    button.append(element('small', '', topicName(i))); button.addEventListener('click', () => read(i, true)); node.append(button);
  }
  if (found.length > 100) node.append(element('p', 'caption', '显示前 100 条，输入关键词缩小范围。'));
}
function renderComparison() {
  if (!scale) return;
  const node = $('#compare'); node.replaceChildren(element('h3', 'compare-title', `${scale.count} 条 · MiniLM 384 算法比较`));
  const table = element('table', 'comparison'), head = element('tr', '');
  for (const name of ['布局', '近邻保持', '旧点位移']) head.append(element('th', '', name));
  const thead = element('thead', ''); thead.append(head); table.append(thead);
  const tbody = element('tbody', '');
  for (const name of methods) {
    const m = (name === 'umap' ? layoutForPreset(preset, 'minilm384') : scale.layouts[name]).stages[stage].metrics, row = element('tr', '');
    for (const value of [names[name], pct(m.recall), pct(m.drift_p95)]) row.append(element('td', '', value));
    tbody.append(row);
  }
  table.append(tbody); node.append(table);
  if (bundle.umap_presets?.length) {
    node.append(element('h3', 'compare-title', `UMAP 精调 · ${embeddingInfo().label}`));
    const table = element('table', 'comparison'), head = element('tr', '');
    for (const title of ['版本', '近邻保持', '同群近邻']) head.append(element('th', '', title));
    const heading = element('thead', ''); heading.append(head); table.append(heading);
    const body = element('tbody', '');
    for (const item of bundle.umap_presets) {
      const m = layoutForPreset(item.id).stages[stage].metrics, row = element('tr', '');
      for (const value of [item.label, pct(m.recall), pct(m.same_topic)]) row.append(element('td', '', value));
      body.append(row);
    }
    table.append(body); node.append(table);
    for (const item of bundle.umap_presets) {
      const p = layoutForPreset(item.id).parameters;
      node.append(element('p', 'compare-note', `${item.label}：近邻 ${p.n_neighbors}，min_dist ${p.min_dist}，spread ${p.spread}。${item.description}。`));
    }
  }
  if (bundle.embedding_models?.length > 1) {
    node.append(element('h3', 'compare-title', '相同参数的向量对照'));
    const table = element('table', 'comparison'), header = element('tr', '');
    for (const title of ['模型', '近邻保持', '同群近邻']) header.append(element('th', '', title));
    const heading = element('thead', ''); heading.append(header); table.append(heading);
    const body = element('tbody', '');
    for (const model of bundle.embedding_models) {
      const m = layoutForPreset(preset, model.id).stages[stage].metrics, row = element('tr', '');
      for (const value of [model.label, pct(m.recall), pct(m.same_topic)]) row.append(element('td', '', value));
      body.append(row);
    }
    table.append(body); node.append(table);
    node.append(element('p', 'compare-note', '主题成员、星体属性、渲染和取景规则固定；近邻保持率以各自模型的原向量计算。同维模型也可直接对照。'));
    const comparisons = scale.embedding_neighbor_comparisons || Object.entries(scale.embedding_variants || {}).map(([id, value]) => ({
      left: 'minilm384', right: id, overlap_at_7: value.source_neighbor_overlap_at_7,
    }));
    const modelLabel = id => bundle.embedding_models.find(model => model.id === id)?.label || id;
    for (const pair of comparisons) {
      node.append(element('p', 'compare-note', `${modelLabel(pair.left)} / ${modelLabel(pair.right)}：原向量 top-7 近邻平均重合 ${pct(pair.overlap_at_7)}。`));
    }
  }
  node.append(element('p', 'compare-note', `MiniLM 384 的 HDBSCAN 识别出 ${scale.topics.length} 个主题，${scale.outliers} 条笔记保持独立。对照共用这些成员关系。`));
  node.append(element('p', 'compare-note', `MiniLM 384 固定旧点后，新增笔记的近邻保持：${methods.map(name => `${names[name]} ${pct(scale.layouts[name].stages.frozen.metrics.new_recall)}`).join('；')}。`));
  node.append(element('p', 'compare-note', '默认固定旧点，通过 transform 放置新增笔记；“位置变化”显示完整重算的位移。A / B 只切换渲染，所有坐标和星体属性共用。'));
  if (scale.previous_clustering) node.append(element('p', 'compare-note', `主题改善：独立笔记 ${scale.previous_clustering.outliers} → ${scale.outliers}，主题 ${scale.previous_clustering.topics} → ${scale.topics.length}。`));
  if (scale.cluster_candidates?.length) {
    node.append(element('h3', 'compare-title', '聚类策略比较'));
    const candidates = element('table', 'comparison');
    const header = element('tr', '');
    for (const title of ['策略', '主题', '独立', '标签一致']) header.append(element('th', '', title));
    const heading = element('thead', ''); heading.append(header); candidates.append(heading);
    const rows = element('tbody', '');
    for (const candidate of scale.cluster_candidates) {
      const row = element('tr', '');
      for (const value of [candidate.name, candidate.clusters, candidate.outliers, pct(candidate.tag_purity)]) row.append(element('td', '', value));
      rows.append(row);
    }
    candidates.append(rows); node.append(candidates);
    node.append(element('p', 'compare-note', '标签一致率用于辅助阅读：各组最常见的原标签占已归类笔记的比例。标签不参与成员分配，各策略覆盖的笔记不同。'));
  }
  const best = [...methods].sort((a, b) => (scale.layouts[b].stages.refit.metrics.recall || 0) - (scale.layouts[a].stages.refit.metrics.recall || 0))[0];
  node.append(element('p', 'compare-note', `这个规模下，${names[best]} 的近邻保持率最高。优先进入视觉设计对比，PCA 保留为基线。主题是否合理，可阅读代表笔记判断。`));
  node.append(element('h3', 'compare-title', '算法参数'));
  node.append(element('p', 'method-detail', JSON.stringify(layout().parameters, null, 2)));
  node.append(element('p', 'method-detail', `${layout().incremental}\n${scale.cluster_parameters.space}\n高维轮廓系数：${scale.semantic_silhouette?.toFixed(3) ?? '—'}\n抽样查询：${data().metrics.queries}\n计算耗时：${data().seconds.toFixed(2)} 秒`));
}
function read(i, center = false) {
  selected = i;
  const value = note(i); text('#reader-title', value.title); text('#reader-content', value.content || '此笔记没有正文。');
  text('#reader-topic', topicName(i));
  const properties = scale.star_attributes?.[i];
  const role = properties?.role === 'core' ? '主题核心' : properties?.role === 'isolated' ? '相对独特的笔记' : '';
  text('#reader-meta', `${role ? role + ' · ' : ''}${value.name}${value.create_time ? ' · ' + value.create_time.slice(0, 10) : ''}`);
  $('#reader-tags').replaceChildren(...value.tags.map(tag => element('span', '', tag)));
  const node = $('#neighbors'); node.replaceChildren();
  for (const peer of peers()[i]) {
    const local = scale.indices.indexOf(peer.index);
    if (local >= points.length) continue;
    const button = element('button', 'note-row', bundle.notes[peer.index].title);
    button.append(element('span', '', pct(peer.similarity)));
    button.addEventListener('click', () => read(local, true)); node.append(button);
  }
  $('#reader').hidden = false;
  if (center) {
    view.x = points[i][0] + (width > 650 ? 160 : 0) / view.scale;
    view.y = points[i][1];
  }
  requestDraw();
}
function closeReader() { selected = -1; $('#reader').hidden = true; requestDraw(); }
$('#reader-close').addEventListener('click', closeReader);

function pick(x, y) {
  const excerpt = scene.pickExcerpt(x, y);
  if (excerpt >= 0) return excerpt;
  let closest = -1, distance = 12;
  points.forEach((point, i) => {
    if (topicFilter != null && scale.labels[i] !== topicFilter) return;
    const at = project(point), delta = Math.hypot(x - at.x, y - at.y);
    if (delta < distance) { closest = i; distance = delta; }
  });
  return closest;
}
function local(event) { const box = field.getBoundingClientRect(); return { x: event.clientX - box.left, y: event.clientY - box.top }; }
function gesture() {
  const values = [...pointers.values()], a = values[0], b = values[1];
  return b ? { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2, distance: Math.hypot(a.x - b.x, a.y - b.y) }
    : { ...a, distance: 0 };
}
field.addEventListener('pointerdown', event => {
  if (event.pointerType === 'mouse' && event.button !== 0) return;
  field.focus({ preventScroll: true });
  const at = local(event); pointers.set(event.pointerId, at); field.setPointerCapture(event.pointerId);
  if (pointers.size === 1) { origin = at; dragged = false; picking = pick(at.x, at.y); }
  else dragged = true;
  $('#hover').hidden = true;
});
field.addEventListener('pointermove', event => {
  const at = local(event);
  if (!pointers.has(event.pointerId)) {
    const hit = pick(at.x, at.y); hovered = hit;
    $('#hover').hidden = hit < 0;
    if (hit >= 0) {
      text('#hover', `${topicName(hit)} · ${note(hit).title}`);
      $('#hover').style.left = `${Math.max(8, Math.min(width - 270, at.x + 14))}px`;
      $('#hover').style.top = `${Math.min(height - 85, at.y + 15)}px`;
    }
    requestDraw(); return;
  }
  const before = gesture(); pointers.set(event.pointerId, at); const after = gesture();
  if (Math.hypot(at.x - origin.x, at.y - origin.y) > 4) dragged = true;
  if (before.distance && after.distance) zoom(after.distance / before.distance, before.x, before.y);
  view.x -= (after.x - before.x) / view.scale; view.y -= (after.y - before.y) / view.scale; requestDraw();
});
function end(event) {
  if (!pointers.has(event.pointerId)) return;
  if (event.type === 'pointerup' && !dragged && picking >= 0) read(picking);
  pointers.delete(event.pointerId);
  if (field.hasPointerCapture(event.pointerId)) field.releasePointerCapture(event.pointerId);
  if (pointers.size) origin = [...pointers.values()][0];
}
for (const event of ['pointerup', 'pointercancel', 'lostpointercapture']) field.addEventListener(event, end);
field.addEventListener('pointerleave', () => { $('#hover').hidden = true; hovered = -1; requestDraw(); });
field.addEventListener('wheel', event => { event.preventDefault(); const at = local(event); zoom(Math.exp(Math.max(-1, Math.min(1, -event.deltaY * .002))), at.x, at.y); }, { passive: false });
field.addEventListener('keydown', event => {
  if (event.key === 'Home') fit();
  else if (['+', '='].includes(event.key)) zoom(1.3);
  else if (event.key === '-') zoom(1 / 1.3);
  else if (event.key === 'Escape') { closeReader(); event.stopPropagation(); }
  else if (event.key.startsWith('Arrow')) {
    view.x += (event.key === 'ArrowRight' ? 50 : event.key === 'ArrowLeft' ? -50 : 0) / view.scale;
    view.y += (event.key === 'ArrowDown' ? 50 : event.key === 'ArrowUp' ? -50 : 0) / view.scale;
  } else return;
  event.preventDefault(); requestDraw();
});
$('#fit').addEventListener('click', () => { topicFilter = null; renderTopics(); fit(); });
$('#framing').addEventListener('change', () => { topicFilter = null; renderTopics(); fit(); });
$('#zoom-in').addEventListener('click', () => zoom(1.4));
$('#zoom-out').addEventListener('click', () => zoom(1 / 1.4));
for (const selector of ['#labels', '#colors', '#movement', '#additions']) $(selector).addEventListener('change', () => { requestDraw(); if (scale) thumbnails(); });
for (const button of document.querySelectorAll('[data-visual]')) button.addEventListener('click', () => {
  visualMode = button.dataset.visual;
  for (const item of document.querySelectorAll('[data-visual]')) item.setAttribute('aria-pressed', String(item.dataset.visual === visualMode));
  requestDraw();
});
for (const button of document.querySelectorAll('[data-preset]')) button.addEventListener('click', () => {
  preset = button.dataset.preset; method = 'umap';
  topicFilter = null; closeReader(); change({ reset: true });
});
function immersion(enabled) {
  document.body.classList.toggle('immersive', enabled);
  text('#immersion', enabled ? '布局与数据' : '沉浸预览');
  requestAnimationFrame(() => { resize(); fit(); if (scale) thumbnails(); });
}
$('#immersion').addEventListener('click', () => immersion(!document.body.classList.contains('immersive')));
$('#fullscreen').addEventListener('click', async () => {
  immersion(true);
  if (!document.fullscreenElement) await document.documentElement.requestFullscreen?.().catch(() => {});
  else await document.exitFullscreen?.();
});
document.addEventListener('fullscreenchange', () => {
  const active = Boolean(document.fullscreenElement);
  $('#fullscreen').setAttribute('aria-label', active ? '退出全屏' : '全屏预览');
  $('#fullscreen').title = active ? '退出全屏' : '全屏预览';
});
document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && $('#reader').hidden && !document.fullscreenElement) immersion(false);
});
$('#preview-algorithm').addEventListener('change', () => { method = $('#preview-algorithm').value; change({ reset: true }); });
$('#preview-embedding').addEventListener('change', () => {
  embedding = $('#preview-embedding').value; method = 'umap';
  topicFilter = null; closeReader(); change({ reset: true });
});
$('#preview-scale').addEventListener('change', () => {
  $('#scale').value = $('#preview-scale').value;
  $('#scale').dispatchEvent(new Event('change'));
});
for (const button of document.querySelectorAll('[data-method]')) button.addEventListener('click', () => { method = button.dataset.method; change({ reset: true }); });
for (const button of document.querySelectorAll('[data-tab]')) button.addEventListener('click', () => {
  tab = button.dataset.tab;
  for (const name of ['topics', 'notes', 'compare']) $(`#${name}`).hidden = name !== tab;
  for (const item of document.querySelectorAll('[data-tab]')) item.setAttribute('aria-selected', String(item.dataset.tab === tab));
});
$('#search').addEventListener('input', renderNotes);
$('#scale').addEventListener('change', () => { scale = bundle.scales[Number($('#scale').value)]; $('#preview-scale').value = $('#scale').value; topicFilter = null; closeReader(); change({ reset: true }); });
$('#stage').addEventListener('change', () => { stage = $('#stage').value; change(); });
function display(value) {
  bundle = value; scale = bundle.scales.at(-1); topicFilter = null; closeReader();
  const models = bundle.embedding_models || [embeddingInfo()];
  if (!models.some(model => model.id === embedding)) embedding = models[0].id;
  $('#preview-embedding').replaceChildren(...models.map(model => {
    const option = element('option', '', model.label); option.value = model.id; return option;
  }));
  $('#preview-embedding').value = embedding;
  text('#source-kind', bundle.source.kind === 'personal' ? '个人笔记' : bundle.source.kind === 'demo' ? '演示语料' : '验证材料');
  text('#source-name', bundle.source.label);
  text('#source-info', `${number(bundle.source.vector_notes)} 条已有向量 / ${bundle.source.dimensions} 维${bundle.source.excluded ? ` / ${bundle.source.excluded} 条无向量` : ''}`);
  $('#source-info').title = `${bundle.source.model || ''}\n${bundle.source.fingerprint}`;
  const select = $('#scale'); select.replaceChildren();
  bundle.scales.forEach((value, i) => {
    const option = element('option', '', `${number(value.count)} 条${i === bundle.scales.length - 1 ? '（全量）' : ''}`);
    option.value = i; select.append(option);
  });
  select.value = bundle.scales.length - 1; select.disabled = false; $('#stage').disabled = false;
  $('#preview-scale').replaceChildren(...[...select.options].map(option => option.cloneNode(true)));
  $('#preview-scale').value = select.value;
  $('#empty').hidden = true; $('#report').hidden = false;
  if (location.protocol !== 'file:') $('#offline').hidden = false;
  change({ reset: true });
}
async function poll() {
  try {
    const response = await fetch('/api/status'), state = await response.json();
    text('#progress', state.message);
    if (!state.has_bundle && state.status === 'empty') text('#loading-message', '导入已有笔记向量，生成语义星图。');
    if (state.has_bundle && state.revision && state.revision !== revision) {
      const response = await fetch('/api/bundle');
      if (!response.ok) throw new Error('读取布局失败');
      display(await response.json()); revision = state.revision;
    }
    if (state.status === 'error') text('#progress', state.message);
  } catch (error) { text('#progress', `读取失败：${error.message}`); }
  setTimeout(poll, bundle ? 15000 : 1600);
}
const dialog = $('#import-dialog');
for (const selector of ['#import-open', '#empty-import']) $(selector).addEventListener('click', () => dialog.showModal());
$('#import-close').addEventListener('click', () => dialog.close());
$('#import-form').addEventListener('submit', async event => {
  event.preventDefault(); const file = $('#data-file').files[0]; if (!file) return;
  $('#import-submit').disabled = true; text('#import-error', '');
  try {
    const response = await fetch('/api/import', { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file });
    const result = await response.json(); if (!response.ok) throw new Error(result.error);
    dialog.close(); text('#progress', '读取笔记向量');
  } catch (error) { text('#import-error', error.message); }
  finally { $('#import-submit').disabled = false; }
});
// BOOT
const initial = $('#initial-data');
if (initial) {
  try { display(JSON.parse(initial.textContent)); revision = initial.dataset.revision; }
  catch (error) { text('#progress', `读取失败：${error.message}`); }
}
poll();
// END BOOT
