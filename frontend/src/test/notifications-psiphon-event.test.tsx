import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { DiscordNotifications } from '@/components/ui/notifications/DiscordNotifications';
import { EmailNotifications } from '@/components/ui/notifications/EmailNotifications';
import { TelegramNotifications } from '@/components/ui/notifications/TelegramNotifications';
import { AllSetting } from '@/models/setting';

import { renderWithProviders } from './test-utils';

const channels = [
  ['Telegram', TelegramNotifications, 'tgEnabledEvents'],
  ['Discord', DiscordNotifications, 'discordEnabledEvents'],
  ['e-mail', EmailNotifications, 'smtpEnabledEvents'],
] as const;

describe.each(channels)('the %s notification settings', (_name, Component, field) => {
  it('offer a new Psiphon region as an event of its own, beside the events that are on', () => {
    const updateSetting = vi.fn();
    renderWithProviders(
      <Component
        allSetting={new AllSetting({ [field]: 'login.attempt,cpu.high' })}
        updateSetting={updateSetting}
      />,
    );

    expect(screen.getByText('Psiphon')).toBeTruthy();
    const toggle = screen.getByRole('checkbox', { name: 'New region found' });
    expect((toggle as HTMLInputElement).checked).toBe(false);
    fireEvent.click(toggle);
    expect(updateSetting).toHaveBeenCalledWith({
      [field]: 'login.attempt,cpu.high,psiphon.regions.new',
    });
  });

  it('show the event as on, and switch it off, when it is among the events that are on', () => {
    const updateSetting = vi.fn();
    renderWithProviders(
      <Component
        allSetting={new AllSetting({ [field]: 'psiphon.regions.new,login.attempt' })}
        updateSetting={updateSetting}
      />,
    );

    const toggle = screen.getByRole('checkbox', { name: 'New region found' });
    expect((toggle as HTMLInputElement).checked).toBe(true);
    fireEvent.click(toggle);
    expect(updateSetting).toHaveBeenCalledWith({ [field]: 'login.attempt' });
  });
});
