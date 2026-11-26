export const reducedMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

export function animateMotion(node, frames, tier = 'responsive', options = {}) {
  const style = getComputedStyle(document.documentElement);
  const reduced = reducedMotion();
  const duration = reduced ? 80 : Number.parseInt(style.getPropertyValue(`--motion-${tier}-duration`), 10);
  const easing = reduced ? 'linear' : style.getPropertyValue(`--motion-${tier}-easing`).trim();
  return node.animate(reduced ? frames.map(({ opacity }) => ({ opacity: opacity ?? 1 })) : frames,
    { duration, easing, ...options, ...(reduced ? { duration: 80, delay: 0 } : {}) });
}

export function motionPositions(nodes, key = node => node.dataset.name) {
  if (reducedMotion()) return new Map();
  return new Map([...nodes].map(node => [key(node), node.getBoundingClientRect()]));
}

export function moveFrom(nodes, positions, key = node => node.dataset.name) {
  if (!positions.size) return;
  const moves = [...nodes].map(node => {
    const before = positions.get(key(node));
    if (!before) return [node, 0, 0];
    const after = node.getBoundingClientRect();
    return [node, before.left - after.left, before.top - after.top];
  });
  for (const [node, horizontal, vertical] of moves) {
    if (horizontal || vertical) animateMotion(node, [
      { transform: `translate(${horizontal}px, ${vertical}px)` }, { transform: 'none' },
    ]);
  }
}
