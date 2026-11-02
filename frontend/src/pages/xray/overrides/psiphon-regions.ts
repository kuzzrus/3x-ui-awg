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

/** How the scan of the regions stands: the one that runs, or the last one that ran. */
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
}

/** True once a tunnel has said which regions have Psiphon servers. */
export const regionsKnown = (regions: PsiphonRegion[]): boolean =>
  regions.some((r) => r.available !== undefined);

/** The words a region option adds to its name. */
export interface RegionLabels {
  noServers: string;
  noTunnel: string;
  ms: (ms: number) => string;
}

export function useRegionLabels(): RegionLabels {
  const { t } = useTranslation();
  return useMemo(
    () => ({
      noServers: t('pages.xray.psiphon.regionNoServers'),
      noTunnel: t('pages.xray.psiphon.regionScanNoTunnel'),
      ms: (ms: number) => t('pages.xray.psiphon.probeResult', { ms }),
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
 * Options of a region picker: what a scan found first, the fastest at the top, then the regions not scanned, then those
 * that did not connect. A region without servers is greyed out and last, unless a scan connected one there.
 */
export function regionPickerOptions(
  regions: PsiphonRegion[],
  labels: RegionLabels,
  scanned: PsiphonScanResult[] = [],
) {
  const found = new Map(scanned.map((r) => [r.region, r]));
  const rank = (r: PsiphonRegion) => {
    if (found.get(r.code)?.connected) return 0;
    if (r.available === false) return 3;
    return found.has(r.code) ? 2 : 1;
  };
  const label = (r: PsiphonRegion) => {
    const name = `${r.name} (${r.code})`;
    const result = found.get(r.code);
    if (result?.connected)
      return result.latencyMs ? `${name} — ${labels.ms(result.latencyMs)}` : name;
    if (r.available === false) return `${name} — ${labels.noServers}`;
    return result ? `${name} — ${labels.noTunnel}` : name;
  };
  const speed = (r: PsiphonRegion) => found.get(r.code)?.latencyMs || slowest;
  return [...regions]
    .sort((a, b) => rank(a) - rank(b) || (rank(a) === 0 ? speed(a) - speed(b) : 0))
    .map((r) => ({ value: r.code, label: label(r), disabled: rank(r) === 3 }));
}
