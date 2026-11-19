import { describe, expect, it } from 'vitest';

import { tunnelAddresses, tunnelAddressesText } from '@/lib/xray/amneziawg-tunnel-addresses';
import { genAmneziaWGConfig } from '@/lib/xray/inbound-link';
import { buildAmneziaWGClientConfig } from '@/pages/clients/amneziawgConfig';
import type { AmneziawgInboundSettings } from '@/schemas/protocols/inbound/amneziawg';
import type { ClientRecord, InboundOption } from '@/hooks/useClients';

// The same table as TestTunnelAddresses in internal/amneziawg: the two sides must read an AllowedIPs list alike.
const CASES: [string, string[], string[]][] = [
  ['one address', ['10.8.1.2/32'], ['10.8.1.2/32']],
  ['both families', ['10.8.1.2/32', 'fd86::2/128'], ['10.8.1.2/32', 'fd86::2/128']],
  [
    'the order of the entries is kept',
    ['fd86::2/128', '10.8.1.2/32'],
    ['fd86::2/128', '10.8.1.2/32'],
  ],
  ['bare addresses are single hosts', ['10.8.1.2', 'fd86::2'], ['10.8.1.2', 'fd86::2']],
  ['a network after the address', ['10.8.1.5/32', '192.168.50.0/24'], ['10.8.1.5/32']],
  ['a network before the address', ['192.168.50.0/24', '10.8.1.5/32'], ['10.8.1.5/32']],
  ['an IPv6 network and no IPv6 address', ['10.8.1.5/32', 'fd00:50::/64'], ['10.8.1.5/32']],
  [
    'a network in each family',
    ['10.8.1.5/32', '192.168.50.0/24', 'fd86::5/128', 'fd00:50::/64'],
    ['10.8.1.5/32', 'fd86::5/128'],
  ],
  ['a second single host is a network', ['10.8.1.2/32', '10.8.1.3/32'], ['10.8.1.2/32']],
  ['default routes after the address', ['10.8.1.2/32', '0.0.0.0/0', '::/0'], ['10.8.1.2/32']],
  ['an IPv4 address with a mask, as a legacy client has it', ['10.8.1.2/24'], ['10.8.1.2/24']],
  [
    'an IPv4 list with nothing single-host keeps its first',
    ['192.168.50.0/24', '10.9.0.0/16'],
    ['192.168.50.0/24'],
  ],
  [
    'an IPv6 list with nothing single-host has no address',
    ['10.8.1.2/32', 'fd86::/64'],
    ['10.8.1.2/32'],
  ],
  ['entries that are not addresses are skipped', ['not-an-ip', '10.8.1.2/32'], ['10.8.1.2/32']],
  ['an address out of range is skipped', ['10.8.1.256/32', '10.8.1.2/32'], ['10.8.1.2/32']],
  ['a mask out of range is skipped', ['10.8.1.9/33'], []],
  ['an IPv6 mask out of range is skipped', ['fd86::9/129'], []],
  ['a v4-mapped v6 address is neither', ['::ffff:10.8.1.2/128'], []],
  ['spaces are trimmed', [' 10.8.1.2/32 '], ['10.8.1.2/32']],
  ['nothing', [], []],
];

describe('tunnelAddresses', () => {
  it.each(CASES)('%s', (_name, ips, want) => {
    expect(tunnelAddresses(ips)).toEqual(want);
  });
});

describe('tunnelAddressesText', () => {
  it('returns a string that holds only tunnel addresses as it is', () => {
    expect(tunnelAddressesText('10.8.1.2/32,fd86::2/128')).toBe('10.8.1.2/32,fd86::2/128');
    expect(tunnelAddressesText('10.8.1.2/32, fd86::2/128')).toBe('10.8.1.2/32, fd86::2/128');
    expect(tunnelAddressesText('')).toBe('');
  });

  it('drops the networks behind the client', () => {
    expect(tunnelAddressesText('10.8.1.5/32,192.168.50.0/24')).toBe('10.8.1.5/32');
    expect(tunnelAddressesText('192.168.50.0/24, 10.8.1.5/32, fd86::5/128, fd00:50::/64')).toBe(
      '10.8.1.5/32, fd86::5/128',
    );
  });
});

describe('the AmneziaWG client configs carry the tunnel addresses only', () => {
  const settings = {
    server: {
      publicKey: 'serverPubKey==',
      primaryDns: '8.8.8.8',
      secondaryDns: '',
      mtu: 1420,
      jc: 4,
      jmin: 40,
      jmax: 100,
      s1: 30,
      s2: 90,
      s3: 0,
      s4: 0,
      h1: '',
      h2: '',
      h3: '',
      h4: '',
    },
    clients: [
      {
        email: 'gw',
        privateKey: 'clientPrivKey==',
        allowedIPs: ['192.168.50.0/24', '10.8.1.5/32', 'fd00:50::/64'],
      },
    ],
  } as unknown as AmneziawgInboundSettings;
  const addressLine = (conf: string) => conf.split('\n').find((l) => l.startsWith('Address = '));

  it('in the config that goes into a share link', () => {
    const conf = genAmneziaWGConfig({
      settings,
      address: 'awg.example.test',
      port: 51820,
      remark: 'gw',
      peerIndex: 0,
    });
    expect(addressLine(conf)).toBe('Address = 10.8.1.5/32');
  });

  it('in the config of the client dialog', () => {
    const client = {
      email: 'gw',
      privateKey: 'clientPrivKey==',
      allowedIPs: '10.8.1.5/32,192.168.50.0/24',
    } as unknown as ClientRecord;
    const inbound = {
      id: 1,
      tag: 'awg-1',
      remark: 'awg',
      port: 51820,
      protocol: 'amneziawg',
      awgServer: settings.server,
    } as unknown as InboundOption;

    expect(addressLine(buildAmneziaWGClientConfig(client, inbound, 'awg.example.test'))).toBe(
      'Address = 10.8.1.5/32',
    );
    expect(
      addressLine(
        buildAmneziaWGClientConfig(
          client,
          inbound,
          'awg.example.test',
          '',
          '192.168.60.0/24, 10.8.1.6/32',
        ),
      ),
    ).toBe('Address = 10.8.1.6/32');
  });
});
