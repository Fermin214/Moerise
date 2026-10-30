import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { basename, dirname, join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { setTimeout as delay } from 'node:timers/promises';
import { test } from 'node:test';
import { runProcess } from '../compose-process.mjs';

for (const ignoresSignal of [false, true]) {
  test(`abort waits for a child to close (ignores SIGTERM: ${ignoresSignal})`, async t => {
    const root = await mkdtemp(join(tmpdir(), 'moerise-process-test-'));
    const marker = join(root, 'pid');
    t.after(async () => {
      assert.equal(dirname(resolve(root)), resolve(tmpdir()));
      assert.ok(basename(root).startsWith('moerise-process-test-'));
      await rm(root, { recursive: true });
    });
    const controller = new AbortController();
    const code = `
      process.on('SIGTERM', () => {
        ${ignoresSignal ? '' : 'setTimeout(() => process.exit(0), 100);'}
      });
      require('node:fs').writeFileSync(process.argv[1], String(process.pid));
      setInterval(() => {}, 1000);
    `;
    const result = runProcess(process.execPath, ['-e', code, marker], {
      signal: controller.signal, timeout: 10_000,
    });
    const rejected = assert.rejects(result, { name: 'AbortError' });
    let pid;
    const deadline = Date.now() + 5000;
    while (!pid && Date.now() < deadline) {
      try { pid = Number(await readFile(marker, 'utf8')); } catch { await delay(20); }
    }
    assert.ok(pid, 'child did not start');
    controller.abort();
    await rejected;
    assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
  });
}

test('an already cancelled command does not launch a process', async () => {
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(runProcess(process.execPath, ['-e', 'process.exit(1)'], {
    signal: controller.signal,
  }), { name: 'AbortError' });
});
