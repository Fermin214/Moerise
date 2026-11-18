import { createDensityProcessor } from './map-star-cloud.js';

const createCanvas = () => typeof document === 'undefined' ? new OffscreenCanvas(512, 512) : document.createElement('canvas');

// Cached Canvas density fields and point sprites. Every light source is a note.
export class StarScene {
  constructor(canvas, onReady = () => {}) {
    this.canvas = canvas;
    this.stats = canvas.dataset || {};
    this.ctx = canvas.getContext('2d', { alpha: false });
    this.sprites = new Map();
    this.points = [];
    this.excerpts = [];
    this.timings = [];
    this.density = createDensityProcessor();
    this.onReady = onReady;
    this.generation = 0;
  }

  setData(points, attributes, labels, topics, subtopics) {
    this.points = points;
    this.attributes = attributes;
    this.labels = labels || [];
    this.topics = topics;
    this.subtopics = subtopics;
    this.titles = new Map();
    this.titleOrder = [];
    return this.ready = this.prepareCloud();
  }

  setTitle(index, title) {
    if (title) this.titles.set(index, title);
    else this.titles.delete(index);
    this.titleOrder = [...this.titles.keys()].sort((left, right) =>
      (this.attributes[right]?.luminosity || 0) - (this.attributes[left]?.luminosity || 0) || left - right);
  }

  async prepareCloud() {
    const generation = ++this.generation;
    this.cloud = this.mist = null;
    this.stats.cloud = 'loading';
    const data = await this.density.prepare(this.points, this.labels);
    if (!data || generation !== this.generation) return;
    const { size, bounds, pixels } = data;
    this.bounds = bounds;
    const raw = createCanvas(); raw.width = raw.height = size;
    const density = raw.getContext('2d');
    density.putImageData(new ImageData(pixels, size, size), 0, 0);
    this.cloud = raw;
    this.mist = createCanvas(); this.mist.width = this.mist.height = size;
    const c = this.mist.getContext('2d');
    c.filter = 'blur(18px)'; c.drawImage(raw, 0, 0);
    this.stats.cloud = data.mode;
    this.stats.cloudMs = data.elapsed.toFixed(2);
    this.onReady();
  }

  sprite(tint) {
    if (this.sprites.has(tint)) return this.sprites.get(tint);
    const canvas = createCanvas(); canvas.width = canvas.height = 96;
    const ctx = canvas.getContext('2d'), g = ctx.createRadialGradient(48, 48, 0, 48, 48, 48);
    g.addColorStop(0, '#f8fbff'); g.addColorStop(.025, '#edf5ff');
    g.addColorStop(.065, tint + 'dc'); g.addColorStop(.16, tint + '42');
    g.addColorStop(.37, tint + '0c'); g.addColorStop(1, tint + '00');
    ctx.fillStyle = g; ctx.fillRect(0, 0, 96, 96); this.sprites.set(tint, canvas); return canvas;
  }

  draw(view, options) {
    const steps = this.drawSteps(view, options);
    let step;
    do { step = steps.next(); } while (!step.done);
    return step.value;
  }

  async drawAsync(view, options, canContinue) {
    const steps = this.drawSteps(view, options, true);
    try {
      let step;
      do {
        if (!canContinue()) return false;
        step = steps.next();
        if (!step.done) {
          // Flush deferred Canvas commands per batch before the final composite.
          this.ctx.getImageData(0, 0, 1, 1);
          await (globalThis.scheduler?.yield ? scheduler.yield() : new Promise(resolve => setTimeout(resolve, 0)));
        }
      } while (!step.done);
      return true;
    } finally { steps.return(); }
  }

