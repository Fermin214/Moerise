import { l, setAttr, setText } from './i18n.js';
import { paintRegions, regionPath } from './map-paint.js';
import { buildMapRegions, insidePolygon, visibleRegions } from './map-regions.js';

const palette = ['#6D94CA', '#67A997', '#AA8EBB', '#D39C69', '#7F9DA9', '#BD8997', '#94A66F', '#898FBB'];
const tagColors = new Map([['想法', '#7FA6E8'], ['灵感', '#7DBBA5'], ['摘录', '#B09BDD'], ['教训', '#E39A86']]);
const markerBudget = 160;

export function createMapScene(field, { onPick, onHover, onTopic, onExcerpts }) {
  const canvas = document.createElement('canvas');
  canvas.className = 'map-canvas'; canvas.setAttribute('aria-hidden', 'true');
  const controls = document.createElement('div'); controls.className = 'map-points';
  const labels = document.createElement('div'); labels.className = 'map-topics';
  field.append(canvas, controls, labels);
  const context = canvas.getContext('2d', { alpha: false });
  const reduced = matchMedia('(prefers-reduced-motion:reduce)');
  let points = [], topics = [], view = null, selected = '', hovered = '', dragged = '';
  let frame = null, destroyed = false, last = 0, width = 0, height = 0, ratio = 1;
  const byName = new Map(), colors = new Map(), neighbors = new Map(), bodies = new Map(), markers = new Map();
  const topicButtons = new Map(), regionsByName = new Map();
  const labelWidths = new Map();
  const excerpts = new Map();
  let roots = [], visible = [], shown = [], excerptKey = '';
  let worker = null, raster = null, rasterPending = false;
  const fallback = document.createElement('canvas');

  function makeWorker() {
    if (typeof Worker === 'undefined' || typeof OffscreenCanvas === 'undefined') return;
    try {
      worker = new Worker(new URL('./map-worker.js', import.meta.url), { type: 'module' });
      worker.onmessage = ({ data }) => {
        raster?.bitmap?.close?.(); raster = data; rasterPending = false; queue();
      };
      worker.onerror = () => { worker?.terminate(); worker = null; rasterPending = false; queue(); };
    } catch { worker = null; }
  }
  function reusableRaster(target) {
    return raster && raster.width === target.width && raster.height === target.height && raster.ratio === target.ratio &&
      raster.view.scale === target.view.scale &&
      Math.abs(raster.view.x - target.view.x) * target.view.scale < 80 &&
      Math.abs(raster.view.y - target.view.y) * target.view.scale < 80;
  }
  function baseMap() {
    const target = { type: 'draw', view: { ...view }, width: width + 256, height: height + 256, viewportArea: width * height, ratio };
    if (!reusableRaster(target) && !rasterPending) {
      if (worker) { rasterPending = true; worker.postMessage(target); }
      else {
        fallback.width = Math.round(target.width * ratio); fallback.height = Math.round(target.height * ratio);
        const brush = fallback.getContext('2d', { alpha: false });
        paintRegions(brush, target, roots);
        raster = { ...target, bitmap: fallback };
      }
    }
    if (!raster) return false;
    const scale = view.scale / raster.view.scale;
    context.drawImage(raster.bitmap, width / 2 + (raster.view.x - view.x) * view.scale - raster.width * scale / 2,
      height / 2 + (raster.view.y - view.y) * view.scale - raster.height * scale / 2, raster.width * scale, raster.height * scale);
    return true;
  }

  function screen(point) {
    const offset = bodies.get(point.name);
    return { x: width / 2 + (point.x - view.x) * view.scale + (offset?.x || 0),
      y: height / 2 + (point.y - view.y) * view.scale + (offset?.y || 0) };
  }
  function focusRegion(region) {
    const bounds = region.bounds;
    const scale = Math.min(Math.max(1, width - 100) / Math.max(0.01, bounds.right - bounds.left),
      Math.max(1, height - 100) / Math.max(0.01, bounds.bottom - bounds.top));
    onTopic({ x: region.x, y: region.y, scale: Math.min(10000, Math.max(view.scale * 1.4, scale)) });
  }
  function queue() {
    if (!destroyed && frame === null) frame = requestAnimationFrame(draw);
  }
  function neighborhood(name) {
    const result = new Set([name]);
    for (const edge of neighbors.get(name) || []) {
      result.add(edge.name);
      for (const next of (neighbors.get(edge.name) || []).slice(0, 2)) {
        if (result.size < 28) result.add(next.name);
      }
    }
    return result;
  }
  function wake(name) {
    if (reduced.matches) return;
    for (const id of neighborhood(name)) {
      if (!bodies.has(id)) bodies.set(id, { x: 0, y: 0, vx: 0, vy: 0 });
    }
    queue();
  }
  function simulate(timestamp) {
    if (!bodies.size || reduced.matches) return false;
    const delta = Math.min(1.8, Math.max(0.5, (timestamp - last) / 16.67 || 1));
    let moving = false;
    const forces = new Map();
    for (const [name, body] of bodies) {
      let fx = -body.x * 0.13, fy = ((name === hovered ? -6 : 0) - body.y) * 0.13;
      for (const neighbor of neighbors.get(name) || []) {
        const other = bodies.get(neighbor.name);
        if (!other) continue;
        fx += (other.x - body.x) * 0.045 * neighbor.weight;
        fy += (other.y - body.y) * 0.045 * neighbor.weight;
      }
      forces.set(name, { fx, fy });
    }
    for (const [name, body] of bodies) {
      if (name === dragged) continue;
      const { fx, fy } = forces.get(name);
      body.vx = (body.vx + fx * delta) * 0.72;
      body.vy = (body.vy + fy * delta) * 0.72;
      body.x += body.vx * delta; body.y += body.vy * delta;
      if (Math.abs(body.vx) + Math.abs(body.vy) > 0.035) moving = true;
      if (name !== hovered && !dragged && Math.abs(body.x) + Math.abs(body.y) < 0.08) bodies.delete(name);
    }
    return moving;
  }
  function buttonFor(point) {
    const marker = document.createElement('button');
    marker.type = 'button'; marker.className = 'map-dot'; marker.dataset.name = point.name;
    marker.style.color = colors.get(point.name);
    marker.style.setProperty('--node-color', colors.get(point.name));
    setAttr(marker, 'aria-label', l("笔记 {0}", { 0: point.name.slice(6) }));
    marker.addEventListener('click', event => { if (event.detail === 0) onPick(point.name); });
    marker.addEventListener('focus', () => {
      onHover(point.name);
      const position = screen(point);
      if (position.x < 20 || position.x > width - 20 || position.y < 20 || position.y > height - 20) {
        onTopic({ x: point.x, y: point.y, scale: view.scale });
      }
      field.scrollLeft = field.scrollTop = 0;
    });
    markers.set(point.name, marker); controls.append(marker);
    return marker;
  }
  function cardPoints(candidates, labelRects) {
    const placed = labelRects.map(rect => ({ left: rect.x, right: rect.right, top: rect.y, bottom: rect.bottom }));
    const names = new Set(), compact = width < 600;
    const cardWidth = compact ? 132 : 164, cardHeight = 72, limit = compact ? 4 : 10;
    const sorted = candidates.map(point => ({ point, ...screen(point) }))
      .sort((a, b) => Math.hypot(a.x - width / 2, a.y - height / 2) - Math.hypot(b.x - width / 2, b.y - height / 2));
    for (const point of sorted) {
      const parcel = regionsByName.get(point.point.name);
      if (!parcel || !insidePolygon({ x: point.point.x - (cardWidth / 2 + 8) / view.scale, y: point.point.y - (cardHeight / 2 + 8) / view.scale }, parcel.polygon) ||
          !insidePolygon({ x: point.point.x + (cardWidth / 2 + 8) / view.scale, y: point.point.y + (cardHeight / 2 + 8) / view.scale }, parcel.polygon) ||
          !insidePolygon({ x: point.point.x - (cardWidth / 2 + 8) / view.scale, y: point.point.y + (cardHeight / 2 + 8) / view.scale }, parcel.polygon) ||
          !insidePolygon({ x: point.point.x + (cardWidth / 2 + 8) / view.scale, y: point.point.y - (cardHeight / 2 + 8) / view.scale }, parcel.polygon)) continue;
      const rect = { left: point.x - cardWidth / 2 - 10, right: point.x + cardWidth / 2 + 10,
        top: point.y - cardHeight / 2 - 10, bottom: point.y + cardHeight / 2 + 10 };
      if (rect.left < 10 || rect.right > width - 10 || rect.top < 60 || rect.bottom > height - 10 ||
          placed.some(other => rect.left < other.right && rect.right > other.left && rect.top < other.bottom && rect.bottom > other.top)) continue;
      placed.push(rect); names.add(point.point.name);
      if (names.size === limit) break;
    }
    return names;
  }
  function draw(timestamp) {
    frame = null;
    if (!view || destroyed) return;
    width = field.clientWidth; height = field.clientHeight;
    const cardWidth = width < 600 ? '132px' : '164px';
    if (field.style.getPropertyValue('--map-excerpt-width') !== cardWidth) field.style.setProperty('--map-excerpt-width', cardWidth);
    const nextRatio = Math.min(2, devicePixelRatio || 1);
    if (canvas.width !== Math.round(width * nextRatio) || canvas.height !== Math.round(height * nextRatio)) {
      ratio = nextRatio; canvas.width = Math.round(width * ratio); canvas.height = Math.round(height * ratio);
    }
    const moving = simulate(timestamp); last = timestamp;
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.fillStyle = '#FBFCFE'; context.fillRect(0, 0, width, height);
    const focus = dragged || hovered || selected;
    const painted = baseMap();
    const layer = visibleRegions(roots, view, width, height);
    visible = layer.regions;
    const candidates = visible.filter(node => node.name).map(node => byName.get(node.name)).filter(Boolean);
    // Sparse collections remain directly accessible at the overview.
    if (points.length <= 24) for (const point of points) if (!candidates.includes(point)) candidates.push(point);
    shown = candidates.slice(0, markerBudget);
    const labelRects = placeLabels(layer.labels);
    const cards = cardPoints(shown, labelRects);
    const nextExcerpts = [...cards].sort().join('|');
    if (nextExcerpts !== excerptKey) { excerptKey = nextExcerpts; onExcerpts?.([...cards]); }
    field.dataset.detail = visible.some(node => node.name) ? 'notes' : visible.some(node => node.kind !== 'topic') ? 'districts' : 'topics';
    field.dataset.regions = String(visible.length);
    if (focus && byName.has(focus)) {
      const source = screen(byName.get(focus));
      for (const edge of neighbors.get(focus) || []) {
        const target = screen(byName.get(edge.name));
        context.beginPath(); context.moveTo(source.x, source.y); context.lineTo(target.x, target.y);
        context.strokeStyle = colors.get(focus); context.globalAlpha = 0.18 + edge.weight * 0.25;
        context.lineWidth = 0.8 + edge.weight; context.stroke();
      }
    }
    context.globalAlpha = 1;
    for (const name of new Set([focus, selected, ...bodies.keys()])) {
      const parcel = regionsByName.get(name), body = bodies.get(name);
      if (!parcel || !visible.some(node => node.id === parcel.id)) continue;
      regionPath(context, parcel.polygon, view, width, height, body?.x || 0, body?.y || 0);
      context.fillStyle = `${colors.get(name)}A0`; context.fill();
      context.strokeStyle = name === focus || name === selected ? colors.get(name) : '#FBFCFE';
      context.lineWidth = name === focus || name === selected ? 2.5 : 1.6; context.stroke();
    }
    const keep = new Set(shown.map(point => point.name));
    for (const [name, marker] of markers) {
      if (!keep.has(name) && marker !== document.activeElement) { marker.remove(); markers.delete(name); }
    }
    for (const point of shown) {
      const marker = markers.get(point.name) || buttonFor(point), position = screen(point);
      const card = cards.has(point.name) && excerpts.get(point.name) !== '';
      const wasCard = marker.classList.contains('map-excerpt');
      marker.classList.toggle('map-excerpt', card);
      if (wasCard !== card) marker.style.color = card ? 'var(--ink)' : colors.get(point.name);
      if (card && marker.textContent !== (excerpts.get(point.name) || '')) {
        const content = document.createElement('span'); content.textContent = excerpts.get(point.name) || '';
        marker.replaceChildren(content);
      }
      else if (!card && marker.textContent) marker.textContent = '';
      const transform = `translate(${position.x - (card ? width < 600 ? 66 : 82 : 16)}px, ${position.y - (card ? 36 : 16)}px)`;
      if (marker.style.transform !== transform) marker.style.transform = transform;
      const pressed = String(point.name === selected);
      if (marker.getAttribute('aria-pressed') !== pressed) {
        marker.classList.toggle('on', point.name === selected); marker.setAttribute('aria-pressed', pressed);
      }
    }
    if (painted && field.dataset.rendered !== 'true') field.dataset.rendered = 'true';
    if (moving) queue();
  }
  function placeLabels(active) {
    const placed = [];
    const activeIds = new Set(active.map(node => node.id));
    for (const [id, button] of topicButtons) {
      if (!activeIds.has(id)) button.hidden = true;
      const value = button.offsetWidth;
      if (value) labelWidths.set(id, value);
    }
    // Subtopic names take priority when their parent has expanded.
    for (const topic of [...active].reverse()) {
      const button = topicButtons.get(topic.id);
      if (!button) continue;
      const center = screen({ ...topic, name: '' });
      const labelWidth = labelWidths.get(topic.id) || Math.min(240, topic.label.length * 14 + 20);
      const x = Math.max(8, Math.min(width - labelWidth - 8, center.x - labelWidth / 2));
      const expanded = topic.children.some(node => activeIds.has(node.id)) || visible.some(node => node.id.startsWith(topic.id + '/'));
      const y = expanded ? height / 2 + (topic.bounds.top - view.y) * view.scale - 34 : center.y - 15;
      const rect = { x, y, right: x + labelWidth, bottom: y + 30 };
      const outside = center.x < -50 || center.x > width + 50 || center.y < -50 || center.y > height + 50;
      const collision = placed.some(other => rect.x < other.right + 10 && rect.right + 10 > other.x &&
        rect.y < other.bottom + 8 && rect.bottom + 8 > other.y);
      const hidden = outside || collision || topic.kind === 'district' && topic.area * view.scale ** 2 < Math.min(14000, width * height / 24);
      if (button.hidden !== hidden) button.hidden = hidden;
      if (!button.hidden) {
        rect.y = Math.max(10, y); rect.bottom = rect.y + 30;
        const transform = `translate(${x}px, ${rect.y}px)`;
        if (button.style.transform !== transform) button.style.transform = transform;
        placed.push(rect);
      }
    }
    return placed;
  }
  return {
    setData(result, scale) {
      const geometry = buildMapRegions(result.points, result.topics || [], palette);
      points = geometry.points; topics = result.topics || []; roots = geometry.roots;
      worker?.terminate(); worker = null; raster?.bitmap?.close?.(); raster = null; rasterPending = false;
      byName.clear(); colors.clear(); neighbors.clear(); bodies.clear(); markers.clear(); controls.replaceChildren();
      labels.replaceChildren(); topicButtons.clear(); regionsByName.clear(); labelWidths.clear(); hovered = dragged = '';
      excerpts.clear(); visible = shown = []; excerptKey = '';
      for (const point of points) {
        byName.set(point.name, point); neighbors.set(point.name, []);
        colors.set(point.name, point.semantic ? tagColors.get(point.tags[0]) || '#9AA3AF' : '#A9ADB4');
      }
      for (const edge of result.links || []) {
        neighbors.get(edge.source).push({ name: edge.target, weight: edge.weight });
        neighbors.get(edge.target).push({ name: edge.source, weight: edge.weight });
      }
      for (const [name, edges] of neighbors) {
        neighbors.set(name, edges.sort((a, b) => b.weight - a.weight || a.name.localeCompare(b.name)).slice(0, 8));
      }
      topics.forEach((topic, index) => {
        const color = palette[index % palette.length];
        for (const name of topic.members) colors.set(name, color);
      });
      function register(region) {
        if (region.name) regionsByName.set(region.name, region);
        for (const child of region.children) register(child);
        if (!region.label) return;
        const button = document.createElement('button');
        button.type = 'button'; button.className = 'map-topic'; button.dataset.topic = region.id;
        button.style.color = `color-mix(in srgb, ${region.color} 40%, #263343)`;
        button.classList.toggle('map-district', region.kind === 'district');
        if (region.fallback) setText(button, l("主题")); else button.textContent = region.label;
        setAttr(button, 'aria-label', region.fallback ? l("聚焦主题") : l("聚焦主题：{0}", { 0: region.label }));
        button.addEventListener('click', () => focusRegion(region));
        topicButtons.set(region.id, button); labels.append(button);
      }
      for (const root of roots) {
        if (root.kind === 'topic' && !root.label) { root.fallback = true; root.label = String(l("主题")); }
        register(root);
      }
      makeWorker();
      if (worker) worker.postMessage({ type: 'data', roots });
      queue();
    },
    draw(next, name) { view = next; selected = name; queue(); },
    pick(clientX, clientY) {
      if (!view) return '';
      const rect = field.getBoundingClientRect(), x = clientX - rect.left, y = clientY - rect.top;
      const world = { x: view.x + (x - width / 2) / view.scale, y: view.y + (y - height / 2) / view.scale };
      for (const region of visible) if (region.name && insidePolygon(world, region.polygon)) return region.name;
      if (points.length > 24) return '';
      let picked = '', distance = 17 * 17;
      for (const point of shown) {
        const position = screen(point), delta = (position.x - x) ** 2 + (position.y - y) ** 2;
        if (delta < distance) { picked = point.name; distance = delta; }
      }
      return picked;
    },
    activate(clientX, clientY) {
      const rect = field.getBoundingClientRect(), world = { x: view.x + (clientX - rect.left - width / 2) / view.scale,
        y: view.y + (clientY - rect.top - height / 2) / view.scale };
      const target = visible.find(node => insidePolygon(world, node.polygon));
      if (target && !target.name) { focusRegion(target); return true; }
      return false;
    },
    hover(name) {
      if (name === hovered) return;
      hovered = name; if (name) wake(name); queue();
    },
    excerpt(name, text) { excerpts.set(name, text); queue(); },
    drag(name, x, y) {
      dragged = name; wake(name);
      if (!bodies.has(name)) bodies.set(name, { x: 0, y: 0, vx: 0, vy: 0 });
      Object.assign(bodies.get(name), { x, y, vx: 0, vy: 0 }); queue();
    },
    release() { dragged = ''; if (reduced.matches) bodies.clear(); queue(); },
    position(name) { return view && byName.has(name) ? screen(byName.get(name)) : null; },
    destroy() {
      destroyed = true; if (frame !== null) cancelAnimationFrame(frame);
      worker?.terminate(); raster?.bitmap?.close?.();
      canvas.remove(); controls.remove(); labels.remove(); bodies.clear(); markers.clear();
    },
  };
}
