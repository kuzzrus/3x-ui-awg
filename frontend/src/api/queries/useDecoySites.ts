import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { keys } from '@/api/queryKeys';
import { DecoySiteListSchema, DecoySiteSchema, type DecoySite } from '@/schemas/node';
import { HttpUtil, Msg } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';

export type { DecoySite };

// The static sites the panel holds, which an agent node can serve as its decoy.
export function useDecoySites(enabled = true): DecoySite[] {
  const { data } = useQuery({
    queryKey: keys.nodes.decoySites(),
    queryFn: async (): Promise<DecoySite[]> => {
      const msg = await HttpUtil.get('/panel/api/nodes/decoySites', undefined, { silent: true });
      if (!msg?.success) throw new Error(msg?.msg || 'Failed to fetch the decoy sites');
      const validated = parseMsg(msg, DecoySiteListSchema, 'nodes/decoySites');
      return Array.isArray(validated.obj) ? validated.obj : [];
    },
    enabled,
  });
  return data ?? [];
}

export function useDecoySiteMutations() {
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: keys.nodes.decoySites() });

  const uploadMut = useMutation({
    mutationFn: async (file: File): Promise<Msg<DecoySite>> => {
      const form = new FormData();
      form.append('site', file);
      const raw = await HttpUtil.post('/panel/api/nodes/decoySites/upload', form, {
        headers: { 'Content-Type': 'multipart/form-data' },
      });
      return parseMsg(raw, DecoySiteSchema, 'nodes/decoySites/upload');
    },
    onSuccess: (msg) => {
      if (msg?.success) void refresh();
    },
  });

  const removeMut = useMutation({
    mutationFn: (sha256: string) => HttpUtil.post(`/panel/api/nodes/decoySites/del/${sha256}`),
    onSuccess: (msg) => {
      if (msg?.success) void refresh();
    },
  });

  return {
    upload: uploadMut.mutateAsync,
    uploading: uploadMut.isPending,
    remove: removeMut.mutateAsync,
    removing: removeMut.isPending,
  };
}
