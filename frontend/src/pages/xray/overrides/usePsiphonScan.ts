/* oxlint-disable react/set-state-in-effect -- same fetch-on-open shape as PsiphonModal.tsx: the state is set
   when a response lands, at modal-open frequency and on a slow poll while a scan runs. */
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { HttpUtil } from '@/utils';

import type { PsiphonScanStatus } from './psiphon-regions';

const SCAN_URL = '/panel/api/xray/psiphon/scan';
// How often the status is read again while a scan runs.
const POLL_MS = 2000;

/** The scan of the regions: its status, kept current while it runs, and the two things that can be asked of it. */
export function usePsiphonScan(active: boolean, onError: (text: string) => void) {
  const { t } = useTranslation();
  const [scan, setScan] = useState<PsiphonScanStatus | null>(null);
  const [busy, setBusy] = useState(false);

  // Counts the actions, so that a status which left before one finished is not taken for the newer one.
  const generation = useRef(0);

  const refresh = useCallback(async () => {
    const asked = generation.current;
    const msg = await HttpUtil.get<PsiphonScanStatus>(SCAN_URL);
    if (asked === generation.current && msg?.success && msg.obj) setScan(msg.obj);
  }, []);

  // What is held belongs to the install that ran the scan, so it is dropped when the window closes or Psiphon goes, which
  // also keeps a stale table from showing for the moment before the fresh status lands.
  useEffect(() => {
    if (!active) {
      setScan(null);
      return;
    }
    void refresh();
  }, [active, refresh]);

  const running = !!scan?.running;
  useEffect(() => {
    if (!active || !running) return;
    const timer = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(timer);
  }, [active, running, refresh]);

  // Both answer the status, also when they fail: a scan that was refused says why, and the status stays as it was.
  const act = useCallback(
    async (action: 'start' | 'cancel') => {
      setBusy(true);
      generation.current++;
      try {
        const msg = await HttpUtil.post<PsiphonScanStatus>(`${SCAN_URL}/${action}`, {});
        if (msg?.obj && 'results' in msg.obj) setScan(msg.obj);
        if (!msg?.success) onError(msg?.msg || t('pages.xray.psiphon.scanFailed'));
      } finally {
        generation.current++;
        setBusy(false);
      }
    },
    [onError, t],
  );

  return {
    scan,
    busy,
    start: useCallback(() => act('start'), [act]),
    cancel: useCallback(() => act('cancel'), [act]),
  };
}
