import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fireEvent, waitFor } from '@testing-library/react';

import SubscriptionBalancersTab from '@/pages/settings/SubscriptionBalancersTab';
import type { AllSetting } from '@/models/setting';
import type { SubBalancer } from '@/schemas/subBalancer';
import { renderWithProviders } from './test-utils';

const balancers: SubBalancer[] = [
  {
    id: 4,
    remark: 'mixed',
    strategy: 'leastLoad',
    inboundIds: [1],
    nodeIds: [10, 11],
    memberWeights: { '1': 2.5 },
    sortOrder: 2,
    enabled: true,
  },
  {
    id: 5,
    remark: 'nodes only',
    strategy: 'random',
    inboundIds: [],
    nodeIds: [11],
    sortOrder: 3,
    enabled: false,
  },
  {
    id: 6,
    remark: 'inbounds only',
    strategy: 'random',
    inboundIds: [1, 2],
    nodeIds: [],
    sortOrder: 4,
    enabled: true,
  },
];

const update = vi.fn();

vi.mock('@/api/queries/useSubBalancersQuery', () => ({
  useSubBalancersQuery: () => ({
    balancers,
    loading: false,
    fetched: true,
    fetchError: '',
    refetch: vi.fn(),
  }),
}));

vi.mock('@/api/queries/useSubBalancerMutations', () => ({
  useSubBalancerMutations: () => ({ create: vi.fn(), update, remove: vi.fn() }),
}));

vi.mock('@/api/queries/useInboundOptions', () => ({
  useInboundOptions: () => ({
    data: [
      { id: 1, tag: 'inb-vless', remark: 'First', protocol: 'vless', port: 443, enable: true },
      { id: 2, tag: 'inb-ws', remark: 'Second', protocol: 'vmess', port: 8443, enable: true },
    ],
    isLoading: false,
  }),
}));

vi.mock('@/api/queries/useNodesQuery', () => ({
  useNodesQuery: () => ({
    nodes: [
      { id: 10, name: 'berlin' },
      { id: 11, name: 'paris' },
    ],
  }),
}));

function renderTab() {
  renderWithProviders(
    <SubscriptionBalancersTab
      allSetting={{ subJsonObservatory: '' } as AllSetting}
      updateSetting={vi.fn()}
    />,
  );
}

function rowOf(remark: string): HTMLElement {
  const row = Array.from(document.querySelectorAll<HTMLElement>('.ant-table-tbody tr')).find((r) =>
    r.textContent?.includes(remark),
  );
  if (!row) throw new Error(`Row '${remark}' not found`);
  return row;
}

describe('SubscriptionBalancersTab', () => {
  beforeEach(() => {
    update.mockReset();
    update.mockResolvedValue({ success: true });
  });

  it('counts the nodes and the inbounds of each balancer', () => {
    renderTab();
    expect(rowOf('mixed').textContent).toContain('2 Nodes, 1 Inbounds');
    expect(rowOf('nodes only').textContent).toContain('1 Nodes');
    expect(rowOf('nodes only').textContent).not.toContain('Inbounds');
    expect(rowOf('inbounds only').textContent).toContain('2 Inbounds');
    expect(rowOf('inbounds only').textContent).not.toContain('Nodes');
  });

  // The switch re-sends the whole row, so a field it leaves out would be wiped.
  it('keeps the nodes and the weights when a balancer is switched off', async () => {
    renderTab();
    const toggle = rowOf('mixed').querySelector<HTMLElement>('.ant-switch');
    if (!toggle) throw new Error('Switch not found');
    fireEvent.click(toggle);
    await waitFor(() => expect(update).toHaveBeenCalledTimes(1));
    expect(update).toHaveBeenCalledWith(4, {
      remark: 'mixed',
      strategy: 'leastLoad',
      inboundIds: [1],
      nodeIds: [10, 11],
      memberWeights: { '1': 2.5 },
      sortOrder: 2,
      enabled: false,
    });
  });

  it('keeps a balancer of nodes alone as it is when it is switched on', async () => {
    renderTab();
    const toggle = rowOf('nodes only').querySelector<HTMLElement>('.ant-switch');
    if (!toggle) throw new Error('Switch not found');
    fireEvent.click(toggle);
    await waitFor(() => expect(update).toHaveBeenCalledTimes(1));
    expect(update).toHaveBeenCalledWith(5, {
      remark: 'nodes only',
      strategy: 'random',
      inboundIds: [],
      nodeIds: [11],
      memberWeights: undefined,
      sortOrder: 3,
      enabled: true,
    });
  });
});
