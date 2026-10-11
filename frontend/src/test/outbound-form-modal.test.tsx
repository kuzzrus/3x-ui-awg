import { afterEach, describe, it, expect, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { QueryClient } from '@tanstack/react-query';

import OutboundFormModal from '@/pages/xray/outbounds/OutboundFormModal';
import { keys } from '@/api/queryKeys';
import { HttpUtil, Msg } from '@/utils';
import {
  renderWithProviders,
  fieldLabels,
  listSelectOptions,
  chooseSelectOption,
} from './test-utils';

function renderModal(outbound: Record<string, unknown> | null = null) {
  return renderWithProviders(
    <OutboundFormModal
      open
      outbound={outbound}
      existingTags={[]}
      onClose={() => {}}
      onConfirm={() => {}}
    />,
  );
}

function toggleSockoptsSwitch() {
  const item = Array.from(document.querySelectorAll('.ant-form-item')).find(
    (el) =>
      (el.querySelector('.ant-form-item-label label')?.textContent ?? '').trim() === 'Sockopts',
  );
  const control = item?.querySelector('.ant-switch');
  if (!control) throw new Error('Sockopts switch not found');
  fireEvent.click(control);
}

describe('OutboundFormModal', () => {
  it('renders add mode without crashing', () => {
    renderModal(null);
    expect(document.querySelector('.ant-modal')).toBeTruthy();
    expect(fieldLabels().length).toBeGreaterThan(0);
  });

  it('field structure differs per protocol (not a vacuous snapshot loop)', async () => {
    renderModal(null);
    const protocols = listSelectOptions('protocol');
    expect(protocols.length).toBeGreaterThan(3);

    const labelsByProto: Record<string, string[]> = {};
    for (const proto of protocols) {
      chooseSelectOption('protocol', proto);
      // Flush antd Form.useWatch('protocol') so protocol-specific fields render before
      // reading; otherwise every iteration sees the same default (vless) DOM.
      await act(async () => {
        await new Promise((r) => setTimeout(r, 0));
      });
      labelsByProto[proto] = fieldLabels();
    }

    // Distinct protocols must yield distinct field sets (a vacuous loop is all-identical).
    const distinctShapes = new Set(Object.values(labelsByProto).map((l) => l.join('|')));
    expect(distinctShapes.size).toBeGreaterThan(1);

    // vless carries an Encryption field; wireguard does not — proves real protocol switching.
    if (labelsByProto.vless) {
      expect(labelsByProto.vless).toContain('Encryption');
    }
    if (labelsByProto.wireguard) {
      expect(labelsByProto.wireguard).not.toContain('Encryption');
    }
  }, 30000); // iterates every protocol, re-rendering a heavy modal each time — slow on CI runners

  // Freedom's card and the Transport tab's Sockopts block both write
  // sockopt.domainStrategy, so freedom must show only one control for it.
  it('hides the Transport sockopt strategy for freedom', () => {
    renderModal({ protocol: 'freedom', tag: 'direct', settings: {} });
    toggleSockoptsSwitch();

    expect(fieldLabels()).toContain('Sockopts');
    expect(fieldLabels()).not.toContain('Domain Strategy');
    expect(fieldLabels()).toContain('Strategy');
  });

  it('keeps the Transport sockopt strategy for protocols without a card field', () => {
    renderModal({ protocol: 'vless', tag: 'proxy', settings: {} });
    toggleSockoptsSwitch();

    expect(fieldLabels()).toContain('Domain Strategy');
  });

  // The core migrates freedom's outbound-root targetStrategy into the very same
  // sockopt.domainStrategy the card writes, so the modal must not offer both.
  it('offers freedom one strategy knob and other protocols the root one', async () => {
    renderModal(null);

    chooseSelectOption('protocol', 'freedom');
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
    const freedomLabels = fieldLabels();
    expect(freedomLabels).toContain('Strategy');
    expect(freedomLabels).not.toContain('Target Strategy');

    chooseSelectOption('protocol', 'vless');
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(fieldLabels()).toContain('Target Strategy');
  });

  it('saves a vless reverse outbound while reverse sniffing stays disabled', async () => {
    const onConfirm = vi.fn();
    renderWithProviders(
      <OutboundFormModal
        open
        outbound={{
          protocol: 'vless',
          tag: 'reverse-out',
          settings: {
            vnext: [
              {
                address: 'example.com',
                port: 443,
                users: [{ id: 'c9f0c2d0-0000-4000-8000-000000000000', encryption: 'none' }],
              },
            ],
            reverse: { tag: 'r1', sniffing: {} },
          },
          streamSettings: { network: 'tcp', security: 'none' },
        }}
        existingTags={[]}
        onClose={() => {}}
        onConfirm={onConfirm}
      />,
    );
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

    const ok = document.querySelector('.ant-modal-footer .ant-btn-primary') as HTMLElement;
    expect(ok).toBeTruthy();
    await act(async () => {
      fireEvent.click(ok);
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(onConfirm).toHaveBeenCalledTimes(1);
    const payload = onConfirm.mock.calls[0][0] as {
      settings: { reverse?: { tag?: string } };
    };
    expect(payload.settings.reverse?.tag).toBe('r1');
  });

  describe('node tags', () => {
    afterEach(() => vi.restoreAllMocks());

    const hop = { protocol: 'freedom', tag: 'eu-hop', settings: {}, nodeTags: ['eu'] };

    async function save(outbound: Record<string, unknown> | null, edit?: () => void) {
      const onConfirm = vi.fn();
      renderWithProviders(
        <OutboundFormModal
          open
          outbound={outbound}
          existingTags={[]}
          onClose={() => {}}
          onConfirm={onConfirm}
        />,
      );
      await act(async () => {
        await new Promise((r) => setTimeout(r, 0));
      });
      edit?.();
      const ok = document.querySelector('.ant-modal-footer .ant-btn-primary') as HTMLElement;
      await act(async () => {
        fireEvent.click(ok);
      });
      await act(async () => {
        await new Promise((r) => setTimeout(r, 0));
      });
      return onConfirm;
    }

    it('keeps the node tags of a saved outbound, which the form used to drop', async () => {
      const onConfirm = await save(hop);

      expect(onConfirm).toHaveBeenCalledTimes(1);
      expect(onConfirm.mock.calls[0][0]).toMatchObject({ tag: 'eu-hop', nodeTags: ['eu'] });
    });

    it('saves the tags typed into the field', async () => {
      const onConfirm = await save({ ...hop, nodeTags: undefined }, () => {
        fireEvent.change(screen.getByLabelText('Agent nodes'), { target: { value: 'us,exit,' } });
      });

      expect(onConfirm.mock.calls[0][0]).toMatchObject({ nodeTags: ['us', 'exit'] });
    });

    it('leaves the key out of an outbound that is for every node', async () => {
      const onConfirm = await save({ ...hop, nodeTags: undefined });

      expect(onConfirm.mock.calls[0][0]).not.toHaveProperty('nodeTags');
    });

    it('drops the key when the last tag is removed', async () => {
      const onConfirm = await save(hop, () => {
        const select = screen.getByLabelText('Agent nodes').closest('.ant-select');
        const remove = select?.querySelector('.ant-select-selection-item-remove');
        if (!remove) throw new Error('the tag has no remove button');
        fireEvent.click(remove);
      });

      expect(onConfirm.mock.calls[0][0]).not.toHaveProperty('nodeTags');
    });

    it('suggests the tags the agent nodes carry', async () => {
      vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) =>
        url === '/panel/api/nodes/list'
          ? new Msg(true, '', [{ id: 1, name: 'a', kind: 'agent', tags: ['eu', 'exit'] }])
          : new Msg(false, '', null),
      );
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      renderWithProviders(
        <OutboundFormModal
          open
          outbound={null}
          existingTags={[]}
          onClose={() => {}}
          onConfirm={() => {}}
        />,
        { queryClient },
      );

      await waitFor(() => expect(queryClient.getQueryData(keys.nodes.list())).toHaveLength(1));
      chooseSelectOption(screen.getByLabelText('Agent nodes').id, 'exit');

      const chosen = Array.from(document.querySelectorAll('.ant-select-selection-item'));
      expect(chosen.map((item) => item.textContent)).toContain('exit');
    });
  });
});
