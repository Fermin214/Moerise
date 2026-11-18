import { densitySteps } from './map-star-density.js';

const yieldTask = () => globalThis.scheduler?.yield ? scheduler.yield() : new Promise(resolve => setTimeout(resolve, 0));

export function createDensityProcessor() {
  let worker = null, disabled = false, dead = false, sequence = 0, job = null;
  function finish(value) {
    if (!job) return;
    clearTimeout(job.timer);
    job.resolve(value); job = null;
  }
  async function fallback(task) {
    const steps = densitySteps(task.points, task.labels), started = performance.now();
    try {
      let step;
      do {
        if (dead || job !== task) return;
        step = steps.next();
        if (!step.done) await yieldTask();
      } while (!step.done);
      if (job === task) finish({ ...step.value, elapsed: performance.now() - started, mode: 'fallback' });
    } finally { steps.return(); }
  }
  function fail() {
    worker?.terminate(); worker = null; disabled = true;
    if (job) { clearTimeout(job.timer); fallback(job); }
  }
  return {
    prepare(points, labels) {
      finish(null);
      if (dead || !points.length) return Promise.resolve(null);
      return new Promise(resolve => {
        const task = job = { id: ++sequence, points, labels, resolve, timer: null };
        if (!disabled && typeof Worker === 'function') {
          try {
            if (!worker) {
              worker = new Worker(new URL('./map-density-worker.js', import.meta.url), { type: 'module' });
              worker.addEventListener('message', ({ data }) => {
                if (job?.id === data.id && data.pixels instanceof Uint8ClampedArray) finish({ ...data, mode: 'worker' });
              });
              worker.addEventListener('error', event => { event.preventDefault(); fail(); });
              worker.addEventListener('messageerror', fail);
            }
            task.timer = setTimeout(fail, 15000);
            worker.postMessage({ id: task.id, points, labels });
            return;
          } catch { fail(); return; }
        }
        fallback(task);
      });
    },
    destroy() { dead = true; worker?.terminate(); worker = null; finish(null); },
  };
}
