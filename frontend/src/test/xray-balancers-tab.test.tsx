import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import BalancersTab from '@/pages/xray/balancers/BalancersTab';
import type { SetTemplate, XraySettingsValue } from '@/hooks/useXraySetting';
import { renderWithProviders } from './test-utils';

vi.mock('@/api/queries/useNodesQuery', () => ({
  useNodesQuery: () => ({
    nodes: [
      { id: 10, name: 'berlin' },
      { id: 11, name: 'paris' },
    ],
  }),
}));

vi.mock('@/api/queries/useClientOptions', () => ({
  useClientOptions: () => ({ data: ['alice@example.com', 'relay@example.com'] }),
}));

const template = {
  outbounds: [{ tag: 'direct', protocol: 'freedom' }],
  routing: {
    rules: [],
    balancers: [
      {
        tag: 'exits',
        selector: ['xui-node-'],
        strategy: { type: 'leastPing' },
        nodeMembers: { client: 'relay@example.com', nodeIds: [10, 11] },
      },
      { tag: 'plain', selector: ['direct'] },
      {
        tag: 'mixed',
        selector: ['direct'],
        nodeMembers: { client: 'relay@example.com', nodeIds: [10] },
      },
    ],
  },
} as unknown as XraySettingsValue;

function renderTab(templateSettings: XraySettingsValue = template) {
  const setTemplateSettings = vi.fn();
  renderWithProviders(
    <BalancersTab
      templateSettings={templateSettings}
      setTemplateSettings={setTemplateSettings as unknown as SetTemplate}
      clientReverseTags={[]}
      subscriptionOutboundTags={[]}
      isMobile={false}
    />,
  );
  return setTemplateSettings;
}

function rowOf(tag: string): HTMLElement {
  const row = Array.from(document.querySelectorAll<HTMLElement>('.ant-table-tbody tr')).find((r) =>
    r.textContent?.includes(tag),
  );
  if (!row) throw new Error(`Row '${tag}' not found`);
  return row;
}

function editAndSave(tag: string) {
  const edit = rowOf(tag).querySelector<HTMLElement>('button[aria-label="Edit"]');
  if (!edit) throw new Error('Edit button not found');
  fireEvent.click(edit);
  const save = document.querySelector<HTMLElement>('.ant-modal-footer .ant-btn-primary');
  if (!save) throw new Error('Save button not found');
  fireEvent.click(save);
}

function savedTemplate(
  setTemplateSettings: ReturnType<typeof vi.fn>,
  from: XraySettingsValue = template,
): XraySettingsValue {
  const update = setTemplateSettings.mock.calls[0][0] as (
    prev: XraySettingsValue | null,
  ) => XraySettingsValue;
  return update(from);
}

async function deleteAndConfirm(tag: string) {
  const more = rowOf(tag).querySelector<HTMLElement>('button[aria-label="more"]');
  if (!more) throw new Error('More button not found');
  fireEvent.click(more);
  fireEvent.click(await screen.findByText('Delete'));
  const ok = await waitFor(() => {
    const button = document.querySelector<HTMLElement>(
      '.ant-modal-confirm-btns .ant-btn-dangerous',
    );
    if (!button) throw new Error('The confirmation is not open');
    return button;
  });
  fireEvent.click(ok);
}

describe('the balancers of the Xray template', () => {
  it('shows the nodes of a balancer next to its selector', () => {
    renderTab();
    expect(rowOf('exits').textContent).toContain('berlin');
    expect(rowOf('exits').textContent).toContain('paris');
    expect(rowOf('plain').textContent).toContain('direct');
    expect(rowOf('plain').textContent).not.toContain('berlin');
  });

  // The placeholder that stands for the nodes in the stored selector is not an outbound to show or to pick.
  it('hides the placeholder that a balancer of nodes alone has as its selector', () => {
    renderTab();
    expect(rowOf('exits').textContent).not.toContain('xui-node-');
  });

  // The balancer is rebuilt from the form on a save, so a key the form does not carry back is gone. Nodes alone
  // are stored with the placeholder, for a panel rolled back to an older version refuses an empty selector.
  it('keeps the nodes and the client of a balancer that is edited and saved', async () => {
    const setTemplateSettings = renderTab();
    editAndSave('exits');

    await waitFor(() => expect(setTemplateSettings).toHaveBeenCalledTimes(1));
    const balancers = savedTemplate(setTemplateSettings).routing?.balancers as Array<
      Record<string, unknown>
    >;
    expect(balancers[0]).toMatchObject({
      tag: 'exits',
      selector: ['xui-node-'],
      strategy: { type: 'leastPing' },
      nodeMembers: { client: 'relay@example.com', nodeIds: [10, 11] },
    });
  });

  it('stores no placeholder for a balancer that has outbounds beside its nodes', async () => {
    const setTemplateSettings = renderTab();
    editAndSave('mixed');

    await waitFor(() => expect(setTemplateSettings).toHaveBeenCalledTimes(1));
    const balancers = savedTemplate(setTemplateSettings).routing?.balancers as Array<
      Record<string, unknown>
    >;
    expect(balancers[2]).toMatchObject({
      tag: 'mixed',
      selector: ['direct'],
      nodeMembers: { client: 'relay@example.com', nodeIds: [10] },
    });
  });

  it('writes no nodeMembers for a balancer that has no nodes', async () => {
    const setTemplateSettings = renderTab();
    editAndSave('plain');

    await waitFor(() => expect(setTemplateSettings).toHaveBeenCalledTimes(1));
    const balancers = savedTemplate(setTemplateSettings).routing?.balancers as Array<
      Record<string, unknown>
    >;
    expect(balancers[1]).toEqual({ tag: 'plain', selector: ['direct'], fallbackTag: '' });
  });

  it('takes the loopback of a deleted balancer along', async () => {
    const withLoopback = {
      outbounds: [
        { tag: 'direct', protocol: 'freedom' },
        { tag: '_bl_solo', protocol: 'loopback', settings: { inboundTag: '_bl_solo' } },
      ],
      routing: {
        rules: [{ type: 'field', inboundTag: ['_bl_solo'], balancerTag: 'solo' }],
        balancers: [{ tag: 'solo', selector: ['direct'] }],
      },
    } as unknown as XraySettingsValue;
    const setTemplateSettings = renderTab(withLoopback);
    await deleteAndConfirm('solo');

    await waitFor(() => expect(setTemplateSettings).toHaveBeenCalledTimes(1));
    const saved = savedTemplate(setTemplateSettings, withLoopback);
    expect(saved.outbounds).toEqual([{ tag: 'direct', protocol: 'freedom' }]);
    expect(saved.routing?.rules).toEqual([]);
    expect(saved.routing?.balancers).toEqual([]);
  });
});
