import { useState } from 'react';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import PsiphonCountries from '@/pages/xray/overrides/PsiphonCountries';
import { HttpUtil, Msg } from '@/utils';

import { chooseSelectOption, renderWithProviders } from './test-utils';

const REGIONS = [
  { code: 'DE', name: 'Germany' },
  { code: 'US', name: 'United States' },
  { code: 'FR', name: 'France' },
  { code: 'JP', name: 'Japan' },
];

interface TestCountry {
  region: string;
  port: number;
  enabled: boolean;
  running: boolean;
  tunnel: { connected: boolean; tunnelCount: number; serverRegion?: string };
  error?: string;
}

const connected = (region: string, port: number): TestCountry => ({
  region,
  port,
  enabled: true,
  running: true,
  tunnel: { connected: true, tunnelCount: 1, serverRegion: region },
});

const connecting = (region: string, port: number): TestCountry => ({
  region,
  port,
  enabled: true,
  running: true,
  tunnel: { connected: false, tunnelCount: 0 },
});

function listOf(...countries: TestCountry[]) {
  return { max: 6, countries };
}

function template(extra: Record<string, unknown> = {}): XraySettingsValue {
  return {
    outbounds: [],
    routing: { rules: [], balancers: [] },
    ...extra,
  } as unknown as XraySettingsValue;
}

type Mutate = (mutator: (tt: XraySettingsValue) => void) => void;

// What XrayPage does: run the mutator on a copy of the template and render again with the result.
function Harness({
  initial,
  onTemplate,
}: {
  initial: XraySettingsValue;
  onTemplate: (tt: XraySettingsValue) => void;
}) {
  const [tt, setTt] = useState(initial);
  const onMutateTemplate: Mutate = (mutator) => {
    const next = JSON.parse(JSON.stringify(tt)) as XraySettingsValue;
    mutator(next);
    setTt(next);
    onTemplate(next);
  };
  return (
    <PsiphonCountries
      open
      regions={REGIONS}
      templateSettings={tt}
      onMutateTemplate={onMutateTemplate}
    />
  );
}

// Renders the countries on a template and returns it as it stands after what the component did to it.
function renderCountries(view: ReturnType<typeof listOf>, initial: XraySettingsValue = template()) {
  vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', view));
  let current = initial;
  renderWithProviders(<Harness initial={initial} onTemplate={(tt) => (current = tt)} />);
  return { template: () => current };
}

// The names also stand in the dropdown of the picker, so a row is looked for inside the table.
const inTable = () => within(screen.getByRole('table'));
const rowOf = (text: string) => inTable().getByText(text).closest('tr') as HTMLElement;

// The confirmations of the tabs are dialogs of their own, outside the table.
const dialog = () => screen.findByRole('dialog');

function outboundOf(region: string, port: number) {
  return {
    tag: `psiphon-${region}`,
    protocol: 'socks',
    settings: { servers: [{ address: '127.0.0.1', port }] },
  };
}

