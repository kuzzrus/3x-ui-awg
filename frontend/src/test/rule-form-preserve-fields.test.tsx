import { afterEach, describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import RuleFormModal from '@/pages/xray/routing/RuleFormModal';
import { keys } from '@/api/queryKeys';
import { HttpUtil, Msg } from '@/utils';

import { chooseSelectOption, renderWithProviders } from './test-utils';

function renderRule(rule: Record<string, unknown> | null, onConfirm = vi.fn()) {
  renderWithProviders(
    <RuleFormModal
      open
      rule={rule}
      inboundTags={[]}
      outboundTags={['direct']}
      balancerTags={[]}
      onClose={vi.fn()}
      onConfirm={onConfirm}
    />,
  );
  return onConfirm;
}

describe('RuleFormModal edit preserves unsurfaced fields', () => {
  afterEach(() => vi.restoreAllMocks());

  it('keeps a field the form does not surface (ruleTag) when saving an edit', () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', []));
    const onConfirm = vi.fn();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    renderWithProviders(
      <QueryClientProvider client={queryClient}>
        <RuleFormModal
          open
          rule={{ type: 'field', outboundTag: 'block', ruleTag: 'my-tag', enabled: true }}
          inboundTags={[]}
          outboundTags={['block']}
          balancerTags={[]}
          onClose={vi.fn()}
          onConfirm={onConfirm}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm.mock.calls[0][0]).toMatchObject({ ruleTag: 'my-tag' });
  });

  it('selects existing clients for the user criterion', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(
      new Msg(true, '', [
        { id: 1, email: 'alice@example.com' },
        { id: 2, email: 'bob@example.com' },
      ]),
    );
    const onConfirm = vi.fn();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    renderWithProviders(
      <RuleFormModal
        open
        rule={null}
        inboundTags={[]}
        outboundTags={['direct']}
        balancerTags={[]}
        onClose={vi.fn()}
        onConfirm={onConfirm}
      />,
      { queryClient },
    );

    await waitFor(() =>
      expect(HttpUtil.get).toHaveBeenCalledWith('/panel/api/clients/list', undefined, {
        silent: true,
      }),
    );
    await waitFor(() => expect(queryClient.getQueryData(keys.clients.all())).toHaveLength(2));
    const userField = screen.getByLabelText('User');
    expect(screen.getByText('Select users')).toBeTruthy();
    chooseSelectOption(userField.id, 'alice@example.com');
    chooseSelectOption(userField.id, 'bob@example.com');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    expect(onConfirm).toHaveBeenCalledWith(
      expect.objectContaining({ user: ['alice@example.com', 'bob@example.com'] }),
    );
  });

  it('preserves a saved user that is no longer in the client list', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(
      new Msg(true, '', [{ id: 1, email: 'alice@example.com' }]),
    );
    const onConfirm = vi.fn();

    renderWithProviders(
      <RuleFormModal
        open
        rule={{ type: 'field', user: ['removed@example.com'], outboundTag: 'direct' }}
        inboundTags={[]}
        outboundTags={['direct']}
        balancerTags={[]}
        onClose={vi.fn()}
        onConfirm={onConfirm}
      />,
    );

    await waitFor(() => expect(screen.getByText('removed@example.com')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    expect(onConfirm).toHaveBeenCalledWith(
      expect.objectContaining({ user: ['removed@example.com'] }),
    );
  });

  it('saves the node tags a rule is for, trimmed and once each', () => {
    const onConfirm = renderRule(null);

    fireEvent.change(screen.getByLabelText('Agent nodes'), { target: { value: ' eu,eu,exit,' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    expect(onConfirm).toHaveBeenCalledWith(expect.objectContaining({ nodeTags: ['eu', 'exit'] }));
  });

  it('leaves the node tags out of a rule that is for every node', () => {
    const onConfirm = renderRule(null);

    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    expect(onConfirm.mock.calls[0][0]).not.toHaveProperty('nodeTags');
  });

  it('keeps the node tags of a saved rule, and drops the key with the last one', () => {
    const kept = renderRule({ type: 'field', outboundTag: 'direct', nodeTags: ['eu', 'us'] });
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));
    expect(kept).toHaveBeenCalledWith(expect.objectContaining({ nodeTags: ['eu', 'us'] }));
  });

  it('drops the nodeTags key when every tag is removed', () => {
    const onConfirm = renderRule({ type: 'field', outboundTag: 'direct', nodeTags: ['eu'] });

    const select = screen.getByLabelText('Agent nodes').closest('.ant-select');
    const remove = select?.querySelector('.ant-select-selection-item-remove');
    if (!remove) throw new Error('the tag has no remove button');
    fireEvent.click(remove);
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm.mock.calls[0][0]).not.toHaveProperty('nodeTags');
  });

  it('suggests the tags the agent nodes carry', async () => {
    vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) =>
      url === '/panel/api/nodes/list'
        ? new Msg(true, '', [
            { id: 1, name: 'a', kind: 'agent', tags: ['eu', 'exit'] },
            { id: 2, name: 'b', kind: 'agent', tags: ['EU'] },
          ])
        : new Msg(false, '', null),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const onConfirm = vi.fn();
    renderWithProviders(
      <RuleFormModal
        open
        rule={null}
        inboundTags={[]}
        outboundTags={['direct']}
        balancerTags={[]}
        onClose={vi.fn()}
        onConfirm={onConfirm}
      />,
      { queryClient },
    );

    await waitFor(() => expect(queryClient.getQueryData(keys.nodes.list())).toHaveLength(2));
    const field = screen.getByLabelText('Agent nodes');
    chooseSelectOption(field.id, 'exit');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    expect(onConfirm).toHaveBeenCalledWith(expect.objectContaining({ nodeTags: ['exit'] }));
    const offered = Array.from(document.querySelectorAll('.ant-select-item-option')).map(
      (o) => o.getAttribute('title') ?? o.textContent,
    );
    expect(offered).toEqual(['eu', 'exit']);
  });

  it('accepts a custom user identifier that is not a client record', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(
      new Msg(true, '', [{ id: 1, email: 'alice@example.com' }]),
    );
    const onConfirm = vi.fn();

    renderWithProviders(
      <RuleFormModal
        open
        rule={null}
        inboundTags={[]}
        outboundTags={['direct']}
        balancerTags={[]}
        onClose={vi.fn()}
        onConfirm={onConfirm}
      />,
    );

    const userField = screen.getByLabelText('User');
    fireEvent.change(userField, { target: { value: 'office-proxy,' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    expect(onConfirm).toHaveBeenCalledWith(expect.objectContaining({ user: ['office-proxy'] }));
  });

  // Xray takes a port as a number and the schema of the form takes text, so a rule stored with numbers could not be saved.
  it('saves a rule whose ports are stored as numbers, as text', () => {
    const onConfirm = renderRule({
      type: 'field',
      outboundTag: 'direct',
      port: 443,
      sourcePort: 53,
      vlessRoute: 7,
    });

    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm).toHaveBeenCalledWith(
      expect.objectContaining({ port: '443', sourcePort: '53', vlessRoute: '7' }),
    );
  });

  // The shape Xray refuses must not become an empty field: the rule would be saved without its port, and match every one.
  it('does not save a rule whose port is neither a number nor text, and says why', async () => {
    const onConfirm = renderRule({ type: 'field', outboundTag: 'direct', port: [80, 443] });

    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    await waitFor(() => expect(document.body.textContent).toContain('cannot read'));
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('still saves a rule whose ports are text, or have none', () => {
    const onConfirm = renderRule({ type: 'field', outboundTag: 'direct', port: '80,443' });

    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    expect(onConfirm).toHaveBeenCalledWith(expect.objectContaining({ port: '80,443' }));
    expect(onConfirm.mock.calls[0][0]).not.toHaveProperty('sourcePort');
  });

  // Xray takes a network as a list or as text, and the inbound tags and the protocols as text or as a list.
  it('saves a rule whose network is a list, and whose inbound tags and protocols are text', () => {
    const onConfirm = renderRule({
      type: 'field',
      outboundTag: 'direct',
      network: ['tcp', 'udp'],
      inboundTag: 'api',
      protocol: 'http, tls',
    });

    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm).toHaveBeenCalledWith(
      expect.objectContaining({
        network: 'tcp,udp',
        inboundTag: ['api'],
        protocol: ['http', 'tls'],
      }),
    );
  });

  it('does not save a rule whose inbound tags are of a shape that no list or text is', async () => {
    const onConfirm = renderRule({ type: 'field', outboundTag: 'direct', inboundTag: 5 });

    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }));

    await waitFor(() => expect(document.body.textContent).toContain('cannot read'));
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
