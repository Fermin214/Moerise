import { l, setText, setAttr } from './i18n.js';
import { createMapScene } from './map-scene.js';
const statuses = new Set(['disabled', 'waiting', 'building', 'unavailable', 'ready', 'empty']);
const resource = /^memos\/[A-Za-z0-9_-]+$/;
const digest = /^[a-f0-9]{64}$/;

export function validMap(value) {
  if (!value || !statuses.has(value.status) || typeof value.algorithm !== 'string' || !value.algorithm ||
      !Number.isSafeInteger(value.projection_revision) || value.projection_revision < 0 || !Array.isArray(value.points)) return false;
  const ready = value.status === 'ready' || value.status === 'empty';
  if (ready ? !digest.test(value.layout_version) || !digest.test(value.layout_frame) || typeof value.vector_generation !== 'string' || !value.vector_generation :
      value.layout_version !== '' || value.layout_frame !== '' || value.vector_generation !== '' || value.points.length) return false;
  if ((value.status === 'ready' && !value.points.length) || (value.status === 'empty' && value.points.length)) return false;
  const names = new Set();
  for (const point of value.points) {
    if (!point || !resource.test(point.name) || names.has(point.name) || typeof point.source_hash !== 'string' || !point.source_hash ||
        !Number.isFinite(point.x) || !Number.isFinite(point.y) || typeof point.semantic !== 'boolean' ||
        !Array.isArray(point.tags) || point.tags.some(tag => typeof tag !== 'string')) return false;
    names.add(point.name);
  }
  if (value.algorithm === 'pca-topics-v3' && (!Array.isArray(value.topics) || !Array.isArray(value.links))) return false;
  if (value.topics !== undefined || value.links !== undefined) {
    if (!Array.isArray(value.topics) || !Array.isArray(value.links) || value.topics.length > value.points.length ||
        value.links.length > value.points.length * 3 || !ready && (value.topics.length || value.links.length)) return false;
    const semantic = new Set(value.points.filter(point => point.semantic).map(point => point.name));
    const ids = new Set(), assigned = new Set(), districtIds = new Set(), pairs = new Map();
    for (const topic of value.topics) {
      if (!topic || typeof topic.id !== 'string' || !/^topic-[a-f0-9]{16}$/.test(topic.id) || ids.has(topic.id) ||
          typeof topic.label !== 'string' || topic.label.length > 200 || !Number.isFinite(topic.x) || !Number.isFinite(topic.y) ||
          !Array.isArray(topic.members) || !topic.members.length) return false;
      ids.add(topic.id);
      for (const name of topic.members) {
        if (!semantic.has(name) || assigned.has(name)) return false;
        assigned.add(name);
      }
      if (topic.districts !== undefined) {
        if (!Array.isArray(topic.districts) || topic.districts.length > 6) return false;
        const members = new Set(topic.members), local = new Set();
        for (const district of topic.districts) {
          if (!district || typeof district.id !== 'string' || !/^district-[a-f0-9]{16}$/.test(district.id) || districtIds.has(district.id) ||
              typeof district.label !== 'string' || district.label.length > 200 || !Number.isFinite(district.x) || !Number.isFinite(district.y) ||
              !Array.isArray(district.members) || !district.members.length) return false;
          districtIds.add(district.id);
          for (const name of district.members) {
            if (!members.has(name) || local.has(name)) return false;
            local.add(name);
          }
        }
        if (topic.districts.length && local.size !== members.size) return false;
      }
    }
    for (const link of value.links) {
      if (!link || !semantic.has(link.source) || !semantic.has(link.target) || link.source === link.target ||
          !Number.isFinite(link.weight) || link.weight < 0.55 || link.weight > 1) return false;
      const [a, b] = [link.source, link.target].sort();
      if (!pairs.has(a)) pairs.set(a, new Set());
      if (pairs.get(a).has(b)) return false;
      pairs.get(a).add(b);
    }
  }
  return true;
}

