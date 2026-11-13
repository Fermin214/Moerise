export function initShell() {
  const app = document.getElementById('app'), main = document.getElementById('main');
  const sidebar = document.getElementById('sidebar'), dialog = document.getElementById('navigation-dialog');
  const header = document.getElementById('mobile-header'), menu = document.getElementById('navigation-toggle');
  const close = document.getElementById('navigation-close'), search = document.getElementById('search-panel');
  const searchToggle = document.getElementById('search-toggle'), query = document.getElementById('q');
  const searchHome = document.createComment('search');
  search.before(searchHome);
  const compact = matchMedia('(max-width:760px), (max-width:1024px) and (pointer:coarse)');
  let searchOpen = false, gesture = null;

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
    menu.setAttribute('aria-expanded', 'true');
    document.documentElement.classList.add('navigation-open');
    close.focus({ preventScroll: true });
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
    if (editingSearch && !search.hidden) focused.focus({ preventScroll: true });
  }
  menu.addEventListener('click', openNavigation);
  close.addEventListener('click', closeNavigation);
  dialog.addEventListener('click', event => { if (event.target === dialog) closeNavigation(); });
  dialog.addEventListener('close', () => {
    menu.setAttribute('aria-expanded', String(dialog.open));
    document.documentElement.classList.toggle('navigation-open', dialog.open);
  });
  dialog.addEventListener('keydown', event => {
    if (event.key !== 'Tab') return;
    const controls = [...dialog.querySelectorAll('a[href], button, input, select, textarea, [tabindex]')]
      .filter(node => node.tabIndex >= 0 && !node.disabled && node.getClientRects().length);
    const first = controls[0], last = controls.at(-1);
    const target = event.shiftKey && document.activeElement === first ? last :
      !event.shiftKey && document.activeElement === last ? first : null;
    if (target) { event.preventDefault(); target.focus(); }
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
    app.style.setProperty('--shell-top', `${header.getBoundingClientRect().height}px`);
  }).observe(header);
  layout();
  return {
    closeNavigation,
    reset() { closeNavigation(); closeSearch(); },
  };
}
