import { describe, expect, it } from 'vitest';

import { genInboundLinks, genNaiveProxyLink } from '@/lib/xray/inbound-link';
import { InboundSchema } from '@/schemas/api/inbound';

// Multi-client naiveproxy renders one naive+https:// link per entry in
// settings.clients: the inbound's own domain and public port, never its loopback port.
function naiveInbound(publicPort?: number) {
  return InboundSchema.parse({
    id: 72,
    remark: 'np-mc',
    port: 18443,
    protocol: 'naiveproxy',
    settings: {
      domain: 'naive.example.com',
      certFile: '/etc/ssl/naive.pem',
      keyFile: '/etc/ssl/naive.key',
      ...(publicPort === undefined ? {} : { publicPort }),
      clients: [
        { email: 'alice', naiveProxyPassword: 'pw-alice', enable: true },
        { email: 'bob', naiveProxyPassword: 'pw-bob', enable: true },
      ],
    },
  });
}

describe('naiveproxy multi-client link fan-out', () => {
  it('emits one naive+https:// link per client on the domain and public 443', () => {
    const out = genInboundLinks({
      inbound: naiveInbound(),
      remark: 'np-mc',
      fallbackHostname: 'panel.example.test',
    });
    const links = out.split('\r\n').filter(Boolean);
    expect(links).toEqual([
      'naive+https://alice:pw-alice@naive.example.com:443#np-mc',
      'naive+https://bob:pw-bob@naive.example.com:443#np-mc',
    ]);
    for (const link of links) {
      expect(link).not.toContain('18443');
      expect(link).not.toContain('panel.example.test');
    }
  });

  it('dials the inbound publicPort when the front door is not on 443', () => {
    const out = genInboundLinks({
      inbound: naiveInbound(8443),
      remark: 'np-mc',
      fallbackHostname: 'panel.example.test',
    });
    const links = out.split('\r\n').filter(Boolean);
    expect(links[0]).toBe('naive+https://alice:pw-alice@naive.example.com:8443#np-mc');
  });

  it('rejects a public port outside 1-65535', () => {
    expect(() => naiveInbound(0)).toThrow();
    expect(() => naiveInbound(70000)).toThrow();
  });

  it('skips a client that has no password yet', () => {
    const inbound = naiveInbound();
    if (inbound.protocol !== 'naiveproxy') throw new Error('unexpected protocol');
    inbound.settings.clients[1].naiveProxyPassword = '';
    const out = genInboundLinks({
      inbound,
      remark: 'np-mc',
      fallbackHostname: 'panel.example.test',
    });
    const links = out.split('\r\n').filter(Boolean);
    expect(links).toHaveLength(1);
    expect(links[0]).toContain('alice:pw-alice@');
  });
});

describe('genNaiveProxyLink', () => {
  it('keeps reserved characters in a hand-set credential out of the authority', () => {
    const email = 'a b+c@example.com';
    const password = 'p:a@s/s+w#rd';
    const link = genNaiveProxyLink({
      inbound: naiveInbound(),
      clientEmail: email,
      clientPassword: password,
    });
    const url = new URL(link);
    expect(url.protocol).toBe('naive+https:');
    expect(url.hostname).toBe('naive.example.com');
    expect(url.port).toBe('443');
    expect(decodeURIComponent(url.username)).toBe(email);
    expect(decodeURIComponent(url.password)).toBe(password);
  });

  it('adds no fragment when there is no remark', () => {
    const link = genNaiveProxyLink({
      inbound: naiveInbound(),
      clientEmail: 'alice',
      clientPassword: 'pw-alice',
    });
    expect(link).toBe('naive+https://alice:pw-alice@naive.example.com:443');
  });

  it('returns empty for a non-naiveproxy inbound', () => {
    const vless = InboundSchema.parse({
      id: 1,
      port: 443,
      protocol: 'vless',
      settings: { clients: [] },
    });
    expect(genNaiveProxyLink({ inbound: vless, clientEmail: 'a', clientPassword: 'x' })).toBe('');
  });

  it('returns empty when the client has no password or the inbound no domain', () => {
    const inbound = naiveInbound();
    expect(genNaiveProxyLink({ inbound, clientEmail: 'alice' })).toBe('');
    if (inbound.protocol !== 'naiveproxy') throw new Error('unexpected protocol');
    inbound.settings.domain = '';
    expect(genNaiveProxyLink({ inbound, clientEmail: 'alice', clientPassword: 'pw' })).toBe('');
  });
});
