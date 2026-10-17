import { describe, it, expect, vi } from 'vitest';
import { fireEvent, waitFor } from '@testing-library/react';

import BalancerFormModal from '@/pages/xray/balancers/BalancerFormModal';
import type { BalancerFormValue } from '@/pages/xray/balancers/BalancerFormModal';
import type { BalancerObject } from '@/schemas/routing';
import { renderWithProviders } from './test-utils';

vi.mock('@/api/queries/useNodesQuery', () => ({
  useNodesQuery: () => ({
    nodes: [
      { id: 10, name: 'berlin' },
      { id: 11, name: 'paris' },
      { id: 0, name: 'under-berlin', guid: 'g1', parentGuid: 'p1', transitive: true },
    ],
  }),
}));

vi.mock('@/api/queries/useClientOptions', () => ({
  useClientOptions: () => ({ data: ['alice@example.com', 'relay@example.com'] }),
}));

function renderModal(onConfirm = vi.fn(), balancer: BalancerFormValue | null = null) {
  renderWithProviders(
    <BalancerFormModal
      open
      balancer={balancer}
      outboundTags={['proxy', 'direct']}
      balancerTags={[]}
      balancers={[]}
      templateSettings={null}
      otherTags={['existing']}
      onClose={() => {}}
      onConfirm={onConfirm}
    />,
  );
  return { onConfirm };
}

function erroredItemCount(): number {
  return document.querySelectorAll('.ant-form-item-has-error').length;
}

function explainText(): string {
  return Array.from(document.querySelectorAll('.ant-form-item-explain'))
    .map((el) => (el.textContent ?? '').trim())
    .join(' | ');
}

function selectOf(label: string): HTMLElement {
  const item = Array.from(document.querySelectorAll('.ant-modal .ant-form-item')).find((el) =>
    el.querySelector('label')?.textContent?.trim().startsWith(label),
  );
  const select = item?.querySelector('.ant-select');
  if (!select) throw new Error(`Select '${label}' not found`);
  return select as HTMLElement;
}

function optionTitles(label: string): string[] {
  fireEvent.mouseDown(selectOf(label));
  return Array.from(document.querySelectorAll('.ant-select-item-option')).map((o) =>
    (o.getAttribute('title') ?? o.textContent ?? '').trim(),
  );
}

function pick(label: string, title: string) {
  const select = selectOf(label);
  fireEvent.mouseDown(select);
  const option = Array.from(document.querySelectorAll('.ant-select-item-option')).find(
    (o) => (o.getAttribute('title') ?? o.textContent ?? '').trim() === title,
  );
  if (!option) throw new Error(`Option '${title}' not found`);
  fireEvent.click(option);
  fireEvent.keyDown(select, { key: 'Escape' });
}

function tagInput(): HTMLInputElement {
  const el = document.querySelector<HTMLInputElement>(
    '.ant-modal input[placeholder="unique balancer tag"]',
  );
  if (!el) throw new Error('Tag input not found');
  return el;
}

function primaryButton(): HTMLElement {
  const btn = document.querySelector('.ant-modal-footer .ant-btn-primary');
  if (!btn) throw new Error('Primary button not found');
  return btn as HTMLElement;
}

describe('BalancerFormModal', () => {
  it('shows no validation errors when freshly opened in add mode', () => {
    renderModal();
    expect(document.querySelector('.ant-modal')).toBeTruthy();
    expect(erroredItemCount()).toBe(0);
    expect(explainText()).not.toContain('Tag is required');
    expect(explainText()).not.toContain('Pick at least one outbound');
    expect(primaryButton().hasAttribute('disabled')).toBe(false);
  });

  it('reveals required-field errors only after a save attempt, without confirming', () => {
    const { onConfirm } = renderModal();
    expect(erroredItemCount()).toBe(0);

    fireEvent.click(primaryButton());

    expect(erroredItemCount()).toBe(2);
    expect(explainText()).toContain('Tag is required');
    expect(explainText()).toContain('Pick at least one outbound');
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('disables save and warns when the chosen fallback would create a balancer cycle', () => {
    const editing: BalancerFormValue = {
      tag: 'A',
      strategy: 'random',
      selector: ['proxy'],
      fallbackTag: 'B',
      nodeIds: [],
      nodeClient: '',
    };
    const others: BalancerObject[] = [{ tag: 'B', selector: ['direct'], fallbackTag: 'A' }];
    const onConfirm = vi.fn();
    renderWithProviders(
      <BalancerFormModal
        open
        balancer={editing}
        outboundTags={['proxy', 'direct']}
        balancerTags={['A', 'B']}
        balancers={others}
        templateSettings={null}
        otherTags={['B']}
        onClose={() => {}}
        onConfirm={onConfirm}
      />,
    );

    expect(document.querySelector('.ant-alert-error')).toBeTruthy();
    expect(primaryButton().hasAttribute('disabled')).toBe(true);

    fireEvent.click(primaryButton());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('offers the nodes of the panel and not the transitive sub-nodes under them', () => {
    renderModal();
    expect(optionTitles('Nodes')).toEqual(['berlin', 'paris']);
  });

  it('confirms a balancer made of nodes alone, with the client that logs in to them', async () => {
    const { onConfirm } = renderModal();
    fireEvent.change(tagInput(), { target: { value: 'exits' } });
    pick('Nodes', 'paris');
    pick('Nodes', 'berlin');
    pick('Client for the nodes', 'relay@example.com');

    fireEvent.click(primaryButton());

    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    expect(onConfirm).toHaveBeenCalledWith({
      tag: 'exits',
      strategy: 'random',
      selector: [],
      fallbackTag: '',
      nodeIds: [11, 10],
      nodeClient: 'relay@example.com',
    });
  });

  it('asks for the client when nodes are picked and no client is', async () => {
    const { onConfirm } = renderModal();
    fireEvent.change(tagInput(), { target: { value: 'exits' } });
    pick('Nodes', 'berlin');

    fireEvent.click(primaryButton());

    await waitFor(() =>
      expect(explainText()).toContain('Pick the client that logs in to the nodes'),
    );
    expect(explainText()).not.toContain('Pick at least one outbound');
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('seeds the nodes and the client from the edited balancer', async () => {
    const { onConfirm } = renderModal(vi.fn(), {
      tag: 'exits',
      strategy: 'leastPing',
      selector: [],
      fallbackTag: '',
      nodeIds: [11],
      nodeClient: 'alice@example.com',
    });

    fireEvent.click(primaryButton());

    await waitFor(() => expect(onConfirm).toHaveBeenCalledTimes(1));
    expect(onConfirm).toHaveBeenCalledWith(
      expect.objectContaining({ nodeIds: [11], nodeClient: 'alice@example.com', selector: [] }),
    );
  });
});
