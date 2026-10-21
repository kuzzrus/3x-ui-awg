import { describe, expect, it } from 'vitest';

import { nodeHostsProtocol } from '@/lib/xray/node-protocols';

describe('nodeHostsProtocol', () => {
  it('lets a panel node host the sidecar protocols and an agent only the Xray ones', () => {
    for (const protocol of ['mtproto', 'amneziawg', 'tuic', 'masque']) {
      expect(nodeHostsProtocol(protocol)).toBe(true);
      expect(nodeHostsProtocol(protocol, 'panel')).toBe(true);
      expect(nodeHostsProtocol(protocol, 'agent')).toBe(false);
    }
    for (const protocol of ['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria', 'wireguard']) {
      expect(nodeHostsProtocol(protocol, 'panel')).toBe(true);
      expect(nodeHostsProtocol(protocol, 'agent')).toBe(true);
    }
  });

  it('keeps what only the panel serves off every node', () => {
    for (const protocol of ['http', 'mixed', 'tunnel', 'tun', 'tproxy', 'naiveproxy']) {
      expect(nodeHostsProtocol(protocol, 'panel')).toBe(false);
      expect(nodeHostsProtocol(protocol, 'agent')).toBe(false);
    }
  });
});
