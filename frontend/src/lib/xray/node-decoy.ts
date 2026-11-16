import { DEFAULT_DECOY_PORT, type NodeRecord } from '@/schemas/node';

// Where a probe of an agent node's port is handed to its decoy: the loopback listener, and the domain its certificate is for.
export interface NodeDecoyTarget {
  node: NodeRecord;
  target: string;
  serverName: string;
}

// The REALITY fallback of an inbound on a node that serves a decoy, or null when it serves none or has no domain for it.
export function nodeDecoyTarget(node: NodeRecord | null | undefined): NodeDecoyTarget | null {
  const decoy = node?.decoy;
  if (!node || node.kind !== 'agent' || !decoy?.mode || !decoy.domain) return null;
  return {
    node,
    target: `127.0.0.1:${decoy.port || DEFAULT_DECOY_PORT}`,
    serverName: decoy.domain,
  };
}
