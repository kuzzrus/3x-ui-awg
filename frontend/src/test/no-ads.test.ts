import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// The fork shows no advertising: a sync from upstream must not bring the sponsor slots back.
const SRC = join(import.meta.dirname, '..');
const TRANSLATIONS = join(SRC, '..', '..', 'internal', 'web', 'translation');

describe('advertising', () => {
  it('has no sponsor source files', () => {
    const files = readdirSync(SRC, { recursive: true, encoding: 'utf8' });
    expect(files.filter((file) => /sponsor/i.test(file))).toEqual([]);
  });

  it('has no sponsor translation keys', () => {
    for (const locale of ['en-US', 'ru-RU']) {
      const text = readFileSync(join(TRANSLATIONS, `${locale}.json`), 'utf8');
      expect(text, locale).not.toContain('"sponsors":');
    }
  });
});
