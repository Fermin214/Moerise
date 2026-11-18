import { StarScene } from './map-star-paint.js';

export function createStarRenderer(canvas, onReady = () => {}) {
  const ctx = canvas.getContext('2d', { alpha: false });
  let worker = null, fallback = null, dead = false, id = 0, active = null, pending = null, timer = null;
  let points = [], attributes = [], labels = [], generation = 0;
  const renderer = {
    ctx, order: [], visible: [], projected: [], excerpts: [], labelBoxes: [],
    setData(next, stars, groups, topics, subtopics) {
      generation++;
      active?.resolve(false); pending?.resolve(false); pending = null;
      points = next; attributes = stars; labels = groups;
      renderer.order = points.map((_, i) => i).sort((a, b) => (attributes?.[b]?.luminosity || 0) - (attributes?.[a]?.luminosity || 0));
      if (worker) {
        const coordinates = new Float64Array(points.length * 2), stars = new Float64Array(points.length * 4);
        const groups = new Float64Array(points.length);
        for (let i = 0; i < points.length; i++) {
          coordinates[i * 2] = points[i][0]; coordinates[i * 2 + 1] = points[i][1];
          const a = attributes[i] || { seed: (i * .6180339) % 1, radius: .7, luminosity: .55, role: 'ordinary' };
          stars[i * 4] = a.seed; stars[i * 4 + 1] = a.radius; stars[i * 4 + 2] = a.luminosity;
          stars[i * 4 + 3] = a.role === 'core' ? 1 : a.role === 'isolated' ? 2 : 0;
          groups[i] = labels[i] ?? -1;
        }
        try { worker.postMessage({ type: 'data', coordinates, stars, groups, topics, subtopics },
          [coordinates.buffer, stars.buffer, groups.buffer]); } catch { fail(); }
      } else fallback?.setData(points, attributes, labels);
    },
    setTitle(index, title) {
      if (worker) {
        try { worker.postMessage({ type: 'excerpt', index, title }); } catch { fail(); }
      }
    },
    draw(view, options) {
      if (dead) return Promise.resolve(false);
      return new Promise(resolve => {
        active?.resolve(false); pending?.resolve(false);
        pending = { id: ++id, generation, view, options, resolve };
        send();
      });
    },
    pickExcerpt(x, y) {
      return renderer.excerpts.find(box => x >= box.x && x <= box.x + box.w && y >= box.y && y <= box.y + box.h)?.index ?? -1;
    },
    projectionAt(i) {
      const values = renderer.projected;
      return values instanceof Float64Array ? { x: values[i * 2], y: values[i * 2 + 1] } : values[i];
    },
    destroy() {
      dead = true; clearTimeout(timer); worker?.terminate(); fallback?.destroy();
      active?.resolve(false); pending?.resolve(false); active = pending = null;
    },
  };
  function composite(source, frame, stats, mode) {
    const started = performance.now();
    ctx.save(); ctx.setTransform(1, 0, 0, 1, 0, 0); ctx.drawImage(source, 0, 0); ctx.restore();
    for (const key of ['visible', 'projected', 'excerpts', 'labelBoxes']) renderer[key] = frame[key];
    Object.assign(canvas.dataset, stats, { renderer: mode, compositeMs: (performance.now() - started).toFixed(2) });
  }
  async function drawFallback(task) {
    const staging = fallback.canvas;
    if (staging.width !== canvas.width || staging.height !== canvas.height) { staging.width = canvas.width; staging.height = canvas.height; }
    fallback.ctx.setTransform(staging.width / task.options.width, 0, 0, staging.height / task.options.height, 0, 0);
    const rendered = await fallback.drawAsync(task.view, task.options, () => !dead && task.generation === generation);
    if (rendered && !dead) composite(staging, fallback, fallback.stats, 'fallback');
    task.resolve(rendered);
    if (active === task) { active = null; send(); }
  }
  function send() {
    if (active || !pending || (!worker && !fallback)) return;
    active = pending; pending = null;
    if (fallback) { drawFallback(active); return; }
    const { width, height, fitScale, mode, selected, hovered, neighbors } = active.options;
    timer = setTimeout(fail, 15000);
    try { worker.postMessage({ type: 'draw', id: active.id, view: active.view,
      options: { width, height, fitScale, mode, selected, hovered, neighbors },
      width: canvas.width, height: canvas.height }); } catch { fail(); }
  }
  function fail() {
    if (dead || fallback) return;
    clearTimeout(timer); worker?.terminate(); worker = null;
    active?.resolve(false); pending?.resolve(false); active = pending = null;
    fallback = new StarScene(document.createElement('canvas'), () => onReady(fallback.stats));
    fallback.setData(points, attributes, labels);
    canvas.dataset.renderer = 'fallback'; queueMicrotask(onReady);
  }
  if (typeof Worker === 'function' && typeof OffscreenCanvas === 'function' &&
      typeof OffscreenCanvas.prototype.transferToImageBitmap === 'function') {
    try {
      worker = new Worker(new URL('./map-render-worker.js', import.meta.url), { type: 'module' });
      worker.addEventListener('error', event => { event.preventDefault(); fail(); });
      worker.addEventListener('messageerror', fail);
      worker.addEventListener('message', ({ data }) => {
        if (dead) { data.bitmap?.close(); return; }
        if (data.type === 'ready') { Object.assign(canvas.dataset, data.stats); onReady(data.stats); return; }
        if (data.type !== 'frame' || data.id !== active?.id) { data.bitmap?.close(); return; }
        clearTimeout(timer);
        const completed = active; active = null;
        if (completed.generation === generation) {
          composite(data.bitmap, data, data.stats, 'worker');
          completed.resolve(true);
        } else completed.resolve(false);
        data.bitmap.close(); send();
      });
    } catch { fail(); }
  } else fail();
  return renderer;
}
