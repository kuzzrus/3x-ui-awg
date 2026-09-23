import { describe, expect, it } from 'vitest';

import { genInboundLinks, genTproxyLink } from '@/lib/xray/inbound-link';
import { InboundSchema } from '@/schemas/api/inbound';

// Multi-client tproxy renders one tg://webproxy link per entry in
// settings.clients, each carrying that client's own MTProxy secret -- but
// always the SAME front-proxy domain, since tproxy has no per-inbound
// address the way mtproto does.
function tproxyInbound() {
  return InboundSchema.parse({
    id: 71,
    remark: 'tp-mc',
    port: 443,
    protocol: 'tproxy',
    settings: {
      clients: [
        { email: 'alice', tproxySecret: '0123456789abcdef0123456789abcdef', enable: true },
        { email: 'bob', tproxySecret: 'fedcba9876543210fedcba9876543210', enable: true },
      ],
    },
  });
}

describe('tproxy multi-client link fan-out', () => {
  it('emits one tg://webproxy per client from settings.clients, no port, no fragment', () => {
    const out = genInboundLinks({
      inbound: tproxyInbound(),
      remark: 'tp-mc',
      fallbackHostname: 'panel.example.test',
      frontProxyDomain: 'proxy.example.com',
    });
    const links = out.split('\r\n').filter(Boolean);
    expect(links).toHaveLength(2);
    expect(links[0]).toContain('tg://webproxy');
    expect(links[0]).toContain('server=proxy.example.com');
    expect(links[0]).toContain('secret=0123456789abcdef0123456789abcdef');
    expect(links[1]).toContain('secret=fedcba9876543210fedcba9876543210');
    for (const link of links) {
      expect(link).not.toContain('port=');
      expect(link).not.toContain('#');
    }
  });

  it('yields no links when the front-proxy domain is not configured', () => {
    const out = genInboundLinks({
      inbound: tproxyInbound(),
      remark: 'tp-mc',
      fallbackHostname: 'panel.example.test',
      frontProxyDomain: '',
    });
    expect(out.split('\r\n').filter(Boolean)).toHaveLength(0);
  });
});

describe('genTproxyLink', () => {
  it('returns empty for a non-tproxy inbound', () => {
    const vless = InboundSchema.parse({
      id: 1,
      port: 443,
      protocol: 'vless',
      settings: { clients: [] },
    });
    expect(
      genTproxyLink({ inbound: vless, frontProxyDomain: 'proxy.example.com', clientSecret: 'x' }),
    ).toBe('');
  });

  it('returns empty when the client has no secret', () => {
    const inbound = tproxyInbound();
    expect(genTproxyLink({ inbound, frontProxyDomain: 'proxy.example.com' })).toBe('');
  });
});
