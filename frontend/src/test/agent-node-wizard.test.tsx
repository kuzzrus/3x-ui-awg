import { afterEach, describe, expect, it, vi, type Mock } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { QueryClient } from '@tanstack/react-query';

import AgentNodeWizard from '@/pages/nodes/AgentNodeWizard';
import { AGENT_INSTALL_COMMAND } from '@/pages/nodes/AgentBundleModal';
import type { AgentPairing } from '@/api/queries/useNodeMutations';
import { keys } from '@/api/queryKeys';
import type { AgentFormValues } from '@/schemas/node';
import { HttpUtil, Msg } from '@/utils';

import { chooseSelectOption, renderWithProviders } from './test-utils';

const SERVER_TITLE = 'Which server is this?';
const LINK_TITLE = 'How will the panel and the node talk to each other?';
const DECOY_TITLE = 'What should anyone who probes the server see?';
const MORE_TITLE = 'Optional settings';
const ADGUARD = 'AdGuard Home, which the node installs and runs';
const ONLINE = 'The node is online. You can close this window.';
const WAITING = 'Waiting for the node to connect…';

const original = Object.getOwnPropertyDescriptor(window, 'location');
afterEach(() => {
  vi.restoreAllMocks();
  if (original) Object.defineProperty(window, 'location', original);
});

type Create = (values: AgentFormValues) => Promise<Msg<AgentPairing>>;

function pairing() {
  return new Msg<AgentPairing>(true, '', {
    bundle: 'xab1.secret-material',
    node: { id: 7, name: 'edge-1', kind: 'agent' },
  });
}

function renderWizard(options: { create?: Mock<Create>; queryClient?: QueryClient } = {}) {
  const create = options.create ?? vi.fn<Create>().mockResolvedValue(pairing());
  const onClose = vi.fn();
  renderWithProviders(<AgentNodeWizard create={create} onClose={onClose} />, {
    queryClient: options.queryClient,
  });
  return { create, onClose };
}

function openAt(url: string, options: Parameters<typeof renderWizard>[0] = {}) {
  Object.defineProperty(window, 'location', { configurable: true, value: new URL(url) });
  return renderWizard(options);
}

const press = (name: string) => fireEvent.click(screen.getByRole('button', { name }));

function fillServer(address = 'node.example.com', name = 'edge-1') {
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: name } });
  fireEvent.change(screen.getByLabelText('Node address'), { target: { value: address } });
}

async function goOn(title: string) {
  press('Next');
  await screen.findByText(title);
}

async function toLink(address?: string) {
  fillServer(address);
  await goOn(LINK_TITLE);
}

async function toDecoy(address?: string) {
  await toLink(address);
  await goOn(DECOY_TITLE);
}

async function toMore(address?: string) {
  await toDecoy(address);
  await goOn(MORE_TITLE);
}

async function createNode(create: Mock<Create>) {
  press('Create node');
  await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
  return create.mock.calls[0][0];
}

const nodeDialsPanel = () =>
  (screen.getByRole('radio', { name: /^The node connects to the panel/ }) as HTMLInputElement)
    .checked;

const choose = (label: string, option: string) =>
  chooseSelectOption(screen.getByLabelText(label).id, option);

