import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { AllSetting } from '@/models/setting';
import FrontProxyTab from '@/pages/settings/FrontProxyTab';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const LABEL = 'Open port 80 in ufw while the certificate is obtained';

function quietServer() {
  vi.spyOn(HttpUtil, 'post').mockImplementation(async () => new Msg(true, '', {}));
}

describe('FrontProxyTab port 80 switch', () => {
  it('is on when the certificate is automatic and the setting is on', async () => {
    quietServer();
    const setting = new AllSetting({ frontProxyCertMode: 'auto', frontProxyAcmeFirewall: true });
    renderWithProviders(<FrontProxyTab allSetting={setting} updateSetting={vi.fn()} />);

    const toggle = await screen.findByRole('switch', { name: LABEL });
    expect(toggle.getAttribute('aria-checked')).toBe('true');
    expect(
      screen.getByText(/never changed: when there is one, the panel does nothing/),
    ).toBeTruthy();
  });

  it('shows the setting as it is when it is off', async () => {
    quietServer();
    const setting = new AllSetting({ frontProxyCertMode: 'auto', frontProxyAcmeFirewall: false });
    renderWithProviders(<FrontProxyTab allSetting={setting} updateSetting={vi.fn()} />);

    const toggle = await screen.findByRole('switch', { name: LABEL });
    expect(toggle.getAttribute('aria-checked')).toBe('false');
  });

  it.each([
    [true, false],
    [false, true],
  ])('turns the setting from %s to %s through updateSetting', async (from, to) => {
    quietServer();
    const updateSetting = vi.fn();
    const setting = new AllSetting({ frontProxyCertMode: 'auto', frontProxyAcmeFirewall: from });
    renderWithProviders(<FrontProxyTab allSetting={setting} updateSetting={updateSetting} />);

    fireEvent.click(await screen.findByRole('switch', { name: LABEL }));
    expect(updateSetting).toHaveBeenLastCalledWith({ frontProxyAcmeFirewall: to });
  });

  it('is not offered for a certificate that the admin keeps', async () => {
    quietServer();
    const setting = new AllSetting({ frontProxyCertMode: 'manual', frontProxyAcmeFirewall: true });
    renderWithProviders(<FrontProxyTab allSetting={setting} updateSetting={vi.fn()} />);

    await screen.findByText(/certificate files/i);
    expect(screen.queryByRole('switch', { name: LABEL })).toBeNull();
  });

  it('is on in a setting that has never been saved', () => {
    expect(new AllSetting({}).frontProxyAcmeFirewall).toBe(true);
  });
});
