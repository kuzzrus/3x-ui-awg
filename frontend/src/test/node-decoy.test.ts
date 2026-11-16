import { describe, it, expect } from 'vitest';

import { nodeDecoyTarget } from '@/lib/xray/node-decoy';
import { DEFAULT_DECOY_PORT, type NodeRecord } from '@/schemas/node';

const node = (patch: Partial<NodeRecord>): NodeRecord => ({ id: 1, kind: 'agent', ...patch });

describe('nodeDecoyTarget', () => {
  it('hands a probe to the loopback listener of the decoy, under the domain of its certificate', () => {
    const decoy = { mode: 'template', domain: 'site.example.com', port: 8443 };
    expect(nodeDecoyTarget(node({ decoy }))).toEqual({
      target: '127.0.0.1:8443',
      serverName: 'site.example.com',
    });
  });

  it('takes the default port of the decoy when the node names none', () => {
    const want = { target: `127.0.0.1:${DEFAULT_DECOY_PORT}`, serverName: 'site.example.com' };
    expect(nodeDecoyTarget(node({ decoy: { mode: 'proxy', domain: 'site.example.com' } }))).toEqual(
      want,
    );
    expect(
      nodeDecoyTarget(node({ decoy: { mode: 'proxy', domain: 'site.example.com', port: 0 } })),
    ).toEqual(want);
  });

  it('trims the domain', () => {
    const decoy = { mode: 'adguard', domain: '  site.example.com ' };
    expect(nodeDecoyTarget(node({ decoy }))?.serverName).toBe('site.example.com');
  });

  it('gives none where the node serves no decoy to hand a probe to', () => {
    const decoy = { mode: 'template', domain: 'site.example.com' };
    expect(nodeDecoyTarget(node({ decoy: { mode: '', domain: 'site.example.com' } }))).toBeNull();
    expect(nodeDecoyTarget(node({ decoy: { mode: 'template', domain: ' ' } }))).toBeNull();
    expect(nodeDecoyTarget(node({ decoy: { mode: 'template' } }))).toBeNull();
    expect(nodeDecoyTarget(node({ decoy: null }))).toBeNull();
    expect(nodeDecoyTarget(node({}))).toBeNull();
    expect(nodeDecoyTarget(node({ kind: 'panel', decoy }))).toBeNull();
    expect(nodeDecoyTarget(undefined)).toBeNull();
  });
});
