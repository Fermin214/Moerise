import { StarScene } from './map-star-paint.js';

const canvas = new OffscreenCanvas(1, 1);
const scene = new StarScene(canvas, () => postMessage({ type: 'ready', stats: scene.stats }));
const titles = new Map();
let snapshot;

onmessage = ({ data }) => {
  if (data.type === 'data') {
    const points = [], attributes = [];
    for (let i = 0; i < data.groups.length; i++) {
      points.push([data.coordinates[i * 2], data.coordinates[i * 2 + 1]]);
      attributes.push({ seed: data.stars[i * 4], radius: data.stars[i * 4 + 1], luminosity: data.stars[i * 4 + 2],
        role: ['ordinary', 'core', 'isolated'][data.stars[i * 4 + 3]] });
    }
    titles.clear();
    snapshot = { labels: true, groups: data.topics, subtopics: data.subtopics, groupLabels: data.groups,
      excerptOrder: [], getNote: index => ({ title: titles.get(index) }) };
    scene.setData(points, attributes, data.groups);
    return;
  }
  if (data.type === 'excerpt') {
    if (data.title) titles.set(data.index, data.title);
    else titles.delete(data.index);
    snapshot.excerptOrder = [...titles.keys()].sort((left, right) =>
      (scene.attributes[right]?.luminosity || 0) - (scene.attributes[left]?.luminosity || 0) || left - right);
    return;
  }
  if (data.type !== 'draw') return;
  const { id, view, options, width, height } = data;
  if (canvas.width !== width || canvas.height !== height) { canvas.width = width; canvas.height = height; }
  scene.ctx.setTransform(width / options.width, 0, 0, height / options.height, 0, 0);
  const started = performance.now();
  scene.draw(view, { ...snapshot, ...options });
  const bitmap = canvas.transferToImageBitmap();
  const projected = new Float64Array(scene.projected.length * 2);
  for (let i = 0; i < scene.projected.length; i++) {
    projected[i * 2] = scene.projected[i].x; projected[i * 2 + 1] = scene.projected[i].y;
  }
  const visible = new Uint32Array(scene.visible);
  postMessage({ type: 'frame', id, bitmap, visible, projected,
    excerpts: scene.excerpts, labelBoxes: scene.labelBoxes, stats: scene.stats,
    renderMs: performance.now() - started }, [bitmap, projected.buffer, visible.buffer]);
};