describe('AgentNodeWizard: the server', () => {
  it('starts with the server and asks only for what that needs', () => {
    renderWizard();

    expect(screen.getByText(SERVER_TITLE)).toBeTruthy();
    for (const label of ['Name', 'Node address', 'Remark']) {
      expect(screen.getByLabelText(label)).toBeTruthy();
    }
    // The domain of a decoy is not a second thing to ask here: it comes on the decoy step.
    const field = screen.getByLabelText('Node address').closest('.ant-form-item');
    expect(field?.textContent).toContain('The domain or the IP of the server.');
    expect(field?.textContent?.toLowerCase()).not.toContain('decoy');
    for (const label of ['Port of the agent', 'Decoy site', 'Panel address']) {
      expect(screen.queryByLabelText(label)).toBeNull();
    }
    const titles = Array.from(document.querySelectorAll('.ant-steps-item-title'));
    expect(titles.map((el) => el.textContent)).toEqual([
      'Server',
      'Link',
      'Decoy',
      'More',
      'Install',
    ]);
    expect(screen.queryByRole('button', { name: 'Back' })).toBeNull();
  });

  it('has no field for a secret, a scheme or a certificate', () => {
    renderWizard();
    for (const label of ['API token', 'Scheme', 'TLS verification', 'Base path']) {
      expect(screen.queryByLabelText(label)).toBeNull();
    }
  });

  it('says under the fields what is missing, and does not go on', async () => {
    const { create } = renderWizard();

    press('Next');
    await waitFor(() =>
      expect(document.querySelectorAll('.ant-form-item-explain-error')).toHaveLength(2),
    );
    expect(document.body.textContent).toContain('required');
    expect(screen.getByText(SERVER_TITLE)).toBeTruthy();
    expect(screen.queryByText(LINK_TITLE)).toBeNull();
    expect(create).not.toHaveBeenCalled();
  });

  it('closes from Cancel while nothing is created yet', () => {
    const { create, onClose } = renderWizard();

    press('Cancel');
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(create).not.toHaveBeenCalled();
  });

  it('keeps what was typed when going back', async () => {
    renderWizard();
    await toLink('node.example.com');

    press('Back');
    await screen.findByText(SERVER_TITLE);
    expect(screen.getByLabelText('Name')).toHaveProperty('value', 'edge-1');
    expect(screen.getByLabelText('Node address')).toHaveProperty('value', 'node.example.com');
  });
});

