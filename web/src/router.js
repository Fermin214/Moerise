export function createRouter({ beforeNavigate, onChange, isDirty, confirmLeave }) {
  const nativeIndex = () => {
    const value = window.navigation?.currentEntry?.index;
    return Number.isSafeInteger(value) && value >= 0 ? value : null;
  };
  let url = location.pathname + location.search;
  let key = history.state?.webKey || crypto.randomUUID();
  let chain = history.state?.webHistory || crypto.randomUUID();
  let index = nativeIndex() ?? (history.state?.webHistory ? history.state.webIndex : 0);
  let sequence = 0, restore = null, approved = null;
  let entry;
  function remember() { entry = { url: location.href, state: history.state }; }
  function stamp() {
    history.replaceState({ ...history.state, webKey: key, webIndex: index, webHistory: chain }, '');
    remember();
  }
  stamp();
  history.scrollRestoration = 'manual';
  function apply(target, kind) {
    beforeNavigate(false);
    if (target.index === null) { chain = crypto.randomUUID(); target.index = 0; }
    url = target.url; key = target.key; index = target.index; stamp();
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
      index: nativeIndex() ?? (history.state?.webHistory === chain ? history.state.webIndex : null),
    };
    // Native fragment entries belong to the mounted page, including back/forward.
    if (target.url === url && target.key === key) {
      beforeNavigate(true);
      if (target.index === null) { chain = crypto.randomUUID(); target.index = 0; }
      key = target.key; index = target.index;
      stamp();
      return;
    }
    if (approved !== null && approved === target.index) { approved = null; apply(target, 'return'); return; }
    if (isDirty() && target.index === null) {
      // Legacy History API cannot locate an unindexed entry. Recreate the source
      // entry before confirming, keeping the mounted form and its URL together.
      const source = entry, ticket = ++sequence;
      chain = crypto.randomUUID(); target.index = 0;
      history.replaceState({ ...history.state, webKey: target.key, webIndex: 0, webHistory: chain }, '');
      index = 1;
      history.pushState({ ...source.state, webKey: key, webIndex: index, webHistory: chain }, '', source.url);
      remember();
      confirmLeave().then(leave => {
        if (leave && ticket === sequence) { approved = 0; history.back(); }
      });
      return;
    }
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
      remember();
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
        webKey: nextKey, webIndex: nextIndex, webHistory: chain, ...(returnKey ? { returnKey } : {}),
      }, '', next);
      url = location.pathname + location.search; key = nextKey; index = nextIndex;
      remember();
      onChange(filtering ? 'filter' : 'enter');
    },
  };
}
