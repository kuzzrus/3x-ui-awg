import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import RoutingTab from '@/pages/xray/routing/RoutingTab';
import type { XraySettingsValue } from '@/hooks/useXraySetting';

import { renderWithProviders } from './test-utils';

const settings = {
  routing: {
    rules: [
      { type: 'field', outboundTag: 'direct', enabled: true, nodeTags: ['eu', 'exit'] },
      { type: 'field', outboundTag: 'block', enabled: true },
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

describe('RoutingTab shows the agent nodes a rule is for', () => {
  it('names the first tag and counts the rest, only on the rule that has them', () => {
    renderRules(false);

    const criteria = Array.from(document.querySelectorAll('.criterion-row')).filter((row) =>
      row.textContent?.startsWith('Nodes'),
    );
    expect(criteria.map((row) => row.textContent)).toEqual(['Nodeseu+1']);
  });

  it('puts a chip on the card of the rule', () => {
    renderRules(true);

    const chips = Array.from(document.querySelectorAll('.criterion-chip')).filter((chip) =>
      chip.textContent?.startsWith('Nodes'),
    );
    expect(chips).toHaveLength(1);
    expect(chips[0].textContent).toContain('eu');
  });
});
