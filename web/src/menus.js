let active = null;
const nativePopover = 'popover' in HTMLElement.prototype;
const nativeInvoker = nativePopover && 'commandForElement' in HTMLButtonElement.prototype;
const nativeGroup = 'focusGroup' in HTMLElement.prototype;
const anchors = CSS.supports('position-area', 'block-end');
let positionFrame = null;
function queuePosition() {
  if (positionFrame !== null || !active) return;
  positionFrame = requestAnimationFrame(() => { positionFrame = null; active?.position(); });
}
document.addEventListener('scroll', queuePosition, { capture: true, passive: true });
window.addEventListener('resize', queuePosition);
window.visualViewport?.addEventListener('resize', queuePosition);
window.visualViewport?.addEventListener('scroll', queuePosition);

export function closeMenus() { active?.close(); }

document.addEventListener('focusin', event => {
  if (active && !active.contains(event.target)) closeMenus();
});
document.addEventListener('pointerdown', event => {
  if (active && !active.contains(event.target)) closeMenus();
});

export function bindMenu(trigger, menu, { signal } = {}) {
  const id = `menu-${crypto.randomUUID()}`;
  trigger.id ||= `${id}-trigger`;
  menu.id ||= id;
  menu.removeAttribute('role');
  const group = document.createElement('div');
  group.setAttribute('focusgroup', 'menu nomemory');
  group.setAttribute('aria-labelledby', trigger.id);
  group.append(...menu.childNodes);
  menu.append(group);
  // Keyboard focusgroup support can ship before implicit accessible menu roles.
  group.setAttribute('role', 'menu');
  for (const item of group.querySelectorAll('[role="menuitem"],button,a[href]')) {
    item.setAttribute('role', 'menuitem');
    if (!nativeGroup) item.tabIndex = -1;
  }
  trigger.setAttribute('aria-haspopup', 'menu');
  trigger.setAttribute('aria-controls', menu.id);
  trigger.setAttribute('aria-expanded', 'false');
  if (nativePopover) menu.setAttribute('popover', 'auto');
  else { menu.style.position = 'fixed'; menu.style.inset = 'auto'; }
  if (nativeInvoker) {
    trigger.setAttribute('commandfor', menu.id);
    trigger.setAttribute('command', 'toggle-popover');
  }
  if (anchors) {
    trigger.style.anchorName = `--${id}`;
    menu.style.positionAnchor = `--${id}`;
  }
  let open = false, enter = false, last = false;
  const options = signal ? { signal } : {};
  const items = () => [...group.querySelectorAll('button,a[href]')]
    .filter(node => !node.disabled && node.getAttribute('aria-disabled') !== 'true' && node.getClientRects().length);
  function focusItem(end = false) {
    const nodes = items(), node = end ? nodes.at(-1) : nodes[0];
    if (!nativeGroup) for (const item of nodes) item.tabIndex = item === node ? 0 : -1;
    node?.focus({ preventScroll: true });
  }
  function position() {
    if (!open || anchors && nativePopover) return;
    const rect = trigger.getBoundingClientRect();
    if (!trigger.isConnected || rect.bottom <= 0 || rect.top >= innerHeight || rect.right <= 0 || rect.left >= innerWidth) {
      close(true); return;
    }
    const size = menu.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.right - size.width, innerWidth - size.width - 8));
    const top = rect.bottom + size.height + 8 <= innerHeight ? rect.bottom + 4 : Math.max(8, rect.top - size.height - 4);
    menu.style.left = `${left}px`;
    menu.style.top = `${top}px`;
  }
  function close(restore = false) {
    if (!open) return;
    if (restore && trigger.isConnected) trigger.focus({ preventScroll: true });
    open = false;
    if (nativePopover && menu.matches(':popover-open')) menu.hidePopover();
    menu.hidden = true;
    trigger.setAttribute('aria-expanded', 'false');
    if (active === api) active = null;
  }
  function opened() {
    open = true;
    active = api;
    trigger.setAttribute('aria-expanded', 'true');
    position();
    if (enter) focusItem(last);
    else trigger.focus({ preventScroll: true, focusVisible: false });
    enter = last = false;
  }
  const api = { close, position, contains: node => trigger.contains(node) || menu.contains(node) };
  trigger.addEventListener('pointerdown', event => { if (event.button === 0) event.preventDefault(); }, options);
  trigger.addEventListener('click', event => {
    if (open) {
      if (!nativeInvoker) close(true);
      return;
    }
    closeMenus();
    enter = event.detail === 0;
    menu.hidden = false;
    if (nativePopover) {
      if (!nativeInvoker) menu.showPopover({ source: trigger });
    } else opened();
  }, options);
  trigger.addEventListener('keydown', event => {
    if (!['ArrowDown', 'ArrowUp'].includes(event.key)) return;
    event.preventDefault();
    last = event.key === 'ArrowUp';
    if (open) focusItem(last);
    else trigger.click();
  }, options);
  if (nativePopover) {
    menu.addEventListener('toggle', () => {
      if (menu.matches(':popover-open')) opened();
      else close();
    }, options);
  }
  group.addEventListener('click', event => {
    if (event.target.closest('button,a[href]')) close(true);
  }, { ...options, capture: true });
  group.addEventListener('keydown', event => {
    if (event.key === 'Escape' || event.key === 'Tab') {
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); }
      close(true);
      return;
    }
    if (nativeGroup || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const nodes = items(), index = nodes.indexOf(document.activeElement);
    if (!nodes.length) return;
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? nodes.length - 1 :
      (index + (event.key === 'ArrowUp' ? -1 : 1) + nodes.length) % nodes.length;
    event.preventDefault();
    for (const item of nodes) item.tabIndex = item === nodes[next] ? 0 : -1;
    nodes[next].focus({ preventScroll: true });
  }, options);
  signal?.addEventListener('abort', () => close(), { once: true });
  return api;
}
