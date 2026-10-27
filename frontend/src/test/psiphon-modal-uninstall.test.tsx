import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import PsiphonModal from '@/pages/xray/overrides/PsiphonModal';
import { HttpUtil, Msg } from '@/utils';

import { renderWithProviders } from './test-utils';

const installed = {
  installed: true,
  configured: true,
  defaultConfig: false,
  running: false,
  port: 19060,
  tunnel: { connected: false, tunnelCount: 0 },
};

// The default outbound, one of a country, the balancer over the countries, and the rules and the monitor that go with them.
function template(extra: Record<string, unknown> = {}): XraySettingsValue {
  return {
    outbounds: [
      { tag: 'psiphon', protocol: 'socks' },
      { tag: 'psiphon-DE', protocol: 'socks' },
      { tag: 'direct', protocol: 'freedom' },
    ],
    routing: {
      rules: [
        { type: 'field', domain: ['a.example'], outboundTag: 'psiphon-DE' },
        { type: 'field', domain: ['b.example'], balancerTag: 'psiphon-balancer' },
        { type: 'field', domain: ['c.example'], outboundTag: 'direct' },
      ],
      balancers: [
        { tag: 'psiphon-balancer', selector: ['psiphon-'], strategy: { type: 'leastPing' } },
      ],
    },
    observatory: { subjectSelector: ['psiphon-'] },
    ...extra,
  } as unknown as XraySettingsValue;
}

// Opens the window on a template, asks to uninstall, and returns the template as the confirmation left it.
async function uninstall(tt: XraySettingsValue) {
  vi.spyOn(HttpUtil, 'get').mockImplementation((async (url: string) =>
    url.endsWith('/regions')
      ? new Msg(true, '', [])
      : new Msg(true, '', { max: 6, countries: [] })) as never);
  const post = vi
    .spyOn(HttpUtil, 'post')
    .mockImplementation(
      (async (url: string) =>
        new Msg(true, '', url.endsWith('/status') ? installed : null)) as never,
    );
  const current = tt;
  const onMutateTemplate = vi.fn((mutator: (t: XraySettingsValue) => void) => mutator(current));
  renderWithProviders(
    <PsiphonModal
      open
      templateSettings={tt}
      onClose={() => {}}
      onAddOutbound={() => {}}
      onRemoveOutbound={() => {}}
      onMutateTemplate={onMutateTemplate}
    />,
  );

  fireEvent.click(await screen.findByRole('button', { name: 'Uninstall' }));
  const popover = (await screen.findByText(/Stop Psiphon and remove the binary/)).closest(
    '.ant-popover',
  ) as HTMLElement;
  fireEvent.click(within(popover).getByRole('button', { name: 'Delete' }));
  await waitFor(() => expect(post).toHaveBeenCalledWith('/panel/api/xray/psiphon/uninstall'));
  await waitFor(() => expect(onMutateTemplate).toHaveBeenCalled());
  return { template: current, popover };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('uninstalling Psiphon from its window', () => {
  it('takes every outbound of Psiphon, the balancer over the countries, and what refers to them out of the template', async () => {
    const { template: after } = await uninstall(template());

    expect((after.outbounds ?? []).map((o) => o?.tag)).toEqual(['direct']);
    expect(after.routing?.balancers).toEqual([]);
    expect(after.routing?.rules?.map((r) => r.domain)).toEqual([['c.example']]);
    expect(after.observatory).toBeUndefined();
  });

  it('lists what goes with them before it asks', async () => {
    const { popover } = await uninstall(template());

    expect(within(popover).getByText(/Deleting this also updates your routing/)).toBeTruthy();
    expect(within(popover).getAllByText(/removed \(no destination left\)/)).toHaveLength(2);
  });

  it('lets go of a balancer that falls back to the one of the countries, and of the loopback it made', async () => {
    const tt = template();
    tt.outbounds?.push({
      tag: '_bl_psiphon-balancer',
      protocol: 'loopback',
      settings: { inboundTag: '_bl_psiphon-balancer' },
    } as never);
    tt.routing?.balancers?.push({
      tag: 'main',
      selector: ['direct'],
      fallbackTag: '_bl_psiphon-balancer',
    } as never);
    tt.routing?.rules?.push({
      type: 'field',
      inboundTag: ['_bl_psiphon-balancer'],
      balancerTag: 'psiphon-balancer',
    } as never);

    const { template: after, popover } = await uninstall(tt);

    expect(within(popover).getByText(/Balancer main — its fallback is cleared/)).toBeTruthy();
    expect((after.outbounds ?? []).map((o) => o?.tag)).toEqual(['direct']);
    expect(after.routing?.balancers).toEqual([
      { tag: 'main', selector: ['direct'], fallbackTag: '' },
    ]);
    expect(after.routing?.rules?.map((r) => r.domain)).toEqual([['c.example']]);
  });

  it('plans from the template as the uninstall will leave it: the burst monitor goes, the loopback rule is not listed', async () => {
    // The fallback to the balancer of Psiphon is what keeps the burst monitor, and the loopback rule is the panel's own.
    const tt = {
      outbounds: [
        { tag: 'psiphon', protocol: 'socks' },
        { tag: 'direct', protocol: 'freedom' },
        {
          tag: '_bl_psiphon-balancer',
          protocol: 'loopback',
          settings: { inboundTag: '_bl_psiphon-balancer' },
        },
      ],
      routing: {
        rules: [
          { type: 'field', inboundTag: ['_bl_psiphon-balancer'], balancerTag: 'psiphon-balancer' },
        ],
        balancers: [
          { tag: 'psiphon-balancer', selector: ['psiphon-'], strategy: { type: 'leastPing' } },
          { tag: 'main', selector: ['direct'], fallbackTag: '_bl_psiphon-balancer' },
        ],
      },
      burstObservatory: { subjectSelector: ['direct', 'psiphon-'] },
    } as unknown as XraySettingsValue;

    const { template: after, popover } = await uninstall(tt);

    expect(
      within(popover).getByText(/last balancer using the Burst Observatory, so it will be removed/),
    ).toBeTruthy();
    expect(within(popover).queryByText(/^Rule /)).toBeNull();
    expect(after.burstObservatory).toBeUndefined();
  });
});
