import { act, fireEvent, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { AllSetting } from '@/models/setting';
import GeneralTab from '@/pages/settings/GeneralTab';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

afterEach(() => vi.restoreAllMocks());

// Mounting fetches inbound options with no visible change; settle it inside act().
async function renderGeneralTab(updateSetting: (patch: Partial<AllSetting>) => void) {
  renderWithProviders(
    <MemoryRouter initialEntries={['/settings']}>
      <GeneralTab allSetting={new AllSetting({ pageSize: 25 })} updateSetting={updateSetting} />
    </MemoryRouter>,
  );
  await act(async () => {});
}

describe('GeneralTab', () => {
  it('keeps the stored page size when the field is cleared', async () => {
    const updateSetting = vi.fn();

    await renderGeneralTab(updateSetting);

    const pageSizeInput = screen.getByDisplayValue('25');
    fireEvent.change(pageSizeInput, { target: { value: '' } });
    fireEvent.blur(pageSizeInput);

    expect(updateSetting).not.toHaveBeenCalled();
    expect((pageSizeInput as HTMLInputElement).value).toBe('25');
  });

  it('offers the panel outbound only the outbounds the panel itself has', async () => {
    const template = {
      xraySetting: {
        outbounds: [
          { tag: 'direct', protocol: 'freedom' },
          { tag: 'eu-hop', protocol: 'freedom', nodeTags: ['eu'] },
          { tag: 'any-hop', protocol: 'freedom', nodeTags: [] },
        ],
        routing: {
          balancers: [
            { tag: 'nodes-only', selector: ['eu-hop'] },
            { tag: 'with-direct', selector: ['eu-hop', 'direct'] },
          ],
        },
      },
      subscriptionOutboundTags: [],
    };
    vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string) =>
      url === '/panel/api/xray/'
        ? new Msg(true, '', JSON.stringify(template))
        : new Msg(false, '', null),
    );

    renderWithProviders(
      <MemoryRouter initialEntries={['/settings']}>
        <GeneralTab allSetting={new AllSetting({})} updateSetting={vi.fn()} />
      </MemoryRouter>,
    );
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

    const select = screen.getAllByText('Direct connection')[0].closest('.ant-select');
    if (!select) throw new Error('the panel outbound select was not found');
    fireEvent.mouseDown(select.querySelector('.ant-select-selector') ?? select);
    const offered = Array.from(document.querySelectorAll('.ant-select-item-option')).map(
      (option) => option.getAttribute('title') ?? option.textContent,
    );

    expect(offered).toContain('direct');
    expect(offered).toContain('any-hop');
    expect(offered).toContain('with-direct');
    expect(offered).not.toContain('eu-hop');
    expect(offered).not.toContain('nodes-only');
  });

  it('forwards typed page sizes unchanged, zero included', async () => {
    const updateSetting = vi.fn();

    await renderGeneralTab(updateSetting);

    fireEvent.change(screen.getByDisplayValue('25'), { target: { value: '0' } });

    expect(updateSetting).toHaveBeenCalledWith({ pageSize: 0 });
  });
});
