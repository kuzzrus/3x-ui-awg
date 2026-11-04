import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import PsiphonCountries from '@/pages/xray/overrides/PsiphonCountries';
import PsiphonModal from '@/pages/xray/overrides/PsiphonModal';
import PsiphonScan from '@/pages/xray/overrides/PsiphonScan';
import type {
  PsiphonRegion,
  PsiphonScanResult,
  PsiphonScanStatus,
} from '@/pages/xray/overrides/psiphon-regions';
import { HttpUtil, Msg } from '@/utils';

import { chooseSelectOption, listSelectOptions, renderWithProviders } from './test-utils';

const REGIONS: PsiphonRegion[] = [
  { code: 'AQ', name: 'Antarctica', available: false },
  { code: 'DE', name: 'Germany', available: true },
  { code: 'FR', name: 'France', available: true },
  { code: 'JP', name: 'Japan', available: true },
  { code: 'US', name: 'United States', available: true },
];

const de: PsiphonScanResult = {
  region: 'DE',
  connected: true,
  connectMs: 4200,
  latencyMs: 217,
  ip: '203.0.113.7',
  country: 'DE',
};
const us: PsiphonScanResult = {
  region: 'US',
  connected: true,
  connectMs: 3100,
  latencyMs: 62,
  ip: '203.0.113.9',
  country: 'US',
};
const jp: PsiphonScanResult = {
  region: 'JP',
  connected: false,
  error: 'did not connect within 1m0s',
};

const idle: PsiphonScanStatus = { running: false, total: 0, current: [], results: [] };
const finished: PsiphonScanStatus = {
  running: false,
  total: 3,
  current: [],
  startedAt: 1793650000,
  finishedAt: 1793650044,
  results: [jp, de, us],
};

function renderScan(
  scan: PsiphonScanStatus | null,
  props: { onStart?: (all: boolean) => void; onCancel?: () => void } = {},
) {
  renderWithProviders(
    <PsiphonScan
      scan={scan}
      regions={REGIONS}
      busy={false}
      onStart={props.onStart ?? (() => {})}
      onCancel={props.onCancel ?? (() => {})}
    />,
  );
}

