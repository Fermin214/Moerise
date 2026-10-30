import assert from 'node:assert/strict';
import { test } from 'node:test';
import { buildSettingsPatch } from '../src/settings.js';

const snapshot = {
  version: 4,
  settings: {
    timezone: 'Asia/Hong_Kong',
    review: { count: 3, min_age_hours: 72, recent_days: 7, include_archived: false },
    notifications: { enabled: false, time: '09:00', times: ['09:00'] },
    llm: { enabled: false, model: null },
    embedding: { enabled: false, dimensions: 1024 },
    channel: { kind: 'feishu-app', app_id: null },
    insights: { default_prompt: '默认提示词' },
  },
};

test('settings patch merges only changed public fields and replaces time arrays', () => {
  const draft = structuredClone(snapshot.settings);
  draft.review.count = 5;
  draft.review.include_archived = true;
  draft.notifications.times = ['09:00', '18:00'];
  draft.llm.model = 'model-a';
  draft.llm.api_key = 'must-not-leak';
  draft.channel.app_secret = 'must-not-leak';
  draft.channel.webhook_url = 'must-not-leak';
  draft.memos = { token: 'must-not-leak' };
  assert.deepEqual(buildSettingsPatch(snapshot, draft), {
    version: 4,
    settings: {
      review: { count: 5, include_archived: true },
      notifications: { times: ['09:00', '18:00'] }, llm: { model: 'model-a' },
    },
    secrets: {},
  });
  assert.equal(snapshot.settings.review.count, 3);
});

test('secret actions keep, replace and clear have distinct payloads', () => {
  assert.deepEqual(buildSettingsPatch(snapshot, {}, {
    llm_api_key: { action: 'keep' },
    embedding_api_key: { action: 'replace', value: 'new-key' },
    app_secret: { action: 'clear' },
    webhook_url: { action: 'replace', value: 'https://example.test/hook/token' },
    unknown: { action: 'replace', value: 'must-not-leak' },
  }), {
    version: 4, settings: {},
    secrets: {
      llm_api_key: { action: 'keep' }, embedding_api_key: { action: 'replace', value: 'new-key' },
      app_secret: { action: 'clear' }, webhook_url: { action: 'replace', value: 'https://example.test/hook/token' },
    },
  });
});

test('masked secrets, blank replacement, unknown actions and stray values are rejected', () => {
  for (const value of ['', ' ', '••••••••', '********', 'sk-…', 'sk-●●']) {
    assert.throws(() => buildSettingsPatch(snapshot, {}, { llm_api_key: { action: 'replace', value } }),
      { code: 'SETTINGS_INPUT_INVALID' });
  }
  for (const operation of [{ action: 'reset' }, { action: 'keep', value: 'old' }, { action: 'clear', value: '' }]) {
    assert.throws(() => buildSettingsPatch(snapshot, {}, { app_secret: operation }),
      { code: 'SETTINGS_INPUT_INVALID' });
  }
  assert.throws(() => buildSettingsPatch(snapshot, { timezone: null }), { code: 'SETTINGS_INPUT_INVALID' });
});

test('patch uses the supplied version and leaves omitted fields and credentials untouched', () => {
  assert.deepEqual(buildSettingsPatch({ ...snapshot, version: 9 }, { timezone: 'UTC' }), {
    version: 9, settings: { timezone: 'UTC' }, secrets: {},
  });
  assert.deepEqual(buildSettingsPatch(snapshot, { timezone: snapshot.settings.timezone, review: snapshot.settings.review }), { version: 4, settings: {}, secrets: {} });
  assert.throws(() => buildSettingsPatch({ ...snapshot, version: 0 }, {}), { code: 'SETTINGS_INPUT_INVALID' });
});
