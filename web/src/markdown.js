// Build DOM nodes rather than inserting HTML; note text is always untrusted.
export function safeLink(value) {
  if (!value || /[\u0000-\u0020\\]/.test(value) || value.startsWith('//')) return null;
  if (value.startsWith('/') || value.startsWith('#')) return value;
  try {
    const url = new URL(value);
    return ['http:', 'https:', 'mailto:'].includes(url.protocol) ? url.href : null;
  } catch { return null; }
}

function inline(parent, text, depth = 0) {
  if (depth > 8) { parent.append(document.createTextNode(text)); return; }
  const pattern = /(`+)([^`\n]+)\1|(!?)\[([^\]\n]*)\]\(([^)\s]+)\)|\*\*([^*\n]+)\*\*|__([^_\n]+)__|\*([^*\n]+)\*|~~([^~\n]+)~~/g;
  let last = 0;
  for (const match of text.matchAll(pattern)) {
    parent.append(document.createTextNode(text.slice(last, match.index)));
    if (match[1]) {
      const code = document.createElement('code');
      code.textContent = match[2];
      parent.append(code);
    } else if (match[4] !== undefined) {
      const url = safeLink(match[5]);
      if (url && !match[3]) {
        const link = document.createElement('a');
        link.href = url;
        link.rel = 'noopener noreferrer';
        if (/^https?:/.test(url)) link.target = '_blank';
        link.textContent = match[4];
        parent.append(link);
      } else {
        parent.append(document.createTextNode(match[0]));
      }
    } else {
      const node = document.createElement(match[6] || match[7] ? 'strong' : match[8] ? 'em' : 'del');
      inline(node, match[6] || match[7] || match[8] || match[9], depth + 1);
      parent.append(node);
    }
    last = match.index + match[0].length;
  }
  parent.append(document.createTextNode(text.slice(last)));
}

export function renderMarkdown(text) {
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
        inline(li, next[2]);
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
    inline(block, content.join('\n'));
    fragment.append(block);
  }
  return fragment;
}