describe('AgentNodeWizard: the link to the panel', () => {
  it('asks for a port only when the panel dials the node, and says why it is not 443', async () => {
    renderWizard();
    await toLink();

    expect(nodeDialsPanel()).toBe(false);
    expect(screen.getByLabelText('Port of the agent')).toHaveProperty('value', '8443');
    expect(document.body.textContent).toContain('443 is taken by the Xray of the node');
    expect(screen.queryByLabelText('Panel address')).toBeNull();

    fireEvent.click(screen.getByRole('radio', { name: /^The node connects to the panel/ }));
    expect(screen.queryByLabelText('Port of the agent')).toBeNull();
    expect(screen.getByLabelText('Panel address')).toBeTruthy();

    fireEvent.click(screen.getByRole('radio', { name: /^The panel connects to the node/ }));
    expect(screen.getByLabelText('Port of the agent')).toHaveProperty('value', '8443');
  });

  // The number box clamps what is typed into it to the range, so an emptied box is the one way to a wrong port.
  it('says in a sentence what is wrong with an emptied port box, and does not go on', async () => {
    renderWizard();
    await toLink();

    fireEvent.change(screen.getByLabelText('Port of the agent'), { target: { value: '' } });
    press('Next');
    await waitFor(() =>
      expect(document.body.textContent).toContain(
        'The port of the agent must be a number from 1 to 65535',
      ),
    );
    expect(screen.queryByText(DECOY_TITLE)).toBeNull();
  });

  it('says each thing once, under the field, and not again in a tooltip', async () => {
    renderWizard();
    const tooltipOf = (label: string) =>
      screen
        .getByText(label, { selector: 'label' })
        .closest('.ant-form-item')
        ?.querySelector('.ant-form-item-tooltip');
    await toLink();

    expect(tooltipOf('Connection')).toBeNull();
    expect(tooltipOf('Port of the agent')).toBeNull();
    // The panel address keeps its tooltip, which shows that this look finds one where there is one.
    fireEvent.click(screen.getByRole('radio', { name: /^The node connects to the panel/ }));
    expect(tooltipOf('Panel address')).not.toBeNull();
    fireEvent.click(screen.getByRole('radio', { name: /^The panel connects to the node/ }));
    await goOn(DECOY_TITLE);
    choose('Decoy site', ADGUARD);
    expect(tooltipOf('Domain')).toBeNull();
  });

  it('says what each way costs', async () => {
    renderWizard();
    await toLink();

    expect(document.body.textContent).toContain('has to be open in its firewall');
    expect(document.body.textContent).toContain('Nothing has to be opened on the server');
  });

  it('has the node dial the panel on a panel opened over https at a public name', async () => {
    const { create } = openAt('https://panel.example.com/');
    await toLink();

    expect(nodeDialsPanel()).toBe(true);
    expect(document.body.textContent).toContain('Recommended here');
    expect(screen.getByLabelText('Panel address')).toHaveProperty(
      'value',
      'https://panel.example.com/',
    );
    expect(screen.queryByLabelText('Port of the agent')).toBeNull();

    await goOn(DECOY_TITLE);
    await goOn(MORE_TITLE);
    expect(await createNode(create)).toMatchObject({
      pull: true,
      masterUrl: 'https://panel.example.com/',
    });
  });

  // The address a node is committed to dial is the one the browser is at, so it is the default only where a
  // node can be expected to reach it, with a certificate it trusts.
  it.each([
    [
      'an IP address, where a certificate is likelier to be self-signed',
      'https://203.0.113.5:2053/',
    ],
    ['an IPv6 address', 'https://[2001:db8::1]:2053/'],
    ['localhost, as through a tunnel', 'https://localhost:2053/'],
    ['a name of a private network', 'https://panel.lan/'],
    ['a name with a private suffix and a port', 'https://nas.home.arpa:2053/'],
    ['a public name over http, which a node cannot dial', 'http://panel.example.com/'],
  ])('has the panel dial the node when the panel is opened at %s', async (_what, url) => {
    openAt(url);
    await toLink();

    expect(nodeDialsPanel()).toBe(false);
    expect(document.body.textContent).not.toContain('Recommended here');
    expect(screen.getByLabelText('Port of the agent')).toHaveProperty('value', '8443');
    expect(screen.queryByLabelText('Panel address')).toBeNull();
  });

  it('fills in the address the panel was opened at, which has to be https to be used', async () => {
    renderWizard();
    await toLink();
    fireEvent.click(screen.getByRole('radio', { name: /^The node connects to the panel/ }));

    const field = screen.getByLabelText('Panel address') as HTMLInputElement;
    expect(field.value).toBe(window.location.origin + '/');
    press('Next');
    await waitFor(() => expect(document.body.textContent).toContain('must be an https:// address'));
    expect(screen.queryByText(DECOY_TITLE)).toBeNull();
  });

  it('turns down a panel address that carries a login, and a pin that is no hash', async () => {
    renderWizard();
    await toLink();
    fireEvent.click(screen.getByRole('radio', { name: /^The node connects to the panel/ }));

    fireEvent.change(screen.getByLabelText('Panel address'), {
      target: { value: 'https://admin:pw@panel.example.com/' },
    });
    press('Next');
    await waitFor(() => expect(document.body.textContent).toContain('must be an https:// address'));

    fireEvent.change(screen.getByLabelText('Panel address'), {
      target: { value: 'https://panel.example.com/' },
    });
    fireEvent.change(screen.getByLabelText('Panel certificate SHA-256 (optional)'), {
      target: { value: 'not-a-hash' },
    });
    press('Next');
    await waitFor(() => expect(document.body.textContent).toContain('must be a SHA-256 hash'));
    expect(screen.queryByText(DECOY_TITLE)).toBeNull();

    fireEvent.change(screen.getByLabelText('Panel certificate SHA-256 (optional)'), {
      target: { value: Array(32).fill('AB').join(':') },
    });
    await goOn(DECOY_TITLE);
  });

  it('sends the panel address and the pin when the node dials the panel', async () => {
    const { create } = renderWizard();
    await toLink();
    fireEvent.click(screen.getByRole('radio', { name: /^The node connects to the panel/ }));
    fireEvent.change(screen.getByLabelText('Panel address'), {
      target: { value: ' https://panel.example.com/secret/ ' },
    });
    const pin = Array(32).fill('AB').join(':');
    fireEvent.change(screen.getByLabelText('Panel certificate SHA-256 (optional)'), {
      target: { value: pin },
    });
    await goOn(DECOY_TITLE);
    await goOn(MORE_TITLE);

    expect(await createNode(create)).toMatchObject({
      name: 'edge-1',
      pull: true,
      masterUrl: 'https://panel.example.com/secret/',
      masterPin: pin,
    });
  });

  it('sends the port of the agent when the panel dials the node after all', async () => {
    const { create } = renderWizard();
    await toLink();
    fireEvent.click(screen.getByRole('radio', { name: /^The node connects to the panel/ }));
    fireEvent.click(screen.getByRole('radio', { name: /^The panel connects to the node/ }));
    fireEvent.change(screen.getByLabelText('Port of the agent'), { target: { value: '9443' } });
    await goOn(DECOY_TITLE);
    await goOn(MORE_TITLE);

    expect(await createNode(create)).toMatchObject({ pull: false, port: 9443 });
  });
});

