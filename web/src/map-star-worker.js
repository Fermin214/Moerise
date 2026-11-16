import { densitySteps } from './map-star-density.js';

self.onmessage = ({ data: { id, points, labels } }) => {
  const started = performance.now(), steps = densitySteps(points, labels);
  let step;
  do { step = steps.next(); } while (!step.done);
  const result = step.value;
  self.postMessage({ id, ...result, elapsed: performance.now() - started }, result ? [result.pixels.buffer] : []);
};
