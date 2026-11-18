const dimensions = new Map();
let generation = 0;

export function prepareImage(image, key) {
  image.loading = 'lazy';
  image.decoding = 'async';
  const cached = dimensions.get(key);
  if (cached) { image.width = cached.width; image.height = cached.height; }
  const expected = generation;
  image.addEventListener('load', () => {
    if (expected !== generation || !image.naturalWidth || !image.naturalHeight) return;
    dimensions.delete(key);
    dimensions.set(key, { width: image.naturalWidth, height: image.naturalHeight });
    if (dimensions.size > 256) dimensions.delete(dimensions.keys().next().value);
  }, { once: true });
}

export function clearMedia() { generation++; dimensions.clear(); }
