import { useQuery } from '@tanstack/react-query';

import { keys } from '@/api/queryKeys';
import { HttpUtil } from '@/utils';

// The decoy pages built into this panel, which are the ones an agent node serves too.
export function useDecoyTemplates(enabled = true): string[] {
  const { data } = useQuery({
    queryKey: keys.nodes.decoyTemplates(),
    queryFn: async (): Promise<string[]> => {
      const msg = await HttpUtil.post<{ templates?: string[] }>(
        '/panel/api/xray/frontproxy/status',
        undefined,
        { silentSuccess: true },
      );
      if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch the decoy pages');
      return msg.obj?.templates ?? [];
    },
    enabled,
    staleTime: Infinity,
  });
  return data ?? [];
}
