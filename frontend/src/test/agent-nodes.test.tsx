import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { QueryClient } from '@tanstack/react-query';

import AgentBundleModal, { AGENT_INSTALL_COMMAND } from '@/pages/nodes/AgentBundleModal';
import AgentNodeFormModal from '@/pages/nodes/AgentNodeFormModal';
import NodeList from '@/pages/nodes/NodeList';
import { keys } from '@/api/queryKeys';
import type { NodeRecord } from '@/schemas/node';
import { ClipboardManager, HttpUtil, Msg } from '@/utils';

import { chooseSelectOption, renderWithProviders } from './test-utils';

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
      tags: [],
      sidecars: [],
    });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it('runs Tor on the agent when asked to, and stops it when unchecked', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    const node: NodeRecord = {
      id: 4,
      name: 'edge-4',
      kind: 'agent',
      address: 'a.example.com',
      port: 9443,
      enable: true,
    };
    const { unmount } = renderWithProviders(
      <AgentNodeFormModal open node={node} save={save} onOpenChange={() => {}} />,
    );

    const tor = screen.getByRole('checkbox', { name: /Tor, a SOCKS proxy on 127.0.0.1:19050/ });
    expect((tor as HTMLInputElement).checked).toBe(false);
    fireEvent.click(tor);
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({ id: 4, sidecars: ['tor'] });
    unmount();

    renderWithProviders(
      <AgentNodeFormModal
        open
        node={{ ...node, sidecars: ['tor'] }}
        save={save}
        onOpenChange={() => {}}
      />,
    );
    const held = screen.getByRole('checkbox', { name: /Tor, a SOCKS proxy/ }) as HTMLInputElement;
    expect(held.checked).toBe(true);
    fireEvent.click(held);
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
    expect(save.mock.calls[1][0]).toMatchObject({ id: 4, sidecars: [] });
  });

  it('offers Psiphon beside Tor, and sends both in the order they are listed', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={save} onOpenChange={() => {}} />,
    );

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'edge-1' } });
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'node.example.com' } });
    fireEvent.click(
      screen.getByRole('checkbox', { name: /Psiphon, a SOCKS proxy on 127.0.0.1:19060/ }),
    );
    fireEvent.click(
      screen.getByRole('checkbox', { name: /Tor, a SOCKS proxy on 127.0.0.1:19050/ }),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0].sidecars).toEqual(['tor', 'psiphon']);
  });

  it('serves a decoy page when asked to, with the defaults of a decoy', async () => {
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(
      new Msg(true, '', { templates: ['maintenance', 'parked'] }),
    );
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={save} onOpenChange={() => {}} />,
    );

    expect(screen.queryByLabelText('Domain')).toBeNull();
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'edge-1' } });
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'node.example.com' } });
    chooseSelectOption(screen.getByLabelText('Decoy site').id, 'A page built into the panel');
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: ' decoy.example.com ' } });
    await waitFor(() => expect(screen.getByLabelText('Page')).toBeTruthy());
    // The pages come from the panel, a moment after the field is there.
    await waitFor(() => chooseSelectOption(screen.getByLabelText('Page').id, 'Parked domain'));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      decoyMode: 'template',
      decoyTemplate: 'parked',
      decoyDomain: 'decoy.example.com',
      decoyCert: 'auto',
      decoyPort: 7443,
    });
  });

  it('asks for the files of a certificate on the node, and for the address of a proxy', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={save} onOpenChange={() => {}} />,
    );

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'edge-1' } });
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'node.example.com' } });
    chooseSelectOption(screen.getByLabelText('Decoy site').id, 'A reverse proxy to a site');
    expect(screen.getByLabelText('Address of the site')).toBeTruthy();
    expect(screen.queryByLabelText('Page')).toBeNull();
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'decoy.example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(document.body.textContent).toContain('A decoy needs a domain'));
    expect(save).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText('Address of the site'), {
      target: { value: 'https://site.example' },
    });
    fireEvent.click(screen.getByRole('radio', { name: 'Files on the node' }));
    expect(screen.queryByLabelText('Email for the certificate')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(save).not.toHaveBeenCalled());

    fireEvent.change(screen.getByLabelText('Certificate file'), {
      target: { value: '/etc/ssl/n.pem' },
    });
    fireEvent.change(screen.getByLabelText('Key file'), { target: { value: '/etc/ssl/n.key' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      decoyMode: 'proxy',
      decoyProxyUrl: 'https://site.example',
      decoyCert: 'manual',
      decoyCertFile: '/etc/ssl/n.pem',
      decoyKeyFile: '/etc/ssl/n.key',
    });
  });

  it('serves AdGuard Home as the decoy, which needs no page and no site', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={save} onOpenChange={() => {}} />,
    );

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'edge-1' } });
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'node.example.com' } });
    chooseSelectOption(
      screen.getByLabelText('Decoy site').id,
      'AdGuard Home, which the node installs and runs',
    );
    expect(screen.queryByLabelText('Page')).toBeNull();
    expect(screen.queryByLabelText('Address of the site')).toBeNull();
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'decoy.example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      decoyMode: 'adguard',
      decoyDomain: 'decoy.example.com',
      decoyCert: 'auto',
      decoyPort: 7443,
    });
  });

  it('shows the decoy an agent holds, and takes it off when asked to', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    const node: NodeRecord = {
      id: 4,
      name: 'edge-4',
      kind: 'agent',
      address: 'a.example.com',
      port: 9443,
      enable: true,
      decoy: {
        mode: 'proxy',
        proxyUrl: 'https://site.example',
        domain: 'decoy.example.com',
        cert: 'auto',
        email: 'ops@example.com',
        port: 7500,
      },
    };
    renderWithProviders(
      <AgentNodeFormModal open node={node} save={save} onOpenChange={() => {}} />,
    );

    expect(screen.getByLabelText('Domain')).toHaveProperty('value', 'decoy.example.com');
    expect(screen.getByLabelText('Address of the site')).toHaveProperty(
      'value',
      'https://site.example',
    );
    expect(screen.getByLabelText('Port on 127.0.0.1')).toHaveProperty('value', '7500');
    chooseSelectOption(screen.getByLabelText('Decoy site').id, 'None');
    expect(screen.queryByLabelText('Domain')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({ id: 4, decoyMode: '' });
  });

  // The port is only wanted with a decoy, so a box cleared on the way to taking the decoy off must not stop the save.
  it('takes a decoy off even when the port box was cleared first', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    const node: NodeRecord = {
      id: 4,
      name: 'edge-4',
      kind: 'agent',
      address: 'a.example.com',
      port: 9443,
      enable: true,
      decoy: {
        mode: 'proxy',
        proxyUrl: 'https://site.example',
        domain: 'decoy.example.com',
        port: 7500,
      },
    };
    renderWithProviders(
      <AgentNodeFormModal open node={node} save={save} onOpenChange={() => {}} />,
    );

    fireEvent.change(screen.getByLabelText('Port on 127.0.0.1'), { target: { value: '' } });
    chooseSelectOption(screen.getByLabelText('Decoy site').id, 'None');
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({ id: 4, decoyMode: '', decoyPort: 7443 });
  });

  it('serves a decoy on the usual port when the port box is cleared', async () => {
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    renderWithProviders(
      <AgentNodeFormModal open node={null} save={save} onOpenChange={() => {}} />,
    );

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'edge-1' } });
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'node.example.com' } });
    chooseSelectOption(screen.getByLabelText('Decoy site').id, 'A page built into the panel');
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'decoy.example.com' } });
    fireEvent.change(screen.getByLabelText('Port on 127.0.0.1'), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({ decoyMode: 'template', decoyPort: 7443 });
  });

  it('labels the agent with tags, suggesting the ones the nodes already carry', async () => {
    vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) =>
      url === '/panel/api/nodes/list'
        ? new Msg(true, '', [{ id: 9, name: 'other', kind: 'agent', tags: ['us'] }])
        : new Msg(false, '', null),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const save = vi.fn().mockResolvedValue(new Msg(true, '', {}));
    const node: NodeRecord = {
      id: 4,
      name: 'edge-4',
      kind: 'agent',
      address: 'a.example.com',
      port: 9443,
      enable: true,
      tags: ['eu'],
    };
    renderWithProviders(
      <AgentNodeFormModal open node={node} save={save} onOpenChange={() => {}} />,
      { queryClient },
    );

    const field = screen.getByLabelText('Tags');
    expect(document.querySelector('.ant-select-selection-item')?.textContent).toBe('eu');
    await waitFor(() => expect(queryClient.getQueryData(keys.nodes.list())).toHaveLength(1));
    chooseSelectOption(field.id, 'us');
    fireEvent.change(field, { target: { value: 'exit,' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({ id: 4, tags: ['eu', 'us', 'exit'] });
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
    expect(document.body.textContent).toContain('use Pair again');
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

  function renderList(
    onRepair = noop,
    onUpdateNode = noop,
    onLogs = noop,
    onRestartXray = noop,
    { list = nodes, isMobile = false }: { list?: NodeRecord[]; isMobile?: boolean } = {},
  ) {
    renderWithProviders(
      <NodeList
        isMobile={isMobile}
        nodes={list}
        latestVersion="3.0.1"
        selectedIds={[]}
        onSelectionChange={noop}
        onAdd={noop}
        onAddAgent={noop}
        onMtls={noop}
        onEdit={noop}
        onRepair={onRepair}
        onLogs={onLogs}
        onRestartXray={onRestartXray}
        onDelete={noop}
        onProbe={noop}
        onToggleEnable={noop}
        onUpdateNode={onUpdateNode}
        onUpdateSelected={noop}
      />,
    );
  }

  it('marks the agent and offers to pair it again', () => {
    renderList();

    expect(screen.getAllByText('Agent')).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Pair again' })).toHaveLength(1);
  });

  // Two role queries over the whole table take seconds, and more than the default 5 s on a loaded runner.
  it('offers each node its update, the agent under its own name', () => {
    const onUpdateNode = vi.fn();
    renderList(noop, onUpdateNode);

    expect(screen.getAllByRole('button', { name: 'Update Panel' })).toHaveLength(1);
    expect(screen.getAllByText('Update available')).toHaveLength(2);
    fireEvent.click(screen.getByRole('button', { name: 'Update agent' }));
    expect(onUpdateNode).toHaveBeenCalledWith(expect.objectContaining({ id: 2, kind: 'agent' }));
  }, 20_000);

  it('pairs the agent row again, not the panel row', () => {
    const onRepair = vi.fn();
    renderList(onRepair);

    fireEvent.click(screen.getByRole('button', { name: 'Pair again' }));
    expect(onRepair).toHaveBeenCalledWith(expect.objectContaining({ id: 2, kind: 'agent' }));
  });

  it('shows the tags of an agent under its name, and none for a panel', () => {
    renderList(noop, noop, noop, noop, {
      list: [
        { ...nodes[0], tags: ['stale'] },
        { ...nodes[1], tags: ['eu', 'exit'] },
      ],
    });

    expect(screen.getByText('eu')).toBeTruthy();
    expect(screen.getByText('exit')).toBeTruthy();
    expect(screen.queryByText('stale')).toBeNull();
  });

  it('shows how the helper programs of an agent stand, and nothing for a panel', () => {
    const tor = (state?: string, message?: string): NodeRecord => ({
      ...nodes[1],
      sidecars: ['tor'],
      sidecarStates: state ? [{ name: 'tor', state, message }] : [],
    });
    const chips = () => Array.from(document.querySelectorAll('.node-sidecars .ant-tag'));

    renderList(noop, noop, noop, noop, { list: [{ ...nodes[0], sidecars: ['tor'] }, tor()] });
    expect(chips().map((chip) => chip.textContent)).toEqual(['Tor: waiting']);
    document.body.innerHTML = '';

    renderList(noop, noop, noop, noop, { list: [tor('installing')] });
    expect(chips().map((chip) => chip.textContent)).toEqual(['Tor: installing']);
    document.body.innerHTML = '';

    renderList(noop, noop, noop, noop, { list: [tor('running', 'Bootstrapped 100%')] });
    expect(chips().map((chip) => chip.textContent)).toEqual(['Tor: running']);
    expect(chips()[0].className).toContain('ant-tag-success');
    document.body.innerHTML = '';

    renderList(noop, noop, noop, noop, { list: [tor('failed', 'apt-get: no network')] });
    expect(chips().map((chip) => chip.textContent)).toEqual(['Tor: failed']);
    expect(chips()[0].className).toContain('ant-tag-error');
  });

  it('shows each helper program of an agent on its own', () => {
    renderList(noop, noop, noop, noop, {
      list: [
        {
          ...nodes[1],
          sidecars: ['tor', 'psiphon'],
          sidecarStates: [
            { name: 'tor', state: 'failed', message: 'no network' },
            { name: 'psiphon', state: 'running', message: 'tunnel up' },
          ],
        },
      ],
    });

    const chips = Array.from(document.querySelectorAll('.node-sidecars .ant-tag'));
    expect(chips.map((chip) => chip.textContent)).toEqual(['Tor: failed', 'Psiphon: running']);
  });

  it('shows how the decoy of an agent stands, and nothing for a panel or a node with none', () => {
    const decoy = { mode: 'template', template: 'parked', domain: 'decoy.example.com', port: 7500 };
    const chips = () => Array.from(document.querySelectorAll('.node-decoy .ant-tag'));
    const cases: Array<[Partial<NodeRecord>, string, string | undefined]> = [
      [{ decoyState: { state: 'off' } }, 'Decoy: waiting', undefined],
      [{ decoyState: { state: 'starting' } }, 'Decoy: starting', 'ant-tag-processing'],
      [{ decoyState: { state: 'running', cert: 'obtained' } }, 'Decoy: running', 'ant-tag-success'],
      [
        { decoyState: { state: 'running', cert: 'obtaining' } },
        'Decoy: getting a certificate',
        'ant-tag-processing',
      ],
      [
        { decoyState: { state: 'running', cert: 'failed', message: 'no valid A records' } },
        'Decoy: certificate failed',
        'ant-tag-error',
      ],
      [
        { decoyState: { state: 'failed', message: 'port in use' } },
        'Decoy: failed',
        'ant-tag-error',
      ],
    ];
    for (const [state, text, color] of cases) {
      renderList(noop, noop, noop, noop, { list: [{ ...nodes[1], decoy, ...state }] });
      expect(chips().map((chip) => chip.textContent)).toEqual([text]);
      if (color) expect(chips()[0].className).toContain(color);
      document.body.innerHTML = '';
    }

    renderList(noop, noop, noop, noop, {
      list: [
        { ...nodes[0], decoy },
        { ...nodes[1], decoy: { mode: '' } },
      ],
    });
    expect(chips()).toHaveLength(0);
  });

  it('says why a decoy failed when pointed at, and until when its certificate is good', async () => {
    renderList(noop, noop, noop, noop, {
      list: [
        {
          ...nodes[1],
          decoy: { mode: 'template', domain: 'decoy.example.com' },
          decoyState: { state: 'running', cert: 'failed', message: 'no valid A records' },
        },
      ],
    });
    const chip = document.querySelector('.node-decoy .ant-tag');
    if (!chip) throw new Error('no chip');
    fireEvent.mouseEnter(chip);
    await waitFor(() => expect(document.body.textContent).toContain('no valid A records'));
  });

  it('tells in the info of an agent where the fallback of its REALITY inbound must point', () => {
    renderList(noop, noop, noop, noop, {
      list: [
        {
          ...nodes[1],
          decoy: { mode: 'template', domain: 'decoy.example.com', port: 7500 },
          decoyState: { state: 'running', cert: 'obtained' },
        },
      ],
      isMobile: true,
    });

    fireEvent.click(screen.getByRole('button', { name: 'More Information' }));

    expect(document.querySelector('.card-stats')?.textContent).toContain(
      'Decoy siteDecoy: runningFallback target: 127.0.0.1:7500 for decoy.example.com',
    );
  });

  it('says what a helper program last said when pointed at', async () => {
    renderList(noop, noop, noop, noop, {
      list: [
        {
          ...nodes[1],
          sidecars: ['tor'],
          sidecarStates: [{ name: 'tor', state: 'failed', message: 'apt-get: no network' }],
        },
      ],
    });

    const chip = document.querySelector('.node-sidecars .ant-tag');
    if (!chip) throw new Error('no chip');
    fireEvent.mouseEnter(chip);
    await waitFor(() => expect(document.body.textContent).toContain('apt-get: no network'));
  });

  it('lists the helper programs of an agent in its info on a phone', () => {
    renderList(noop, noop, noop, noop, {
      list: [
        {
          ...nodes[1],
          sidecars: ['tor'],
          sidecarStates: [
            { name: 'tor', state: 'running', message: 'Bootstrapped 10% (conn_done): Connected' },
          ],
        },
      ],
      isMobile: true,
    });

    fireEvent.click(screen.getByRole('button', { name: 'More Information' }));

    expect(document.querySelector('.card-stats')?.textContent).toContain(
      'Helper programsTor: runningBootstrapped 10% (conn_done): Connected',
    );
  });

  it('lists the tags of an agent in its info on a phone', () => {
    renderList(noop, noop, noop, noop, {
      list: [{ ...nodes[1], tags: ['eu', 'exit'] }],
      isMobile: true,
    });

    fireEvent.click(screen.getByRole('button', { name: 'More Information' }));

    expect(document.querySelector('.card-stats')?.textContent).toContain('Tagseu, exit');
  });

  it('offers an agent its log and a restart of Xray, and a panel node neither', () => {
    const onLogs = vi.fn();
    const onRestartXray = vi.fn();
    renderList(noop, noop, onLogs, onRestartXray);

    expect(screen.getAllByLabelText('Agent log')).toHaveLength(1);
    expect(screen.getAllByLabelText('Restart Xray')).toHaveLength(1);
    fireEvent.click(screen.getByLabelText('Agent log'));
    expect(onLogs).toHaveBeenCalledWith(expect.objectContaining({ id: 2, kind: 'agent' }));
    fireEvent.click(screen.getByLabelText('Restart Xray'));
    expect(onRestartXray).toHaveBeenCalledWith(expect.objectContaining({ id: 2, kind: 'agent' }));
  });

  it('tells both why the config of an agent is adjusted and why its core failed', async () => {
    renderList(noop, noop, noop, noop, {
      list: [
        {
          ...nodes[1],
          lastError: 'the config of this node was adjusted: a rule is blocked',
          xrayState: 'error',
          xrayError: 'core failed to start',
        },
      ],
    });

    const icon = document.querySelector('.anticon-exclamation-circle');
    if (!icon) throw new Error('the status cell has no issue icon');
    fireEvent.mouseEnter(icon);

    await waitFor(() => expect(document.body.textContent).toContain('a rule is blocked'));
    expect(document.body.textContent).toContain('core failed to start');
  });

  it('has a button to add an agent', () => {
    renderList();
    expect(screen.getByRole('button', { name: /Add agent/ })).toBeTruthy();
  });
});