describe('AgentNodeWizard: the decoy', () => {
  it('serves a decoy page when asked to, with the defaults of a decoy', async () => {
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(
      new Msg(true, '', { templates: ['maintenance', 'parked'] }),
    );
    const { create } = renderWizard();
    await toDecoy();

    expect(screen.queryByLabelText('Domain')).toBeNull();
    choose('Decoy site', 'A page built into the panel');
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: ' decoy.example.com ' } });
    await waitFor(() => expect(screen.getByLabelText('Page')).toBeTruthy());
    // The pages come from the panel, a moment after the field is there.
    await waitFor(() => chooseSelectOption(screen.getByLabelText('Page').id, 'Parked domain'));
    await goOn(MORE_TITLE);

    expect(await createNode(create)).toMatchObject({
      decoyMode: 'template',
      decoyTemplate: 'parked',
      decoyDomain: 'decoy.example.com',
      decoyCert: 'auto',
      decoyPort: 7443,
    });
  });

  it('asks for the files of a certificate on the node, and for the address of a proxy', async () => {
    const { create } = renderWizard();
    await toDecoy();

    choose('Decoy site', 'A reverse proxy to a site');
    expect(screen.getByLabelText('Address of the site')).toBeTruthy();
    expect(screen.queryByLabelText('Page')).toBeNull();
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'decoy.example.com' } });
    press('Next');
    await waitFor(() => expect(document.body.textContent).toContain('A decoy needs a domain'));
    expect(screen.queryByText(MORE_TITLE)).toBeNull();

    fireEvent.change(screen.getByLabelText('Address of the site'), {
      target: { value: 'https://site.example' },
    });
    fireEvent.click(screen.getByRole('radio', { name: 'Files on the node' }));
    expect(screen.queryByLabelText('Email for the certificate')).toBeNull();
    press('Next');
    expect(screen.queryByText(MORE_TITLE)).toBeNull();

    fireEvent.change(screen.getByLabelText('Certificate file'), {
      target: { value: '/etc/ssl/n.pem' },
    });
    fireEvent.change(screen.getByLabelText('Key file'), { target: { value: '/etc/ssl/n.key' } });
    await goOn(MORE_TITLE);
    expect(await createNode(create)).toMatchObject({
      decoyMode: 'proxy',
      decoyProxyUrl: 'https://site.example',
      decoyCert: 'manual',
      decoyCertFile: '/etc/ssl/n.pem',
      decoyKeyFile: '/etc/ssl/n.key',
    });
  });

  it('serves AdGuard Home as the decoy, which needs no page and no site', async () => {
    const { create } = renderWizard();
    await toDecoy();

    choose('Decoy site', ADGUARD);
    expect(screen.queryByLabelText('Page')).toBeNull();
    expect(screen.queryByLabelText('Address of the site')).toBeNull();
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'decoy.example.com' } });
    await goOn(MORE_TITLE);

    expect(await createNode(create)).toMatchObject({
      decoyMode: 'adguard',
      decoyDomain: 'decoy.example.com',
      decoyCert: 'auto',
      decoyPort: 7443,
    });
  });

  it('serves the decoy for the name of the node when no other domain is given', async () => {
    const { create } = renderWizard();
    await toDecoy('edge.example.org');

    choose('Decoy site', ADGUARD);
    expect(screen.getByLabelText('Domain')).toHaveProperty('placeholder', 'edge.example.org');
    expect(document.body.textContent).toContain('Left empty, it is the address of the node');
    await goOn(MORE_TITLE);

    expect(await createNode(create)).toMatchObject({
      decoyMode: 'adguard',
      decoyDomain: 'edge.example.org',
    });
  });

  it('asks for the domain of the decoy when the node is reached by an IP address', async () => {
    const { create } = renderWizard();
    await toDecoy('203.0.113.7');

    choose('Decoy site', ADGUARD);
    expect(screen.getByLabelText('Domain')).toHaveProperty('placeholder', 'node.example.com');
    press('Next');
    await waitFor(() => expect(document.body.textContent).toContain('A decoy needs a domain'));
    expect(screen.queryByText(MORE_TITLE)).toBeNull();

    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'decoy.example.com' } });
    await goOn(MORE_TITLE);
    expect(await createNode(create)).toMatchObject({
      address: '203.0.113.7',
      decoyDomain: 'decoy.example.com',
    });
  });

  it('serves a decoy on the usual port when the port box is cleared', async () => {
    const { create } = renderWizard();
    await toDecoy();

    choose('Decoy site', 'A page built into the panel');
    fireEvent.change(screen.getByLabelText('Domain'), { target: { value: 'decoy.example.com' } });
    fireEvent.change(screen.getByLabelText('Port on 127.0.0.1'), { target: { value: '' } });
    await goOn(MORE_TITLE);

    expect(await createNode(create)).toMatchObject({ decoyMode: 'template', decoyPort: 7443 });
  });

  it('holds a node back at the step with the problem, and not at an earlier one', async () => {
    renderWizard();
    await toDecoy('edge.example.org');
    choose('Decoy site', ADGUARD);
    await goOn(MORE_TITLE);

    // The decoy took the name of the node, so an IP address in its place leaves it without a domain.
    for (const title of [DECOY_TITLE, LINK_TITLE, SERVER_TITLE]) {
      press('Back');
      await screen.findByText(title);
    }
    fillServer('203.0.113.7');
    await goOn(LINK_TITLE);
    await goOn(DECOY_TITLE);
    press('Next');
    await waitFor(() => expect(document.body.textContent).toContain('A decoy needs a domain'));
    expect(screen.queryByText(MORE_TITLE)).toBeNull();
  });

  it('says what is left to do by hand once there is a decoy, with the port it was given', async () => {
    renderWizard();
    await toDecoy();

    const note = 'One thing is left for you to do on this panel';
    expect(document.body.textContent).not.toContain(note);
    choose('Decoy site', ADGUARD);
    expect(document.body.textContent).toContain(note);
    expect(document.body.textContent).toContain('127.0.0.1:7443');
    fireEvent.change(screen.getByLabelText('Port on 127.0.0.1'), { target: { value: '7500' } });
    expect(document.body.textContent).toContain('127.0.0.1:7500');
    choose('Decoy site', 'None');
    expect(document.body.textContent).not.toContain(note);
  });
});

