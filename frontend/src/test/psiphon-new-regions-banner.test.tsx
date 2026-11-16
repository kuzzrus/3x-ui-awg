import { act, cleanup, fireEvent, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import OutboundsTab from '@/pages/xray/outbounds/OutboundsTab';
import PsiphonNewRegionsBanner from '@/pages/xray/overrides/PsiphonNewRegionsBanner';
import type { PsiphonScanStatus } from '@/pages/xray/overrides/psiphon-regions';
import { HttpUtil, Msg } from '@/utils';

import { renderWithProviders } from './test-utils';

const STARTED = 1793650000;
const FINISHED = 1793650044;
const EARLIER = FINISHED - 700000;
const DISMISSED_KEY = 'xui.psiphon.newRegions.dismissed';

const withNewRegion: PsiphonScanStatus = {
  running: false,
  total: 2,
  current: [],
  startedAt: STARTED,
  finishedAt: FINISHED,
  results: [{ region: 'US', connected: true }],
  seen: [
    { region: 'US', at: FINISHED, firstAt: FINISHED },
    { region: 'FR', at: EARLIER, firstAt: EARLIER },
  ],
};
const BANNER = 'A new Psiphon region is available: United States (US)';

function answerWith(scan: PsiphonScanStatus | null) {
  vi.mocked(HttpUtil.get).mockImplementation(async () =>
    scan ? new Msg(true, '', scan) : new Msg(false, 'unavailable', null),
  );
}

beforeEach(() => localStorage.clear());

describe('the banner of a new Psiphon region', () => {
  it('says which region the last scan found, and opens Psiphon', async () => {
    answerWith(withNewRegion);
    const onOpen = vi.fn();
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={onOpen} />);

    await screen.findByText(BANNER);
    fireEvent.click(screen.getByRole('button', { name: 'Open Psiphon' }));
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it('says nothing when the scan found no region that was new', async () => {
    answerWith({ ...withNewRegion, seen: [{ region: 'US', at: FINISHED, firstAt: EARLIER }] });
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={vi.fn()} />);

    await waitFor(() => expect(HttpUtil.get).toHaveBeenCalled());
    expect(screen.queryByText(/A new Psiphon region/)).toBeNull();
  });

  it('says nothing when the status cannot be read', async () => {
    answerWith(null);
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={vi.fn()} />);

    await waitFor(() => expect(HttpUtil.get).toHaveBeenCalled());
    expect(screen.queryByText(/A new Psiphon region/)).toBeNull();
  });

  it('says so as soon as a scan that runs connects a region that no scan before it had found', async () => {
    answerWith({
      ...withNewRegion,
      running: true,
      finishedAt: undefined,
      seen: [{ region: 'FR', at: EARLIER, firstAt: EARLIER }],
    });
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={vi.fn()} />);

    await screen.findByText(BANNER);
  });

  it('looks again while the scan runs, and says so once it has found a region', async () => {
    const spy = vi.spyOn(globalThis, 'setInterval');
    onTestFinished(() => spy.mockRestore());
    const running = { ...withNewRegion, running: true, finishedAt: undefined, results: [] };
    answerWith({ ...running, seen: [{ region: 'FR', at: EARLIER, firstAt: EARLIER }] });
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={vi.fn()} />);
    await waitFor(() => expect(HttpUtil.get).toHaveBeenCalledTimes(1));
    expect(screen.queryByText(BANNER)).toBeNull();

    // The page looks at a scan that runs every ten seconds; the callback is called by hand rather than waited for.
    const lookingEvery = () => spy.mock.calls.find(([, ms]) => ms === 10000)?.[0];
    await waitFor(() => expect(lookingEvery()).toBeTypeOf('function'));
    const look = lookingEvery() as () => void;
    answerWith(withNewRegion);
    await act(async () => look());
    await screen.findByText(BANNER);
  });

  it('stays dismissed, also after a reload, until another scan finds a region', async () => {
    answerWith(withNewRegion);
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={vi.fn()} />);
    await screen.findByText(BANNER);

    fireEvent.click(screen.getByRole('button', { name: /close/i }));
    await waitFor(() => expect(screen.queryByText(BANNER)).toBeNull());
    expect(localStorage.getItem(DISMISSED_KEY)).toBe(String(STARTED));

    cleanup();
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={vi.fn()} />);
    await waitFor(() => expect(HttpUtil.get).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(BANNER)).toBeNull();

    cleanup();
    const later = FINISHED + 604800;
    answerWith({
      ...withNewRegion,
      startedAt: STARTED + 604800,
      finishedAt: later,
      seen: [
        { region: 'US', at: later, firstAt: later },
        { region: 'FR', at: EARLIER, firstAt: EARLIER },
      ],
    });
    renderWithProviders(<PsiphonNewRegionsBanner onOpen={vi.fn()} />);
    await screen.findByText(BANNER);
  });
});

describe('the Outbounds tab', () => {
  it('carries the banner of a new Psiphon region above its table', async () => {
    answerWith(withNewRegion);
    const onShowPsiphon = vi.fn();
    renderWithProviders(
      <OutboundsTab
        templateSettings={
          { outbounds: [{ tag: 'proxy-a', protocol: 'vmess' }] } as unknown as XraySettingsValue
        }
        setTemplateSettings={vi.fn()}
        outboundsTraffic={[]}
        outboundTestStates={{}}
        subscriptionTestStates={{}}
        testingAll={false}
        inboundTags={[]}
        isMobile={false}
        onResetTraffic={vi.fn()}
        onTest={vi.fn()}
        onTestSubscription={vi.fn()}
        onTestAll={vi.fn()}
        onShowWarp={vi.fn()}
        onShowNord={vi.fn()}
        onShowPia={vi.fn()}
        onShowTor={vi.fn()}
        onShowPsiphon={onShowPsiphon}
        onShowWireproxy={vi.fn()}
      />,
    );

    await screen.findByText(BANNER);
    fireEvent.click(screen.getByRole('button', { name: 'Open Psiphon' }));
    expect(onShowPsiphon).toHaveBeenCalledTimes(1);
  });
});
