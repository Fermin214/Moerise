import { l, setAttr } from './i18n.js';
import { createStarRenderer } from './map-star-renderer.js';
import { createMapScene as legacyScene } from './map-scene.js';

const palette = ['#b4d1e9', '#d4c7ad', '#b7bad9', '#a8c8c8', '#cbb7c4', '#bcc8b3', '#9cb9d6', '#c0b5d1', '#cdbfb6', '#a8c5c9', '#c4cbdc', '#c8c4a9'];

export function createMapScene(field, options) {
  if (options.algorithm !== 'umap-balanced-v1') return legacyScene(field, options);
  return starScene(field, options);
}

function starScene(field, { onPick, onHover, onTopic, onExcerpts }) {
  const canvas = document.createElement('canvas');
  canvas.className = 'map-canvas'; canvas.setAttribute('aria-hidden', 'true');
  const controls = document.createElement('div'); controls.className = 'map-points';
  const labelsLayer = document.createElement('div'); labelsLayer.className = 'map-topics';
  field.append(canvas, controls, labelsLayer);
  const scene = createStarRenderer(canvas, () => queue()), ctx = scene.ctx;
  const markers = new Map(), excerpts = new Map(), labels = new Map(), byName = new Map(), neighbors = new Map();
  const cells = new Map();
  let cellSize = 1;
  let points = [], coordinates = [], groupLabels = [], groups = [], subtopics = [], view = null;
  let selected = '', hovered = '', fitScale = 1, mode = 'photo', raf = null, dead = false, excerptKey = '';
  let width = 0, height = 0, paintSequence = 0;
  const screen = point => ({ x: width / 2 + (point.x - view.x) * view.scale,
    y: height / 2 + (point.y - view.y) * view.scale });
  const queue = () => { if (!dead && raf === null) raf = requestAnimationFrame(paint); };
  function marker(point) {
    const node = document.createElement('button');
    node.type = 'button'; node.className = 'map-dot'; node.dataset.name = point.name;
    setAttr(node, 'aria-label', excerpts.get(point.name) || l("笔记 {0}", { 0: point.name.slice(6) }));
    node.addEventListener('click', event => { if (event.detail === 0) onPick(point.name); });
    node.addEventListener('focus', () => {
      onHover(point.name);
      if (view) {
        const position = screen(point);
        if (position.x < 20 || position.x > width - 20 || position.y < 20 || position.y > height - 20) {
          onTopic({ x: point.x, y: point.y, scale: view.scale });
        }
      }
      field.scrollLeft = field.scrollTop = 0;
    });
    markers.set(point.name, node); controls.append(node);
    return node;
  }
  async function paint() {
    raf = null;
    if (!view || dead) return;
    const sequence = ++paintSequence;
    width = field.clientWidth; height = field.clientHeight;
    const ratio = Math.min(devicePixelRatio || 1, 2);
    if (canvas.width !== Math.round(width * ratio) || canvas.height !== Math.round(height * ratio)) {
      canvas.width = Math.round(width * ratio); canvas.height = Math.round(height * ratio);
    }
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    const selectedIndex = byName.get(selected) ?? -1;
    const rendered = await scene.draw(view, {
      width, height, fitScale, mode, labels: true, colors: false, selected: selectedIndex,
      hovered: byName.get(hovered) ?? -1, filter: null, groups, subtopics, groupLabels, palette,
      getNote: i => ({ title: excerpts.get(points[i].name) || '' }),
      neighbors: (neighbors.get(selected) || []).map(name => byName.get(name)).filter(i => i != null),
      base: coordinates, movement: false, additions: false,
    });
    if (!rendered || dead || sequence !== paintSequence) return;
    const visible = scene.visible.filter(i => {
      const p = scene.projectionAt(i);
      return p.x >= 12 && p.x <= width - 12 && p.y >= 12 && p.y <= height - 12;
    });
    const accessible = Array.from(visible.slice(0, 120), i => ({ point: points[i], i, ...scene.projectionAt(i) }));
    const focused = document.activeElement?.dataset.name;
    for (const name of [selected, focused]) {
      const i = byName.get(name);
      if (i != null && !accessible.some(p => p.i === i)) accessible.push({ point: points[i], i, ...screen(points[i]) });
    }
    const keep = new Set(accessible.map(value => value.point.name));
    for (const [name, node] of markers) if (!keep.has(name)) { node.remove(); markers.delete(name); }
    for (const { point, x, y } of accessible) {
      const node = markers.get(point.name) || marker(point);
      node.style.transform = `translate(${x - 16}px, ${y - 16}px)`;
      const pressed = String(point.name === selected);
      if (node.getAttribute('aria-pressed') !== pressed) node.setAttribute('aria-pressed', pressed);
      node.classList.toggle('on', point.name === selected);
    }
    const activeLabels = new Set();
    for (const box of scene.labelBoxes || []) {
      const record = labels.get(box.id);
      if (!record) continue;
      record.node.hidden = false; activeLabels.add(box.id);
      record.node.style.transform = `translate(${box.x}px, ${box.y}px)`;
      record.node.style.width = `${box.w}px`; record.node.style.height = `${box.h}px`;
    }
    for (const [id, { node }] of labels) if (!activeLabels.has(id)) node.hidden = true;
    let requested = [];
    if (view.scale / fitScale > 2.7) {
      const visibleSet = new Set(visible);
      for (const i of scene.order) {
        if (visibleSet.has(i)) requested.push(points[i].name);
        if (requested.length >= (width < 600 ? 3 : 7)) break;
      }
    }
    const key = requested.join('|');
    if (key !== excerptKey) { excerptKey = key; onExcerpts?.(requested); }
    field.dataset.rendered = 'true'; field.dataset.detail = view.scale / fitScale > 2.7 ? 'notes' : 'topics';
    field.dataset.stars = String(points.length); field.dataset.drawMs = canvas.dataset.drawMs;
  }
  return {
    setData(result, scale) {
      fitScale = scale;
      points = result.points.filter(point => point.semantic);
      coordinates = points.map(point => [point.x, point.y]);
      cells.clear();
      let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
      for (const point of points) {
        x0 = Math.min(x0, point.x); y0 = Math.min(y0, point.y);
        x1 = Math.max(x1, point.x); y1 = Math.max(y1, point.y);
      }
      cellSize = Math.max(.0001, Math.max(x1 - x0, y1 - y0) / Math.max(1, Math.sqrt(points.length)));
      for (const point of points) {
        const key = `${Math.floor(point.x / cellSize)},${Math.floor(point.y / cellSize)}`;
        if (!cells.has(key)) cells.set(key, []);
        cells.get(key).push(point);
      }
      groupLabels = points.map(() => -1); groups = []; subtopics = [];
      byName.clear(); neighbors.clear(); excerpts.clear(); labels.clear();
      markers.clear(); controls.replaceChildren(); labelsLayer.replaceChildren(); excerptKey = '';
      points.forEach((point, i) => { byName.set(point.name, i); neighbors.set(point.name, []); });
      const register = (id, members, title) => {
        const node = document.createElement('button');
        node.type = 'button'; node.className = 'map-topic map-star-topic'; node.hidden = true;
        node.dataset.topic = id;
        setAttr(node, 'aria-label', l("聚焦主题：{0}", { 0: title }));
        node.addEventListener('click', () => onTopic({ topic: true, members: members.map(i => points[i]) }));
        labelsLayer.append(node); labels.set(id, { node });
      };
      for (const topic of result.topics || []) {
        const code = Number.parseInt(topic.id.slice(-8), 16);
        const members = topic.members.map(name => byName.get(name)).filter(i => i != null);
        for (const i of members) groupLabels[i] = code;
        groups.push({ id: code, label: topic.label, count: members.length, members });
        register(code, members, topic.label);
        for (const district of topic.districts || []) {
          const members = district.members.map(name => byName.get(name)).filter(i => i != null);
          subtopics.push({ id: district.id, parent: code, label: district.label, members });
          register(district.id, members, district.label);
        }
      }
      for (const link of result.links || []) {
        neighbors.get(link.source)?.push(link.target); neighbors.get(link.target)?.push(link.source);
      }
      scene.setData(coordinates, points.map(point => point.stellar), groupLabels, groups, subtopics);
      queue();
    },
    draw(next, name, scale) { view = next; selected = name; fitScale = scale || fitScale; queue(); },
    mode(next) { mode = next; queue(); },
    pick(clientX, clientY) {
      if (!view) return '';
      const rect = field.getBoundingClientRect(), x = clientX - rect.left, y = clientY - rect.top;
      const excerpt = scene.pickExcerpt(x, y);
      if (excerpt >= 0) return points[excerpt].name;
      const wx = view.x + (x - width / 2) / view.scale, wy = view.y + (y - height / 2) / view.scale;
      const radius = 13 / view.scale;
      const left = Math.floor((wx - radius) / cellSize), right = Math.floor((wx + radius) / cellSize);
      const top = Math.floor((wy - radius) / cellSize), bottom = Math.floor((wy + radius) / cellSize);
      let candidates = [];
      if ((right - left + 1) * (bottom - top + 1) > 400) candidates = points;
      else for (let xx = left; xx <= right; xx++) for (let yy = top; yy <= bottom; yy++) candidates.push(...(cells.get(`${xx},${yy}`) || []));
      let closest = '', distance = radius * radius;
      for (const point of candidates) {
        const delta = (wx - point.x) ** 2 + (wy - point.y) ** 2;
        if (delta < distance) { closest = point.name; distance = delta; }
      }
      return closest;
    },
    hover(name) { if (hovered !== name) { hovered = name; queue(); } },
    excerpt(name, text) {
      const index = byName.get(name);
      if (index == null || (excerpts.get(name) || '') === text) return;
      if (text) excerpts.set(name, text);
      else excerpts.delete(name);
      scene.setTitle(index, text);
      const node = markers.get(name);
      if (node) setAttr(node, 'aria-label', text || l("笔记 {0}", { 0: name.slice(6) }));
      queue();
    },
    position(name) { const i = byName.get(name); return i != null && view ? screen(points[i]) : null; },
    activate() {}, drag() {}, release() {},
    destroy() {
      dead = true; if (raf !== null) cancelAnimationFrame(raf);
      scene.destroy(); cells.clear();
      canvas.remove(); controls.remove(); labelsLayer.remove();
      markers.clear(); labels.clear(); excerpts.clear();
    },
  };
}
