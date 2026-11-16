import { describe, expect, it } from 'vitest';

import { agentEndpoint, defaultAgentValues } from '@/pages/nodes/agentFormValues';
import type { AgentFormValues } from '@/schemas/node';

// Each field gets a value of its own, so that a field carried under another name, or not at all, shows.
function distinctValues(): AgentFormValues {
  return {
    ...defaultAgentValues(),
    id: 3,
    name: 'edge-3',
    remark: 'a remark',
    address: 'node.example.com',
    port: 9443,
    allowPrivateAddress: true,
    outboundTag: 'direct',
    tags: ['eu'],
    sidecars: ['tor'],
    decoyMode: 'upload',
    decoyTemplate: 'parked',
    decoyProxyUrl: 'https://site.example',
    decoySite: 'c'.repeat(64),
    decoyDomain: 'decoy.example.com',
    decoyCert: 'manual',
    decoyEmail: 'ops@example.com',
    decoyCertFile: '/etc/ssl/n.pem',
    decoyKeyFile: '/etc/ssl/n.key',
    decoyPort: 7500,
  };
}

describe('agentEndpoint', () => {
  it('carries every field of the decoy, which the panel takes whole or not at all', () => {
    const values = distinctValues();
    const sent = agentEndpoint(values) as Record<string, unknown>;

    const decoyFields = Object.keys(defaultAgentValues()).filter((key) => key.startsWith('decoy'));
    expect(decoyFields.length).toBeGreaterThan(0);
    for (const field of decoyFields) {
      expect(sent[field], field).toEqual(values[field as keyof AgentFormValues]);
    }
  });

  it('carries the details of the node and the lists that a save replaces', () => {
    const values = distinctValues();
    expect(agentEndpoint(values)).toMatchObject({
      name: 'edge-3',
      remark: 'a remark',
      address: 'node.example.com',
      port: 9443,
      allowPrivateAddress: true,
      outboundTag: 'direct',
      tags: ['eu'],
      sidecars: ['tor'],
    });
  });
});