export function fitMapView(points, width, height) {
  let left = Infinity, right = -Infinity, top = Infinity, bottom = -Infinity;
  for (const point of points) {
    left = Math.min(left, point.x); right = Math.max(right, point.x);
    top = Math.min(top, point.y); bottom = Math.max(bottom, point.y);
  }
  if (!points.length) return { x: 0, y: 0, scale: 100 };
  return { x: left / 2 + right / 2, y: top / 2 + bottom / 2,
    scale: Math.max(0.01, Math.min(points.length === 1 ? 100 : 1000, Math.max(1, width - 80) / Math.max(0.01, right - left),
      Math.max(1, height - 96) / Math.max(0.01, bottom - top))) };
}

export function transformMapView(view, factor, before, after = before) {
  const scale = Math.max(0.01, Math.min(10000, view.scale * factor));
  return { x: view.x + before.x / view.scale - after.x / scale,
    y: view.y + before.y / view.scale - after.y / scale, scale };
}

function validView(value) {
  return value && Number.isFinite(value.x) && Math.abs(value.x) < 1e6 && Number.isFinite(value.y) && Math.abs(value.y) < 1e6 &&
    Number.isFinite(value.scale) && value.scale >= 0.01 && value.scale <= 10000;
}

function node(tag, className = '', text = '') {
  const element = document.createElement(tag);
  element.className = className; setText(element, text);
  return element;
}

