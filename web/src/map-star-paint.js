// Cached Canvas density fields and point sprites. Every light source is a note.
export class StarScene {
  constructor(canvas) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d', { alpha: false });
    this.sprites = new Map();
    this.points = [];
    this.excerpts = [];
    this.timings = [];
  }

  setData(points, attributes, labels) {
    if (this.points === points) return;
    this.points = points;
    this.attributes = attributes;
    this.labels = labels || [];
    this.order = points.map((_, i) => i).sort((a, b) => (attributes?.[b]?.luminosity || 0) - (attributes?.[a]?.luminosity || 0));
    this.prepareCloud();
  }

  prepareCloud() {
    const points = this.points, size = 512;
    if (!points.length) { this.cloud = null; return; }
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const [x, y] of points) { x0 = Math.min(x0, x); y0 = Math.min(y0, y); x1 = Math.max(x1, x); y1 = Math.max(y1, y); }
    const extent = Math.max(.5, x1 - x0, y1 - y0) * 1.22;
    this.bounds = { x: (x0 + x1 - extent) / 2, y: (y0 + y1 - extent) / 2, extent };
    const raw = document.createElement('canvas'); raw.width = raw.height = size;
    const density = raw.getContext('2d');
    const mass = new Float32Array(size * size), channels = [0, 1, 2].map(() => new Float32Array(size * size));
    const tints = [[107, 132, 171], [121, 117, 152], [88, 136, 146], [113, 130, 160]];
    const radius = 15, sigma = 5, kernel = [];
    for (let y = -radius; y <= radius; y++) {
      for (let x = -radius; x <= radius; x++) {
        if (x * x + y * y <= radius * radius) kernel.push([x, y, Math.exp(-(x * x + y * y) / (2 * sigma * sigma))]);
      }
    }
    // Floating-point accumulation keeps dense regions from saturating.
    points.forEach(([x, y], i) => {
      const px = Math.round((x - this.bounds.x) / extent * size), py = Math.round((y - this.bounds.y) / extent * size);
      const tint = this.labels[i] >= 0 ? tints[this.labels[i] % tints.length] : tints[0];
      for (const [dx, dy, weight] of kernel) {
        const xx = px + dx, yy = py + dy;
        if (xx < 0 || yy < 0 || xx >= size || yy >= size) continue;
        const j = yy * size + xx; mass[j] += weight;
        for (let channel = 0; channel < 3; channel++) channels[channel][j] += tint[channel] * weight;
      }
    });
    const pixels = density.createImageData(size, size), rgba = pixels.data;
    for (let i = 0; i < rgba.length; i += 4) {
      const j = i / 4, excess = Math.max(0, mass[j] - 1.7);
      if (excess <= 0) continue;
      const signal = Math.pow(excess / (excess + 3.5), 1.3);
      for (let channel = 0; channel < 3; channel++) rgba[i + channel] = Math.round(channels[channel][j] / mass[j]);
      rgba[i + 3] = Math.round(118 * signal);
    }
    density.putImageData(pixels, 0, 0);
    this.cloud = raw;
    this.mist = document.createElement('canvas'); this.mist.width = this.mist.height = size;
    const c = this.mist.getContext('2d');
    c.filter = 'blur(18px)'; c.drawImage(raw, 0, 0);
  }

  sprite(tint) {
    if (this.sprites.has(tint)) return this.sprites.get(tint);
    const canvas = document.createElement('canvas'); canvas.width = canvas.height = 96;
    const ctx = canvas.getContext('2d'), g = ctx.createRadialGradient(48, 48, 0, 48, 48, 48);
    g.addColorStop(0, '#f8fbff'); g.addColorStop(.025, '#edf5ff');
    g.addColorStop(.065, tint + 'dc'); g.addColorStop(.16, tint + '42');
    g.addColorStop(.37, tint + '0c'); g.addColorStop(1, tint + '00');
    ctx.fillStyle = g; ctx.fillRect(0, 0, 96, 96); this.sprites.set(tint, canvas); return canvas;
  }

  draw(view, options) {
    const started = performance.now();
    const { width: w, height: h, fitScale, mode, labels, colors, selected, hovered, filter,
      points = this.points, groups, subtopics = [], groupLabels, palette, getNote, base, movement } = options;
    const ctx = this.ctx, z = view.scale / fitScale, photo = mode === 'photo';
    const at = point => ({ x: w / 2 + (point[0] - view.x) * view.scale, y: h / 2 + (point[1] - view.y) * view.scale });
    const smooth = (a, b, value) => { const t = Math.max(0, Math.min(1, (value - a) / (b - a))); return t * t * (3 - 2 * t); };
    ctx.globalAlpha = 1; ctx.globalCompositeOperation = 'source-over';
    const night = ctx.createRadialGradient(w * .47, h * .46, 0, w * .5, h * .5, Math.max(w, h) * .72);
    night.addColorStop(0, '#040c18'); night.addColorStop(.58, '#030a14'); night.addColorStop(1, '#02060e');
    ctx.fillStyle = night; ctx.fillRect(0, 0, w, h);
    if (this.cloud && photo) {
      const origin = at([this.bounds.x, this.bounds.y]), extent = this.bounds.extent * view.scale;
      ctx.globalCompositeOperation = 'screen';
      const fade = 1 / (1 + Math.max(0, z - 1) * .4);
      ctx.globalAlpha = .045 * fade;
      if (this.mist) ctx.drawImage(this.mist, origin.x, origin.y, extent, extent);
      ctx.globalAlpha = .54 * fade * (filter != null ? .6 : 1);
      ctx.drawImage(this.cloud, origin.x, origin.y, extent, extent);
      ctx.globalCompositeOperation = 'source-over'; ctx.globalAlpha = 1;
    }
    if (movement) {
      ctx.strokeStyle = '#bca57870'; ctx.lineWidth = .6;
      for (let i = 0; i < Math.min(base.length, points.length); i++) {
        const a = at(base[i]), b = at(points[i]);
        ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
      }
    }
    const projected = points.map(at);
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
      const { x, y } = projected[i];
      if (x < -30 || y < -30 || x > w + 30 || y > h + 30) continue;
      visible++;
      const a = this.attributes?.[i] || { seed: (i * .6180339) % 1, radius: .7, luminosity: .55, role: 'ordinary' };
      const dim = filter == null || groupLabels[i] === filter ? 1 : .13;
      const active = i === selected || i === hovered;
      const special = a.role !== 'ordinary';
      let tint = groupLabels[i] >= 0 ? groupTints[groupLabels[i] % groupTints.length] : '#c3d0e0';
      if (a.seed < .09 && a.luminosity > .6) tint = '#e4d7ba';
      if (colors && groupLabels[i] >= 0) tint = palette[groupLabels[i] % palette.length];
      if (a.role === 'core') tint = a.seed < .4 ? '#f0dfbd' : '#d9e8fc';
      if (a.role === 'isolated') tint = '#e2d0ad';
      const r = (.56 + a.radius * .55 + a.luminosity * .22 + (a.role === 'core' ? .3 : 0)) * zoomGrowth + (active ? .45 : 0);
      const alpha = Math.min(1, (special ? .97 : .58 + a.luminosity * .39) * dim);
      stars.push({ i, x, y, a, r, alpha, tint, dim, special, active });
      if (photo || special || active) {
        const halo = r * (special || active ? 9 : 5.8);
        ctx.globalAlpha = alpha * (active ? .85 : special ? .62 : .22);
        ctx.drawImage(this.sprite(tint), x - halo / 2, y - halo / 2, halo, halo);
      }
    }
    ctx.globalCompositeOperation = 'source-over';
    for (const { i, x, y, a, r, alpha, tint, dim, special, active } of stars) {
      ctx.globalAlpha = alpha; ctx.fillStyle = special ? '#fbf5e8' : a.luminosity > .80 ? '#edf3fc' : tint;
      ctx.beginPath(); ctx.arc(x, y, r * (photo ? .76 : .80), 0, Math.PI * 2); ctx.fill();
      if (special && photo && dim > .5) {
        const length = r * (a.role === 'core' ? 5 : 3), line = ctx.createLinearGradient(x - length, y, x + length, y);
        line.addColorStop(0, tint + '00'); line.addColorStop(.5, tint + '85'); line.addColorStop(1, tint + '00');
        ctx.strokeStyle = line; ctx.lineWidth = .45; ctx.beginPath(); ctx.moveTo(x - length, y); ctx.lineTo(x + length, y); ctx.stroke();
        const vertical = ctx.createLinearGradient(x, y - length, x, y + length);
        vertical.addColorStop(0, tint + '00'); vertical.addColorStop(.5, tint + '65'); vertical.addColorStop(1, tint + '00');
        ctx.strokeStyle = vertical; ctx.beginPath(); ctx.moveTo(x, y - length); ctx.lineTo(x, y + length); ctx.stroke();
      }
      if (active || (options.additions && i >= base.length)) {
        ctx.globalAlpha = .8 * dim; ctx.strokeStyle = active ? '#cfdfef' : '#c9b48c'; ctx.lineWidth = .7;
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
    if (labels) {
      const maximum = w < 600 ? 3 : 6;
      let placed = 0;
      for (const group of [...groups].sort((a, b) => b.count - a.count)) {
        if (filter != null && filter !== group.id) continue;
        if (placed >= maximum && z < 1.6) break;
        const members = group.members || points.map((_, i) => i).filter(i => groupLabels[i] === group.id);
        const c = center(members); if (!c) continue;
        const before = boxes.length;
        place(group.label, c, .80 - .35 * smooth(1.5, 4, z), false, group.id);
        placed += Number(boxes.length > before);
      }
      if (z > 1.35) {
        for (const group of subtopics) {
          if (filter != null && filter !== group.parent) continue;
          const c = center(group.members); if (!c) continue;
          place(group.label, c, .68 * smooth(1.35, 3.2, z), true, group.id);
        }
      }
    }
    this.excerpts = [];
    const excerptOpacity = smooth(2.7, 5, z);
    if (excerptOpacity > .01) {
      const ordered = this.order;
      for (const i of ordered) {
        if (this.excerpts.length >= (w < 600 ? 3 : 7)) break;
        if (filter != null && groupLabels[i] !== filter) continue;
        const p = projected[i], originalTitle = getNote(i).title;
        if (!originalTitle) continue;
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
    this.canvas.dataset.stars = String(points.length);
    this.canvas.dataset.visible = String(visible);
    this.canvas.dataset.mode = mode;
    this.canvas.dataset.drawMs = (performance.now() - started).toFixed(2);
    return visible;
  }

  pickExcerpt(x, y) {
    return this.excerpts.find(box => x >= box.x && x <= box.x + box.w && y >= box.y && y <= box.y + box.h)?.index ?? -1;
  }
}
