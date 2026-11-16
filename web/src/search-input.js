export function bindSearchInput(input, search, { signal, delay = 250 } = {}) {
  let timer, composing = false;
  const cancel = () => { clearTimeout(timer); timer = null; };
  const schedule = () => {
    cancel();
    if (!composing) timer = setTimeout(() => search(input.value), delay);
  };
  input.addEventListener('compositionstart', () => { composing = true; cancel(); }, { signal });
  input.addEventListener('compositionend', () => { composing = false; schedule(); }, { signal });
  input.addEventListener('input', event => { if (!event.isComposing) schedule(); }, { signal });
  input.addEventListener('keydown', event => {
    if (event.key !== 'Enter' || composing || event.isComposing || event.keyCode === 229) return;
    event.preventDefault();
    cancel();
    search(input.value);
  }, { signal });
  signal?.addEventListener('abort', cancel, { once: true });
  return { cancel };
}
