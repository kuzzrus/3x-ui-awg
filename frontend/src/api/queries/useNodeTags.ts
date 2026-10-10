import { useMemo } from 'react';

import { useNodesQuery } from '@/api/queries/useNodesQuery';

// The tags the agent nodes carry, once each however they are spelled: what the tag pickers suggest.
export function useNodeTags(enabled = true): string[] {
  const { nodes } = useNodesQuery({ enabled });
  return useMemo(() => {
    const byKey = new Map<string, string>();
    for (const node of nodes) {
      for (const tag of node.tags ?? []) {
        if (!byKey.has(tag.toLowerCase())) byKey.set(tag.toLowerCase(), tag);
      }
    }
    return [...byKey.values()].sort((a, b) => a.localeCompare(b));
  }, [nodes]);
}
