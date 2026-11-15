const epsilon = 1e-10;

export function polygonArea(polygon) {
  return Math.abs(polygon.reduce((sum, p, i) => {
    const q = polygon[(i + 1) % polygon.length];
    return sum + p.x * q.y - q.x * p.y;
  }, 0)) / 2;
}

export function polygonCenter(polygon) {
  let area = 0, x = 0, y = 0;
  for (let i = 0; i < polygon.length; i++) {
    const p = polygon[i], q = polygon[(i + 1) % polygon.length], cross = p.x * q.y - q.x * p.y;
    area += cross; x += (p.x + q.x) * cross; y += (p.y + q.y) * cross;
  }
  return Math.abs(area) > epsilon ? { x: x / (3 * area), y: y / (3 * area) } :
    { x: polygon.reduce((sum, p) => sum + p.x, 0) / polygon.length,
      y: polygon.reduce((sum, p) => sum + p.y, 0) / polygon.length };
}

export function insidePolygon(point, polygon) {
  return polygon.every((p, i) => {
    const q = polygon[(i + 1) % polygon.length];
    return (q.x - p.x) * (point.y - p.y) - (q.y - p.y) * (point.x - p.x) >= -epsilon;
  });
}

function hull(points) {
  const sorted = [...points].sort((a, b) => a.x - b.x || a.y - b.y);
  const cross = (o, a, b) => (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);
  const lower = [], upper = [];
  for (const p of sorted) {
    while (lower.length > 1 && cross(lower.at(-2), lower.at(-1), p) <= 0) lower.pop();
    lower.push(p);
  }
  for (const p of sorted.reverse()) {
    while (upper.length > 1 && cross(upper.at(-2), upper.at(-1), p) <= 0) upper.pop();
    upper.push(p);
  }
  return [...lower.slice(0, -1), ...upper.slice(0, -1)];
}

function landmass(points) {
  const center = { x: points.reduce((sum, p) => sum + p.x, 0) / points.length,
    y: points.reduce((sum, p) => sum + p.y, 0) / points.length };
  const spread = Math.sqrt(points.reduce((sum, p) => sum + (p.x - center.x) ** 2 + (p.y - center.y) ** 2, 0) / points.length);
  const padding = Math.max(0.055, spread * 0.16);
  return hull(points.flatMap(p => Array.from({ length: 8 }, (_, i) => ({
    x: p.x + Math.cos(i * Math.PI / 4) * padding, y: p.y + Math.sin(i * Math.PI / 4) * padding,
  }))));
}

// Clip a convex region at the perpendicular bisector between two sites.
export function voronoiCell(boundary, site, others) {
  let polygon = boundary;
  for (const other of others) {
    if (other === site) continue;
    const nx = other.x - site.x, ny = other.y - site.y;
    if (Math.abs(nx) + Math.abs(ny) < epsilon) continue;
    const limit = (other.x ** 2 + other.y ** 2 - site.x ** 2 - site.y ** 2) / 2;
    const next = [];
    for (let i = 0; i < polygon.length; i++) {
      const a = polygon[i], b = polygon[(i + 1) % polygon.length];
      const da = a.x * nx + a.y * ny - limit, db = b.x * nx + b.y * ny - limit;
      if (da <= epsilon) next.push(a);
      if ((da < 0 && db > 0) || (da > 0 && db < 0)) {
        const t = da / (da - db);
        next.push({ x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t });
      }
    }
    polygon = next;
    if (!polygon.length) break;
  }
  return polygon;
}

function region(id, kind, polygon, points, color, label = '', shade = 0) {
  return { id, kind, polygon, color, label, shade, ...polygonCenter(polygon), area: polygonArea(polygon),
    bounds: { left: Math.min(...polygon.map(p => p.x)), right: Math.max(...polygon.map(p => p.x)),
      top: Math.min(...polygon.map(p => p.y)), bottom: Math.max(...polygon.map(p => p.y)) },
    members: points.map(p => p.name), children: [] };
}

function partition(points, count) {
  const sites = [points[0]], nearest = points.map(() => Infinity);
  while (sites.length < count) {
    const last = sites.at(-1);
    let farthest = -1, distance = 0;
    points.forEach((p, i) => {
      nearest[i] = Math.min(nearest[i], (p.x - last.x) ** 2 + (p.y - last.y) ** 2);
      if (nearest[i] > distance) { distance = nearest[i]; farthest = i; }
    });
    if (farthest < 0 || distance < epsilon) break;
    sites.push(points[farthest]);
  }
  const groups = sites.map(() => []);
  for (const point of points) {
    let best = 0, distance = Infinity;
    sites.forEach((site, i) => {
      const value = (point.x - site.x) ** 2 + (point.y - site.y) ** 2;
      if (value < distance) { best = i; distance = value; }
    });
    groups[best].push(point);
  }
  return { sites, groups };
}

