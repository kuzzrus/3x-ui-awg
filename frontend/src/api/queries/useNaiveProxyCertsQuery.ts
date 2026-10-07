import { useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

export type NaiveProxyCertState = '' | 'obtaining' | 'obtained' | 'failed';

// One NaiveProxy inbound's certificate: for an automatic one what the panel has ordered,
// for a manual one the expiry of the file the admin set.
export interface NaiveProxyCert {
  inboundId: number;
  domain: string;
  enable: boolean;
  mode: 'auto' | 'manual';
  state: NaiveProxyCertState;
  notAfter?: string;
  error?: string;
  hint?: string; // 'reach': Let's Encrypt did not get the panel's answer on port 80
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

// Quickly while an enabled automatic inbound still waits for its order, so the notice flips as soon
// as it lands. A disabled one stays idle for good and must not keep the fast poll going.
export function certsPollInterval(certs: NaiveProxyCert[] | undefined): number {
  const ordering = certs?.some(
    (c) => c.mode === 'auto' && c.enable && (c.state === '' || c.state === 'obtaining'),
  );
  return ordering ? 3_000 : 30_000;
}

export function useNaiveProxyCertsQuery(enabled = true) {
  return useQuery({
    queryKey: keys.xray.naiveProxyCerts(),
    queryFn: fetchNaiveProxyCerts,
    enabled,
    refetchInterval: (query) => certsPollInterval(query.state.data),
  });
}