export function createMapPage({ request, handleFailure, validNote, renderNote, initialView, onViewChange, initialData, onData = () => {} }) {
  let alive = true, mapSequence = 0, noteSequence = 0, mapController = null, noteController = null, timer = null, animation = null;
  let result = null, view = null, overviewScale = 100, selected = initialView?.selected || '', readName = '', loading = false;
  let candidate = '', dragged = false, origin = null, nodeDrag = false, holdTimer = null;
  let scene = null, hovered = '', hoverTimer = null, hoverController = null, hoverSequence = 0;
  let excerptTimer = null, desiredExcerpts = [];
  const excerptCache = new Map(), excerptControllers = new Map();
  const points = new Map(), pointers = new Map();
  const root = node('div', 'map-page');
  const field = node('div', 'card map-field'), message = node('p', 'sub'), reader = node('aside', 'map-reader');
  const preview = node('div', 'map-preview'), tools = node('div', 'map-tools');
  preview.hidden = true; preview.setAttribute('role', 'tooltip');
  field.id = 'map-field'; field.tabIndex = 0; field.setAttribute('role', 'group'); setAttr(field, 'aria-label', l("观星"));
  setAttr(field, 'aria-description', l("拖动空白平移，拖动笔记牵动相关节点，滚轮或双指缩放。方向键平移，加减键缩放，Home 显示全图，R 刷新。Tab 选点，Enter 阅读。"));
  setAttr(reader, 'aria-label', l("笔记详情"));
  message.hidden = true; reader.hidden = true; root.append(field, message, reader);
  const activeMap = ticket => alive && mapSequence === ticket;
  const activeNote = ticket => alive && noteSequence === ticket;

  function saveView() {
    if (alive && result && view) onViewChange({ frame: result.layout_frame, ...view, selected });
  }
  function say(text = '', error = false) {
    setText(message, text); message.hidden = !text;
    message.className = error ? 'error' : 'sub'; message.setAttribute('role', error ? 'alert' : 'status');
  }
  function busy() { root.setAttribute('aria-busy', String(loading || !!noteController)); field.setAttribute('aria-busy', String(loading)); }
  function clearReader() {
    noteSequence++; noteController?.abort(); noteController = null; readName = '';
    reader.replaceChildren(); reader.hidden = true; root.classList.remove('reading'); busy();
  }
  function positionMarkers() {
    animation = null;
    if (!alive || !view || !result) return;
    scene?.draw(view, selected, overviewScale);
    saveView();
  }
  function move(next) {
    if (!validView(next)) return;
    view = next; saveView();
    if (animation === null) animation = requestAnimationFrame(positionMarkers);
  }
  function local(clientX, clientY) {
    const rect = field.getBoundingClientRect();
    return { x: clientX - rect.left - rect.width / 2, y: clientY - rect.top - rect.height / 2 };
  }
  function fit() { if (result) { setHover(''); move(fitMapView(result.points, field.clientWidth, field.clientHeight)); } }
  function tool(label, path, action, parent = tools) {
    const button = node('button', 'btn'); button.type = 'button';
    setAttr(button, 'aria-label', label); setAttr(button, 'title', label);
    const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    icon.setAttribute('class', 'i'); icon.setAttribute('viewBox', '0 0 24 24'); icon.setAttribute('aria-hidden', 'true');
    const shape = document.createElementNS('http://www.w3.org/2000/svg', 'path'); shape.setAttribute('d', path); icon.append(shape); button.append(icon);
    button.addEventListener('click', action); parent.append(button);
    return button;
  }
  tool(l("放大"), 'M5 12h14M12 5v14', () => { if (view) move(transformMapView(view, 1.35, { x: 0, y: 0 })); });
  tool(l("缩小"), 'M5 12h14', () => { if (view) move(transformMapView(view, 1 / 1.35, { x: 0, y: 0 })); });
  tool(l("显示全图"), 'M8 3H3v5M16 3h5v5M21 16v5h-5M8 21H3v-5', fit);
  tool(l("刷新地图"), 'M20 7v5h-5M4 17v-5h5M6 7a7 7 0 0 1 12-1l2 3M4 15l2 3a7 7 0 0 0 12-1', () => load(false));
  function setHover(name) {
    if (hovered === name) return;
    hovered = name; hoverSequence++; clearTimeout(hoverTimer); hoverController?.abort(); hoverController = null;
    preview.hidden = true; scene?.hover(name);
    if (!name || name === selected) return;
    const ticket = hoverSequence;
    hoverTimer = setTimeout(async () => {
      hoverController = new AbortController();
      try {
        const note = await request(`/notes/${name.slice(6)}`, { controller: hoverController });
        if (!alive || ticket !== hoverSequence || !validNote(note) || note.name !== name) return;
        const text = [...note.content.replace(/\s+/gu, ' ').trim()].slice(0, 110).join('') || note.attachments?.[0]?.filename;
        if (!text) return;
        preview.textContent = text;
        const position = scene.position(name);
        if (!position) return;
        preview.hidden = false;
        preview.style.left = `${Math.max(12, Math.min(field.clientWidth - preview.offsetWidth - 12, position.x + 18))}px`;
        preview.style.top = `${Math.max(12, Math.min(field.clientHeight - preview.offsetHeight - 12, position.y + 12))}px`;
      } catch (error) {
        if (alive && ticket === hoverSequence && error.status === 401) handleFailure(message, error);
      } finally { if (ticket === hoverSequence) hoverController = null; }
    }, 240);
  }
  function clearExcerpts() {
    clearTimeout(excerptTimer); desiredExcerpts = []; excerptCache.clear();
    for (const controller of excerptControllers.values()) controller.abort();
    excerptControllers.clear();
  }
  function pumpExcerpts() {
    if (!alive || !scene) return;
    for (const name of desiredExcerpts) {
      if (excerptControllers.size >= 3) break;
      if (excerptCache.has(name) || excerptControllers.has(name)) continue;
      const controller = new AbortController(), target = scene, ticket = mapSequence;
      excerptControllers.set(name, controller);
      request(`/notes/${name.slice(6)}`, { controller }).then(note => {
        if (!activeMap(ticket) || scene !== target || controller.signal.aborted) return;
        if (!validNote(note) || note.name !== name) throw new Error('Invalid map excerpt');
        const text = [...note.content.replace(/!\[[^\]]*\]\([^)]*\)/gu, '')
          .replace(/\[([^\]]*)\]\([^)]*\)/gu, '$1').replace(/[#*`>]/gu, '').replace(/\s+/gu, ' ').trim()]
          .slice(0, 100).join('') || note.attachments?.[0]?.filename || '';
        excerptCache.set(name, text); target.excerpt(name, text);
        if (excerptCache.size > 96) excerptCache.delete(excerptCache.keys().next().value);
      }).catch(error => {
        if (!activeMap(ticket) || scene !== target || controller.signal.aborted) return;
        excerptCache.set(name, ''); target.excerpt(name, '');
        if (error.status === 401) handleFailure(message, error);
      }).finally(() => {
        if (excerptControllers.get(name) === controller) {
          excerptControllers.delete(name);
          if (scene === target && activeMap(ticket)) pumpExcerpts();
        }
      });
    }
  }
  function showExcerpts(names) {
    clearTimeout(excerptTimer); desiredExcerpts = names;
    for (const [name, controller] of excerptControllers) {
      if (!names.includes(name)) { controller.abort(); excerptControllers.delete(name); }
    }
    for (const name of names) if (excerptCache.has(name)) scene.excerpt(name, excerptCache.get(name));
    excerptTimer = setTimeout(pumpExcerpts, 180);
  }
  function refreshLater(delay) {
    clearTimeout(timer);
    if (alive) timer = setTimeout(() => load(false), delay);
  }
  const errorMessages = {
    MAP_RESPONSE_INVALID: l("服务返回的地图格式无效，请刷新页面。"),
    MAP_RESOURCE_LIMIT: l("地图读取超过技术资源预算，请稍后刷新。"),
    MAP_LAYOUT_INVALID: l("地图布局无效，请重建索引。"),
    MAP_REQUEST_CANCELED: l("地图读取已取消。"),
    REQUEST_TIMEOUT: l("地图读取超时，请稍后刷新。"),
  };
  function fail(error) {
    const text = errorMessages[error.code];
    if (text && error.status !== 401) say(text, true);
    else { say('', true); handleFailure(message, error); }
  }
  async function readPoint(name) {
    const point = points.get(name);
    if (!alive || !point) return;
    setHover('');
    clearReader(); const ticket = noteSequence;
    selected = name; saveView(); positionMarkers(); noteController = new AbortController(); busy();
    say(l("正在读取笔记…"));
    try {
      const note = await request(`/notes/${name.slice(6)}`, { controller: noteController });
      if (!activeNote(ticket)) return;
      if (!validNote(note) || note.name !== name) throw Object.assign(new Error('Invalid map note'), { code: 'MAP_RESPONSE_INVALID' });
      const header = node('div', 'map-reader-tools');
      tool(l("关闭笔记详情"), 'M6 6l12 12M18 6L6 18', () => {
        clearReader(); selected = ''; positionMarkers(); field.focus({ preventScroll: true });
      }, header);
      reader.replaceChildren(header, renderNote(note)); reader.hidden = false; root.classList.add('reading'); readName = name; say();
    } catch (error) {
      if (!activeNote(ticket) || error.name === 'AbortError') return;
      fail(error);
    } finally { if (activeNote(ticket)) { noteController = null; busy(); } }
  }
  function resetPointers() {
    clearTimeout(holdTimer); scene?.release();
    for (const id of pointers.keys()) if (field.hasPointerCapture(id)) field.releasePointerCapture(id);
    pointers.clear(); candidate = ''; dragged = false; origin = null; nodeDrag = false; field.classList.remove('dragging');
  }
  function display(next) {
    setHover(''); clearExcerpts(); resetPointers(); scene?.destroy(); scene = null;
    result = next; field.replaceChildren(); points.clear(); field.dataset.rendered = 'false';
    for (const point of next.points) points.set(point.name, point);
    field.append(tools, preview);
    if (!['ready', 'empty'].includes(next.status)) {
      clearReader();
      say({ disabled: l("请在设置中启用 Embedding。"), waiting: l("语义索引正在等待同步或构建。"),
        building: l("语义索引构建中，可在设置中查看进度。"), unavailable: l("语义索引构建失败，可在设置中查看错误。") }[next.status]);
      if (next.status !== 'disabled') refreshLater(next.status === 'unavailable' ? 10000 : 3000);
      return;
    }
    if (!view || initialView?.frame !== next.layout_frame) {
      view = fitMapView(next.points, field.clientWidth, field.clientHeight);
      if (initialView?.frame === next.layout_frame && validView(initialView)) view = { x: initialView.x, y: initialView.y, scale: initialView.scale };
    }
    initialView = { frame: next.layout_frame, ...view };
    scene = createMapScene(field, { onPick: readPoint, onHover: setHover, onExcerpts: showExcerpts,
      onTopic: value => {
        setHover('');
        if (value.topic) {
          const fit = fitMapView(value.members, field.clientWidth * 0.8, field.clientHeight * 0.8);
          move({ ...fit, scale: Math.max(view.scale * 1.4, fit.scale) });
        } else move({ ...view, ...value });
      } });
    overviewScale = fitMapView(next.points, field.clientWidth, field.clientHeight).scale;
    scene.setData(next, overviewScale); positionMarkers();
    say(next.status === 'empty' ? l("还没有可显示的笔记。") : '');
    if (selected && points.has(selected)) {
      if (readName !== selected && !noteController) readPoint(selected);
    } else { selected = ''; clearReader(); saveView(); }
  }
  async function load(initial = true) {
    clearTimeout(timer); timer = null; mapSequence++; const ticket = mapSequence;
    mapController?.abort(); mapController = new AbortController(); loading = true; busy();
    if (initial && !result) say(l("正在加载观星…"));
    try {
      const next = await request('/map', { controller: mapController });
      if (!activeMap(ticket)) return;
      if (!validMap(next)) throw Object.assign(new Error('Invalid map response'), { code: 'MAP_RESPONSE_INVALID' });
      onData(next);
      if (result && ['ready', 'empty'].includes(next.status) && result.status === next.status &&
          result.layout_version === next.layout_version && result.layout_frame === next.layout_frame &&
          result.vector_generation === next.vector_generation && result.projection_revision === next.projection_revision) {
        say(next.status === 'empty' ? l("还没有可显示的笔记。") : '');
        return;
      }
      // A changed selected source must never retain its old reading card.
      const previousPoint = result?.points.find(point => point.name === selected), nextPoint = next.points.find(point => point.name === selected);
      if (!nextPoint || previousPoint?.source_hash !== nextPoint.source_hash) clearReader();
      display(next);
    } catch (error) {
      if (!activeMap(ticket) || error.name === 'AbortError') return;
      if (!result) {
        setHover(''); clearExcerpts(); resetPointers(); scene?.destroy(); scene = null;
        field.replaceChildren(tools, preview); points.clear(); clearReader();
      }
      fail(error);
      if (alive && error.retryable) refreshLater(3000);
    } finally { if (activeMap(ticket)) { mapController = null; loading = false; busy(); } }
  }

  function gesture() {
    const positions = [...pointers.values()].slice(0, 2);
    const center = positions.reduce((total, position) => ({ x: total.x + position.x / positions.length, y: total.y + position.y / positions.length }), { x: 0, y: 0 });
    return { center, distance: positions.length === 2 ? Math.hypot(positions[0].x - positions[1].x, positions[0].y - positions[1].y) : 0 };
  }
  field.addEventListener('pointerdown', event => {
    if (!view || event.target.closest('.map-tools,.map-topic') || (event.pointerType === 'mouse' && event.button !== 0)) return;
    setHover('');
    event.preventDefault(); field.focus({ preventScroll: true });
    field.classList.add('pointer-focus');
    if (!pointers.size) {
      origin = { x: event.clientX, y: event.clientY };
      candidate = event.target.closest('.map-dot')?.dataset.name || scene?.pick(event.clientX, event.clientY) || '';
      dragged = false; nodeDrag = !!candidate && event.pointerType !== 'touch';
      if (candidate && event.pointerType === 'touch') holdTimer = setTimeout(() => {
        if (pointers.size === 1 && !dragged) { nodeDrag = true; scene?.drag(candidate, 0, 0); }
      }, 320);
    }
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (pointers.size > 1) { dragged = true; nodeDrag = false; clearTimeout(holdTimer); scene?.release(); }
    field.setPointerCapture(event.pointerId); field.classList.add('dragging');
  });
  field.addEventListener('pointermove', event => {
    if (!view) return;
    if (!pointers.has(event.pointerId)) {
      if (event.pointerType === 'mouse' && !event.target.closest('.map-tools,.map-topic')) {
        setHover(event.target.closest('.map-dot')?.dataset.name || scene?.pick(event.clientX, event.clientY) || '');
      }
      return;
    }
    const before = gesture(); pointers.set(event.pointerId, { x: event.clientX, y: event.clientY }); const after = gesture();
    if (origin && Math.hypot(event.clientX - origin.x, event.clientY - origin.y) > 5) { dragged = true; clearTimeout(holdTimer); }
    if (nodeDrag && pointers.size === 1) {
      if (dragged) scene?.drag(candidate, event.clientX - origin.x, event.clientY - origin.y);
      return;
    }
    const factor = before.distance > 0 && after.distance > 0 ? after.distance / before.distance : 1;
    move(transformMapView(view, factor, local(before.center.x, before.center.y), local(after.center.x, after.center.y)));
  });
  function endPointer(event) {
    if (!pointers.has(event.pointerId)) return;
    const pick = event.type === 'pointerup' && pointers.size === 1 && !dragged ? candidate : '';
    const activate = event.type === 'pointerup' && pointers.size === 1 && !dragged && !candidate;
    pointers.delete(event.pointerId);
    if (field.hasPointerCapture(event.pointerId)) field.releasePointerCapture(event.pointerId);
    if (!pointers.size) resetPointers();
    if (pick) readPoint(pick);
    else if (activate) scene?.activate(event.clientX, event.clientY);
  }
  for (const event of ['pointerup', 'pointercancel', 'lostpointercapture']) field.addEventListener(event, endPointer);
  field.addEventListener('pointerleave', () => { if (!pointers.size) setHover(''); });
  field.addEventListener('blur', () => field.classList.remove('pointer-focus'));
  field.addEventListener('contextmenu', event => { if (nodeDrag) event.preventDefault(); });
  field.addEventListener('wheel', event => {
    if (!view || !result?.points.length) return;
    event.preventDefault(); setHover('');
    const amount = event.deltaY * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? field.clientHeight : 1);
    move(transformMapView(view, Math.exp(Math.max(-1, Math.min(1, -amount * 0.002))), local(event.clientX, event.clientY)));
  }, { passive: false });
  field.addEventListener('keydown', event => {
    field.classList.remove('pointer-focus');
    if (event.altKey || event.ctrlKey || event.metaKey || event.target !== field) return;
    const key = event.key;
    if (key.toLowerCase() === 'r') { event.preventDefault(); load(false); return; }
    if (!view) return;
    if (key === 'Home') { event.preventDefault(); fit(); }
    else if (['+', '=', '-', '_'].includes(key)) { event.preventDefault(); move(transformMapView(view, ['+', '='].includes(key) ? 1.25 : 0.8, { x: 0, y: 0 })); }
    else if (['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(key)) {
      event.preventDefault(); const delta = (event.shiftKey ? 100 : 40) / view.scale;
      move({ ...view, x: view.x + (key === 'ArrowLeft' ? -delta : key === 'ArrowRight' ? delta : 0),
        y: view.y + (key === 'ArrowUp' ? -delta : key === 'ArrowDown' ? delta : 0) });
    }
  });
  const resize = new ResizeObserver(() => {
    if (!view || !result) return;
    overviewScale = fitMapView(result.points, field.clientWidth, field.clientHeight).scale;
    if (!initialView) fit(); else positionMarkers();
  });
  return {
    async mount(parent) {
      parent.append(root); resize.observe(field);
      if (validMap(initialData)) display(initialData);
      await load(!result);
    },
    refresh() { clearReader(); setHover(''); clearExcerpts(); return load(false); },
    destroy() {
      alive = false; mapSequence++; clearTimeout(timer); mapController?.abort(); setHover(''); clearExcerpts(); clearReader(); resetPointers(); resize.disconnect(); scene?.destroy();
      if (animation !== null) cancelAnimationFrame(animation);
      root.remove();
    },
  };
}
