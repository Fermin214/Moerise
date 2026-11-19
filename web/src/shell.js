export function initShell(onOpenNavigation) {
  const app = document.getElementById('app'), main = document.getElementById('main');
  const sidebar = document.getElementById('sidebar'), dialog = document.getElementById('navigation-dialog');
  const header = document.getElementById('mobile-header'), menu = document.getElementById('navigation-toggle');
  const close = document.getElementById('navigation-close'), search = document.getElementById('search-panel');
  const searchToggle = document.getElementById('search-toggle'), query = document.getElementById('q');
  const sidebarBody = sidebar.querySelector('.sidebar-body'), account = document.getElementById('account');
  const searchHome = document.createComment('search');
  search.before(searchHome);
  const compact = matchMedia('(max-width:960px)');
  let searchOpen = false, gesture = null, sidebarScrollTop = 0;
  const measurements = new Map();
  let measurementFrame = null;
  function measuredStyle(node, name, value) {
    if (node.style.getPropertyValue(name) === value) return;
    measurements.set(name, { node, value });
    if (measurementFrame !== null) return;
    measurementFrame = requestAnimationFrame(() => {
      measurementFrame = null;
      for (const [name, { node, value }] of measurements) node.style.setProperty(name, value);
      measurements.clear();
    });
  }

  function revealAccount() {
    sidebarScrollTop = Math.max(0, sidebarBody.scrollTop);
    sidebar.classList.remove('account-hidden');
  }
  sidebarBody.addEventListener('scroll', () => {
    const top = Math.max(0, Math.min(sidebarBody.scrollTop, sidebarBody.scrollHeight - sidebarBody.clientHeight));
    const delta = top - sidebarScrollTop;
    sidebarScrollTop = top;
    if (top === 0 || account.contains(document.activeElement)) sidebar.classList.remove('account-hidden');
    else if (delta) sidebar.classList.toggle('account-hidden', delta > 0);
  }, { passive: true });
  account.addEventListener('focusin', revealAccount);
  new ResizeObserver(() => {
    measuredStyle(sidebar, '--account-space', account.hidden ? '0px' : `${account.offsetHeight + 16}px`);
  }).observe(account);
  new ResizeObserver(revealAccount).observe(sidebarBody);

  function closeNavigation() {
    if (dialog.open) dialog.close();
    menu.setAttribute('aria-expanded', 'false');
    document.documentElement.classList.remove('navigation-open');
  }
  function closeSearch({ focus = false } = {}) {
    const focused = search.contains(document.activeElement);
    searchOpen = false;
    search.hidden = compact.matches;
    searchToggle.setAttribute('aria-expanded', String(!compact.matches));
    if (focused && compact.matches) {
      if (focus) searchToggle.focus({ preventScroll: true });
      else document.activeElement.blur();
    }
  }
  function openNavigation() {
    if (!compact.matches || app.hidden || dialog.open) return;
    closeSearch();
    dialog.showModal();
    revealAccount();
    menu.setAttribute('aria-expanded', 'true');
    document.documentElement.classList.add('navigation-open');
    close.focus({ preventScroll: true });
    onOpenNavigation();
  }
  function layout() {
    const focused = document.activeElement, editingSearch = search.contains(focused);
    closeNavigation();
    if (compact.matches) {
      header.append(search);
      dialog.append(sidebar);
      searchOpen ||= editingSearch;
      search.hidden = !searchOpen;
    } else {
      app.insertBefore(sidebar, main);
      searchHome.after(search);
      search.hidden = false;
    }
    searchToggle.setAttribute('aria-expanded', String(!search.hidden));
    revealAccount();
    if (editingSearch && !search.hidden) focused.focus({ preventScroll: true });
  }
  menu.addEventListener('click', openNavigation);
  close.addEventListener('click', closeNavigation);
  dialog.addEventListener('click', event => { if (event.target === dialog) closeNavigation(); });
  dialog.addEventListener('close', () => {
    menu.setAttribute('aria-expanded', String(dialog.open));
    document.documentElement.classList.toggle('navigation-open', dialog.open);
  });
  searchToggle.addEventListener('click', () => {
    if (searchOpen) closeSearch({ focus: true });
    else {
      searchOpen = true; search.hidden = false;
      searchToggle.setAttribute('aria-expanded', 'true');
      query.focus({ preventScroll: true });
    }
  });
  document.getElementById('search-close').addEventListener('click', () => closeSearch({ focus: true }));
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && !dialog.open && compact.matches && searchOpen) {
      event.preventDefault(); closeSearch({ focus: true });
    }
  });
  document.addEventListener('touchstart', event => {
    gesture = null;
    if (!compact.matches || app.hidden || event.touches.length !== 1 ||
      event.target.closest('input, textarea, select, [contenteditable], pre, .map-field')) return;
    const touch = event.touches[0];
    if (!dialog.open && touch.clientX > 24) return;
    if (dialog.open && !sidebar.contains(event.target)) return;
    gesture = { id: touch.identifier, x: touch.clientX, y: touch.clientY, closing: dialog.open, horizontal: false };
  }, { passive: true });
  document.addEventListener('touchmove', event => {
    if (!gesture) return;
    const touch = [...event.touches].find(item => item.identifier === gesture.id);
    if (!touch || event.touches.length !== 1) { gesture = null; return; }
    const dx = touch.clientX - gesture.x, dy = touch.clientY - gesture.y;
    if (!gesture.horizontal && Math.abs(dy) > 12 && Math.abs(dy) >= Math.abs(dx)) { gesture = null; return; }
    if (Math.abs(dx) > 16 && Math.abs(dx) > Math.abs(dy) * 1.5) gesture.horizontal = true;
    if (gesture.horizontal && event.cancelable) event.preventDefault();
  }, { passive: false });
  document.addEventListener('touchend', event => {
    if (!gesture) return;
    const start = gesture; gesture = null;
    const touch = [...event.changedTouches].find(item => item.identifier === start.id);
    if (!touch) return;
    const dx = touch.clientX - start.x, dy = touch.clientY - start.y;
    if (Math.abs(dx) < 60 || Math.abs(dx) < Math.abs(dy) * 1.5) return;
    if (start.closing && dx < 0) closeNavigation();
    else if (!start.closing && dx > 0) openNavigation();
  }, { passive: true });
  document.addEventListener('touchcancel', () => { gesture = null; }, { passive: true });
  compact.addEventListener('change', layout);
  new ResizeObserver(() => {
    measuredStyle(app, '--shell-top', `${header.getBoundingClientRect().height}px`);
  }).observe(header);
  layout();
  return {
    closeNavigation,
    reset() { closeNavigation(); closeSearch(); revealAccount(); },
  };
}
