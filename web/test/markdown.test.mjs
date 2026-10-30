import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { performance } from 'node:perf_hooks';
import { renderMarkdown, safeLink } from '../src/markdown.js';

class Node {
  constructor(tag, text = '') { this.tag = tag; this.children = []; this.text = text; }
  append(...nodes) { this.children.push(...nodes); }
  set textContent(text) { this.children = []; this.text = text; }
  get textContent() { return this.text + this.children.map(node => node.textContent).join(''); }
}

const previousDocument = globalThis.document;
before(() => {
  globalThis.document = {
    createElement: tag => new Node(tag),
    createTextNode: text => new Node('#text', text),
    createDocumentFragment: () => new Node('#fragment'),
  };
});
after(() => {
  if (previousDocument === undefined) delete globalThis.document;
  else globalThis.document = previousDocument;
});

function elements(node, tag) {
  return [...(node.tag === tag ? [node] : []), ...node.children.flatMap(child => elements(child, tag))];
}

const note = {
  name: 'memos/parent',
  attachments: [
    { name: 'attachments/a', type: 'application/pdf', content_url: '/api/app/v1/notes/parent/attachments/a/content' },
    { name: 'attachments/pic', type: 'image/png', content_url: '/api/app/v1/notes/parent/attachments/pic/content' },
    { name: 'attachments/svg', type: 'image/svg+xml', content_url: '/api/app/v1/notes/parent/attachments/svg/content' },
    { name: 'attachments/other', type: 'image/png', content_url: '/api/app/v1/notes/another/attachments/other/content' },
    { name: 'attachments/external', type: 'image/png', content_url: 'https://untrusted.test/image.png' },
  ],
};

test('preserves supported Markdown blocks, inline delimiters and literal untrusted content', () => {
  const result = renderMarkdown([
    '# Heading **bold**', '', '> quote *em*', '', '- __strong__', '- ~~deleted~~', '',
    '1. ordered', '2. ``code``', '', '```js', '<script>raw()</script>', '```', '',
    '**outer ~~inner~~** [site](https://example.test/a?q=1) [mail](mailto:a@example.test)',
    '[local](/notes/abc) [anchor](#section) ![remote](https://example.test/image.png)',
    '<script>untrusted()</script> [bad](javascript:alert(1))',
  ].join('\r\n'));
  assert.equal(elements(result, 'h1')[0].textContent, 'Heading bold');
  assert.equal(elements(result, 'blockquote')[0].textContent, 'quote em');
  assert.equal(elements(result, 'ul')[0].textContent, 'strongdeleted');
  assert.equal(elements(result, 'ol')[0].textContent, 'orderedcode');
  assert.equal(elements(result, 'pre')[0].textContent, '<script>raw()</script>');
  assert.deepEqual(elements(result, 'strong').map(node => node.textContent), ['bold', 'strong', 'outer inner']);
  assert.deepEqual(elements(result, 'del').map(node => node.textContent), ['deleted', 'inner']);
  assert.equal(elements(result, 'em')[0].textContent, 'em');
  assert.deepEqual(elements(result, 'code').map(node => node.textContent), ['code', '<script>raw()</script>']);
  const links = elements(result, 'a');
  assert.deepEqual(links.map(node => node.href), ['https://example.test/a?q=1', 'mailto:a@example.test', '/notes/abc', '#section']);
  assert.equal(links[0].target, '_blank');
  assert.ok(links.every(link => link.rel === 'noopener noreferrer'));
  assert.equal(elements(result, 'img').length, 0);
  assert.equal(elements(result, 'script').length, 0);
  assert.match(result.textContent, /!\[remote\]\(https:\/\/example\.test\/image\.png\)/);
  assert.match(result.textContent, /\[bad\]\(javascript:alert\(1\)\)/);
});

