import { el, button } from './dom.js';

let current, timer, actionable;

export function dismissSnackbar() {
  clearTimeout(timer);
  current?.remove();
  current = null;
  actionable = false;
}

export function snackbar(message, { label, action, duration = 3000 } = {}) {
  if (!action && actionable && current?.isConnected) return;
  dismissSnackbar();
  const bar = el('div', 'snackbar');
  const status = el('span', '', message);
  status.setAttribute('role', 'status');
  bar.append(status);
  if (action) bar.append(button(label, 'ghost', () => {
    if (current !== bar) return;
    dismissSnackbar();
    action();
  }));
  current = bar;
  actionable = Boolean(action);
  ([...document.querySelectorAll('dialog[open]')].at(-1) || document.body).append(bar);
  timer = setTimeout(dismissSnackbar, duration);
}