// A template with the outbound of DE and a rule that has nowhere else to go.
function templateWithARuleOnDE() {
  const tt = template({
    outbounds: [outboundOf('DE', 19101), { tag: 'direct', protocol: 'freedom' }],
  });
  (tt.routing as unknown as { rules: unknown[] }).rules = [
    { type: 'field', domain: ['example.com'], outboundTag: 'psiphon-DE' },
  ];
  return tt;
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe('PsiphonCountries', () => {
  it('lists the countries with how each tunnel stands and how many there may be', async () => {
    renderCountries(listOf(connected('DE', 19101), connecting('US', 19102)));

    await screen.findByText('Germany (DE)');
    expect(within(rowOf('Germany (DE)')).getByText('Connected (DE)')).toBeTruthy();
    expect(within(rowOf('Germany (DE)')).getByText('19101')).toBeTruthy();
    expect(within(rowOf('United States (US)')).getByText('Connecting')).toBeTruthy();
    expect(screen.getByText('2 of 6')).toBeTruthy();
  });

  it('tells a start nobody retries from a tunnel the supervisor will start again, and from a stopped one', async () => {
    const down = { running: false };
    const failed: TestCountry = {
      ...connected('DE', 19101),
      ...down,
      enabled: false,
      error: 'port 19101 is held by another process',
    };
    const waiting: TestCountry = { ...connected('US', 19102), ...down };
    // It crashed, and the last thing the supervisor knows about it is how: still a tunnel it will start again.
    const crashed: TestCountry = { ...connected('FR', 19103), ...down, error: 'exit status 2' };
    const stopped: TestCountry = { ...connected('JP', 19104), ...down, enabled: false };
    renderCountries(listOf(failed, waiting, crashed, stopped));

    await screen.findByText('Germany (DE)');
    expect(within(rowOf('Germany (DE)')).getByText('Failed')).toBeTruthy();
    expect(within(rowOf('United States (US)')).getByText('Restarting')).toBeTruthy();
    expect(within(rowOf('France (FR)')).getByText('Restarting')).toBeTruthy();
    expect(within(rowOf('Japan (JP)')).getByText('Stopped')).toBeTruthy();
  });

  it('adds a country and starts it at once', async () => {
    renderCountries(listOf());
    const post = vi
      .spyOn(HttpUtil, 'post')
      .mockResolvedValue(new Msg(true, '', listOf(connecting('FR', 19101))));
    await screen.findByText('0 of 6');

    chooseSelectOption('psiphon-add-country', 'France (FR)');
    fireEvent.click(screen.getByRole('button', { name: /^Add$/ }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post.mock.calls[0]).toEqual(['/panel/api/xray/psiphon/countries/add', { region: 'FR' }]);
    expect(post.mock.calls[1]).toEqual([
      '/panel/api/xray/psiphon/countries/start',
      { region: 'FR' },
    ]);
    expect(await inTable().findByText('France (FR)')).toBeTruthy();
  });

  it('keeps the list that comes back with a failed start, so the row says why', async () => {
    renderCountries(listOf());
    const failedRow: TestCountry = {
      ...connected('FR', 19101),
      enabled: false,
      running: false,
      error: 'port 19101 is held by another process',
    };
    const post = vi
      .spyOn(HttpUtil, 'post')
      .mockResolvedValueOnce(new Msg(true, '', listOf({ ...failedRow, error: undefined })))
      .mockResolvedValueOnce(
        new Msg(false, 'FR: port 19101 is held by another process', listOf(failedRow)),
      );
    await screen.findByText('0 of 6');

    chooseSelectOption('psiphon-add-country', 'France (FR)');
    fireEvent.click(screen.getByRole('button', { name: /^Add$/ }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    await inTable().findByText('France (FR)');
    expect(within(rowOf('France (FR)')).getByText('Failed')).toBeTruthy();
  });

  it('switches the outbound of a country on with the port of its tunnel, and off again', async () => {
    const result = renderCountries(listOf(connected('DE', 19101)));
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('switch', { name: 'Outbound psiphon-DE' }));
    expect(result.template().outbounds).toEqual([
      {
        tag: 'psiphon-DE',
        protocol: 'socks',
        settings: { servers: [{ address: '127.0.0.1', port: 19101 }] },
      },
    ]);

    fireEvent.click(screen.getByRole('switch', { name: 'Outbound psiphon-DE' }));
    fireEvent.click(within(await dialog()).getByRole('button', { name: 'Delete' }));
    expect(result.template().outbounds).toEqual([]);
  });

  it('asks before the switch drops an outbound even when no rule names it, and keeps it when asked not to', async () => {
    const plain = template({ outbounds: [outboundOf('DE', 19101)] });
    const result = renderCountries(listOf(connected('DE', 19101)), plain);
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('switch', { name: 'Outbound psiphon-DE' }));
    const asked = await dialog();
    expect(screen.getByRole('dialog', { name: 'Remove the outbound psiphon-DE?' })).toBe(asked);
    expect(result.template().outbounds).toHaveLength(1);

    fireEvent.click(within(asked).getByRole('button', { name: 'Cancel' }));
    expect(result.template().outbounds).toHaveLength(1);
  });

  it('offers to fix an outbound that points at another port than its tunnel', async () => {
    const stale = template({
      outbounds: [
        {
          tag: 'psiphon-DE',
          protocol: 'socks',
          settings: { servers: [{ address: '127.0.0.1', port: 19155 }] },
        },
      ],
    });
    const result = renderCountries(listOf(connected('DE', 19101)), stale);
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Fix the port' }));
    const outbound = result.template().outbounds?.[0] as {
      settings: { servers: { port: number }[] };
    };
    expect(outbound.settings.servers[0].port).toBe(19101);
    expect(screen.queryByRole('button', { name: 'Fix the port' })).toBeNull();
  });

  it('adds the balancer on the prefix of the country outbounds and the monitor it needs', async () => {
    const result = renderCountries(listOf(connected('DE', 19101)));
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Add the balancer' }));
    const after = result.template() as unknown as {
      routing: {
        balancers: { tag: string; selector: string[]; strategy: { type: string } }[];
      };
      observatory?: { subjectSelector: string[] };
    };
    expect(after.routing.balancers).toEqual([
      { tag: 'psiphon-balancer', selector: ['psiphon-'], strategy: { type: 'leastPing' } },
    ]);
    expect(after.observatory?.subjectSelector).toEqual(['psiphon-']);
  });

  it('takes the balancer out together with the monitor nothing else needs', async () => {
    const withBalancer = template({
      observatory: {
        subjectSelector: ['psiphon-'],
        probeURL: 'https://www.google.com/generate_204',
        probeInterval: '1m',
        enableConcurrency: true,
      },
    });
    (withBalancer.routing as unknown as { balancers: unknown[] }).balancers = [
      { tag: 'psiphon-balancer', selector: ['psiphon-'], strategy: { type: 'leastPing' } },
    ];
    const result = renderCountries(listOf(connected('DE', 19101)), withBalancer);
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    fireEvent.click(within(await dialog()).getByRole('button', { name: 'Delete' }));
    const after = result.template() as unknown as {
      routing: { balancers: unknown[] };
      observatory?: unknown;
    };
    expect(after.routing.balancers).toEqual([]);
    expect(after.observatory).toBeUndefined();
  });

  it('removes a country with its outbound and what refers to that outbound', async () => {
    const result = renderCountries(listOf(connected('DE', 19101)), templateWithARuleOnDE());
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', listOf()));
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Remove the country' }));
    const asked = await dialog();
    expect(within(asked).getByText(/removed \(no destination left\)/)).toBeTruthy();
    fireEvent.click(within(asked).getByRole('button', { name: 'Delete' }));

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/panel/api/xray/psiphon/countries/remove', {
        region: 'DE',
      }),
    );
    await waitFor(() =>
      expect((result.template().outbounds ?? []).map((o) => o?.tag)).toEqual(['direct']),
    );
    expect((result.template().routing as unknown as { rules: unknown[] }).rules).toEqual([]);
  });

  it('shows what goes with an outbound before its switch takes it out, and keeps it when asked not to', async () => {
    const result = renderCountries(listOf(connected('DE', 19101)), templateWithARuleOnDE());
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('switch', { name: 'Outbound psiphon-DE' }));
    const asked = await dialog();
    expect(within(asked).getByText(/Deleting this also updates your routing/)).toBeTruthy();
    expect(within(asked).getByText(/removed \(no destination left\)/)).toBeTruthy();
    expect(result.template().outbounds).toHaveLength(2);

    fireEvent.click(within(asked).getByRole('button', { name: 'Cancel' }));
    expect(result.template().outbounds).toHaveLength(2);
    expect((result.template().routing as unknown as { rules: unknown[] }).rules).toHaveLength(1);
  });

  it('takes the outbound and the rules that lead only to it out once its switch is confirmed', async () => {
    const result = renderCountries(listOf(connected('DE', 19101)), templateWithARuleOnDE());
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('switch', { name: 'Outbound psiphon-DE' }));
    fireEvent.click(within(await dialog()).getByRole('button', { name: 'Delete' }));

    expect((result.template().outbounds ?? []).map((o) => o?.tag)).toEqual(['direct']);
    expect((result.template().routing as unknown as { rules: unknown[] }).rules).toEqual([]);
  });

  it('does not delete the balancer that another balancer falls back to', async () => {
    const used = template({
      outbounds: [{ tag: 'direct', protocol: 'freedom' }],
    });
    (used.routing as unknown as { balancers: unknown[] }).balancers = [
      { tag: 'psiphon-balancer', selector: ['psiphon-'], strategy: { type: 'leastPing' } },
      { tag: 'main', selector: ['direct'], fallbackTag: '_bl_psiphon-balancer' },
    ];
    const result = renderCountries(listOf(connected('DE', 19101)), used);
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));

    expect(await screen.findByText(/it is used as fallback by: main/)).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
    const after = result.template().routing as unknown as { balancers: { tag: string }[] };
    expect(after.balancers.map((b) => b.tag)).toEqual(['psiphon-balancer', 'main']);
  });

  it('takes the loopback of the balancer along when it deletes the balancer', async () => {
    const withLoopback = template({
      outbounds: [
        { tag: 'direct', protocol: 'freedom' },
        {
          tag: '_bl_psiphon-balancer',
          protocol: 'loopback',
          settings: { inboundTag: '_bl_psiphon-balancer' },
        },
      ],
    });
    (withLoopback.routing as unknown as { balancers: unknown[]; rules: unknown[] }).balancers = [
      { tag: 'psiphon-balancer', selector: ['psiphon-'], strategy: { type: 'leastPing' } },
    ];
    (withLoopback.routing as unknown as { rules: unknown[] }).rules = [
      { type: 'field', inboundTag: ['_bl_psiphon-balancer'], balancerTag: 'psiphon-balancer' },
    ];
    const result = renderCountries(listOf(connected('DE', 19101)), withLoopback);
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    fireEvent.click(within(await dialog()).getByRole('button', { name: 'Delete' }));

    const after = result.template();
    expect((after.outbounds ?? []).map((o) => o?.tag)).toEqual(['direct']);
    expect((after.routing as unknown as { rules: unknown[] }).rules).toEqual([]);
  });

  it('does not put a removed country back when an older answer lands late', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let late: (answer: Msg<unknown>) => void = () => {};
    const get = vi
      .spyOn(HttpUtil, 'get')
      .mockResolvedValueOnce(new Msg(true, '', listOf(connecting('DE', 19101))))
      .mockImplementationOnce(
        () =>
          new Promise<Msg<unknown>>((resolve) => {
            late = resolve;
          }),
      );
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', listOf()));
    renderWithProviders(<Harness initial={template()} onTemplate={() => {}} />);
    await screen.findByText('Connecting');

    // The poll leaves, and its answer is held back while the country is removed.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    expect(get).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole('button', { name: 'Remove the country' }));
    fireEvent.click(within(await dialog()).getByRole('button', { name: 'Delete' }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/panel/api/xray/psiphon/countries/remove', {
        region: 'DE',
      }),
    );
    await screen.findByText('0 of 6');

    await act(async () => {
      late(new Msg(true, '', listOf(connecting('DE', 19101))));
    });
    expect(screen.getByText('0 of 6')).toBeTruthy();
    expect(screen.queryByText('Germany (DE)')).toBeNull();
  });

  it('makes the routing the balancer needs when the template has none', async () => {
    const bare = { outbounds: [] } as unknown as XraySettingsValue;
    const result = renderCountries(listOf(connected('DE', 19101)), bare);
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Add the balancer' }));

    const routing = result.template().routing as unknown as { balancers: { tag: string }[] };
    expect(routing.balancers.map((b) => b.tag)).toEqual(['psiphon-balancer']);
  });

  it('reads the list again while a tunnel waits for the supervisor, and shows it up again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const waiting: TestCountry = {
      ...connected('DE', 19101),
      running: false,
      error: 'exit status 2',
    };
    const get = vi
      .spyOn(HttpUtil, 'get')
      .mockResolvedValueOnce(new Msg(true, '', listOf(waiting)))
      .mockResolvedValue(new Msg(true, '', listOf(connected('DE', 19101))));
    renderWithProviders(<Harness initial={template()} onTemplate={() => {}} />);
    await screen.findByText('Restarting');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    await screen.findByText('Connected (DE)');
    expect(get).toHaveBeenCalledTimes(2);
  });

  it('reads the list again while a tunnel is connecting, and stops once all are up', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const get = vi
      .spyOn(HttpUtil, 'get')
      .mockResolvedValueOnce(new Msg(true, '', listOf(connecting('DE', 19101))))
      .mockResolvedValue(new Msg(true, '', listOf(connected('DE', 19101))));
    renderWithProviders(<Harness initial={template()} onTemplate={() => {}} />);
    await screen.findByText('Connecting');
    expect(get).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    await screen.findByText('Connected (DE)');
    expect(get).toHaveBeenCalledTimes(2);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(get).toHaveBeenCalledTimes(2);
  });

  it('probes the running countries and shows the latency and the exit', async () => {
    renderCountries(listOf(connected('DE', 19101), { ...connected('US', 19102), running: false }));
    // The server answers for every country, the one that is down with a reason.
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(
      new Msg(true, '', [
        { region: 'DE', latencyMs: 62, ip: '172.104.225.141', country: 'DE' },
        { region: 'US', latencyMs: 0, error: 'not running' },
      ]),
    );
    await screen.findByText('Germany (DE)');

    fireEvent.click(screen.getByRole('button', { name: 'Probe all' }));

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/panel/api/xray/psiphon/countries/probe', {}),
    );
    expect(await screen.findByText(/62 ms · 172\.104\.225\.141 \(DE\)/)).toBeTruthy();
    expect(screen.queryByText('No answer')).toBeNull();
  });
});
