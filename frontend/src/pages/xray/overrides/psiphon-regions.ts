import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

/** A region of the picker. `available` is left out until a tunnel has said which regions it has servers in. */
export interface PsiphonRegion {
  code: string;
  name: string;
  available?: boolean;
}

/** What a scan found of one region. A latency is there only when a request went through the tunnel. */
export interface PsiphonScanResult {
  region: string;
  connected: boolean;
  connectMs?: number;
  latencyMs?: number;
  ip?: string;
  country?: string;
  error?: string;
}

/** The last scan in which a tunnel came up in a region: one can connect in a scan and not in the next, so it is kept. */
export interface PsiphonSeenRegion {
  region: string;
  at: number;
  connectMs?: number;
  latencyMs?: number;
  ip?: string;
  country?: string;
}

/** How the scan stands: the one that runs or the last one that ran, with the regions that connected in earlier scans. */
export interface PsiphonScanStatus {
  running: boolean;
  discovering?: boolean;
  cancelled?: boolean;
  error?: string;
  total: number;
  current: string[];
  startedAt?: number;
  finishedAt?: number;
  results: PsiphonScanResult[];
  seen?: PsiphonSeenRegion[];
}

/** The day a scan last found a tunnel in a region, as the pickers and the table write it. */
export const seenDate = (at: number): string => new Date(at * 1000).toLocaleDateString();

/** True once a tunnel has said which regions have Psiphon servers. */
export const regionsKnown = (regions: PsiphonRegion[]): boolean =>
  regions.some((r) => r.available !== undefined);

/** The words a region option adds to its name. */
export interface RegionLabels {
  noServers: string;
  noTunnel: string;
  ms: (ms: number) => string;
  seen: (at: number) => string;
}

export function useRegionLabels(): RegionLabels {
  const { t } = useTranslation();
  return useMemo(
    () => ({
      noServers: t('pages.xray.psiphon.regionNoServers'),
      noTunnel: t('pages.xray.psiphon.regionScanNoTunnel'),
      ms: (ms: number) => t('pages.xray.psiphon.probeResult', { ms }),
      seen: (at: number) => t('pages.xray.psiphon.regionSeenBefore', { date: seenDate(at) }),
    }),
    [t],
  );
}

// A region that connected is told by its latency; one that connected but could not be timed has none and goes last.
const slowest = Number.MAX_SAFE_INTEGER;
const bySpeed = (a: PsiphonScanResult, b: PsiphonScanResult) =>
  (a.latencyMs || slowest) - (b.latencyMs || slowest);

/** The results of a scan, the regions that connected first and the fastest of them first. */
export function sortScanResults(results: PsiphonScanResult[]): PsiphonScanResult[] {
  return [
    ...results.filter((r) => r.connected).sort(bySpeed),
    ...results.filter((r) => !r.connected),
  ];
}

/**
 * Options of a region picker: what the last scan found first, the fastest at the top, then the regions that connected earlier,
 * those not scanned and those that did not connect. A region without servers is greyed out and last, unless a scan connected it.
 */
export function regionPickerOptions(
  regions: PsiphonRegion[],
  labels: RegionLabels,
  scanned: PsiphonScanResult[] = [],
  seen: PsiphonSeenRegion[] = [],
) {
  const found = new Map(scanned.map((r) => [r.region, r]));
  const earlier = new Map(seen.map((s) => [s.region, s]));
  const rank = (r: PsiphonRegion) => {
    if (found.get(r.code)?.connected) return 0;
    if (earlier.has(r.code)) return 1;
    if (r.available === false) return 4;
    return found.has(r.code) ? 3 : 2;
  };
  const label = (r: PsiphonRegion) => {
    const name = `${r.name} (${r.code})`;
    const result = found.get(r.code);
    if (result?.connected)
      return result.latencyMs ? `${name} — ${labels.ms(result.latencyMs)}` : name;
    const before = earlier.get(r.code);
    if (before)
      return result
        ? `${name} — ${labels.noTunnel}, ${labels.seen(before.at)}`
        : `${name} — ${labels.seen(before.at)}`;
    if (r.available === false) return `${name} — ${labels.noServers}`;
    return result ? `${name} — ${labels.noTunnel}` : name;
  };
  const speed = (r: PsiphonRegion) => found.get(r.code)?.latencyMs || slowest;
  const earlierSpeed = (r: PsiphonRegion) => earlier.get(r.code)?.latencyMs || slowest;
  const byRank = (a: PsiphonRegion, b: PsiphonRegion) => {
    if (rank(a) === 0) return speed(a) - speed(b);
    return rank(a) === 1 ? earlierSpeed(a) - earlierSpeed(b) : 0;
  };
  return [...regions]
    .sort((a, b) => rank(a) - rank(b) || byRank(a, b))
    .map((r) => ({ value: r.code, label: label(r), disabled: rank(r) === 4 }));
}
