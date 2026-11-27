import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { runInNewContext } from 'node:vm';

const startup = readFileSync(new URL('../src/theme-init.js', import.meta.url), 'utf8');

function fixture(stored, systemDark, unavailable = false) {
  const dataset = {}, meta = { content: 'light dark' }, system = new EventTarget();
  system.matches = systemDark;
  const context = { window: new EventTarget(), matchMedia: () => system,
    document: { documentElement: { dataset }, querySelector: () => meta, getElementById: () => null } };
  Object.defineProperty(context, 'localStorage', { get() {
    if (unavailable) throw new Error('Storage unavailable');
    return { getItem: () => stored, setItem: (key, value) => { stored = value; }, removeItem: () => { stored = null; } };
  } });
  return { context, dataset, meta, system, stored: () => stored };
}

test('blocking bootstrap sets the resolved HTML and native color scheme', () => {
  for (const [saved, systemDark, unavailable, preference, resolved] of [
    ['dark', false, false, 'dark', 'dark'], ['light', true, false, 'light', 'light'],
    [null, true, false, 'auto', 'dark'], ['invalid', false, false, 'auto', 'light'],
    ['light', true, true, 'auto', 'dark'],
  ]) {
    const { context, dataset, meta } = fixture(saved, systemDark, unavailable);
    runInNewContext(startup, context);
    assert.deepEqual(dataset, { themePreference: preference, theme: resolved });
    assert.equal(meta.content, resolved);
  }
});

const loadTheme = name => import(new URL(`../src/theme.js?${name}`, import.meta.url));

test('runtime follows system changes, pins manual modes and survives storage failures', async () => {
  for (const unavailable of [false, true]) {
    const { context, dataset, meta, system, stored } = fixture(null, false, unavailable);
    const originals = Object.getOwnPropertyDescriptors(globalThis);
    for (const name of ['window', 'document', 'matchMedia', 'localStorage']) {
      Object.defineProperty(globalThis, name, { ...Object.getOwnPropertyDescriptor(context, name), configurable: true });
    }
    try {
      runInNewContext(startup, context);
      const { themePreference, resolvedTheme, setTheme, initTheme } = await loadTheme(String(unavailable));
      initTheme();
      assert.equal(resolvedTheme(), 'light');
      setTheme('light', { persist: true });
      system.matches = true;
      system.dispatchEvent(new Event('change'));
      assert.equal(dataset.theme, 'light');
      assert.equal(stored(), unavailable ? null : 'light');
      setTheme('auto', { persist: true });
      assert.equal(themePreference(), 'auto');
      assert.equal(meta.content, 'dark');
      assert.equal(stored(), null);
      system.matches = false;
      system.dispatchEvent(new Event('change'));
      assert.equal(dataset.theme, 'light');
      setTheme('dark', { persist: true });
      assert.equal(meta.content, 'dark');
      assert.equal(stored(), unavailable ? null : 'dark');
    } finally {
      for (const name of ['window', 'document', 'matchMedia', 'localStorage']) {
        if (originals[name]) Object.defineProperty(globalThis, name, originals[name]);
        else delete globalThis[name];
      }
    }
  }
});
