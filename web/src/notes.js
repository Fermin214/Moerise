import { l, setAttr, setText } from './i18n.js';
import { el, button, icon } from './dom.js';
import { attachmentURL, rasterImage, noteID } from './routes.js';
import { renderMarkdown } from './markdown.js';
import { bindMenu } from './menus.js';
import { prepareImage } from './media.js';
import { snackbar } from './snackbar.js';

export function validNote(note) {
  return note && noteID(note.name) && typeof note.content === 'string' &&
    ['NORMAL', 'ARCHIVED'].includes(note.state) && typeof note.source_hash === 'string' && !!note.source_hash;
}

export function compareNotes(a, b) {
  return Date.parse(b.create_time) - Date.parse(a.create_time) || (a.name < b.name ? 1 : a.name > b.name ? -1 : 0);
}

export function renderAttachments(note, referenced = new Set()) {
  const box = el('div', 'attachment-list');
  for (const item of note.attachments || []) {
    const url = attachmentURL(note, item);
    if (referenced.has(url) && rasterImage(item.type)) continue;
    if (!url) { box.append(el('span', 'attachment sub', l("{0}，暂时无法打开", { 0: item.filename || l("附件") }))); continue; }
    const link = el('a', rasterImage(item.type) ? 'image-link' : 'attachment');
    link.href = url;
    if (rasterImage(item.type)) {
      link.target = '_blank'; link.rel = 'noopener noreferrer';
      const image = el('img');
      image.src = url; setAttr(image, 'alt', item.filename || l("图片"));
      prepareImage(image, `${note.source_hash}:${url}`);
      image.addEventListener('error', () => {
        image.remove(); link.className = 'attachment'; setText(link, l("{0}，打开原图", { 0: item.filename || l("图片") }));
      }, { once: true });
      link.append(image);
    } else {
      link.download = item.filename || 'attachment';
      setText(link, item.filename || l("下载附件"));
      if (Number.isFinite(item.size)) link.append(el('span', 'sub', item.size < 1024 ? `${item.size} B` : `${Math.ceil(item.size / 1024)} KiB`));
    }
    box.append(link);
  }
  return box;
}

function noteBody(note, onTag, renderedTags) {
  const content = el('div', 'note-content');
  content.dataset.userContent = '';
  content.append(renderMarkdown(note.content, note, { onTag, renderedTags }));
  return content;
}

function appendTags(container, tags, renderedTags, onTag) {
  for (const tag of new Set(tags || [])) {
    if (!renderedTags.has(tag)) container.append(onTag ? button(`#${tag}`, 'tag', () => onTag(tag)) : el('span', 'tag', `#${tag}`));
  }
}

function legacyCopy(content) {
  const previousFocus = document.activeElement;
  const field = document.createElement('textarea');
  field.value = content;
  field.readOnly = true;
  field.className = 'clipboard-field';
  const writeOriginal = event => {
    if (!event.clipboardData) return;
    event.clipboardData.setData('text/plain', content);
    event.preventDefault();
  };
  field.addEventListener('copy', writeOriginal);
  document.body.append(field);
  field.select();
  try { return document.execCommand('copy'); }
  finally { field.remove(); previousFocus?.focus({ preventScroll: true }); }
}

function copyAction(content) {
  return async () => {
    let copied = false;
    try {
      await navigator.clipboard.writeText(content);
      copied = true;
    } catch {
      try { copied = legacyCopy(content); } catch { copied = false; }
    }
    snackbar(copied ? l("已复制") : l("复制失败"));
  };
}

const foldReaders = new WeakMap();
const bodyObserver = new ResizeObserver(entries => {
  for (const { target, borderBoxSize } of entries) {
    if (target.isConnected) foldReaders.get(target)?.(borderBoxSize?.[0]?.blockSize ?? target.offsetHeight);
  }
});

export function observeFoldedBodies(list) {
  bodyObserver.disconnect();
  for (const body of list.querySelectorAll('.note-fold-content')) bodyObserver.observe(body);
}

