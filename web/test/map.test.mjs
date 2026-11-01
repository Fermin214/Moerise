import assert from 'node:assert/strict';
import { test } from 'node:test';
import { validMap, fitMapView, transformMapView } from '../src/map.js';

const point = { name: 'memos/a', source_hash: 'source', x: 0, y: 0, semantic: true, tags: [] };
const ready = () => ({ status: 'ready', algorithm: 'pca-fixed-v2', projection_revision: 1,
  layout_version: 'a'.repeat(64), layout_frame: 'b'.repeat(64), vector_generation: 'published', points: [{ ...point }] });

test('map responses distinguish ready, empty and non-ready without stale points', () => {
  assert.ok(validMap(ready()));
  assert.ok(validMap({ ...ready(), status: 'empty', points: [] }));
  for (const status of ['disabled', 'waiting', 'building', 'unavailable']) {
    const value = { ...ready(), status, points: [], layout_version: '', layout_frame: '', vector_generation: '' };
    assert.ok(validMap(value));
    assert.equal(validMap({ ...value, points: [point] }), false);
    assert.equal(validMap({ ...value, layout_frame: 'b'.repeat(64) }), false);
  }
});

test('map validation rejects duplicate, foreign-resource, invalid-coordinate and incomplete responses', () => {
  for (const value of [null, {}, { ...ready(), status: 'unknown' }, { ...ready(), layout_frame: '' },
    { ...ready(), layout_version: '' }, { ...ready(), vector_generation: {} }, { ...ready(), points: [] },
    { ...ready(), status: 'empty' }, { ...ready(), points: [point, point] }, { ...ready(), projection_revision: 1.2 }]) assert.equal(validMap(value), false);
  for (const change of [{ name: '../session' }, { name: 'a' }, { x: Infinity }, { y: NaN }, { tags: null },
    { tags: [1] }, { semantic: 'yes' }, { source_hash: '' }]) assert.equal(validMap({ ...ready(), points: [{ ...point, ...change }] }), false);
});

test('fit keeps all coordinates inside the prototype frame without changing authoritative positions', () => {
  const points = Array.from({ length: 10000 }, (_, i) => ({ x: Math.sin(i) * 13, y: Math.cos(i) * 7 }));
  for (const width of [268, 696]) {
    const view = fitMapView(points, width, 440);
    for (const point of points) {
      assert.ok(Math.abs((point.x - view.x) * view.scale) <= width / 2 - 28 + 1e-8);
      assert.ok(Math.abs((point.y - view.y) * view.scale) <= 220 - 28 + 1e-8);
    }
  }
  assert.deepEqual(fitMapView([], 696, 440), { x: 0, y: 0, scale: 100 });
  assert.deepEqual(fitMapView([{ x: 4, y: 7 }], 696, 440), { x: 4, y: 7, scale: 100 });
});

test('zoom and pinch preserve the world coordinate under the gesture anchor, including scale limits', () => {
  const view = { x: 4, y: 7, scale: 90 }, before = { x: 110, y: -70 }, after = { x: 130, y: -50 };
  for (const factor of [0.5, 2, 1e10, 1e-10]) {
    const next = transformMapView(view, factor, before, after);
    assert.ok(Math.abs(view.x + before.x / view.scale - next.x - after.x / next.scale) < 1e-8);
    assert.ok(Math.abs(view.y + before.y / view.scale - next.y - after.y / next.scale) < 1e-8);
    assert.ok(next.scale >= 0.01 && next.scale <= 10000);
  }
  assert.deepEqual(transformMapView(view, 1, { x: 0, y: 0 }, { x: 90, y: 45 }), { x: 3, y: 6.5, scale: 90 });
});
