// Exercise live translations without navigating away from the state under test.
// The Settings browser flow covers the language preference control itself.
export async function returningBrowser(page) {
  await page.addInitScript(() => {
    try { if (!localStorage.getItem('moerise.privacy.v1')) localStorage.setItem('moerise.privacy.v1', 'off'); } catch {}
  });
}
export async function setUILanguage(page, value) {
  await page.evaluate(async language => {
    const script = document.querySelector('script[type="module"][src*="/app"]');
    const url = new URL(script.src);
    url.pathname = url.pathname.replace(/\/app(?=\.|$)/, '/i18n');
    const { setLanguage } = await import(url.href);
    setLanguage(language, { persist: true });
  }, value);
}
