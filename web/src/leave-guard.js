import { l } from './i18n.js';
import { el, button } from './dom.js';
import { createModal } from './modal.js';

export function createLeaveGuard(isDirty) {
  let listening = false, pending = null;
  const warn = event => {
    if (!isDirty()) return;
    event.preventDefault();
    event.returnValue = '';
  };
  function update() {
    const dirty = !!isDirty();
    if (dirty === listening) return;
    listening = dirty;
    window[dirty ? 'addEventListener' : 'removeEventListener']('beforeunload', warn);
  }
  for (const event of ['input', 'change', 'submit']) {
    document.addEventListener(event, () => queueMicrotask(update));
  }
  return {
    update,
    cancel() { pending?.close(); },
    confirm(dirty = isDirty()) {
      if (!dirty) return Promise.resolve(true);
      if (pending) return pending.promise;
      let resolve, discard = false;
      const promise = new Promise(done => { resolve = done; });
      const modal = createModal({
        parent: document.body, className: 'card leave-dialog', label: l("放弃未保存的修改？"),
        onClose: () => { pending = null; resolve(discard); },
      });
      const title = el('h2', '', l("放弃未保存的修改？"));
      const actions = el('div', 'editor-actions');
      const keep = button(l("继续编辑"), 'pbtn', () => modal.close());
      keep.autofocus = true;
      actions.append(keep, button(l("放弃修改"), 'danger', () => { discard = true; modal.close(); }));
      modal.dialog.append(title, actions);
      pending = { promise, close: () => modal.close() };
      modal.show();
      return promise;
    },
  };
}
