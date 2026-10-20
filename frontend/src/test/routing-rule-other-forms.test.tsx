import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import RoutingTab from '@/pages/xray/routing/RoutingTab';
import type { XraySettingsValue } from '@/hooks/useXraySetting';

import { renderWithProviders } from './test-utils';

// Xray takes a network as a list or as text, and inbound tags as text or as a list: the list of rules must be built
// from either form, or the page goes blank and the rule cannot be opened.
const settings = {
  routing: {
    rules: [
      {
        type: 'field',
        outboundTag: 'direct',
        enabled: true,
        inboundTag: 'api',
        network: ['tcp', 'udp'],
      },
      { type: 'field', outboundTag: 'direct', enabled: true, inboundTag: '_bl_exits, other' },
      { type: 'field', outboundTag: 'block', enabled: true, inboundTag: ['in-1'], network: 'udp' },
    ],
  },
} as unknown as XraySettingsValue;

function renderRules(isMobile: boolean) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(
    <QueryClientProvider client={queryClient}>
      <RoutingTab
        templateSettings={settings}
        setTemplateSettings={vi.fn()}
        inboundTags={[]}
        clientReverseTags={[]}
        isMobile={isMobile}
      />
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole('tab', { name: /Routing Rules/ }));
}

describe('RoutingTab lists a rule whose fields are in the other form', () => {
  it('shows the table, with the network of a list shown as the first and a count, and a loopback rule given as text hidden', () => {
    renderRules(false);

    const l4 = Array.from(document.querySelectorAll('.criterion-row'))
      .filter((row) => row.textContent?.startsWith('L4'))
      .map((row) => row.textContent);
    expect(l4).toEqual(['L4TCP+1', 'L4UDP']);
    expect(document.querySelectorAll('.ant-table-tbody tr.ant-table-row')).toHaveLength(2);
  });

  it('shows the cards', () => {
    renderRules(true);

    const chips = Array.from(document.querySelectorAll('.criterion-chip'))
      .filter((chip) => chip.textContent?.startsWith('L4'))
      .map((chip) => chip.textContent);
    expect(chips).toHaveLength(2);
    expect(chips[0]).toBe('L4TCP +1');
  });
});
