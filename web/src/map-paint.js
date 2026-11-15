import { visibleRegions } from './map-regions.js';

function tint(color, amount) {
  const channels = color.slice(1).match(/../g).map(value => parseInt(value, 16));
  return `rgb(${channels.map(value => Math.round(value + (255 - value) * amount)).join(',')})`;
}

export function regionPath(context, polygon, view, width, height, dx = 0, dy = 0) {
  context.beginPath();
  polygon.forEach((p, i) => {
    const x = width / 2 + (p.x - view.x) * view.scale + dx, y = height / 2 + (p.y - view.y) * view.scale + dy;
    if (i) context.lineTo(x, y); else context.moveTo(x, y);
  });
  context.closePath();
}

export function paintRegions(context, { view, width, height, ratio, viewportArea = width * height }, roots) {
  context.setTransform(ratio, 0, 0, ratio, 0, 0);
  context.fillStyle = '#FBFCFE'; context.fillRect(0, 0, width, height);
  context.lineJoin = 'round';
  const { regions, labels } = visibleRegions(roots, view, width, height, viewportArea);
  for (const root of roots) {
    regionPath(context, root.polygon, view, width, height);
    context.fillStyle = tint(root.color, 0.44); context.fill();
    context.strokeStyle = tint(root.color, 0.3); context.lineWidth = 1.2; context.stroke();
  }
  for (const node of regions) {
    regionPath(context, node.polygon, view, width, height);
    context.fillStyle = tint(node.color, Math.max(0.15, 0.43 + node.shade)); context.fill();
    context.strokeStyle = '#FBFCFE'; context.lineWidth = node.kind === 'note' ? 1.6 : 2.5; context.stroke();
  }
  for (const node of labels) {
    if (node.kind !== 'district') continue;
    regionPath(context, node.polygon, view, width, height);
    context.strokeStyle = '#FBFCFE'; context.lineWidth = 3.5; context.stroke();
  }
}
