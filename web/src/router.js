export function createRouter({ beforeNavigate, onChange, isDirty, confirmLeave }) {
  let url = location.pathname + location.search;
  let key = history.state?.webKey || crypto.randomUUID();
  let index = history.state?.webIndex || 0;
  let sequence = 0, restore = null, approved = null;
  history.replaceState({ ...history.state, webKey: key, webIndex: index }, '');
  history.scrollRestoration = 'manual';
  function apply(target, kind) {
    beforeNavigate(false);
    url = target.url; key = target.key; index = target.index;
    onChange(kind);
  }
  window.addEventListener('popstate', () => {
    if (restore) {
      const resume = restore; restore = null; resume();
      return;
    }
    const targetURL = location.pathname + location.search;
    const target = {
      url: targetURL,
      key: history.state?.webKey || (targetURL === url ? key : crypto.randomUUID()),
      index: history.state?.webIndex ?? index + 1,
    };
    // Native fragment entries belong to the mounted page, including back/forward.
    if (target.url === url && target.key === key) {
      key = target.key; index = target.index;
      history.replaceState({ ...history.state, webKey: key, webIndex: index }, '');
      return;
    }
    if (approved === target.index) { approved = null; apply(target, 'return'); return; }
    const distance = index - target.index;
    if (isDirty() && distance) {
      const ticket = ++sequence;
      restore = async () => {
        if (await confirmLeave() && ticket === sequence) {
          approved = target.index;
          history.go(-distance);
        }
      };
      history.go(distance);
      return;
    }
    apply(target, 'return');
  });
  return {
    get url() { return url; },
    get key() { return key; },
    cancel() { sequence++; },
    replaceURL(next) {
      history.replaceState({ ...history.state }, '', next);
      url = location.pathname + location.search;
    },
    async navigate(next, { replace = false, returnKey = null } = {}) {
      if (next === url) { beforeNavigate(true); return; }
      const ticket = ++sequence;
      if (isDirty() && !(await confirmLeave())) return;
      if (ticket !== sequence) return;
      const path = new URL(next, location.origin).pathname;
      const filtering = path === new URL(url, location.origin).pathname && ['/', '/archived'].includes(path);
      beforeNavigate(true);
      const nextKey = replace ? key : crypto.randomUUID(), nextIndex = index + (replace ? 0 : 1);
      history[replace ? 'replaceState' : 'pushState']({
        webKey: nextKey, webIndex: nextIndex, ...(returnKey ? { returnKey } : {}),
      }, '', next);
      url = location.pathname + location.search; key = nextKey; index = nextIndex;
      onChange(filtering ? 'filter' : 'enter');
    },
  };
}
