// Shared by the worker and the yielding fallback. No DOM or Canvas dependency.
export function* densitySteps(points, labels) {
  if (!points.length) return null;
  const size = 512;
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (let i = 0; i < points.length; i++) {
    const [x, y] = points[i];
    x0 = Math.min(x0, x); y0 = Math.min(y0, y); x1 = Math.max(x1, x); y1 = Math.max(y1, y);
    if (i % 2048 === 2047) yield;
  }
  const extent = Math.max(.5, x1 - x0, y1 - y0) * 1.22;
  const bounds = { x: (x0 + x1 - extent) / 2, y: (y0 + y1 - extent) / 2, extent };
  const mass = new Float32Array(size * size), channels = [0, 1, 2].map(() => new Float32Array(size * size));
  const tints = [[107, 132, 171], [121, 117, 152], [88, 136, 146], [113, 130, 160]];
  const radius = 15, sigma = 5, kernel = [];
  for (let y = -radius; y <= radius; y++) {
    for (let x = -radius; x <= radius; x++) {
      if (x * x + y * y <= radius * radius) kernel.push([x, y, Math.exp(-(x * x + y * y) / (2 * sigma * sigma))]);
    }
  }
  for (let i = 0; i < points.length; i++) {
    const [x, y] = points[i];
    const px = Math.round((x - bounds.x) / extent * size), py = Math.round((y - bounds.y) / extent * size);
    const tint = labels[i] >= 0 ? tints[labels[i] % tints.length] : tints[0];
    for (const [dx, dy, weight] of kernel) {
      const xx = px + dx, yy = py + dy;
      if (xx < 0 || yy < 0 || xx >= size || yy >= size) continue;
      const j = yy * size + xx; mass[j] += weight;
      for (let channel = 0; channel < 3; channel++) channels[channel][j] += tint[channel] * weight;
    }
    if (i % 64 === 63) yield;
  }
  const pixels = new Uint8ClampedArray(size * size * 4);
  for (let j = 0; j < mass.length; j++) {
    const excess = Math.max(0, mass[j] - 1.7);
    if (excess > 0) {
      const signal = Math.pow(excess / (excess + 3.5), 1.3);
      for (let channel = 0; channel < 3; channel++) pixels[j * 4 + channel] = Math.round(channels[channel][j] / mass[j]);
      pixels[j * 4 + 3] = Math.round(118 * signal);
    }
    if (j % 8192 === 8191) yield;
  }
  return { size, bounds, pixels };
}
