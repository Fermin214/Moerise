import { spawn, execFileSync } from 'node:child_process';
import { EventEmitter } from 'node:events';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const build = mkdtempSync(join(tmpdir(), 'moerise-testweb-'));
const binary = join(build, process.platform === 'win32' ? 'testweb.exe' : 'testweb');
execFileSync('go', ['build', '-o', binary, './scripts/testweb'], { cwd: repo, stdio: 'pipe' });
const children = new Set();
process.on('exit', () => {
  for (const child of children) child.kill();
  rmSync(build, { recursive: true, force: true });
});

export function createWebServer({ coreOrigin, dist = join(repo, 'web/dist') } = {}) {
  const server = new EventEmitter();
  let child, port;
  server.listen = (requestedPort, host = '127.0.0.1') => {
    child = spawn(binary, ['--core', coreOrigin, '--dist', dist, '--address', `${host}:${requestedPort}`], { cwd: repo });
    children.add(child);
    child.once('error', error => server.emit('error', error));
    let line = '';
    child.stdout.on('data', data => {
      line += data.toString();
      if (port === undefined && line.includes('\n')) {
        port = Number(line.trim());
        server.emit('listening');
      }
    });
    child.stderr.on('data', data => process.stderr.write(data));
    child.once('exit', () => { children.delete(child); server.emit('close'); });
    return server;
  };
  server.address = () => ({ port });
  server.close = callback => {
    if (!child || child.exitCode !== null || child.signalCode !== null) callback?.();
    else { child.once('exit', () => callback?.()); child.kill(); }
  };
  server.closeAllConnections = () => {};
  return server;
}
