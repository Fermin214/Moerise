(() => {
  let stored;
  try { stored = localStorage.getItem('moerise-theme'); } catch {}
  const preference = stored === 'light' || stored === 'dark' ? stored : 'auto';
  const resolved = preference === 'auto' ? (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light') : preference;
  document.documentElement.dataset.themePreference = preference;
  document.documentElement.dataset.theme = resolved;
  document.querySelector('meta[name="color-scheme"]').content = resolved;
})();
