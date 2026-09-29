import { useQuery } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

export interface NaiveProxyEngineStatus {
  installed: boolean;
  supported: boolean;
  platform: string;
}

async function fetchNaiveProxyEngine(): Promise<NaiveProxyEngineStatus> {
  const msg = await HttpUtil.post<NaiveProxyEngineStatus>(
    '/panel/api/xray/naiveproxy/status',
    undefined,
    { silent: true },
  );
  if (!msg?.success || !msg.obj) {
    throw new Error(msg?.msg || 'Failed to fetch the NaiveProxy engine status');
  }
  return msg.obj;
}

export function useNaiveProxyEngineQuery() {
  return useQuery({
    queryKey: keys.xray.naiveProxyEngine(),
    queryFn: fetchNaiveProxyEngine,
    staleTime: 30_000,
  });
}
