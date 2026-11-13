import { spawn } from 'node:child_process';

export async function runProcess(file, args, { signal, timeout = 180_000, ...options } = {}) {
  const deadline = AbortSignal.timeout(timeout);
  const cancellation = signal ? AbortSignal.any([signal, deadline]) : deadline;
  cancellation.throwIfAborted();
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, { ...options, signal: cancellation, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    const output = [];
    let bytes = 0;
    let failure;
    let killTimer;
    const forceExit = () => {
      killTimer ??= setTimeout(() => child.kill('SIGKILL'), 1000).unref();
    };
    for (const stream of [child.stdout, child.stderr]) {
      stream.on('data', chunk => {
        bytes += chunk.length;
        if (bytes > 16 * 1024 * 1024) {
          failure ??= Object.assign(new Error('Command output limit exceeded'), { code: 'MAXBUFFER' });
          child.kill('SIGTERM');
          forceExit();
        } else if (stream === child.stdout) output.push(chunk);
      });
    }
    child.once('error', error => {
      failure ??= error;
      if (error.name === 'AbortError') forceExit();
    });
    // Abort emits error before the process closes; cleanup must wait for its exit.
    child.once('close', (status, signal) => {
      clearTimeout(killTimer);
      if (failure) reject(failure);
      else if (status !== 0) reject(Object.assign(new Error('Command failed'), { status, code: signal }));
      else resolve(Buffer.concat(output).toString('utf8').trim());
    });
  });
}
