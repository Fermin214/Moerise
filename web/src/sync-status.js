import { icon } from './dom.js';
import { l, setAttr } from './i18n.js';
import { syncCloud } from './sync-ring.js';

const names = {
  unknown: l("等待确认"),
  conflict: l("同步冲突"),
  offline: l("离线"),
  failure: l("请求失败，请重试。"),
};
const alerts = new Map();
let generation = 0;

function render() {
  const node = document.getElementById('sync-status');
  if (!node) return;
  const active = new Set(alerts.values());
  const state = !navigator.onLine ? 'offline' : ['conflict', 'unknown', 'failure'].find(value => active.has(value));
  node.hidden = !state;
  if (!state) {
    node.removeAttribute('data-sync');
    node.removeAttribute('aria-label');
    node.removeAttribute('title');
    node.replaceChildren();
    return;
  }
  node.dataset.sync = state;
  setAttr(node, 'aria-label', names[state]);
  setAttr(node, 'title', names[state]);
  node.replaceChildren(icon(syncCloud));
}

export const syncStatus = {
  start(operation, { write = false } = {}) {
    const stamp = generation;
    let done = false;
    return result => {
      if (done || stamp !== generation) return;
      done = true;
      if (result === 'canceled') return;
      if (result === 'success') alerts.delete(operation);
      else alerts.set(operation, result === 'timeout' ? (write ? 'unknown' : 'failure') :
        Object.hasOwn(names, result) ? result : 'unknown');
      render();
    };
  },
  clear(operation) {
    if (alerts.delete(operation)) render();
  },
  reset() {
    generation++;
    alerts.clear();
    render();
  },
};

if (typeof window !== 'undefined') for (const event of ['online', 'offline']) window.addEventListener(event, render);
