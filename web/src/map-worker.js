import { paintRegions } from './map-paint.js';

let roots = [], canvas;

self.onmessage = ({ data }) => {
  if (data.type === 'data') {
    roots = data.roots;
    return;
  }
  const { view, width, height, ratio } = data;
  canvas ||= new OffscreenCanvas(1, 1);
  canvas.width = Math.round(width * ratio); canvas.height = Math.round(height * ratio);
  const context = canvas.getContext('2d', { alpha: false });
  paintRegions(context, data, roots);
  const bitmap = canvas.transferToImageBitmap();
  self.postMessage({ view, width, height, ratio, bitmap }, [bitmap]);
};