function foldBody(card, body, expandedNotes) {
  const preview = el('div', 'note-fold-body');
  const toggle = button('', 'ghost note-fold-toggle', () => {
    const expanded = expandedNotes.has(card.dataset.name);
    const header = document.querySelector('.mobile-header');
    const readableTop = () => Math.max(0, header?.getBoundingClientRect().bottom || 0);
    const above = expanded && card.getBoundingClientRect().top < readableTop();
    if (expanded) expandedNotes.delete(card.dataset.name);
    else expandedNotes.add(card.dataset.name);
    update();
    if (above && card.getBoundingClientRect().top < readableTop()) {
      const top = window.scrollY + card.getBoundingClientRect().top - readableTop();
      window.scrollTo({ top, behavior: 'instant' });
    }
  });
  let long = false;
  function update() {
    const expanded = expandedNotes.has(card.dataset.name);
    preview.classList.toggle('note-fold-collapsed', long && !expanded);
    toggle.hidden = !long;
    toggle.setAttribute('aria-expanded', String(expanded));
    setText(toggle, expanded ? l("收起") : l("展开"));
  }
  foldReaders.set(body, height => {
    if (!height || card.skipped) return;
    long = height > 380;
    update();
  });
  card.addEventListener('contentvisibilityautostatechange', event => {
    if (!event.skipped && card.isConnected) {
      requestAnimationFrame(() => {
        if (card.isConnected && !card.skipped) foldReaders.get(body)?.(body.offsetHeight);
      });
    }
  });
  preview.addEventListener('focusin', event => {
    if (!preview.classList.contains('note-fold-collapsed') ||
        event.target.getBoundingClientRect().bottom <= preview.getBoundingClientRect().bottom) return;
    expandedNotes.add(card.dataset.name);
    update();
  });
  preview.append(body);
  update();
  return [preview, toggle];
}

export function renderNoteCard(note, { detail, formatTime, href, onTag, actions, pending, expandedNotes }) {
  const card = el('article', `card${detail ? ' note' : ''}`);
  card.dataset.name = note.name;
  card.dataset.sourceHash = note.source_hash;
  const header = el('div', 'hd'), time = el('time', '', formatTime(note.create_time));
  const copy = [l("复制"), copyAction(note.content), ''];
  const commands = [...actions];
  const destructive = commands.findIndex(([, , cls]) => cls === 'dg');
  commands.splice(destructive < 0 ? commands.length : destructive, 0, copy);
  if (Number.isFinite(Date.parse(note.create_time))) time.dateTime = note.create_time;
  if (detail) header.append(time);
  else {
    const link = el('a', 'dlink');
    link.href = href; link.dataset.route = ''; link.append(time); header.append(link);
    const wrap = el('div', 'mw'), menu = el('div', 'menu'), more = button('', 'btn s more', () => {});
    more.append(icon('M5 12h.01M12 12h.01M19 12h.01', 'i dots'));
    setAttr(more, 'aria-label', l("更多操作"));
    menu.hidden = true;
    for (const command of commands) {
      const [label, action, cls] = command;
      const control = button(label, `mi ${cls}`, action);
      control.disabled = command !== copy && pending; menu.append(control);
    }
    wrap.append(more, menu); header.append(wrap);
    bindMenu(more, menu);
  }
  const tags = el('div', 'note-tags');
  const renderedTags = new Set();
  const body = noteBody(note, onTag, renderedTags), referenced = new Set([...body.querySelectorAll('img')].map(image => image.getAttribute('src')));
  appendTags(tags, note.tags, renderedTags, onTag);
  const attachments = renderAttachments(note, referenced);
  card.append(header);
  if (expandedNotes) {
    const content = el('div', 'note-fold-content');
    content.append(body, attachments, tags);
    card.append(...foldBody(card, content, expandedNotes));
  } else card.append(body, attachments, tags);
  if (detail) {
    const bar = el('div', 'note-actions');
    for (const command of commands) {
      const [label, action, cls] = command;
      const control = button(label, `ghost ${cls}`, action);
      control.disabled = command !== copy && pending; bar.append(control);
    }
    card.append(bar);
  }
  return card;
}

export function renderReadingCard(note, { review = false, className = 'card big discovery-note', formatTime, openNote, onTag, bottomTags = false }) {
  const card = el('article', `${className}${review ? ' rv' : ''}`);
  card.dataset.name = note.name;
  const header = el('div', 'hd'), time = el('time', '', formatTime(note.create_time));
  time.dateTime = note.create_time;
  const open = button('', 'btn s', () => openNote(note));
  setAttr(open, 'aria-label', l("打开笔记")); setAttr(open, 'title', l("打开笔记"));
  open.append(icon('M9 6h9v9M18 6L7 17'));
  const metadata = el('div', 'discovery-meta');
  if (review && Number.isFinite(Date.parse(note.create_time))) {
    const days = Math.max(0, Math.floor((Date.now() - Date.parse(note.create_time)) / 86_400_000));
    header.append(el('span', 'pill', days ? l("{0} 天前", { 0: days }) : l("今天")));
    if (!bottomTags) metadata.append(time);
  } else header.append(time);
  const renderedTags = new Set();
  const body = noteBody(note, onTag, renderedTags), referenced = new Set([...body.querySelectorAll('img')].map(image => image.getAttribute('src')));
  const tags = bottomTags ? el('div', 'note-tags') : metadata;
  appendTags(tags, note.tags, renderedTags, onTag);
  metadata.append(open); header.append(metadata);
  card.append(header, body, renderAttachments(note, referenced));
  if (bottomTags && tags.childElementCount) card.append(tags);
  return card;
}
