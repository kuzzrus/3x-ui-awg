import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import PsiphonModal from '@/pages/xray/overrides/PsiphonModal';
import type { PsiphonRegion } from '@/pages/xray/overrides/psiphon-regions';
import { HttpUtil, Msg } from '@/utils';

import { listSelectOptions, renderWithProviders } from './test-utils';

const UNTOLD: PsiphonRegion[] = [
  { code: 'AQ', name: 'Antarctica' },
  { code: 'DE', name: 'Germany' },
  { code: 'FR', name: 'France' },
];
const TOLD: PsiphonRegion[] = [
  { code: 'AQ', name: 'Antarctica', available: false },
  { code: 'DE', name: 'Germany', available: true },
  { code: 'FR', name: 'France', available: true },
];

const status = (running: boolean, connected: boolean) => ({
  installed: true,
  configured: true,
  defaultConfig: false,
  running,
  port: 19060,
  tunnel: { connected, tunnelCount: connected ? 1 : 0 },
});

const country = (connected: boolean) => ({
  region: 'DE',
  port: 19101,
  enabled: true,
  running: true,
  tunnel: { connected, tunnelCount: connected ? 1 : 0, serverRegion: 'DE' },
});

// A panel whose tunnels say which regions have servers only once they are up, as the real one does.
function panel(tunnels: { default: boolean; countries: boolean }) {
  const state = { up: false };
  vi.spyOn(HttpUtil, 'get').mockImplementation((async (url: string) => {
    if (url.endsWith('/regions')) return new Msg(true, '', state.up ? TOLD : UNTOLD);
    return new Msg(true, '', {
      max: 6,
      countries: tunnels.countries ? [country(state.up)] : [],
    });
  }) as never);
  vi.spyOn(HttpUtil, 'post').mockImplementation(
    (async (url: string) =>
      new Msg(
        true,
        '',
        url.endsWith('/status') ? status(tunnels.default, tunnels.default && state.up) : null,
      )) as never,
  );
  return {
    connect: () => {
      state.up = true;
    },
  };
}

function renderModal() {
  renderWithProviders(
    <PsiphonModal
      open
      templateSettings={{ outbounds: [], routing: { rules: [] } } as unknown as XraySettingsValue}
      onClose={() => {}}
      onAddOutbound={() => {}}
      onRemoveOutbound={() => {}}
      onMutateTemplate={() => {}}
    />,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('the regions in the Psiphon window', () => {
  it('marks the regions without servers in the egress picker once the default tunnel has connected', async () => {
    const tunnels = panel({ default: true, countries: false });
    renderModal();
    await screen.findByText('No active tunnel yet');
    expect(listSelectOptions('psiphon-egress-region')).toEqual([
      'Auto (best server)',
      'Antarctica (AQ)',
      'Germany (DE)',
      'France (FR)',
    ]);

    tunnels.connect();
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));

    await screen.findByText('Tunnel connected');
    await waitFor(() =>
      expect(listSelectOptions('psiphon-egress-region')).toEqual([
        'Auto (best server)',
        'Germany (DE)',
        'France (FR)',
        'Antarctica (AQ) — no servers',
      ]),
    );
  });

  it('marks them in the picker of the countries once a country tunnel has connected', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const tunnels = panel({ default: false, countries: true });
    renderModal();
    await screen.findByText('Connecting');
    // Germany is a country of the list already, so the picker offers the other two.
    expect(listSelectOptions('psiphon-add-country')).toEqual(['Antarctica (AQ)', 'France (FR)']);

    tunnels.connect();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });

    await screen.findByText('Connected (DE)');
    await waitFor(() =>
      expect(listSelectOptions('psiphon-add-country')).toEqual([
        'France (FR)',
        'Antarctica (AQ) — no servers',
      ]),
    );
  });
});
