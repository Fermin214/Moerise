import { renderMarkdown } from './markdown.js';

// Completed blocks stay mounted while only the unfinished tail changes.
export function createStreamRenderer(container) {
  container.dataset.userContent = '';
  let previous = '', buffer = '', scanned = 0, fence = null, tail = null, streaming = false;
  return {
    render(text, running) {
      if (!running) {
        container.replaceChildren(renderMarkdown(text, { attachments: [] }));
        previous = text; buffer = ''; scanned = 0; fence = null; tail = null; streaming = false;
        return;
      }
      if (!streaming || !text.startsWith(previous)) {
        container.replaceChildren(); previous = ''; buffer = ''; scanned = 0; fence = null;
        tail = document.createElement('div'); container.append(tail); streaming = true;
      }
      buffer += text.slice(previous.length); previous = text;
      let boundary = 0, end;
      while ((end = buffer.indexOf('\n', scanned)) !== -1) {
        const line = buffer.slice(scanned, end).replace(/\r$/, '');
        const marker = /^ {0,3}(`{3,}|~{3,})/.exec(line);
        if (fence) {
          if (new RegExp(`^ {0,3}${fence[0]}{${fence.length},}\\s*$`).test(line)) fence = null;
        } else if (marker) fence = marker[1];
        else if (!line.trim()) boundary = end + 1;
        scanned = end + 1;
      }
      if (boundary) {
        container.insertBefore(renderMarkdown(buffer.slice(0, boundary), { attachments: [] }), tail);
        buffer = buffer.slice(boundary); scanned -= boundary;
      }
      // One very long paragraph must not be reparsed for every batch of tokens.
      if (buffer.length <= 4096) tail.replaceChildren(renderMarkdown(buffer, { attachments: [] }));
      else tail.textContent = buffer;
    },
  };
}
