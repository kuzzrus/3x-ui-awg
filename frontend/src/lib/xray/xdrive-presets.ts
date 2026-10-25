import yandexDiskTemplate from './xdrive-yandex.json';

// The template and tuning of Xray-core's own Yandex Disk live test
// (transport/internet/xdrive/yandex_live_test.go), against the WebDAV host.
export function yandexDiskPreset() {
  return {
    template: JSON.parse(JSON.stringify(yandexDiskTemplate)) as Record<string, unknown>,
    segmentBytes: 262144,
    flushIntervalMs: 100,
    pollIntervalMs: 300,
    maxPollIntervalMs: 1500,
    sessionTtlSeconds: 120,
    concurrency: 8,
  };
}

const byKey = (a: [string, unknown], b: [string, unknown]) =>
  a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0;

// Key order carries no meaning in a template, so an edit that only reorders keys still matches.
function canonical(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonical);
  if (value && typeof value === 'object') {
    const entries = Object.entries(value).sort(byKey);
    return Object.fromEntries(entries.map(([key, inner]) => [key, canonical(inner)]));
  }
  return value;
}

const yandexDiskCanonical = JSON.stringify(canonical(yandexDiskTemplate));

/** Whether a template is the Yandex Disk one the preset fills, which the form shows as its own service. */
export function isYandexDiskTemplate(template: unknown): boolean {
  return JSON.stringify(canonical(template)) === yandexDiskCanonical;
}
