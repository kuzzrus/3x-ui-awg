import { describe, expect, it } from 'vitest';

import { isYandexDiskTemplate, yandexDiskPreset } from '@/lib/xray/xdrive-presets';

describe('Yandex Disk XDRIVE preset', () => {
  it('recognises its template however the keys are ordered, and nothing that differs', () => {
    const { template } = yandexDiskPreset();
    expect(isYandexDiskTemplate(template)).toBe(true);
    expect(isYandexDiskTemplate(Object.fromEntries(Object.entries(template).reverse()))).toBe(true);

    expect(isYandexDiskTemplate({ ...template, retry: { status: [500] } })).toBe(false);
    expect(isYandexDiskTemplate({ ...template, flatten: false })).toBe(false);
    expect(isYandexDiskTemplate(undefined)).toBe(false);
    expect(isYandexDiskTemplate('template')).toBe(false);
  });

  it('takes the object names out of a multistatus answer and not the folder itself', () => {
    const { list } = yandexDiskPreset().template as { list: { namesRegex: string } };
    const body =
      '<d:multistatus xmlns:d="DAV:">' +
      '<d:response><d:href>/xdrive-test/</d:href></d:response>' +
      '<d:response><d:href>/xdrive-test/streams~ab12~c2s~000000000.seg</d:href></d:response>' +
      '<d:response><d:href>/xdrive-test/sessions~1792927873-ab12</d:href></d:response>' +
      '</d:multistatus>';
    const names = [...body.matchAll(new RegExp(list.namesRegex, 'g'))].map((m) => m[1]);
    expect(names).toEqual(['streams~ab12~c2s~000000000.seg', 'sessions~1792927873-ab12']);
  });

  it('hands out a fresh template each time, so one form cannot edit another', () => {
    const first = yandexDiskPreset();
    expect(yandexDiskPreset().template).not.toBe(first.template);
    expect(yandexDiskPreset().template).toEqual(first.template);
  });
});