test('resolves native links and raster images through the parent attachment map in every block', () => {
  const result = renderMarkdown([
    '# [资料](/file/attachments/a/report.pdf)',
    '> ![图片](/file/attachments/pic/%E5%9B%BE%E7%89%87.png)',
    '- **[nested](/file/attachments/a/report.pdf?download=1)**',
    '',
    '[image link](/file/attachments/pic/image.png)',
    '[protected](/api/app/v1/notes/parent/attachments/a/content)',
    '![protected image](/api/app/v1/notes/parent/attachments/pic/content)',
    '`[code](/file/attachments/a/report.pdf)`',
  ].join('\n'), note);
  assert.deepEqual(elements(result, 'a').map(node => node.href), [
    note.attachments[0].content_url, note.attachments[0].content_url,
    note.attachments[1].content_url, note.attachments[0].content_url,
  ]);
  const images = elements(result, 'img');
  assert.deepEqual(images.map(node => node.src), [note.attachments[1].content_url, note.attachments[1].content_url]);
  assert.deepEqual(images.map(node => node.alt), ['图片', 'protected image']);
  assert.ok(images.every(image => image.loading === 'lazy'));
  assert.equal(elements(result, 'code')[0].textContent, '[code](/file/attachments/a/report.pdf)');
});

test('unmapped or invalid local attachments stay literal and cannot create links or image requests', () => {
  const text = [
    '[missing](/file/attachments/missing/report.pdf)',
    '![other](/file/attachments/other/private.png)',
    '[external](/file/attachments/external/image.png)',
    '![svg](/file/attachments/svg/image.svg)',
    '![pdf](/file/attachments/a/report.pdf)',
    '[foreign](/api/app/v1/notes/another/attachments/a/content)',
    '[unknown](/api/app/v1/notes/parent/attachments/missing/content)',
    '[malformed](/file/attachments/a/extra/file.pdf)',
    '[traversal](/file/attachments/missing/../../settings)',
    '[encoded traversal](/file/attachments/missing/%2e%2e/%2e%2e/settings)',
    '[encoded missing](/file%2Fattachments%2Fmissing/file.pdf)',
    '![remote](https://example.test/image.png)',
    '[unsafe](javascript:alert(1))',
  ].join(' ');
  const result = renderMarkdown(text, note);
  assert.equal(result.textContent, text);
  assert.equal(elements(result, 'a').length, 0);
  assert.equal(elements(result, 'img').length, 0);
  for (const parent of [undefined, { name: 'memos/parent' }, { ...note, name: 'memos/foreign' }]) {
    const raw = '[资料](/file/attachments/a/report.pdf) ![图片](/file/attachments/pic/image.png)';
    const withoutMap = renderMarkdown(raw, parent);
    assert.equal(withoutMap.textContent, raw);
    assert.equal(elements(withoutMap, 'a').length + elements(withoutMap, 'img').length, 0);
  }
  const unnamed = renderMarkdown('[missing](/file/attachments/a/report.pdf)', {
    attachments: [{ name: 'attachments/a', content_url: '/api/app/v1/notes/undefined/attachments/a/content' }],
  });
  assert.equal(elements(unnamed, 'a').length, 0);
  for (const value of ['javascript:alert(1)', 'data:text/html,x', '//evil.test', '/\\evil.test', 'java\nscript:x', 'java%73cript:x']) {
    assert.equal(safeLink(value), null, value);
  }
});

test('delimiter edge cases retain the original inline subset', () => {
  const cases = [
    ['[[label](https://example.test)', '[label', 'a'],
    ['***bold**', '*bold', 'strong'],
    ['**em*', '*em', 'em'],
    ['``code`', '`code', 'code'],
    ['`code``', 'code`', 'code'],
    ['*** ___ ~~~ []() ` `', '*** ___ ~~~ []()  ', 'code'],
    ['[label\ntext](https://example.test)', '[label\ntext](https://example.test)', null],
    ['[bad](https://example.test/a b)', '[bad](https://example.test/a b)', null],
  ];
  for (const [input, output, tag] of cases) {
    const result = renderMarkdown(input);
    assert.equal(result.textContent, output, input);
    if (tag) assert.equal(elements(result, tag).length, 1, input);
    else assert.equal(elements(result, 'a').length, 0, input);
  }
});

test('64 KiB unclosed inline inputs render within a linear scan budget', { timeout: 5_000 }, () => {
  renderMarkdown('[warmup](https://example.test)');
  const inputs = [
    '['.repeat(65_536),
    '!['.repeat(32_768),
    '['.repeat(32_768) + '](' + 'x'.repeat(32_766),
    'prefix ' + '`'.repeat(65_529),
  ];
  for (const input of inputs) {
    assert.equal(Buffer.byteLength(input), 65_536);
    const start = performance.now();
    const result = renderMarkdown(input);
    const elapsed = performance.now() - start;
    assert.equal(result.textContent, input);
    assert.ok(elapsed < 1_000, `64 KiB rendering took ${elapsed.toFixed(1)} ms`);
  }
});
