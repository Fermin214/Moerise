const statuses = new Set(['disabled', 'waiting', 'building', 'unavailable', 'ready', 'empty']);
const resource = /^memos\/[A-Za-z0-9_-]+$/;
const digest = /^[a-f0-9]{64}$/;
const colors = new Map([['\u60f3\u6cd5', '#7FA6E8'], ['\u7075\u611f', '#7DBBA5'], ['\u6458\u5f55', '#B09BDD'], ['\u6559\u8bad', '#E39A86']]);

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
    scale: Math.max(0.01, Math.min(100, Math.max(1, width - 56) / Math.max(0.01, right - left),
      Math.max(1, height - 56) / Math.max(0.01, bottom - top))) };
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
  element.className = className; element.textContent = text;
  return element;
}

export function createMapPage({ request, handleFailure, validNote, renderNote, initialView, onViewChange }) {
  let alive = true, mapSequence = 0, noteSequence = 0, mapController = null, noteController = null, timer = null, animation = null;
  let result = null, view = null, selected = initialView?.selected || '', readName = '', loading = false;
  let candidate = '', dragged = false, origin = null;
  const markers = new Map(), pointers = new Map();
  const root = node('div', 'map-page'), description = node('p', 'sub', '\u6bcf\u4e2a\u70b9\u662f\u4e00\u6761\u7b14\u8bb0\uff0c\u8bed\u4e49\u76f8\u8fd1\u7684\u9760\u5f97\u66f4\u8fd1 \u00b7 \u70b9\u51fb\u9605\u8bfb');
  const field = node('div', 'card map-field'), message = node('p', 'sub'), reader = node('div', 'map-reader');
  field.id = 'map-field'; field.tabIndex = 0; field.setAttribute('role', 'group'); field.setAttribute('aria-label', '\u8ba4\u77e5\u5730\u56fe');
  field.setAttribute('aria-description', '\u62d6\u52a8\u5e73\u79fb\uff0c\u6eda\u8f6e\u6216\u53cc\u6307\u7f29\u653e\u3002\u65b9\u5411\u952e\u5e73\u79fb\uff0c\u52a0\u51cf\u952e\u7f29\u653e\uff0cHome \u663e\u793a\u5168\u56fe\uff0cR \u5237\u65b0\u3002Tab \u9009\u70b9\uff0cEnter \u9605\u8bfb\u3002');
  message.hidden = true; reader.hidden = true; root.append(description, field, message, reader);
  const activeMap = ticket => alive && mapSequence === ticket;
  const activeNote = ticket => alive && noteSequence === ticket;

  function saveView() {
    if (alive && result && view) onViewChange({ frame: result.layout_frame, ...view, selected });
  }
  function say(text = '', error = false) {
    message.textContent = text; message.hidden = !text;
    message.className = error ? 'error' : 'sub'; message.setAttribute('role', error ? 'alert' : 'status');
  }
  function busy() { root.setAttribute('aria-busy', String(loading || !!noteController)); field.setAttribute('aria-busy', String(loading)); }
  function clearReader() {
    noteSequence++; noteController?.abort(); noteController = null; readName = '';
    reader.replaceChildren(); reader.hidden = true; busy();
  }
  function positionMarkers() {
    animation = null;
    if (!alive || !view || !result) return;
    for (const point of result.points) {
      const marker = markers.get(point.name);
      marker.style.transform = `translate(${(point.x - view.x) * view.scale - 11}px, ${(point.y - view.y) * view.scale - 11}px)`;
      marker.classList.toggle('on', point.name === selected);
      marker.setAttribute('aria-pressed', String(point.name === selected));
    }
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
  function fit() { if (result) move(fitMapView(result.points, field.clientWidth, field.clientHeight)); }
  function refreshLater(delay) {
    clearTimeout(timer);
    if (alive) timer = setTimeout(() => load(false), delay);
  }
  const errorMessages = {
    MAP_RESPONSE_INVALID: '\u670d\u52a1\u8fd4\u56de\u7684\u5730\u56fe\u683c\u5f0f\u65e0\u6548\uff0c\u8bf7\u5237\u65b0\u9875\u9762\u3002',
    MAP_RESOURCE_LIMIT: '\u5730\u56fe\u8bfb\u53d6\u8d85\u8fc7\u6280\u672f\u8d44\u6e90\u9884\u7b97\uff0c\u8bf7\u7a0d\u540e\u5237\u65b0\u3002',
    MAP_LAYOUT_INVALID: '\u5730\u56fe\u5e03\u5c40\u65e0\u6548\uff0c\u8bf7\u91cd\u5efa\u7d22\u5f15\u3002',
    MAP_REQUEST_CANCELED: '\u5730\u56fe\u8bfb\u53d6\u5df2\u53d6\u6d88\u3002',
    REQUEST_TIMEOUT: '\u5730\u56fe\u8bfb\u53d6\u8d85\u65f6\uff0c\u8bf7\u7a0d\u540e\u5237\u65b0\u3002',
  };
  function fail(error) {
    const text = errorMessages[error.code];
    if (text && error.status !== 401) say(text, true);
    else { say('', true); handleFailure(message, error); }
  }
  async function readPoint(name) {
    const point = result?.points.find(item => item.name === name);
    if (!alive || !point) return;
    clearReader(); const ticket = noteSequence;
    selected = name; saveView(); positionMarkers(); noteController = new AbortController(); busy();
    say('\u6b63\u5728\u8bfb\u53d6\u7b14\u8bb0\u2026');
    try {
      const note = await request(`/notes/${name.slice(6)}`, { controller: noteController });
      if (!activeNote(ticket)) return;
      if (!validNote(note) || note.name !== name) throw Object.assign(new Error('Invalid map note'), { code: 'MAP_RESPONSE_INVALID' });
      reader.replaceChildren(renderNote(note)); reader.hidden = false; readName = name; say();
    } catch (error) {
      if (!activeNote(ticket) || error.name === 'AbortError') return;
      fail(error);
    } finally { if (activeNote(ticket)) { noteController = null; busy(); } }
  }
  function resetPointers() {
    for (const id of pointers.keys()) if (field.hasPointerCapture(id)) field.releasePointerCapture(id);
    pointers.clear(); candidate = ''; dragged = false; origin = null; field.classList.remove('dragging');
  }
  function display(next) {
    result = next; field.replaceChildren(); markers.clear(); resetPointers();
    if (!['ready', 'empty'].includes(next.status)) {
      clearReader();
      say({ disabled: '\u8bf7\u5728\u8bbe\u7f6e\u4e2d\u542f\u7528 Embedding\u3002', waiting: '\u8bed\u4e49\u7d22\u5f15\u6b63\u5728\u7b49\u5f85\u540c\u6b65\u6216\u6784\u5efa\u3002',
        building: '\u8bed\u4e49\u7d22\u5f15\u6784\u5efa\u4e2d\uff0c\u53ef\u5728\u8bbe\u7f6e\u4e2d\u67e5\u770b\u8fdb\u5ea6\u3002', unavailable: '\u8bed\u4e49\u7d22\u5f15\u6784\u5efa\u5931\u8d25\uff0c\u53ef\u5728\u8bbe\u7f6e\u4e2d\u67e5\u770b\u9519\u8bef\u3002' }[next.status]);
      if (next.status !== 'disabled') refreshLater(next.status === 'unavailable' ? 10000 : 3000);
      return;
    }
    if (!view || initialView?.frame !== next.layout_frame) {
      view = fitMapView(next.points, field.clientWidth, field.clientHeight);
      if (initialView?.frame === next.layout_frame && validView(initialView)) view = { x: initialView.x, y: initialView.y, scale: initialView.scale };
    }
    initialView = { frame: next.layout_frame, ...view };
    const fragment = document.createDocumentFragment();
    for (const point of next.points) {
      const marker = node('button', 'map-dot'); marker.type = 'button'; marker.dataset.name = point.name;
      marker.setAttribute('aria-label', `\u7b14\u8bb0 ${point.name.slice(6)}`);
      marker.style.backgroundColor = colors.get(point.tags[0]) || '#9AA3AF';
      marker.addEventListener('click', event => { if (event.detail === 0) readPoint(point.name); });
      marker.addEventListener('focus', () => {
        if (!view) return;
        const x = (point.x - view.x) * view.scale, y = (point.y - view.y) * view.scale;
        if (Math.abs(x) > field.clientWidth / 2 - 22 || Math.abs(y) > field.clientHeight / 2 - 22) move({ ...view, x: point.x, y: point.y });
        field.scrollLeft = field.scrollTop = 0;
      });
      markers.set(point.name, marker); fragment.append(marker);
    }
    field.append(fragment); positionMarkers();
    say(next.status === 'empty' ? '\u8fd8\u6ca1\u6709\u53ef\u663e\u793a\u7684\u7b14\u8bb0\u3002' : '');
    if (selected && markers.has(selected)) {
      if (readName !== selected && !noteController) readPoint(selected);
    } else { selected = ''; clearReader(); saveView(); }
  }
  async function load(initial = true) {
    clearTimeout(timer); timer = null; mapSequence++; const ticket = mapSequence;
    mapController?.abort(); mapController = new AbortController(); loading = true; busy();
    if (initial) say('\u6b63\u5728\u52a0\u8f7d\u8ba4\u77e5\u5730\u56fe\u2026');
    try {
      const next = await request('/map', { controller: mapController });
      if (!activeMap(ticket)) return;
      if (!validMap(next)) throw Object.assign(new Error('Invalid map response'), { code: 'MAP_RESPONSE_INVALID' });
      // A changed selected source must never retain its old reading card.
      const previousPoint = result?.points.find(point => point.name === selected), nextPoint = next.points.find(point => point.name === selected);
      if (!nextPoint || previousPoint?.source_hash !== nextPoint.source_hash) clearReader();
      display(next);
    } catch (error) {
      if (!activeMap(ticket) || error.name === 'AbortError') return;
      field.replaceChildren(); markers.clear(); clearReader(); result = null; resetPointers(); fail(error);
      if (alive && error.retryable) refreshLater(3000);
    } finally { if (activeMap(ticket)) { mapController = null; loading = false; busy(); } }
  }

  function gesture() {
    const positions = [...pointers.values()].slice(0, 2);
    const center = positions.reduce((total, position) => ({ x: total.x + position.x / positions.length, y: total.y + position.y / positions.length }), { x: 0, y: 0 });
    return { center, distance: positions.length === 2 ? Math.hypot(positions[0].x - positions[1].x, positions[0].y - positions[1].y) : 0 };
  }
  field.addEventListener('pointerdown', event => {
    if (!view || (event.pointerType === 'mouse' && event.button !== 0)) return;
    event.preventDefault(); field.focus({ preventScroll: true });
    if (!pointers.size) { origin = { x: event.clientX, y: event.clientY }; candidate = event.target.closest('.map-dot')?.dataset.name || ''; dragged = false; }
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (pointers.size > 1) dragged = true;
    field.setPointerCapture(event.pointerId); field.classList.add('dragging');
  });
  field.addEventListener('pointermove', event => {
    if (!pointers.has(event.pointerId) || !view) return;
    const before = gesture(); pointers.set(event.pointerId, { x: event.clientX, y: event.clientY }); const after = gesture();
    if (origin && Math.hypot(event.clientX - origin.x, event.clientY - origin.y) > 5) dragged = true;
    const factor = before.distance > 0 && after.distance > 0 ? after.distance / before.distance : 1;
    move(transformMapView(view, factor, local(before.center.x, before.center.y), local(after.center.x, after.center.y)));
  });
  function endPointer(event) {
    if (!pointers.has(event.pointerId)) return;
    const pick = event.type === 'pointerup' && pointers.size === 1 && !dragged ? candidate : '';
    pointers.delete(event.pointerId);
    if (field.hasPointerCapture(event.pointerId)) field.releasePointerCapture(event.pointerId);
    if (!pointers.size) resetPointers();
    if (pick) readPoint(pick);
  }
  for (const event of ['pointerup', 'pointercancel', 'lostpointercapture']) field.addEventListener(event, endPointer);
  field.addEventListener('wheel', event => {
    if (!view || !result?.points.length) return;
    event.preventDefault();
    const amount = event.deltaY * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? field.clientHeight : 1);
    move(transformMapView(view, Math.exp(Math.max(-1, Math.min(1, -amount * 0.002))), local(event.clientX, event.clientY)));
  }, { passive: false });
  field.addEventListener('keydown', event => {
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
  const resize = new ResizeObserver(() => { if (view && !initialView) fit(); else if (view) positionMarkers(); });
  return {
    async mount(parent) { parent.append(root); resize.observe(field); await load(); },
    destroy() {
      alive = false; mapSequence++; clearTimeout(timer); mapController?.abort(); clearReader(); resetPointers(); resize.disconnect();
      if (animation !== null) cancelAnimationFrame(animation);
      root.remove();
    },
  };
}