  *drawSteps(view, options, segmented = false) {
    const started = performance.now();
    const { width: w, height: h, fitScale, mode, selected, hovered } = options;
    const { points, topics, subtopics, labels: groupLabels } = this;
    const ctx = this.ctx, z = view.scale / fitScale, photo = mode === 'photo';
    const at = point => ({ x: w / 2 + (point[0] - view.x) * view.scale, y: h / 2 + (point[1] - view.y) * view.scale });
    const smooth = (a, b, value) => { const t = Math.max(0, Math.min(1, (value - a) / (b - a))); return t * t * (3 - 2 * t); };
    ctx.globalAlpha = 1; ctx.globalCompositeOperation = 'source-over';
    const night = ctx.createRadialGradient(w * .47, h * .46, 0, w * .5, h * .5, Math.max(w, h) * .72);
    night.addColorStop(0, '#040c18'); night.addColorStop(.58, '#030a14'); night.addColorStop(1, '#02060e');
    ctx.fillStyle = night;
    if (segmented) {
      for (let y = 0; y < h; y += 64) { ctx.fillRect(0, y, w, Math.min(64, h - y)); yield; }
    } else ctx.fillRect(0, 0, w, h);
    function* image(source, x, y, extent) {
      if (!segmented) { ctx.drawImage(source, x, y, extent, extent); return; }
      for (let top = 0; top < h; top += 64) {
        ctx.save(); ctx.beginPath(); ctx.rect(0, top, w, Math.min(64, h - top)); ctx.clip();
        ctx.drawImage(source, x, y, extent, extent); ctx.restore(); yield;
      }
    }
    if (this.cloud && photo) {
      const origin = at([this.bounds.x, this.bounds.y]), extent = this.bounds.extent * view.scale;
      ctx.globalCompositeOperation = 'screen';
      const fade = 1 / (1 + Math.max(0, z - 1) * .4);
      ctx.globalAlpha = .045 * fade;
      if (this.mist) yield* image(this.mist, origin.x, origin.y, extent);
      ctx.globalAlpha = .54 * fade;
      yield* image(this.cloud, origin.x, origin.y, extent);
      ctx.globalCompositeOperation = 'source-over'; ctx.globalAlpha = 1;
    }
    const projected = points.map(at);
    this.projected = projected;
    this.visible = [];
    if (selected >= 0 && selected < points.length) {
      const a = projected[selected]; ctx.strokeStyle = '#99bcd947'; ctx.lineWidth = .65;
      for (const i of options.neighbors) {
        if (!projected[i]) continue;
        const b = projected[i]; ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
      }
    }
    const zoomGrowth = 1 + .30 * Math.log2(Math.max(1, z));
    const stars = [], groupTints = ['#c1d2e9', '#d0c8df', '#bddbd8', '#d3d0df'];
    let visible = 0;
    ctx.globalCompositeOperation = 'screen';
    for (let i = 0; i < points.length; i++) {
      if (i && i % 128 === 0) yield;
      const { x, y } = projected[i];
      if (x < -30 || y < -30 || x > w + 30 || y > h + 30) continue;
      visible++;
      this.visible.push(i);
      const a = this.attributes?.[i] || { seed: (i * .6180339) % 1, radius: .7, luminosity: .55, role: 'ordinary' };
      const active = i === selected || i === hovered;
      const special = a.role !== 'ordinary';
      let tint = groupLabels[i] >= 0 ? groupTints[groupLabels[i] % groupTints.length] : '#c3d0e0';
      if (a.seed < .09 && a.luminosity > .6) tint = '#e4d7ba';
      if (a.role === 'core') tint = a.seed < .4 ? '#f0dfbd' : '#d9e8fc';
      if (a.role === 'isolated') tint = '#e2d0ad';
      const r = (.56 + a.radius * .55 + a.luminosity * .22 + (a.role === 'core' ? .3 : 0)) * zoomGrowth + (active ? .45 : 0);
      const alpha = Math.min(1, special ? .97 : .58 + a.luminosity * .39);
      stars.push({ x, y, a, r, alpha, tint, special, active });
      if (photo || special || active) {
        const halo = r * (special || active ? 9 : 5.8);
        ctx.globalAlpha = alpha * (active ? .85 : special ? .62 : .22);
        ctx.drawImage(this.sprite(tint), x - halo / 2, y - halo / 2, halo, halo);
      }
    }
    ctx.globalCompositeOperation = 'source-over';
    let painted = 0;
    for (const { x, y, a, r, alpha, tint, special, active } of stars) {
      if (painted++ && painted % 128 === 0) yield;
      ctx.globalAlpha = alpha; ctx.fillStyle = special ? '#fbf5e8' : a.luminosity > .80 ? '#edf3fc' : tint;
      ctx.beginPath(); ctx.arc(x, y, r * (photo ? .76 : .80), 0, Math.PI * 2); ctx.fill();
      if (special && photo) {
        const length = r * (a.role === 'core' ? 5 : 3), line = ctx.createLinearGradient(x - length, y, x + length, y);
        line.addColorStop(0, tint + '00'); line.addColorStop(.5, tint + '85'); line.addColorStop(1, tint + '00');
        ctx.strokeStyle = line; ctx.lineWidth = .45; ctx.beginPath(); ctx.moveTo(x - length, y); ctx.lineTo(x + length, y); ctx.stroke();
        const vertical = ctx.createLinearGradient(x, y - length, x, y + length);
        vertical.addColorStop(0, tint + '00'); vertical.addColorStop(.5, tint + '65'); vertical.addColorStop(1, tint + '00');
        ctx.strokeStyle = vertical; ctx.beginPath(); ctx.moveTo(x, y - length); ctx.lineTo(x, y + length); ctx.stroke();
      }
      if (active) {
        ctx.globalAlpha = .8; ctx.strokeStyle = '#cfdfef'; ctx.lineWidth = .7;
        ctx.beginPath(); ctx.arc(x, y, r + 5, 0, Math.PI * 2); ctx.stroke();
      }
    }
    ctx.globalAlpha = 1;
    const boxes = [];
    this.labelBoxes = [];
    const overlap = box => boxes.some(other => box.x < other.x + other.w && box.x + box.w > other.x && box.y < other.y + other.h && box.y + box.h > other.y);
    const place = (title, center, opacity, small = false, id = '') => {
      const position = at(center), text = title.length > 19 ? title.slice(0, 18) + '…' : title;
      const font = `${small ? 12 : 14}px "Noto Serif CJK SC", "Songti SC", Georgia, serif`;
      ctx.font = font;
      const tw = ctx.measureText(text).width, box = { x: position.x - tw / 2 - 10, y: position.y - 28, w: tw + 20, h: 26 };
      if (box.x < 25 || box.x + box.w > w - 25 || box.y < 100 || box.y > h - 85 || overlap(box)) return;
      boxes.push(box);
      if (opacity > .2) this.labelBoxes.push({ ...box, id });
      ctx.globalAlpha = opacity; ctx.textAlign = 'center'; ctx.shadowColor = '#020914'; ctx.shadowBlur = 5;
      ctx.strokeStyle = '#030a14dd'; ctx.lineWidth = 1.8;
      ctx.strokeText(text, position.x, position.y - 12);
      ctx.fillStyle = small ? '#a5bad1' : '#c1cfdf'; ctx.fillText(text, position.x, position.y - 12);
      ctx.shadowBlur = 0; ctx.textAlign = 'left'; ctx.globalAlpha = 1;
    };
    const center = members => {
      const visibleMembers = members.filter(i => i < points.length);
      if (!visibleMembers.length) return null;
      return visibleMembers.reduce((sum, i) => [sum[0] + points[i][0] / visibleMembers.length, sum[1] + points[i][1] / visibleMembers.length], [0, 0]);
    };
    if (topics.length) {
      const maximum = w < 600 ? 3 : 6;
      let placed = 0;
      for (const group of topics) {
        if (placed >= maximum && z < 1.6) break;
        const c = center(group.members); if (!c) continue;
        const before = boxes.length;
        place(group.label, c, .80 - .35 * smooth(1.5, 4, z), false, group.id);
        placed += Number(boxes.length > before);
      }
      if (z > 1.35) {
        for (const group of subtopics) {
          const c = center(group.members); if (!c) continue;
          place(group.label, c, .68 * smooth(1.35, 3.2, z), true, group.id);
        }
      }
    }
    this.excerpts = [];
    const excerptOpacity = smooth(2.7, 5, z);
    if (excerptOpacity > .01) {
      for (const i of this.titleOrder) {
        if (this.excerpts.length >= (w < 600 ? 3 : 7)) break;
        const p = projected[i], originalTitle = this.titles.get(i);
        const title = originalTitle.length > 23 ? originalTitle.slice(0, 23) + '…' : originalTitle;
        ctx.font = '12px "PingFang SC", "Microsoft YaHei", sans-serif';
        const tw = ctx.measureText(title).width;
        const box = { x: p.x + 13, y: p.y - 19, w: tw + 12, h: 24 };
        if (box.x < 25 || box.x + box.w > w - 25 || box.y < 110 || box.y > h - 85 || overlap(box)) continue;
        boxes.push(box); this.excerpts.push({ ...box, index: i });
        ctx.globalAlpha = excerptOpacity * .92; ctx.fillStyle = '#c2d0e0'; ctx.shadowColor = '#020914'; ctx.shadowBlur = 3;
        ctx.strokeStyle = '#030a14dd'; ctx.lineWidth = 1.8; ctx.strokeText(title, box.x + 3, box.y + 15);
        ctx.fillText(title, box.x + 3, box.y + 15); ctx.shadowBlur = 0;
      }
    }
    ctx.globalAlpha = 1; ctx.globalCompositeOperation = 'source-over';
    this.timings.push(performance.now() - started);
    if (this.timings.length > 180) this.timings.shift();
    this.stats.stars = String(points.length);
    this.stats.visible = String(visible);
    this.stats.mode = mode;
    this.stats.drawMs = (performance.now() - started).toFixed(2);
    return visible;
  }

  pickExcerpt(x, y) {
    return this.excerpts.find(box => x >= box.x && x <= box.x + box.w && y >= box.y && y <= box.y + box.h)?.index ?? -1;
  }

  destroy() {
    this.generation++; this.density.destroy(); this.sprites.clear();
    this.cloud = this.mist = null; this.points = []; this.onReady = () => {};
  }
}
