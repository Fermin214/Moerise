import assert from 'node:assert/strict';
import { test } from 'node:test';
import { filters, listQuery, detailURL, safeFrom, attachmentURL, rasterImage } from '../src/routes.js';
import { safeLink } from '../src/markdown.js';

test('combines URL keyword, AND tags, date and opaque page token without losing encoding', () => {
  const values = filters('?query=%23%E6%83%B3%E6%B3%95&tag=a%2Fb&tag=c%26d&tag=a%2Fb&date=2026-10-29');
  const query = listQuery('/archived', values, 'opaque+/=&');
  assert.equal(query.get('state'), 'ARCHIVED');
  assert.equal(query.get('order'), 'created_desc');
  assert.equal(query.get('page_size'), '20');
  assert.equal(query.get('query'), '#想法');
  assert.deepEqual(query.getAll('tag'), ['a/b', 'c&d']);
  assert.equal(query.get('date'), '2026-10-29');
  assert.equal(query.get('page_token'), 'opaque+/=&');
  assert.equal(filters('?date=2026-02-31').date, '');
});

test('detail links only return to known application pages and preserve valid filters', () => {
  const from = '/archived?query=hello&tag=%E6%83%B3%E6%B3%95&tag=a%2Fb&date=2026-10-29';
  assert.equal(safeFrom(from), from);
  const link = new URL(detailURL('memos/abc-123', from), 'https://notes.example');
  assert.equal(link.pathname, '/notes/abc-123');
  assert.equal(link.searchParams.get('from'), from);
  for (const bad of [
    '//evil.test', '/\\evil.test', 'https://evil.test', '/api/app/v1/session',
    '/notes/another', '/%2f%2fevil', '/review/../settings', '/settings#fragment', ' /',
  ]) assert.equal(safeFrom(bad), '/', bad);
  assert.equal(safeFrom('/review?arbitrary=data'), '/review');
});

test('only protected content URLs belonging to the note are rendered as attachments', () => {
  const note = { name: 'memos/a' };
  const attachment = { name: 'attachments/b', content_url: '/api/app/v1/notes/a/attachments/b/content' };
  assert.equal(attachmentURL(note, attachment), attachment.content_url);
  for (const url of ['https://evil.test/a.png', '//evil.test/a.png', '/api/app/v1/notes/c/attachments/b/content', 'data:image/png;base64,xxx']) {
    assert.equal(attachmentURL(note, { ...attachment, content_url: url }), '');
  }
  assert.equal(rasterImage('image/png'), true);
  assert.equal(rasterImage('image/bmp'), false);
  assert.equal(rasterImage('image/svg+xml'), false);
  assert.equal(rasterImage('text/html'), false);
});

test('markdown link policy rejects executable, encoded and protocol-relative navigation', () => {
  for (const value of ['javascript:alert(1)', 'data:text/html,hello', 'vbscript:foo', '//evil.test', 'java\nscript:foo', '\\\\evil.test', 'java%73cript:foo']) {
    assert.equal(safeLink(value), null, value);
  }
  assert.equal(safeLink('https://example.com/path?q=1'), 'https://example.com/path?q=1');
  assert.equal(safeLink('mailto:test@example.com'), 'mailto:test@example.com');
  assert.equal(safeLink('/notes/abc'), '/notes/abc');
});
