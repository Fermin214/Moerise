import { el } from './dom.js';
import { setAttr } from './i18n.js';

export function createModal({ parent, className = '', label, canClose = () => true, onClose = () => {} }) {
  const dialog = el('dialog', className);
  setAttr(dialog, 'aria-label', label);
  let disposed = false;
  function finish() {
    if (disposed) return;
    disposed = true;
    dialog.remove();
    onClose();
  }
  dialog.addEventListener('cancel', event => { if (!canClose()) event.preventDefault(); });
  dialog.addEventListener('close', finish);
  return {
    dialog,
    show() {
      parent.append(dialog);
      dialog.showModal();
    },
    close(force = false) {
      if (disposed || (!force && !canClose())) return;
      if (dialog.open) dialog.close();
      finish();
    },
    destroy() {
      if (dialog.open) dialog.close();
      finish();
    },
  };
}
