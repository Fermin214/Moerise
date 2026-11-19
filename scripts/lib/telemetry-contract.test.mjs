import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';

const read = path => readFile(new URL(`../../${path}`, import.meta.url), 'utf8');
const [contract, web, android] = await Promise.all([
  read('internal/telemetry/schema-v1.json').then(JSON.parse),
  read('web/src/privacy.js'),
  read('mobile/shared/src/commonMain/kotlin/io/github/fermin214/moerise/presentation/PrivacyController.kt'),
]);
const literals = value => [...value.matchAll(/["']([a-z][a-z0-9_ ]*)["']/g)].flatMap(match => match[1].trim().split(/\s+/)).filter(Boolean);

test('Web and Android accept the versioned telemetry dimensions and duration thresholds', () => {
  for (const [field, webSet, androidSet] of [
    ['operation', /const operations = new Set\((.+?)\);/, /val operations = setOf\(([\s\S]*?)\)/],
    ['result', /const results = new Set\((.+?)\);/, /val results = setOf\(([\s\S]*?)\)/],
    ['source', /const sources = new Set\((.+?)\);/, /source !in setOf\(([^)]*)\)/],
    ['duration', /function duration\([^)]*\)\s*\{([^}]*)\}/, /fun duration\([^)]*\) = when\s*\{([^}]*)\}/],
  ]) {
    for (const [client, text, pattern] of [['web', web, webSet], ['android', android, androidSet]]) {
      const implementation = pattern.exec(text)?.[1];
      assert.ok(implementation, `${client} ${field} declaration`);
      const expected = contract.$defs[field].enum;
      assert.deepEqual(literals(implementation).sort(), [...expected].sort(), `${client} ${field} values`);
      if (field === 'duration') {
        const actual = [...implementation.matchAll(/(?:ms|elapsed)\s*<\s*([\d_]+)\s*(?:\?|->)\s*["'](lt\w+)["']/g)]
          .map(([, number, label]) => [Number(number.replaceAll('_', '')), label]);
        const limits = expected.filter(label => label.startsWith('lt')).map(label => {
          const [, amount, unit] = /^lt(\d+)(ms|s)$/.exec(label);
          return [Number(amount) * (unit === 's' ? 1000 : 1), label];
        });
        assert.deepEqual(actual, limits, `${client} duration boundaries`);
      }
    }
  }
});