// The names also stand in the pickers and in the table of the countries, so a row is looked for by its cell.
const rowOf = (text: string) =>
  screen.getByText(text, { selector: 'td' }).closest('tr') as HTMLElement;

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('PsiphonScan', () => {
  it('lists what the scan found, the fastest region first and the ones that never connected last', () => {
    renderScan(finished);

    const names = within(screen.getByRole('table'))
      .getAllByRole('row')
      .slice(1)
      .map((row) => within(row).getAllByRole('cell')[0].textContent);
    expect(names).toEqual(['United States (US)', 'Germany (DE)', 'Japan (JP)']);

    const first = within(rowOf('United States (US)'));
    expect(first.getByText('Connected')).toBeTruthy();
    expect(first.getByText('3.1 s')).toBeTruthy();
    expect(first.getByText('62 ms')).toBeTruthy();
    expect(first.getByText('203.0.113.9 (US)')).toBeTruthy();
    expect(within(rowOf('Japan (JP)')).getByText('No tunnel')).toBeTruthy();
    expect(screen.getByText(/Last scan:/)).toBeTruthy();
    // A short table is not boxed.
    expect(document.querySelector('.ant-table-body')).toBeNull();
  });

  it('says on a region that did not connect now that it did in an earlier scan, and lists those not in this scan', () => {
    const at = 1793000000;
    renderScan({
      ...finished,
      seen: [
        { region: 'JP', at, latencyMs: 40 },
        { region: 'FR', at: at + 5 },
        { region: 'DE', at },
      ],
    });

    const date = new Date(at * 1000).toLocaleDateString();
    expect(within(rowOf('Japan (JP)')).getByText('No tunnel')).toBeTruthy();
    expect(within(rowOf('Japan (JP)')).getByText(`Connected ${date}`)).toBeTruthy();
    expect(within(rowOf('Germany (DE)')).queryByText(`Connected ${date}`)).toBeNull();
    expect(
      screen.getByText(/Connected in an earlier scan, not in this one: France \(FR\)$/),
    ).toBeTruthy();
  });

  it('gives the moment and the measurements of the earlier success in the tooltip of the tag of the day', async () => {
    const at = 1793000000;
    renderScan({
      ...finished,
      seen: [
        {
          region: 'JP',
          at,
          connectMs: 7000,
          latencyMs: 40,
          ip: '203.0.113.5',
          country: 'JP',
        },
      ],
    });

    fireEvent.mouseEnter(within(rowOf('Japan (JP)')).getByText(/^Connected \d/));

    await waitFor(() => {
      const text = document.body.textContent ?? '';
      expect(text).toContain(new Date(at * 1000).toLocaleString());
      expect(text).toContain('7.0 s');
      expect(text).toContain('40 ms');
      expect(text).toContain('203.0.113.5 (JP)');
    });
  });

  it('does not list the regions that connected before when the scan could not look at any region', () => {
    renderScan({
      ...idle,
      error: 'could not learn which regions have servers: no list within 15s',
      finishedAt: 1793650000,
      seen: [{ region: 'FR', at: 1793000000 }],
    });

    expect(screen.getByText(/could not learn which regions/)).toBeTruthy();
    expect(screen.queryByText(/Connected in an earlier scan/)).toBeNull();
  });

  it('does not call the regions that connected before left out while the scan is still going through them', () => {
    renderScan({
      running: true,
      total: 3,
      current: ['DE'],
      startedAt: 1,
      results: [us],
      seen: [{ region: 'FR', at: 1793000000 }],
    });

    expect(screen.queryByText(/Connected in an earlier scan/)).toBeNull();
  });

  it('shows how far a scan has come, what it is scanning now, and offers to cancel it', () => {
    const onCancel = vi.fn();
    renderScan(
      { running: true, total: 3, current: ['JP'], startedAt: 1, results: [us] },
      { onCancel },
    );

    expect(screen.getByText('1 of 3 regions')).toBeTruthy();
    expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('33');
    expect(screen.getByText('Scanning: JP')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Scan regions' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Scan all regions' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel the scan' }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it('lists a region that connected but could not be timed after the ones that were', () => {
    const untimed: PsiphonScanResult = {
      region: 'FR',
      connected: true,
      connectMs: 2000,
      error: 'no answer through the tunnel: timeout',
    };
    renderScan({ ...finished, results: [untimed, de, us] });

    const names = within(screen.getByRole('table'))
      .getAllByRole('row')
      .slice(1)
      .map((row) => within(row).getAllByRole('cell')[0].textContent);
    expect(names).toEqual(['United States (US)', 'Germany (DE)', 'France (FR)']);
  });

  it('says on the latency of a region that connected why it could not be timed', async () => {
    renderScan({
      ...finished,
      results: [
        {
          region: 'FR',
          connected: true,
          connectMs: 2000,
          error: 'no answer through the tunnel: timeout',
        },
        us,
      ],
    });

    const latency = within(rowOf('France (FR)')).getAllByRole('cell')[3];
    fireEvent.mouseEnter(within(latency).getByText('—'));

    await waitFor(() =>
      expect(document.body.textContent).toContain('no answer through the tunnel: timeout'),
    );
  });

  it('offers to scan when none runs, and asks for it', () => {
    const onStart = vi.fn();
    renderScan(idle, { onStart });

    expect(screen.queryByRole('table')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Scan regions' }));
    expect(onStart).toHaveBeenCalledTimes(1);
    expect(onStart).toHaveBeenCalledWith(false);
  });

  it('offers to scan every region too, and asks for it', () => {
    const onStart = vi.fn();
    renderScan(idle, { onStart });

    fireEvent.click(screen.getByRole('button', { name: 'Scan all regions' }));

    expect(onStart).toHaveBeenCalledTimes(1);
    expect(onStart).toHaveBeenCalledWith(true);
  });

  it('keeps a long table in a box that scrolls, with its header in view', () => {
    const many: PsiphonScanResult[] = Array.from({ length: 26 }, (_, i) => ({
      region: `A${String.fromCharCode(65 + i)}`,
      connected: false,
      error: 'did not connect within 1m0s',
    }));
    renderScan({ ...finished, total: 26, results: many });

    expect(document.querySelector('.ant-table-body')).not.toBeNull();
  });

  it('says it is still asking which regions have servers, instead of showing a count of none', () => {
    renderScan({
      running: true,
      discovering: true,
      total: 0,
      current: [],
      startedAt: 1,
      results: [],
    });

    expect(screen.getByText('Asking Psiphon which regions have servers…')).toBeTruthy();
    expect(screen.queryByText(/^\d+ of \d+ regions$/)).toBeNull();
  });

  it('says why a scan could not go on, and that a scan was cancelled', () => {
    renderScan({
      ...idle,
      cancelled: true,
      error: 'could not learn which regions have servers: no list within 45s',
      startedAt: 1,
      finishedAt: 2,
    });

    expect(
      screen.getByText('could not learn which regions have servers: no list within 45s'),
    ).toBeTruthy();
    expect(screen.getByText(/The scan was cancelled/)).toBeTruthy();
  });
});

const tunnelStatus = {
  installed: true,
  configured: true,
  defaultConfig: false,
  running: true,
  port: 19060,
  tunnel: { connected: true, tunnelCount: 1 },
};

// A panel whose scan runs on request: the list of regions and the status of the scan are what the test says they are.
function scanningPanel() {
  const state = { scan: idle, regions: REGIONS, status: tunnelStatus };
  const get = vi.spyOn(HttpUtil, 'get').mockImplementation((async (url: string) => {
    if (url.endsWith('/regions')) return new Msg(true, '', state.regions);
    if (url.endsWith('/scan')) return new Msg(true, '', state.scan);
    return new Msg(true, '', { max: 6, countries: [] });
  }) as never);
  const post = vi.spyOn(HttpUtil, 'post').mockImplementation((async (url: string) => {
    if (url.endsWith('/scan/start')) {
      state.scan = {
        running: true,
        total: 3,
        current: ['DE', 'JP'],
        startedAt: 1793650000,
        results: [us],
      };
      return new Msg(true, '', state.scan);
    }
    if (url.endsWith('/scan/cancel')) {
      state.scan = {
        ...state.scan,
        running: false,
        cancelled: true,
        current: [],
        finishedAt: 1793650010,
      };
      return new Msg(true, '', state.scan);
    }
    if (url.endsWith('/uninstall')) {
      // The panel forgets the last scan and the list of regions with servers together with the install.
      state.scan = idle;
      state.regions = REGIONS.map(({ code, name }) => ({ code, name }));
      state.status = { ...state.status, installed: false, configured: false, running: false };
      return new Msg(true, '', null);
    }
    if (url.endsWith('/install')) {
      state.status = { ...state.status, installed: true, configured: true };
      return new Msg(true, '', null);
    }
    return new Msg(true, '', url.endsWith('/status') ? state.status : null);
  }) as never);
  const scanReads = () => get.mock.calls.filter(([url]) => String(url).endsWith('/scan')).length;
  return { state, get, post, scanReads };
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

describe('the scan in the Psiphon window', () => {
  it('starts a scan, follows it, and puts the regions that answered fastest first in the pickers', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const panel = scanningPanel();
    renderModal();
    await screen.findByText('Tunnel connected');
    expect(listSelectOptions('psiphon-egress-region')).toEqual([
      'Auto (best server)',
      'Germany (DE)',
      'France (FR)',
      'Japan (JP)',
      'United States (US)',
      'Antarctica (AQ) — no servers',
    ]);

    fireEvent.click(await screen.findByRole('button', { name: 'Scan regions' }));
    await screen.findByText('Scanning: DE, JP');
    expect(panel.post).toHaveBeenCalledWith('/panel/api/xray/psiphon/scan/start', {});
    expect(screen.getByText('1 of 3 regions')).toBeTruthy();

    panel.state.scan = finished;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });

    await screen.findByText(/Last scan:/);
    await waitFor(() =>
      expect(listSelectOptions('psiphon-egress-region')).toEqual([
        'Auto (best server)',
        'United States (US) — 62 ms',
        'Germany (DE) — 217 ms',
        'France (FR)',
        'Japan (JP) — no tunnel in the last scan',
        'Antarctica (AQ) — no servers',
      ]),
    );
    expect(screen.queryByText(/^\d+ of \d+ regions$/)).toBeNull();
    // A scan that has ended is not asked about any more.
    const reads = panel.scanReads();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(7000);
    });
    expect(panel.scanReads()).toBe(reads);
  });

  it('starts a scan of every region of the picker when asked for it', async () => {
    const panel = scanningPanel();
    renderModal();

    fireEvent.click(await screen.findByRole('button', { name: 'Scan all regions' }));

    await screen.findByText('Scanning: DE, JP');
    expect(panel.post).toHaveBeenCalledWith('/panel/api/xray/psiphon/scan/start', {
      regions: 'all',
    });
  });

  it('reads the regions again when a scan has ended, for the list a scan that had to ask has left behind', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const panel = scanningPanel();
    // No tunnel has said yet which regions have servers, so none is marked.
    panel.state.regions = REGIONS.map(({ code, name }) => ({ code, name }));
    renderModal();
    await screen.findByText('Tunnel connected');
    fireEvent.click(await screen.findByRole('button', { name: 'Scan regions' }));
    await screen.findByText('Scanning: DE, JP');
    expect(listSelectOptions('psiphon-egress-region')).toContain('Antarctica (AQ)');

    panel.state.regions = REGIONS;
    panel.state.scan = finished;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });

    await waitFor(() =>
      expect(listSelectOptions('psiphon-egress-region')).toEqual([
        'Auto (best server)',
        'United States (US) — 62 ms',
        'Germany (DE) — 217 ms',
        'France (FR)',
        'Japan (JP) — no tunnel in the last scan',
        'Antarctica (AQ) — no servers',
      ]),
    );
  });

  it('hands the last scan to the picker of the countries too', async () => {
    const panel = scanningPanel();
    panel.state.scan = finished;
    renderModal();
    await screen.findByText(/Last scan:/);

    expect(listSelectOptions('psiphon-add-country')).toEqual([
      'United States (US) — 62 ms',
      'Germany (DE) — 217 ms',
      'France (FR)',
      'Japan (JP) — no tunnel in the last scan',
      'Antarctica (AQ) — no servers',
    ]);
  });

  it('hands the regions that connected in earlier scans to the picker of the egress region', async () => {
    const panel = scanningPanel();
    const at = 1793000000;
    panel.state.scan = { ...finished, seen: [{ region: 'JP', at, latencyMs: 40 }] };
    renderModal();
    await screen.findByText(/Last scan:/);

    const date = new Date(at * 1000).toLocaleDateString();
    await waitFor(() =>
      expect(listSelectOptions('psiphon-egress-region')).toContain(
        `Japan (JP) — no tunnel in the last scan, connected ${date}`,
      ),
    );
  });

  it('hands the regions that connected in earlier scans to the picker of the countries', async () => {
    const panel = scanningPanel();
    const at = 1793000000;
    panel.state.scan = { ...finished, seen: [{ region: 'JP', at, latencyMs: 40 }] };
    renderModal();
    await screen.findByText(/Last scan:/);

    const date = new Date(at * 1000).toLocaleDateString();
    await waitFor(() =>
      expect(listSelectOptions('psiphon-add-country')).toContain(
        `Japan (JP) — no tunnel in the last scan, connected ${date}`,
      ),
    );
  });

  it('ignores a status that was asked for before a cancel and arrives after it', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const panel = scanningPanel();
    const running: PsiphonScanStatus = {
      running: true,
      total: 3,
      current: ['DE'],
      startedAt: 1,
      results: [us],
    };
    panel.state.scan = running;
    renderModal();
    const cancel = await screen.findByRole('button', { name: 'Cancel the scan' });

    // The poll that follows is slow to answer: it will say the scan still runs, as it did when it was asked.
    const answer = panel.get.getMockImplementation() as (url: string) => Promise<Msg>;
    let late: () => void = () => {};
    panel.get.mockImplementation((async (url: string) => {
      if (!url.endsWith('/scan')) return answer(url);
      await new Promise<void>((resolve) => {
        late = resolve;
      });
      return new Msg(true, '', running);
    }) as never);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });
    fireEvent.click(cancel);
    await screen.findByText(/The scan was cancelled/);

    await act(async () => {
      late();
      await vi.advanceTimersByTimeAsync(50);
    });

    expect(screen.getByText(/The scan was cancelled/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Scan regions' })).toBeTruthy();
  });

  it('cancels the scan that runs and shows what it had finished', async () => {
    const panel = scanningPanel();
    panel.state.scan = { running: true, total: 3, current: ['DE'], startedAt: 1, results: [us] };
    renderModal();

    fireEvent.click(await screen.findByRole('button', { name: 'Cancel the scan' }));

    await screen.findByText(/The scan was cancelled/);
    expect(panel.post).toHaveBeenCalledWith('/panel/api/xray/psiphon/scan/cancel', {});
    expect(within(rowOf('United States (US)')).getByText('62 ms')).toBeTruthy();
  });

  it('shows nothing of the scan or the marks of the install before it when Psiphon is installed again', async () => {
    const panel = scanningPanel();
    panel.state.scan = finished;
    // Stopped, so that no tunnel coming up or going away reads the regions again by itself.
    panel.state.status = {
      ...tunnelStatus,
      running: false,
      tunnel: { connected: false, tunnelCount: 0 },
    };
    renderModal();
    await screen.findByText(/Last scan:/);

    fireEvent.click(screen.getByRole('button', { name: 'Uninstall' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    await screen.findByText('Psiphon not installed');
    // The status of the new install is slow to arrive: the one of the old install must not be shown meanwhile.
    const answer = panel.get.getMockImplementation() as (url: string) => Promise<Msg>;
    let land: () => void = () => {};
    panel.get.mockImplementation((async (url: string) => {
      if (!url.endsWith('/scan')) return answer(url);
      await new Promise<void>((resolve) => {
        land = resolve;
      });
      return new Msg(true, '', panel.state.scan);
    }) as never);
    fireEvent.click(screen.getByRole('button', { name: 'Install' }));

    await screen.findByRole('button', { name: 'Scan regions' });
    expect(screen.queryByText(/Last scan:/)).toBeNull();
    await act(async () => {
      land();
    });
    await waitFor(() =>
      expect(listSelectOptions('psiphon-add-country')).toEqual([
        'Antarctica (AQ)',
        'Germany (DE)',
        'France (FR)',
        'Japan (JP)',
        'United States (US)',
      ]),
    );
    expect(screen.queryByText(/Last scan:/)).toBeNull();
  });

  it('reads the regions again when Psiphon is installed again, since the uninstall dropped the marks', async () => {
    const panel = scanningPanel();
    // Stopped and never scanned: nothing but the install itself tells the window that the list of regions has changed.
    panel.state.status = {
      ...tunnelStatus,
      running: false,
      tunnel: { connected: false, tunnelCount: 0 },
    };
    renderModal();
    fireEvent.click(await screen.findByRole('button', { name: 'Uninstall' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Install' }));

    expect(
      await screen.findByText(/the regions where Psiphon has servers are marked here/),
    ).toBeTruthy();
  });

  it('does not take the status of the install that is gone when it answers late', async () => {
    const panel = scanningPanel();
    panel.state.scan = finished;
    // Stopped, so that nothing but the install itself reads anything again.
    panel.state.status = {
      ...tunnelStatus,
      running: false,
      tunnel: { connected: false, tunnelCount: 0 },
    };
    const answer = panel.get.getMockImplementation() as (url: string) => Promise<Msg>;
    const held: Array<() => void> = [];
    panel.get.mockImplementation((async (url: string) => {
      if (!url.endsWith('/scan')) return answer(url);
      const scan = panel.state.scan;
      await new Promise<void>((resolve) => held.push(resolve));
      return new Msg(true, '', scan);
    }) as never);
    renderModal();
    await waitFor(() => expect(held.length).toBe(1));

    fireEvent.click(screen.getByRole('button', { name: 'Uninstall' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));
    await screen.findByText('Psiphon not installed');
    fireEvent.click(screen.getByRole('button', { name: 'Install' }));
    await waitFor(() => expect(held.length).toBe(2));

    // The status asked for before the uninstall answers now, while the one of the new install is still in flight.
    await act(async () => {
      held[0]();
    });

    expect(screen.queryByText(/Last scan:/)).toBeNull();
  });

  it('says why a scan was refused', async () => {
    const panel = scanningPanel();
    panel.post.mockImplementation((async (url: string) =>
      url.endsWith('/scan/start')
        ? new Msg(false, 'a scan is already running', idle)
        : new Msg(true, '', url.endsWith('/status') ? tunnelStatus : null)) as never);
    renderModal();

    fireEvent.click(await screen.findByRole('button', { name: 'Scan regions' }));

    expect(await screen.findByText('a scan is already running')).toBeTruthy();
  });
});

describe('the picker of the countries after a scan', () => {
  it('puts the fastest regions first, then the ones not scanned, then the ones that did not connect', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { max: 6, countries: [] }));
    renderWithProviders(
      <PsiphonCountries
        open
        regions={REGIONS}
        templateSettings={null}
        onMutateTemplate={() => {}}
        onTunnelConnected={() => {}}
        scanned={[jp, de, us]}
      />,
    );
    await screen.findByText('No countries yet. Add one and its tunnel starts at once.');

    expect(listSelectOptions('psiphon-add-country')).toEqual([
      'United States (US) — 62 ms',
      'Germany (DE) — 217 ms',
      'France (FR)',
      'Japan (JP) — no tunnel in the last scan',
      'Antarctica (AQ) — no servers',
    ]);
  });

  it('puts a region that connected in an earlier scan after those that connected now and before those not scanned, and keeps it selectable', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { max: 6, countries: [] }));
    const at = 1793000000;
    renderWithProviders(
      <PsiphonCountries
        open
        regions={REGIONS}
        templateSettings={null}
        onMutateTemplate={() => {}}
        onTunnelConnected={() => {}}
        scanned={[us, jp]}
        seen={[
          { region: 'AQ', at },
          { region: 'JP', at, latencyMs: 40 },
        ]}
      />,
    );
    await screen.findByText('No countries yet. Add one and its tunnel starts at once.');

    const date = new Date(at * 1000).toLocaleDateString();
    expect(listSelectOptions('psiphon-add-country')).toEqual([
      'United States (US) — 62 ms',
      `Japan (JP) — no tunnel in the last scan, connected ${date}`,
      `Antarctica (AQ) — connected ${date}`,
      'Germany (DE)',
      'France (FR)',
    ]);
    chooseSelectOption('psiphon-add-country', `Antarctica (AQ) — connected ${date}`);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Add' }).hasAttribute('disabled')).toBe(false),
    );
  });

  it('puts a region that did not connect after the ones not scanned, wherever it stood in the list', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { max: 6, countries: [] }));
    renderWithProviders(
      <PsiphonCountries
        open
        regions={[REGIONS[3], REGIONS[2]]}
        templateSettings={null}
        onMutateTemplate={() => {}}
        onTunnelConnected={() => {}}
        scanned={[jp]}
      />,
    );
    await screen.findByText('No countries yet. Add one and its tunnel starts at once.');

    expect(listSelectOptions('psiphon-add-country')).toEqual([
      'France (FR)',
      'Japan (JP) — no tunnel in the last scan',
    ]);
  });

  it('keeps a region that connected in the scan selectable although the list said it has no servers', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { max: 6, countries: [] }));
    renderWithProviders(
      <PsiphonCountries
        open
        regions={REGIONS}
        templateSettings={null}
        onMutateTemplate={() => {}}
        onTunnelConnected={() => {}}
        scanned={[{ region: 'AQ', connected: true, latencyMs: 300 }]}
      />,
    );
    await screen.findByText('No countries yet. Add one and its tunnel starts at once.');

    expect(listSelectOptions('psiphon-add-country')[0]).toBe('Antarctica (AQ) — 300 ms');
    chooseSelectOption('psiphon-add-country', 'Antarctica (AQ) — 300 ms');
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Add' }).hasAttribute('disabled')).toBe(false),
    );
  });
});
