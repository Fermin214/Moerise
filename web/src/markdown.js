import { attachmentURL, rasterImage } from './routes.js';

// Build DOM nodes rather than inserting HTML; note text is always untrusted.
export function safeLink(value) {
  if (!value || /[\u0000-\u0020\\]/.test(value) || value.startsWith('//')) return null;
  if (value.startsWith('/') || value.startsWith('#')) return value;
  try {
    const url = new URL(value);
    return ['http:', 'https:', 'mailto:'].includes(url.protocol) ? url.href : null;
  } catch { return null; }
}

function attachmentLinks(note) {
  const names = new Map(), urls = new Map();
  if (typeof note?.name !== 'string') return { names, urls };
  for (const item of note?.attachments || []) {
    const url = attachmentURL(note, item);
    if (!url) continue;
    const target = { url, image: rasterImage(item.type) };
    names.set(item.name, target);
    urls.set(url, target);
  }
  return { names, urls };
}

function linkTarget(value, attachments) {
  const url = safeLink(value);
  if (!url) return null;
  if (!url.startsWith('/')) return { url };
  let path, rawPath;
  try {
    rawPath = decodeURIComponent(url.split(/[?#]/, 1)[0]);
    path = decodeURIComponent(new URL(url, 'https://moerise.invalid').pathname);
  }
  catch { return null; }
  const native = /^\/file\/(attachments\/[A-Za-z0-9_-]+)(?:\/[^/]+)?$/.exec(path);
  if (native) return attachments.names.get(native[1]) || null;
  if ([path, rawPath].some(value => value === '/file/attachments' || value.startsWith('/file/attachments/'))) return null;
  if ([path, rawPath].some(value => value.startsWith('/api/app/v1/notes/') && value.includes('/attachments/'))) {
    return attachments.urls.get(path) || null;
  }
  return { url };
}

// 每个位置只计算一次后继分隔符，未闭合标记不会反复扫描剩余正文。
function inlineIndex(text) {
  const delimiters = ['`', ']', '*', '_', '~', '\n'];
  const next = {};
  for (const key of [...delimiters, 'stop', 'ticks']) {
    next[key] = new Uint32Array(text.length + 1);
    next[key][text.length] = text.length;
  }
  for (let i = text.length - 1; i >= 0; i--) {
    for (const key of delimiters) next[key][i] = text[i] === key ? i : next[key][i + 1];
    next.stop[i] = /[)\s]/.test(text[i]) ? i : next.stop[i + 1];
    next.ticks[i] = text[i] === '`' && text[i + 1] === '`' ? next.ticks[i + 1] : i + 1;
  }
  return next;
}

const emphasis = [['**', 'strong'], ['__', 'strong'], ['*', 'em'], ['~~', 'del']];

function inlineMatch(text, i, next) {
  if (text[i] === '`') {
    const start = next.ticks[i], stop = next['`'][start];
    if (start < stop && stop < next['\n'][start] && next.ticks[stop] - stop >= start - i) {
      return { tag: 'code', start, stop, end: stop + start - i };
    }
  }
  const image = text[i] === '!' && text[i + 1] === '[';
  if (text[i] === '[' || image) {
    const start = i + (image ? 2 : 1), stop = next[']'][start];
    if (stop < text.length && stop < next['\n'][start] && text[stop + 1] === '(') {
      const urlStart = stop + 2, urlStop = next.stop[urlStart];
      if (urlStart < urlStop && text[urlStop] === ')') {
        return { tag: 'a', start, stop, end: urlStop + 1, image, urlStart, urlStop };
      }
    }
  }
  for (const [marker, tag] of emphasis) {
    if (!text.startsWith(marker, i)) continue;
    const start = i + marker.length;
    if (start >= text.length) continue;
    const stop = next[marker[0]][start];
    if (start < stop && stop < next['\n'][start] && text.startsWith(marker, stop)) {
      return { tag, start, stop, end: stop + marker.length };
    }
  }
  return null;
}

function inline(parent, text, attachments, depth = 0) {
  if (depth > 8) { parent.append(document.createTextNode(text)); return; }
  const next = inlineIndex(text);
  let last = 0;
  for (let i = 0; i < text.length;) {
    const match = inlineMatch(text, i, next);
    if (!match) { i++; continue; }
    parent.append(document.createTextNode(text.slice(last, i)));
    const content = text.slice(match.start, match.stop);
    if (match.tag === 'code') {
      const code = document.createElement('code');
      code.textContent = content;
      parent.append(code);
    } else if (match.tag === 'a') {
      const target = linkTarget(text.slice(match.urlStart, match.urlStop), attachments);
      if (target && !match.image) {
        const link = document.createElement('a');
        link.href = target.url;
        link.rel = 'noopener noreferrer';
        if (/^https?:/.test(target.url)) link.target = '_blank';
        link.textContent = content;
        parent.append(link);
      } else if (target?.image && match.image) {
        const image = document.createElement('img');
        image.src = target.url;
        image.alt = content;
        image.loading = 'lazy';
        parent.append(image);
      } else {
        parent.append(document.createTextNode(text.slice(i, match.end)));
      }
    } else {
      const node = document.createElement(match.tag);
      inline(node, content, attachments, depth + 1);
      parent.append(node);
    }
    i = last = match.end;
  }
  parent.append(document.createTextNode(text.slice(last)));
}

export function renderMarkdown(text, note) {
  const attachments = attachmentLinks(note);
  const fragment = document.createDocumentFragment();
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  for (let i = 0; i < lines.length;) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }
    const fence = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (fence) {
      const content = [];
      i++;
      while (i < lines.length && !new RegExp(`^ {0,3}${fence[1][0]}{${fence[1].length},}\\s*$`).test(lines[i])) content.push(lines[i++]);
      i++;
      const pre = document.createElement('pre');
      const code = document.createElement('code');
      code.textContent = content.join('\n');
      pre.append(code);
      fragment.append(pre);
      continue;
    }
    const heading = /^(#{1,6})\s+(.+)$/.exec(line);
    const quote = /^>\s?(.*)$/.exec(line);
    const item = /^\s*(?:([-+*])|\d+\.)\s+(.+)$/.exec(line);
    if (item) {
      const list = document.createElement(item[1] ? 'ul' : 'ol');
      while (i < lines.length) {
        const next = /^\s*(?:([-+*])|\d+\.)\s+(.+)$/.exec(lines[i]);
        if (!next || !!next[1] !== !!item[1]) break;
        const li = document.createElement('li');
        inline(li, next[2], attachments);
        list.append(li);
        i++;
      }
      fragment.append(list);
      continue;
    }
    const block = document.createElement(heading ? `h${heading[1].length}` : quote ? 'blockquote' : 'p');
    const content = [heading ? heading[2] : quote ? quote[1] : line];
    i++;
    if (!heading && !quote) {
      while (i < lines.length && lines[i].trim() && !/^(?:#{1,6}\s|>| {0,3}`{3}| {0,3}~{3}|\s*[-+*]\s|\s*\d+\.\s)/.test(lines[i])) content.push(lines[i++]);
    }
    inline(block, content.join('\n'), attachments);
    fragment.append(block);
  }
  return fragment;
}
