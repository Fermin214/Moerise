import assert from 'node:assert/strict';
import { test } from 'node:test';
import { errorText } from '../src/errors.js';

test('unsupported login, rate limits and service errors have Chinese explanations', () => {
  for (const code of [
    'PASSWORD_LOGIN_UNSUPPORTED', 'LOGIN_RATE_LIMITED', 'RATE_LIMITED',
    'UPSTREAM_UNAVAILABLE', 'UPSTREAM_AUTH', 'SESSION_UNAVAILABLE', 'ATTACHMENT_REFERENCED', 'ATTACHMENT_DELETE_REJECTED',
  ]) {
    const text = errorText({ code, message: 'English backend message', requestId: 'request-42' });
    assert.match(text, /[\u4e00-\u9fff]/);
    assert.ok(!text.includes(code));
    assert.ok(!text.includes('request-42'));
    assert.ok(!text.includes('English backend message'));
  }
  assert.match(errorText({ code: 'PASSWORD_LOGIN_UNSUPPORTED' }), /不支持密码登录/);
});

test('unknown errors use Chinese fallback without exposing backend prose', () => {
  const result = errorText({ code: 'NEW_BACKEND_ERROR', message: 'Raw upstream diagnostic', requestId: 'trace-1' });
  assert.match(result, /请求未能完成/);
  assert.ok(!result.includes('NEW_BACKEND_ERROR'));
  assert.ok(!result.includes('trace-1'));
  assert.ok(!result.includes('Raw upstream diagnostic'));
  assert.equal(errorText(new Error('Unexpected JS error')), '请求未能完成，请稍后重试。');
});
