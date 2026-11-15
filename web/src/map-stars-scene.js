import { l, setAttr } from './i18n.js';
import { StarScene } from './map-star-paint.js';
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
  const scene = new StarScene(canvas), ctx = scene.ctx;
  const markers = new Map(), excerpts = new Map(), labels = new Map(), byName = new Map(), neighbors = new Map();
  let points = [], coordinates = [], groupLabels = [], groups = [], subtopics = [], view = null;
  let selected = '', hovered = '', fitScale = 1, mode = 'photo', raf = null, dead = false, excerptKey = '';
  let width = 0, height = 0;
  const screen = point => ({ x: width / 2 + (point.x - view.x) * view.scale,
    y: height / 2 + (point.y - view.y) * view.scale });
  const queue = () => { if (!dead && raf === null) raf = requestAnimationFrame(paint); };
  function marker(point) {
    const node = document.createElement('button');
    node.type = 'button'; node.className = 'map-dot'; node.dataset.name = point.name;
    setAttr(node, 'aria-label', l("笔记 {0}", { 0: point.name.slice(6) }));
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
  function paint() {
    raf = null;
    if (!view || dead) return;
    width = field.clientWidth; height = field.clientHeight;
    const ratio = Math.min(devicePixelRatio || 1, 2);
    if (canvas.width !== Math.round(width * ratio) || canvas.height !== Math.round(height * ratio)) {
      canvas.width = Math.round(width * ratio); canvas.height = Math.round(height * ratio);
    }
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    const selectedIndex = byName.get(selected) ?? -1;
    scene.draw(view, {
      width, height, fitScale, mode, labels: true, colors: false, selected: selectedIndex,
      hovered: byName.get(hovered) ?? -1, filter: null, groups, subtopics, groupLabels, palette,
      getNote: i => ({ title: excerpts.get(points[i].name) || '' }),
      neighbors: (neighbors.get(selected) || []).map(name => byName.get(name)).filter(i => i != null),
      base: coordinates, movement: false, additions: false,
    });
    const visible = points.map((point, i) => ({ point, i, ...screen(point) }))
      .filter(p => p.x >= 12 && p.x <= width - 12 && p.y >= 12 && p.y <= height - 12);
    const accessible = visible.slice(0, 120);
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
      node.setAttribute('aria-pressed', String(point.name === selected));
      node.classList.toggle('on', point.name === selected);
      if (excerpts.get(point.name)) node.setAttribute('aria-label', excerpts.get(point.name));
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
      requested = visible.sort((a, b) => (b.point.stellar?.luminosity || 0) - (a.point.stellar?.luminosity || 0))
        .slice(0, width < 600 ? 3 : 7).map(value => value.point.name);
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
      scene.setData(coordinates, points.map(point => point.stellar), groupLabels);
      queue();
    },
    draw(next, name, scale) { view = next; selected = name; fitScale = scale || fitScale; queue(); },
    mode(next) { mode = next; queue(); },
    pick(clientX, clientY) {
      if (!view) return '';
      const rect = field.getBoundingClientRect(), x = clientX - rect.left, y = clientY - rect.top;
      const excerpt = scene.pickExcerpt(x, y);
      if (excerpt >= 0) return points[excerpt].name;
      let closest = '', distance = 13;
      for (const point of points) {
        const p = screen(point), delta = Math.hypot(x - p.x, y - p.y);
        if (delta < distance) { closest = point.name; distance = delta; }
      }
      return closest;
    },
    hover(name) { if (hovered !== name) { hovered = name; queue(); } },
    excerpt(name, text) { excerpts.set(name, text); queue(); },
    position(name) { const i = byName.get(name); return i != null && view ? screen(points[i]) : null; },
    activate() {}, drag() {}, release() {},
    destroy() {
      dead = true; if (raf !== null) cancelAnimationFrame(raf);
      canvas.remove(); controls.remove(); labelsLayer.remove();
      markers.clear(); labels.clear(); excerpts.clear();
    },
  };
}
