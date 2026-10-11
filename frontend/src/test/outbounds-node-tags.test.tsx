import { describe, expect, it, vi } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import OutboundsTab from '@/pages/xray/outbounds/OutboundsTab';
import type { XraySettingsValue } from '@/hooks/useXraySetting';

import { renderWithProviders } from './test-utils';

const settings = {
  outbounds: [
    { tag: 'direct', protocol: 'freedom' },
    { tag: 'eu-hop', protocol: 'freedom', nodeTags: ['eu', 'exit'] },
  ],
} as unknown as XraySettingsValue;

function renderOutbounds(isMobile: boolean) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(
    <QueryClientProvider client={queryClient}>
      <OutboundsTab
        templateSettings={settings}
        setTemplateSettings={vi.fn()}
        outboundsTraffic={[]}
        outboundTestStates={{}}
        subscriptionTestStates={{}}
        testingAll={false}
        inboundTags={[]}
        isMobile={isMobile}
        onResetTraffic={vi.fn()}
        onTest={vi.fn()}
        onTestSubscription={vi.fn()}
        onTestAll={vi.fn()}
        onShowWarp={vi.fn()}
        onShowNord={vi.fn()}
        onShowPia={vi.fn()}
        onShowTor={vi.fn()}
        onShowPsiphon={vi.fn()}
        onShowWireproxy={vi.fn()}
      />
    </QueryClientProvider>,
  );
}

describe('OutboundsTab shows the agent nodes an outbound is for', () => {
  it('lists the tags under the outbound of the table, and only there', () => {
    renderOutbounds(false);

    const rows = Array.from(document.querySelectorAll('.ant-table-tbody tr.ant-table-row'));
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).not.toContain('exit');
    expect(rows[1].textContent).toContain('eu');
    expect(rows[1].textContent).toContain('exit');
  });

  it('lists them on the card of the outbound as well', () => {
    renderOutbounds(true);

    const cards = Array.from(document.querySelectorAll('.outbound-card'));
    expect(cards).toHaveLength(2);
    expect(cards[0].textContent).not.toContain('exit');
    expect(cards[1].textContent).toContain('exit');
  });
});
