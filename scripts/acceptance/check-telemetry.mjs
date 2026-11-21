import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { mkdir, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { runProcess } from '../lib/process.mjs';

assert.equal(process.argv.length, 3, 'node scripts/acceptance/check-telemetry.mjs <receiver-binary>');
const binary = resolve(process.argv[2]);
const root = resolve('.local', `telemetry-acceptance-${randomBytes(8).toString('hex')}`);
await mkdir(root, { recursive: true, mode: 0o700 });
const listener = createServer();
await new Promise(resolve => listener.listen(0, '127.0.0.1', resolve));
const port = listener.address().port;
await new Promise(resolve => listener.close(resolve));
const database = resolve(root, 'daily.sqlite3');
const receiver = spawn(binary, ['-db', database, '-listen', `127.0.0.1:${port}`, 'serve'], { stdio: 'ignore' });
let failure, succeeded = false;
receiver.once('error', error => { failure = error; });
try {
  const payload = {
    schema: 1, platform: 'web', client_version: '0.0.0', core_version: '0.0.0', system_major: 'unknown',
    rows: [{ operation: 'startup', result: 'success', duration: 'lt1s', source: 'user', count: 2 }],
  };
  let response;
  for (let attempt = 0; attempt < 50; attempt++) {
    if (failure) throw failure;
    assert.equal(receiver.exitCode, null, '接收端提前退出');
    try {
      response = await fetch(`http://127.0.0.1:${port}/v1/batches`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
        signal: AbortSignal.timeout(1000),
      });
      break;
    } catch { await delay(100); }
  }
  assert.equal(response?.status, 204);
  const report = await runProcess(binary, ['-db', database, 'report'], { timeout: 5000 });
  const rows = report.trim().split('\n').map(row => row.split(','));
  assert.equal(rows.length, 2);
  const value = Object.fromEntries(rows[0].map((key, i) => [key, rows[1][i]]));
  assert.equal(value.platform, 'web');
  assert.equal(value.client_version, '0.0.0');
  assert.equal(value.sample_count, '2');
  assert.equal(value.success, '2');
  await writeFile(resolve(root, 'weekly.csv'), report);
  await writeFile(resolve(root, 'result.json'), JSON.stringify({ receiver: 'passed', report: 'passed' }) + '\n');
  succeeded = true;
  console.log(`遥测接收与周报导出通过：${root}`);
} finally {
  if (receiver.exitCode === null && receiver.signalCode === null) {
    await new Promise(resolve => { receiver.once('exit', resolve); receiver.kill(); });
  }
  for (const suffix of ['', '-wal', '-shm', '-journal']) await rm(database + suffix, { force: true });
  if (!succeeded) await writeFile(resolve(root, 'result.json'), JSON.stringify({ receiver: 'failed' }) + '\n');
}