function parcels(parent, points, depth = 0) {
  if (points.length === 1) { parent.name = points[0].name; return; }
  const leaves = points.length <= 40 || depth >= 6;
  const { sites, groups } = leaves ? { sites: points, groups: points.map(p => [p]) } : partition(points, Math.min(8, Math.ceil(points.length / 24)));
  for (let i = 0; i < sites.length; i++) {
    const polygon = voronoiCell(parent.polygon, sites[i], sites);
    if (polygon.length < 3 || polygonArea(polygon) < epsilon || !groups[i].length) continue;
    const child = region(`${parent.id}/${i}`, leaves ? 'note' : 'block', polygon, groups[i], parent.color, '', parent.shade + (i % 5 - 2) * 0.018);
    if (leaves) child.name = sites[i].name;
    else parcels(child, groups[i], depth + 1);
    parent.children.push(child);
  }
}

// The source projection determines relative positions within each district.
function fitMembers(points, polygon) {
  const center = polygonCenter(polygon);
  const source = { x: points.reduce((sum, p) => sum + p.x, 0) / points.length,
    y: points.reduce((sum, p) => sum + p.y, 0) / points.length };
  let factor = Infinity;
  for (const point of points) {
    const dx = point.x - source.x, dy = point.y - source.y;
    for (let i = 0; i < polygon.length; i++) {
      const a = polygon[i], b = polygon[(i + 1) % polygon.length], nx = b.y - a.y, ny = a.x - b.x;
      const projected = dx * nx + dy * ny;
      if (projected > epsilon) factor = Math.min(factor, ((a.x - center.x) * nx + (a.y - center.y) * ny) / projected);
    }
  }
  factor = Number.isFinite(factor) ? Math.max(0, factor) * 0.82 : 1;
  return points.map(p => ({ ...p, x: center.x + (p.x - source.x) * factor, y: center.y + (p.y - source.y) * factor }));
}

export function buildMapRegions(points, topics, palette) {
  const source = new Map(points.map(p => [p.name, p])), display = new Map(source), roots = [], assigned = new Set();
  topics.forEach((topic, index) => {
    const members = topic.members.map(name => source.get(name)).filter(Boolean), color = palette[index % palette.length];
    if (!members.length) return;
    for (const p of members) assigned.add(p.name);
    const root = region(topic.id, 'topic', landmass(members), members, color, topic.label);
    roots.push(root);
    if (!topic.districts?.length) { parcels(root, members); return; }
    let sites = topic.districts.map(district => ({ x: district.x, y: district.y }));
    const center = polygonCenter(root.polygon), radius = Math.sqrt(root.area / Math.PI);
    const spread = Math.max(...sites.map(p => Math.hypot(p.x - center.x, p.y - center.y)));
    sites = sites.map((p, i) => {
      const angle = i * 2.3999632297;
      return { x: center.x + (p.x - center.x) * Math.min(1, radius * 0.6 / Math.max(spread, epsilon)) + Math.cos(angle) * radius * 0.035,
        y: center.y + (p.y - center.y) * Math.min(1, radius * 0.6 / Math.max(spread, epsilon)) + Math.sin(angle) * radius * 0.035 };
    });
    topic.districts.forEach((district, i) => {
      const polygon = voronoiCell(root.polygon, sites[i], sites);
      if (polygon.length < 3 || polygonArea(polygon) < epsilon) return;
      const members = fitMembers(district.members.map(name => source.get(name)).filter(Boolean), polygon);
      for (const p of members) display.set(p.name, p);
      const child = region(district.id, 'district', polygon, members, color, district.label, (i % 5 - 2) * 0.045);
      parcels(child, members); root.children.push(child);
    });
  });
  for (const point of points) {
    if (assigned.has(point.name)) continue;
    const root = region(point.name, 'note', landmass([point]), [point], '#A9ADB4');
    root.name = point.name; roots.push(root);
  }
  function centerNotes(node) {
    if (node.name && points.length > 24) display.set(node.name, { ...display.get(node.name), x: node.x, y: node.y });
    for (const child of node.children) centerNotes(child);
  }
  roots.forEach(centerNotes);
  return { roots, points: points.map(p => display.get(p.name)) };
}

// Screen area controls detail; a bounded traversal avoids dense tessellations.
export function visibleRegions(roots, view, width, height, viewportArea = width * height) {
  const labels = [], budget = 220;
  const visible = node => (node.bounds.right - view.x) * view.scale >= -width / 2 &&
    (node.bounds.left - view.x) * view.scale <= width / 2 &&
    (node.bounds.bottom - view.y) * view.scale >= -height / 2 &&
    (node.bounds.top - view.y) * view.scale <= height / 2;
  const result = roots.filter(visible).slice(0, budget);
  for (const node of result) if (node.label) labels.push(node);
  for (let i = 0; i < result.length;) {
    const node = result[i], children = node.children.filter(visible);
    const minimumArea = node.kind === 'topic' ? Math.min(18000, viewportArea / 32) : node.kind === 'district' ? 6500 : 3000;
    if (children.length && node.area * view.scale ** 2 / node.children.length > minimumArea &&
        result.length + children.length - 1 <= budget) {
      result.splice(i, 1, ...children);
      for (const child of children) if (child.label) labels.push(child);
    } else i++;
  }
  return { regions: result, labels };
}
