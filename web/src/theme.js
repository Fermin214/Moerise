const key = 'moerise-theme';
const systemDark = () => matchMedia('(prefers-color-scheme: dark)');
const normalize = value => value === 'light' || value === 'dark' ? value : 'auto';

export const themePreference = () => normalize(document.documentElement.dataset.themePreference);
export const resolvedTheme = () => {
  const preference = themePreference();
  return preference === 'auto' ? (systemDark().matches ? 'dark' : 'light') : preference;
};

export function setTheme(value, { persist = false } = {}) {
  const preference = normalize(value);
  const root = document.documentElement;
  root.dataset.themePreference = preference;
  const resolved = resolvedTheme();
  root.dataset.theme = document.querySelector('meta[name="color-scheme"]').content = resolved;
  if (persist) {
    try {
      if (preference === 'auto') localStorage.removeItem(key);
      else localStorage.setItem(key, preference);
    } catch {}
  }
}

export function initTheme() {
  setTheme(themePreference());
  systemDark().addEventListener('change', () => {
    if (themePreference() === 'auto') setTheme('auto');
  });
  window.addEventListener('storage', event => {
    let storage;
    try { storage = localStorage; } catch { return; }
    if (event.storageArea === storage && (event.key === key || event.key === null)) {
      setTheme(event.newValue);
      const settings = document.getElementById('settings-theme');
      if (settings) settings.value = themePreference();
    }
  });
}
