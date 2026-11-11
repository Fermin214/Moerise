import assert from 'node:assert/strict';
import { test } from 'node:test';
import { detectLanguage, setLanguage, t, l, resolveText, monthLabel } from '../src/i18n.js';
import { errorText } from '../src/errors.js';

test('locale negotiation, source fallback and deferred messages preserve parameter content', () => {
  assert.equal(detectLanguage(['fr-FR', 'zh-TW', 'en-US']), 'zh-CN');
  assert.equal(detectLanguage(['en-GB', 'zh-CN']), 'en');
  assert.equal(detectLanguage(['de-DE']), 'en');
  const late = l('笔记 {0}', { 0: '<img>{1} #中文' });
  setLanguage('en');
  assert.equal(resolveText(late), 'Note <img>{1} #中文');
  assert.equal(t('missing {value}', { value: '{other}' }), 'missing {other}');
  assert.equal(t('{0} 天前', { 0: 1 }), '1 day ago');
  assert.equal(t('{0} 天前', { 0: 2 }), '2 days ago');
  assert.equal(resolveText(monthLabel(2026, 11)), 'November 2026');
  assert.match(errorText({ code: 'ACCOUNT_NOT_BOUND', message: 'Private upstream detail' }), /^This account/);
  assert.equal(errorText({ code: 'UNKNOWN', message: 'Private upstream detail' }), 'The request could not be completed. Please try again later.');
  setLanguage('zh-CN');
  assert.equal(resolveText(late), '笔记 <img>{1} #中文');
  assert.equal(resolveText(monthLabel(2026, 11, true)), '2026年11月');
});
