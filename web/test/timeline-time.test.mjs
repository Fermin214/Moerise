import assert from 'node:assert/strict';
import { test } from 'node:test';
import { nextServiceDay, serviceDate, timelineTime } from '../src/timeline-time.js';

test('timeline colophons distinguish service days, years and languages', () => {
  const zone = 'Pacific/Kiritimati', instant = '2026-11-23T10:57:00Z';
  const today = serviceDate(zone, Date.parse('2026-11-24T09:59:59Z'));
  assert.equal(today, '2026-11-24');
  assert.equal(timelineTime(instant, zone, today, false), '00:57');
  assert.equal(timelineTime(instant, 'UTC', today, true), 'Nov 23');
  assert.equal(timelineTime(instant, zone, '2026-11-25', false), '11月24日');
  assert.equal(timelineTime(instant, zone, '2026-11-25', true), 'Nov 24');
  assert.equal(timelineTime('2025-12-18T00:00:00Z', zone, today, false), '2025年12月18日');
  assert.equal(timelineTime('2025-12-18T00:00:00Z', zone, today, true), '2025-12-18');
  assert.equal(timelineTime('invalid', zone, today, false), '');
});

test('the service-day timer uses the next local day even on DST transition days', () => {
  const cases = [
    ['Pacific/Kiritimati', '2026-11-24T09:59:59.999Z', '2026-11-24T10:00:00Z'],
    ['America/New_York', '2026-03-08T05:00:00Z', '2026-03-09T04:00:00Z'],
    ['America/New_York', '2026-11-01T04:00:00Z', '2026-11-02T05:00:00Z'],
    ['America/Santiago', '2026-09-05T04:00:00Z', '2026-09-06T04:00:00Z'],
  ];
  function assertBoundaries() {
    for (const [zone, now, midnight] of cases) {
      assert.equal(nextServiceDay(zone, Date.parse(now)), Date.parse(midnight));
    }
  }
  assertBoundaries();
  const nativeTemporal = globalThis.Temporal;
  try { globalThis.Temporal = undefined; assertBoundaries(); }
  finally { globalThis.Temporal = nativeTemporal; }
});
