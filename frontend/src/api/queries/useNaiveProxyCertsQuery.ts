import { useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

export type NaiveProxyCertState = '' | 'obtaining' | 'obtained' | 'failed';

// One NaiveProxy inbound's certificate: for an automatic one what the panel has ordered,
// for a manual one the expiry of the file the admin set.
export interface NaiveProxyCert {
  inboundId: number;
  domain: string;
  mode: 'auto' | 'manual';
  state: NaiveProxyCertState;
  notAfter?: string;
  error?: string;
}

async function fetchNaiveProxyCerts(): Promise<NaiveProxyCert[]> {
  const msg = await HttpUtil.post<NaiveProxyCert[]>('/panel/api/xray/naiveproxy/certs', undefined, {
    silent: true,
  });
  if (!msg?.success) {
    throw new Error(msg?.msg || 'Failed to fetch the NaiveProxy certificates');
  }
  return msg.obj ?? [];
}

// Polls quickly while an order is in flight, so the notice flips as soon as it lands.
export function useNaiveProxyCertsQuery(enabled = true) {
  return useQuery({
    queryKey: keys.xray.naiveProxyCerts(),
    queryFn: fetchNaiveProxyCerts,
    enabled,
    refetchInterval: (query) =>
      query.state.data?.some(
        (c) => c.mode === 'auto' && (c.state === '' || c.state === 'obtaining'),
      )
        ? 3_000
        : 30_000,
  });
}
