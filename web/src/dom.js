import { setText } from './i18n.js';

export function el(tag, className = '', text = '') {
  const node = document.createElement(tag);
  node.className = className;
  if (text) setText(node, text);
  return node;
}

export function button(text, className, action) {
  const node = el('button', className, text);
  node.type = 'button';
  node.addEventListener('click', action);
  return node;
}

export function icon(path, className = 'i') {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', className);
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  const line = document.createElementNS(svg.namespaceURI, 'path');
  line.setAttribute('d', path);
  svg.append(line);
  return svg;
}
