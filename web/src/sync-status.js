import { icon } from './dom.js';
import { l, setAttr } from './i18n.js';
import { syncCloud } from './sync-ring.js';

const names = {
  transfer: l("正在同步笔记"),
  success: l("笔记已同步"),
  unknown: l("等待确认"),
  conflict: l("同步冲突"),
  offline: l("离线"),
  failure: l("请求失败，请重试。"),
};
let pending = 0, last = '', generation = 0;

function render() {
  const node = document.getElementById('sync-status');
  if (!node) return;
  const state = pending ? 'transfer' : !navigator.onLine ? 'offline' : last === 'success' ? '' : last;
  node.hidden = !state;
  if (!state) {
    node.removeAttribute('data-sync');
    return;
  }
  const dot = document.createElement('span');
  dot.className = 'sync-dot';
  dot.setAttribute('aria-hidden', 'true');
  node.dataset.sync = state;
  setAttr(node, 'aria-label', names[state]);
  setAttr(node, 'title', names[state]);
  node.replaceChildren(icon(syncCloud), dot);
}

export const syncStatus = {
  start() {
    const stamp = generation;
    let done = false;
    pending++;
    render();
    return result => {
      if (done || stamp !== generation) return;
      done = true;
      pending = Math.max(0, pending - 1);
      last = names[result] ? result : 'unknown';
      render();
    };
  },
  reset() {
    generation++;
    pending = 0;
    last = '';
    render();
  },
};

if (typeof window !== 'undefined') for (const event of ['online', 'offline']) window.addEventListener(event, render);
