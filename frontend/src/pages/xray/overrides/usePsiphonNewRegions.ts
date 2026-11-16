/* oxlint-disable react/set-state-in-effect -- same fetch-on-mount shape as usePsiphonScan.ts: the state is set when a response lands. */
import { useCallback, useEffect, useState } from 'react';

import { HttpUtil } from '@/utils';

import { newRegionsOf, type PsiphonScanStatus } from './psiphon-regions';

const SCAN_URL = '/panel/api/xray/psiphon/scan';
const DISMISSED_KEY = 'xui.psiphon.newRegions.dismissed';
// The page is not watched as closely as the window of the scan is, so a scan that runs is looked at less often.
const POLL_MS = 10000;

function readDismissed(): number {
  try {
    return Number(localStorage.getItem(DISMISSED_KEY)) || 0;
  } catch {
    return 0;
  }
}

/** The regions that the last scan, or the one that runs, found and no scan before it had, until the admin dismisses them. */
export function usePsiphonNewRegions() {
  const [scan, setScan] = useState<PsiphonScanStatus | null>(null);
  const [dismissed, setDismissed] = useState(readDismissed);

  const refresh = useCallback(async () => {
    const msg = await HttpUtil.get<PsiphonScanStatus>(SCAN_URL, undefined, { silent: true });
    if (msg?.success && msg.obj) setScan(msg.obj);
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const running = !!scan?.running;
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(timer);
  }, [running, refresh]);

  // A scan is told from the one before it by the time it started, which a dismissal outlives from the scan that is under way to
  // the end of it.
  const scanId = scan?.startedAt || scan?.finishedAt || 0;
  const regions = scanId > dismissed ? newRegionsOf(scan) : [];

  const dismiss = useCallback(() => {
    setDismissed(scanId);
    try {
      localStorage.setItem(DISMISSED_KEY, String(scanId));
    } catch {
      /* a browser that keeps nothing shows the banner again after a reload */
    }
  }, [scanId]);

  return { regions, dismiss };
}
