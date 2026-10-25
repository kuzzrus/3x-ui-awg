import { describe, expect, it } from 'vitest';

import { yandexDiskPreset } from '@/lib/xray/xdrive-presets';

type Operation = {
  method: string;
  url: string;
  headers?: Record<string, string>;
  namesRegex?: string;
};

describe('Yandex Disk XDRIVE preset', () => {
  const template = () =>
    yandexDiskPreset().template as Record<string, Operation & Record<string, unknown>>;

  it('reads the login and the app password from the first two secrets', () => {
    expect(template().auth).toMatchObject({
      type: 'basic',
      username: '{secret0}',
      password: '{secret1}',
    });
  });

  it('puts, gets and deletes one object under the folder, and lists the folder with Depth 1', () => {
    const { put, get, delete: del, list } = template();
    expect([put.method, get.method, del.method]).toEqual(['PUT', 'GET', 'DELETE']);
    for (const op of [put, get, del]) {
      expect(op.url).toBe('https://webdav.yandex.ru/{folder}/{name}');
    }
    expect(list).toMatchObject({
      method: 'PROPFIND',
      url: 'https://webdav.yandex.ru/{folder}/',
      headers: { Depth: '1' },
    });
  });

  it('takes the object names out of a multistatus answer and not the folder itself', () => {
    const body =
      '<d:multistatus xmlns:d="DAV:">' +
      '<d:response><d:href>/xdrive-test/</d:href></d:response>' +
      '<d:response><d:href>/xdrive-test/streams~ab12~c2s~000000000.seg</d:href></d:response>' +
      '<d:response><d:href>/xdrive-test/sessions~1792927873-ab12</d:href></d:response>' +
      '</d:multistatus>';
    const names = [...body.matchAll(new RegExp(template().list.namesRegex!, 'g'))].map((m) => m[1]);
    expect(names).toEqual(['streams~ab12~c2s~000000000.seg', 'sessions~1792927873-ab12']);
  });

  it('retries the throttling and server errors a WebDAV host answers', () => {
    expect(template().retry).toMatchObject({ status: [429, 500, 502, 503] });
  });

  it('carries the tuning of the core live test and a template nobody else holds', () => {
    const preset = yandexDiskPreset();
    expect(preset).toMatchObject({
      segmentBytes: 262144,
      flushIntervalMs: 100,
      pollIntervalMs: 300,
      maxPollIntervalMs: 1500,
      sessionTtlSeconds: 120,
      concurrency: 8,
    });
    expect(yandexDiskPreset().template).not.toBe(preset.template);
    expect(yandexDiskPreset().template).toEqual(preset.template);
  });
});
