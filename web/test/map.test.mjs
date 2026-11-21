import assert from 'node:assert/strict';
import { test } from 'node:test';
import { validMap, fitMapView, transformMapView } from '../src/map.js';

const stellar = { seed: 0.5, role: 'ordinary', radius: 1, luminosity: 0.6 };
const point = { name: 'memos/a', source_hash: 'source', x: 0, y: 0, semantic: true, tags: [], stellar, excerpt: '摘要', create_time: '2026-10-20T08:00:00Z' };
const ready = () => ({ status: 'ready', algorithm: 'umap-balanced-v1', projection_revision: 1,
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

test('map validation rejects duplicate, legacy, foreign-resource, invalid-coordinate and incomplete responses', () => {
  for (const value of [null, {}, { ...ready(), status: 'unknown' }, { ...ready(), algorithm: 'pca-topics-v3' },
    { ...ready(), algorithm: 'pca-fixed-v2' }, { ...ready(), layout_frame: '' },
    { ...ready(), layout_version: '' }, { ...ready(), vector_generation: {} }, { ...ready(), points: [] },
    { ...ready(), status: 'empty' }, { ...ready(), points: [point, point] }, { ...ready(), projection_revision: 1.2 }]) assert.equal(validMap(value), false);
  for (const change of [{ name: '../session' }, { name: 'a' }, { x: Infinity }, { y: NaN }, { tags: null },
    { tags: [1] }, { semantic: 'yes' }, { source_hash: '' }, { stellar: undefined }, { excerpt: 123 }, { create_time: 123 }]) assert.equal(validMap({ ...ready(), points: [{ ...point, ...change }] }), false);
});

test('semantic map metadata rejects foreign memberships, file-only relationships and duplicate edges', () => {
  const semantic = () => ({ ...ready(),
    points: [{ ...point }, { ...point, name: 'memos/b', x: 1 }, { ...point, name: 'memos/file', semantic: false, stellar: undefined }],
    topics: [{ id: 'topic-0000000000000001', label: '主题', x: 0, y: 0, members: ['memos/a', 'memos/b'] }],
    links: [{ source: 'memos/a', target: 'memos/b', weight: 0.8 }] });
  assert.ok(validMap(semantic()));
  for (const name of ['memos/missing', 'memos/file', 'memos/a']) {
    const value = semantic();
    value.topics[0].members[1] = name;
    assert.equal(validMap(value), false);
  }
  for (const link of [{ source: 'memos/file' }, { target: 'memos/missing' }, { target: 'memos/a' },
    { weight: 0.54 }, { weight: NaN }, { weight: 1.01 }]) {
    const value = semantic();
    Object.assign(value.links[0], link);
    assert.equal(validMap(value), false);
  }
  const duplicate = semantic();
  duplicate.links.push({ source: 'memos/b', target: 'memos/a', weight: 0.8 });
  assert.equal(validMap(duplicate), false);
  const missing = semantic();
  delete missing.topics;
  assert.equal(validMap(missing), false);
});

test('subtopics stay inside their parent topic without duplicates or foreign notes', () => {
  const value = { ...ready(), links: [],
    points: [{ ...point }, { ...point, name: 'memos/b' }],
    topics: [{ id: 'topic-0000000000000001', label: '技术', x: 0, y: 0, members: ['memos/a', 'memos/b'],
      districts: [{ id: 'district-0000000000000001', label: '存储', x: 0, y: 0, members: ['memos/a'] },
        { id: 'district-0000000000000002', label: '网络', x: 1, y: 0, members: ['memos/b'] }] }] };
  assert.ok(validMap(value));
  for (const name of ['memos/a', 'memos/missing']) {
    const invalid = structuredClone(value); invalid.topics[0].districts[1].members = [name];
    assert.equal(validMap(invalid), false);
  }
});

test('fit keeps all coordinates inside the prototype frame without changing authoritative positions', () => {
  const points = Array.from({ length: 10000 }, (_, i) => ({ x: Math.sin(i) * 13, y: Math.cos(i) * 7 }));
  for (const width of [268, 696]) {
    const view = fitMapView(points, width, 440);
    assert.ok(view.scale > 0);
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
