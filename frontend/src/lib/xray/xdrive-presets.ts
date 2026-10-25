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
