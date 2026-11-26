import { attachmentURL, rasterImage } from './routes.js';
import { prepareImage } from './media.js';

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
    const target = { url, image: rasterImage(item.type), key: `${note.source_hash}:${url}` };
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

const tagBoundary = /[\s.,!?;:()[\]{}<>"'，。！？；：、（）【】《》「」『』“”‘’…*~`|]/u;
const protectedText = /\\[^\n]|(?:[A-Za-z][A-Za-z0-9+.-]*:\/\/|www\.)[^\s<>]+|<[^<>\n]*>|!?\[[^\[\]\n]*\](?:\([^()\n]*\)|\[[^\[\]\n]*\])?/gu;
const fenced = /^ {0,3}(`{3,}|~{3,})/;
const protectedLine = /^(?: {4}|\t| {0,3}(?:#{1,6}(?:\s|$)|\[[^\]]+]:))/;
const paragraphBoundary = /^(?:#{1,6}\s|>| {0,3}`{3}| {0,3}~{3}|\s*[-+*]\s|\s*\d+\.\s)/;

function fenceMarker(line) {
  const match = fenced.exec(line);
  if (match && (!match[1].startsWith('`') || !line.slice(match[0].length).includes('`'))) return match[1];
}

function tagSpans(text, tags) {
  if (!tags.length || !text.includes('#') || protectedLine.test(text)) return [];
  const protectedOffsets = new Uint8Array(text.length);
  for (const match of text.matchAll(protectedText)) protectedOffsets.fill(1, match.index, match.index + match[0].length);
  const spans = [];
  for (let start = text.indexOf('#'); start >= 0; start = text.indexOf('#', start + 1)) {
    if (protectedOffsets[start] || start && text[start - 1] !== '-' && !tagBoundary.test(text[start - 1])) continue;
    const tag = tags.find(name => {
      const end = start + name.length + 1;
      return text.startsWith(name, start + 1) && (end === text.length || tagBoundary.test(text[end])) &&
        !protectedOffsets.subarray(start, end).includes(1);
    });
    if (tag) spans.push({ start, end: start + tag.length + 1, tag });
  }
  return spans;
}

function removeTagLines(lines, tags) {
  if (!tags.length) return lines;
  const kept = lines.slice();
  for (let start = 0; start < lines.length;) {
    const marker = fenceMarker(lines[start]);
    if (marker) {
      const close = new RegExp(`^ {0,3}${marker[0]}{${marker.length},}\\s*$`);
      start++;
      while (start < lines.length && !close.test(lines[start])) start++;
      start++;
      continue;
    }
    let stop = start + 1;
    if (lines[start].trim() && !paragraphBoundary.test(lines[start])) {
      while (stop < lines.length && lines[stop].trim() && !paragraphBoundary.test(lines[stop])) stop++;
    }
    const spans = inlineSpans(lines.slice(start, stop).join('\n'), tags);
    let offset = 0, index = 0;
    for (let row = start; row < stop; row++) {
      const line = lines[row], next = offset + line.length + 1;
      let end = 0, onlyTags = true;
      while (index < spans.length && spans[index].start < next) {
        const span = spans[index++];
        if (line.slice(end, span.start - offset).trim()) onlyTags = false;
        end = span.end - offset;
      }
      if (end && onlyTags && !line.slice(end).trim()) kept[row] = null;
      offset = next;
    }
    start = stop;
  }
  const result = [];
  for (let index = 0; index < kept.length;) {
    if (kept[index]?.trim()) { result.push(kept[index++]); continue; }
    const start = index;
    let removed = false, blank;
    while (index < kept.length && !kept[index]?.trim()) {
      if (kept[index] === null) removed = true;
      else blank ??= kept[index];
      index++;
    }
    if (!removed) result.push(...kept.slice(start, index));
    else if (result.length && index < kept.length && blank !== undefined) result.push(blank);
  }
  return result;
}

function appendPlain(parent, text, start, end, spans, onTag, renderedTags) {
  let low = 0, high = spans.length;
  while (low < high) {
    const middle = (low + high) >>> 1;
    if (spans[middle].start < start) low = middle + 1;
    else high = middle;
  }
  for (let index = low; index < spans.length && spans[index].end <= end; index++) {
    const span = spans[index];
    parent.append(document.createTextNode(text.slice(start, span.start)));
    const control = document.createElement(onTag ? 'button' : 'span');
    control.className = 'inline-tag';
    control.textContent = `#${span.tag}`;
    if (onTag) { control.type = 'button'; control.addEventListener('click', () => onTag(span.tag)); }
    parent.append(control);
    renderedTags.add(span.tag);
    start = span.end;
  }
  parent.append(document.createTextNode(text.slice(start, end)));
}

function inlineMatch(text, i, next) {
  if (text[i] === '`' && !escapedAt(text, i)) {
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

function escapedAt(text, index) {
  let slash = index;
  while (slash > 0 && text[slash - 1] === '\\') slash--;
  return (index - slash) % 2 !== 0;
}

function codeSpans(text) {
  const nextRun = new Map(), matches = [], runs = [...text.matchAll(/`+/g)];
  const blankLines = [...text.matchAll(/\n[ \t]*\n/g)];
  let blank = blankLines.length - 1;
  for (let index = runs.length - 1; index >= 0; index--) {
    const run = runs[index];
    while (blank >= 0 && run.index < blankLines[blank].index) {
      nextRun.clear();
      blank--;
    }
    const opening = run.index + Number(escapedAt(text, run.index));
    const start = run.index + run[0].length;
    const size = start - opening;
    const stop = nextRun.get(size);
    if (stop !== undefined) matches.push({ opening, tag: 'code', start, stop, end: stop + size });
    nextRun.set(run[0].length, run.index);
  }
  const spans = [];
  let end = 0;
  for (const match of matches.reverse()) {
    if (match.opening < end) continue;
    spans.push(match);
    end = match.end;
  }
  return spans;
}

function inlineSpans(text, tags) {
  if (!tags.length || !text.includes('#')) return [];
  const spans = [];
  let offset = 0;
  const code = codeSpans(text);
  let protectedIndex = 0;
  for (const line of text.split('\n')) {
    for (const span of tagSpans(line, tags)) {
      const start = span.start + offset, end = span.end + offset;
      while (protectedIndex < code.length && code[protectedIndex].end <= start) protectedIndex++;
      if (code[protectedIndex]?.opening < end) continue;
      spans.push({ start, end, tag: span.tag });
    }
    offset += line.length + 1;
  }
  return spans;
}

function inline(parent, text, attachments, spans, onTag, renderedTags, depth = 0) {
  const append = (start, end) => appendPlain(parent, text, start, end, spans, onTag, renderedTags);
  if (depth > 8 || !/[`*_~\[]/.test(text)) { append(0, text.length); return; }
  const next = inlineIndex(text);
  const code = new Map(codeSpans(text).map(span => [span.opening, span]));
  let last = 0;
  for (let i = 0; i < text.length;) {
    const match = code.get(i) ?? inlineMatch(text, i, next);
    if (!match) { i++; continue; }
    append(last, i);
    const content = text.slice(match.start, match.stop);
    if (match.tag === 'code') {
      const code = document.createElement('code');
      code.textContent = content.replaceAll('\n', ' ');
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
        prepareImage(image, target.key);
        parent.append(image);
      } else {
        parent.append(document.createTextNode(text.slice(i, match.end)));
      }
    } else {
      const node = document.createElement(match.tag);
      const nested = spans.filter(span => span.start >= match.start && span.end <= match.stop)
        .map(span => ({ ...span, start: span.start - match.start, end: span.end - match.start }));
      inline(node, content, attachments, nested, onTag, renderedTags, depth + 1);
      parent.append(node);
    }
    i = last = match.end;
  }
  append(last, text.length);
}

export function renderMarkdown(text, note, { onTag, renderedTags = new Set() } = {}) {
  const attachments = attachmentLinks(note);
  const tags = [...new Set(note?.tags || [])].filter(tag => typeof tag === 'string' && tag && !tag.startsWith('#') && !/\s/u.test(tag))
    .sort((left, right) => right.length - left.length);
  const fragment = document.createDocumentFragment();
  const lines = removeTagLines(text.replace(/\r\n?/g, '\n').split('\n'), tags);
  for (let i = 0; i < lines.length;) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }
    const fence = fenceMarker(line);
    if (fence) {
      const content = [];
      const closing = new RegExp(`^ {0,3}${fence[0]}{${fence.length},}\\s*$`);
      i++;
      while (i < lines.length && !closing.test(lines[i])) content.push(lines[i++]);
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
        inline(li, next[2], attachments, inlineSpans(next[2], tags), onTag, renderedTags);
        list.append(li);
        i++;
      }
      if (list.children.length) fragment.append(list);
      continue;
    }
    const block = document.createElement(heading ? `h${heading[1].length}` : quote ? 'blockquote' : 'p');
    const content = [heading ? heading[2] : quote ? quote[1] : line];
    i++;
    if (quote) {
      while (i < lines.length && /^>/.test(lines[i])) content.push(lines[i++].replace(/^>[ \t]?/, ''));
    } else if (!heading) {
      while (i < lines.length && lines[i].trim() && !paragraphBoundary.test(lines[i])) content.push(lines[i++]);
    }
    const value = content.join('\n');
    if (!value.trim()) continue;
    inline(block, value, attachments, inlineSpans(value, heading ? [] : tags), onTag, renderedTags);
    fragment.append(block);
  }
  return fragment;
}
