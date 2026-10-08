import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';

import AgentBundleModal, { AGENT_INSTALL_COMMAND } from '@/pages/nodes/AgentBundleModal';
import AgentNodeFormModal from '@/pages/nodes/AgentNodeFormModal';
import NodeList from '@/pages/nodes/NodeList';
import type { NodeRecord } from '@/schemas/node';
import { ClipboardManager, Msg } from '@/utils';

import { renderWithProviders } from './test-utils';

afterEach(() => vi.restoreAllMocks());

describe('AgentBundleModal', () => {
  const pairing = { name: 'edge', port: 8443, bundle: 'xab1.secret-material' };

  it('shows the bundle once with what to do with it', () => {
    renderWithProviders(<AgentBundleModal pairing={pairing} onClose={() => {}} />);

    expect(screen.getByRole('textbox', { name: 'Pairing bundle' })).toHaveProperty(
      'value',
      pairing.bundle,
    );
    expect(document.body.textContent).toContain('Open TCP port 8443 in the firewall');
    expect(document.body.textContent).toContain('shown only once');
    expect((screen.getByDisplayValue(AGENT_INSTALL_COMMAND) as HTMLInputElement).readOnly).toBe(
      true,
    );
  });

  it('copies the bundle and the command', async () => {
    const copy = vi.spyOn(ClipboardManager, 'copyText').mockResolvedValue(true);
    renderWithProviders(<AgentBundleModal pairing={pairing} onClose={() => {}} />);

    fireEvent.click(screen.getByRole('button', { name: /Copy bundle/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Copy command' }));

    await waitFor(() => expect(copy).toHaveBeenCalledTimes(2));
    expect(copy).toHaveBeenNthCalledWith(1, pairing.bundle);
    expect(copy).toHaveBeenNthCalledWith(2, AGENT_INSTALL_COMMAND);
  });

  it('closes only through its button, since the bundle is not stored', () => {
    const onClose = vi.fn();
    renderWithProviders(<AgentBundleModal pairing={pairing} onClose={onClose} />);

    fireEvent.keyDown(document.body, { key: 'Escape', code: 'Escape' });
    const mask = document.querySelector('.ant-modal-wrap');
    if (mask) fireEvent.click(mask);
    expect(onClose).not.toHaveBeenCalled();
    expect(document.querySelector('.ant-modal-close')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('shows nothing without a pairing', () => {
    renderWithProviders(<AgentBundleModal pairing={null} onClose={() => {}} />);
    expect(document.querySelector('.ant-modal')).toBeNull();
  });
});

describe('AgentNodeFormModal', () => {
  it('adds an agent from where it will be reachable, on port 8443 by default', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    const onOpenChange = vi.fn();
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={save} onOpenChange={onOpenChange} />,
    );

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'edge-1' } });
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'node.example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      name: 'edge-1',
      address: 'node.example.com',
      port: 8443,
      allowPrivateAddress: false,
    });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it('has no field for a secret, a scheme or a certificate', () => {
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={vi.fn()} onOpenChange={() => {}} />,
    );
    for (const label of ['API token', 'Scheme', 'TLS verification', 'Base path']) {
      expect(screen.queryByLabelText(label)).toBeNull();
    }
  });

  it('does not save without a name and an address', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={save} onOpenChange={() => {}} />,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(document.body.textContent).toContain('required'));
    expect(save).not.toHaveBeenCalled();
  });

  it('edits the endpoint of an existing agent and offers the enable switch', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', null));
    const node: NodeRecord = {
      id: 4,
      name: 'edge-4',
      kind: 'agent',
      address: 'old.example.com',
      port: 9443,
      enable: true,
    };
    renderWithProviders(
      <AgentNodeFormModal open node={node} save={save} onOpenChange={() => {}} />,
    );

    expect(screen.getByLabelText('Address')).toHaveProperty('value', 'old.example.com');
    expect(document.body.textContent).toContain('Use Pair again');
    expect(screen.getByLabelText('Enabled')).toBeTruthy();

    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'new.example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      id: 4,
      address: 'new.example.com',
      port: 9443,
      enable: true,
    });
  });
});

describe('NodeList with an agent node', () => {
  const noop = () => {};
  const nodes: NodeRecord[] = [
    {
      id: 1,
      name: 'full-panel',
      kind: 'panel',
      enable: true,
      status: 'online',
      panelVersion: '3.0.0',
      guid: 'g1',
    },
    {
      id: 2,
      name: 'thin-agent',
      kind: 'agent',
      enable: true,
      status: 'online',
      panelVersion: '3.0.0',
      guid: 'g2',
    },
  ];

  function renderList(onRepair = noop, onUpdateNode = noop) {
    renderWithProviders(
      <NodeList
        nodes={nodes}
        latestVersion="3.0.1"
        selectedIds={[]}
        onSelectionChange={noop}
        onAdd={noop}
        onAddAgent={noop}
        onMtls={noop}
        onEdit={noop}
        onRepair={onRepair}
        onDelete={noop}
        onProbe={noop}
        onToggleEnable={noop}
        onUpdateNode={onUpdateNode}
        onUpdateSelected={noop}
      />,
    );
  }

  it('marks the agent, offers to pair it again, and never to update its panel', () => {
    renderList();

    expect(screen.getAllByText('Agent')).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Pair again' })).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Update Panel' })).toHaveLength(1);
    expect(screen.getAllByText('Update available')).toHaveLength(1);
  });

  it('pairs the agent row again, not the panel row', () => {
    const onRepair = vi.fn();
    renderList(onRepair);

    fireEvent.click(screen.getByRole('button', { name: 'Pair again' }));
    expect(onRepair).toHaveBeenCalledWith(expect.objectContaining({ id: 2, kind: 'agent' }));
  });

  it('has a button to add an agent', () => {
    renderList();
    expect(screen.getByRole('button', { name: /Add agent/ })).toBeTruthy();
  });
});
