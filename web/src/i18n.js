import { english } from './en.js';

const preferenceKey = 'moerise.language';
const bindings = new WeakMap(), prepared = new WeakSet();
let language = 'zh-CN';

function supported(value) {
  if (/^zh(?:-|$)/i.test(value || '')) return 'zh-CN';
  if (/^en(?:-|$)/i.test(value || '')) return 'en';
  return null;
}

export function detectLanguage(languages = []) {
  return languages.map(supported).find(Boolean) || 'en';
}

export const locale = () => language;
export const resolveText = value => String(typeof value === 'function' ? value() : value ?? '');

export function t(key, values = {}) {
  const template = language === 'en' && Object.hasOwn(english, key) ? english[key] : key;
  const params = Object.fromEntries(Object.entries(values).map(([name, value]) => [name, resolveText(value)]));
  return (typeof template === 'function' ? template(params) : template)
    .replace(/\{(\w+)\}/g, (match, name) => params[name] ?? match);
}

// Deferred messages keep visible text in sync without remounting private forms.
export function l(key, values) {
  const message = () => t(key, values);
  message.toString = message;
  return message;
}

function bind(node, property, value) {
  const properties = bindings.get(node) || new Map();
  if (typeof value === 'function') properties.set(property, value);
  else properties.delete(property);
  if (properties.size) bindings.set(node, properties);
  else bindings.delete(node);
  update(node, property, value);
}

function update(node, property, value) {
  const text = resolveText(value);
  if (property === 'text') {
    if (node.data !== text) node.data = text;
  } else if (node.getAttribute(property) !== text) node.setAttribute(property, text);
}

export function setText(node, value) {
  node.replaceChildren();
  const text = node.ownerDocument.createTextNode('');
  node.append(text); bind(text, 'text', value);
  return value;
}

export function setAttr(node, name, value) {
  bind(node, name, value);
  return value;
}

function translateNode(node) {
  if (node.nodeType === 1 && !prepared.has(node)) {
    prepared.add(node);
    if (node.hasAttribute('data-i18n')) {
      // Bind only the caption, keeping icons and keyboard shortcuts intact.
      const text = [...node.childNodes].find(child => child.nodeType === 3 && child.data.trim()) ||
        node.insertBefore(node.ownerDocument.createTextNode(''), node.firstChild);
      bind(text, 'text', l(node.getAttribute('data-i18n')));
    }
    for (const name of ['aria-label', 'aria-description', 'placeholder', 'title']) {
      const key = node.getAttribute(`data-i18n-${name}`);
      if (key) setAttr(node, name, l(key));
    }
  }
  for (const [property, value] of bindings.get(node) || []) update(node, property, value);
}

export function localize(root) {
  translateNode(root);
  const walker = root.ownerDocument.createTreeWalker(root, 5);
  while (walker.nextNode()) translateNode(walker.currentNode);
}

export function setLanguage(value, { persist = false } = {}) {
  language = supported(value) || 'en';
  if (persist) {
    try { localStorage.setItem(preferenceKey, language); } catch { /* Storage can be disabled. */ }
  }
  if (typeof document !== 'undefined') {
    document.documentElement.lang = language;
    localize(document.documentElement);
    document.querySelectorAll('[data-language]').forEach(select => { select.value = language; });
  }
}

export function initI18n() {
  let saved;
  try { saved = localStorage.getItem(preferenceKey); } catch { /* Use the browser preference. */ }
  setLanguage(supported(saved) || detectLanguage(navigator.languages || [navigator.language]));
  document.querySelectorAll('[data-language]').forEach(select => {
    select.addEventListener('change', () => setLanguage(select.value, { persist: true }));
  });
  // Detached editors can be reattached after login; translate their bindings too.
  new MutationObserver(records => {
    for (const record of records) for (const node of record.addedNodes) {
      if (node.isConnected && [1, 3].includes(node.nodeType)) localize(node);
    }
  }).observe(document.body, { childList: true, subtree: true });
}

export function monthLabel(year, month, compact = false) {
  return () => language === 'zh-CN' ? `${year}${compact ? '年' : ' 年 '}${month}${compact ? '月' : ' 月'}` :
    new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric', timeZone: 'UTC' })
      .format(new Date(Date.UTC(year, month - 1, 1)));
}

export const weekdays = ['一', '二', '三', '四', '五', '六', '日'].map(key => l(key));