describe('AgentNodeWizard: more options', () => {
  it('offers Psiphon beside Tor, and sends both in the order they are listed', async () => {
    const { create } = renderWizard();
    await toMore();

    fireEvent.click(
      screen.getByRole('checkbox', { name: /Psiphon, a SOCKS proxy on 127.0.0.1:19060/ }),
    );
    fireEvent.click(
      screen.getByRole('checkbox', { name: /Tor, a SOCKS proxy on 127.0.0.1:19050/ }),
    );

    expect((await createNode(create)).sidecars).toEqual(['tor', 'psiphon']);
  });

  it('creates the node with the usual options when this step is left as it is', async () => {
    const { create } = renderWizard();
    await toMore();

    expect(await createNode(create)).toMatchObject({
      name: 'edge-1',
      address: 'node.example.com',
      port: 8443,
      allowPrivateAddress: false,
      tags: [],
      sidecars: [],
      decoyMode: '',
    });
  });
});

describe('AgentNodeWizard: the install step', () => {
  async function created(options: Parameters<typeof renderWizard>[0] = {}) {
    const rendered = renderWizard(options);
    await toMore();
    press('Create node');
    await screen.findByText(/The node edge-1 is created/);
    return rendered;
  }

  it('shows the bundle once, with what to do on the server', async () => {
    await created();

    expect(screen.getByRole('textbox', { name: 'Pairing bundle' })).toHaveProperty(
      'value',
      'xab1.secret-material',
    );
    expect(document.body.textContent).toContain('Open TCP port 8443 in the firewall');
    expect(document.body.textContent).toContain('shown only once');
    expect((screen.getByDisplayValue(AGENT_INSTALL_COMMAND) as HTMLInputElement).readOnly).toBe(
      true,
    );
  });

  it('names the panel the node has to reach, in place of a port, when the node dials it', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: new URL('https://panel.example.com/'),
    });
    await created();

    expect(document.body.textContent).toContain('It does have to reach https://panel.example.com/');
    expect(document.body.textContent).not.toContain('in the firewall of the server');
  });

  it('follows the node until it is online, and no other node', async () => {
    const offline = { id: 7, name: 'edge-1', kind: 'agent', status: 'offline' };
    vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) =>
      url === '/panel/api/nodes/list' ? new Msg(true, '', [offline]) : new Msg(false, '', null),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await created({ queryClient });

    await screen.findByText(WAITING);
    await waitFor(() => expect(queryClient.getQueryData(keys.nodes.list())).toHaveLength(1));
    expect(screen.queryByText(ONLINE)).toBeNull();

    // The cache tells its observers a tick later, so the tick is waited out before looking.
    await act(async () => {
      queryClient.setQueryData(keys.nodes.list(), [
        offline,
        { id: 9, name: 'other', kind: 'agent', status: 'online' },
      ]);
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(queryClient.getQueryData(keys.nodes.list())).toHaveLength(2);
    expect(screen.queryByText(ONLINE)).toBeNull();
    expect(screen.getByText(WAITING)).toBeTruthy();

    act(() => {
      queryClient.setQueryData(keys.nodes.list(), [{ ...offline, status: 'online' }]);
    });
    await screen.findByText(ONLINE);
    expect(screen.queryByText(WAITING)).toBeNull();
  });

  it('closes only through Done, since the bundle is not stored', async () => {
    const { onClose } = await created();

    fireEvent.keyDown(document.body, { key: 'Escape', code: 'Escape' });
    const mask = document.querySelector('.ant-modal-wrap');
    if (mask) fireEvent.click(mask);
    expect(onClose).not.toHaveBeenCalled();
    expect(document.querySelector('.ant-modal-close')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Back' })).toBeNull();

    press('Done');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('stays on the last step when the panel refuses the node', async () => {
    // Whether the panel said yes is what decides, not whether it sent something along.
    const refused = new Msg<AgentPairing>(false, 'name taken', pairing().obj);
    const create = vi.fn<Create>().mockResolvedValue(refused);
    renderWizard({ create });
    await toMore();

    press('Create node');
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(document.querySelector('.ant-modal-footer .ant-btn-loading')).toBeNull(),
    );
    expect(screen.getByText(MORE_TITLE)).toBeTruthy();
    expect(screen.queryByText(/is created/)).toBeNull();
    // The loading icon of the button fades out for good in jsdom, so the button is found by its text.
    const footer = document.querySelector('.ant-modal-footer')?.textContent;
    expect(footer).toContain('Create node');
    expect(footer).not.toContain('Done');
  });
});
