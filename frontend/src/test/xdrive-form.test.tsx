import { Form } from 'antd';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { FormProvider, useForm } from 'react-hook-form';
import { describe, expect, it, vi } from 'vitest';

import XDriveForm from '@/lib/xray/forms/transport/XDriveForm';
import { yandexDiskPreset } from '@/lib/xray/xdrive-presets';
import { chooseSelectOption, listSelectOptions, renderWithProviders } from './test-utils';

function Harness({
  save,
  xdrive,
}: {
  save: (value: unknown) => void;
  xdrive: Record<string, unknown>;
}) {
  const methods = useForm({
    defaultValues: { streamSettings: { network: 'xdrive', xdriveSettings: xdrive } },
  });
  return (
    <FormProvider {...methods}>
      <Form>
        <XDriveForm />
        <button type="button" onClick={() => void methods.handleSubmit(save)()}>
          Save XDRIVE
        </button>
      </Form>
    </FormProvider>
  );
}

const YANDEX = 'Yandex Disk (WebDAV)';
const serviceFieldId = () => screen.getByLabelText('Storage service').id;
const pick = (text: string) => chooseSelectOption(serviceFieldId(), text);
const shownService = () =>
  document.getElementById(serviceFieldId())?.closest('.ant-select')?.textContent?.trim();
const inputValue = (label: string) => (screen.getByLabelText(label) as HTMLInputElement).value;
const submitted = async (save: ReturnType<typeof vi.fn>) => {
  fireEvent.click(screen.getByRole('button', { name: 'Save XDRIVE' }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  return (save.mock.calls[0][0] as { streamSettings: { xdriveSettings: Record<string, unknown> } })
    .streamSettings.xdriveSettings;
};

const stored = (secrets: string[]) => ({
  service: 'template',
  remoteFolder: 'xdrive-test',
  secrets,
  template: yandexDiskPreset().template,
});

describe('XDRIVE form storage service', () => {
  it('lists Yandex Disk beside Google Drive', () => {
    renderWithProviders(<Harness save={vi.fn()} xdrive={{ service: 'google-drive' }} />);
    expect(listSelectOptions(serviceFieldId())).toEqual([
      'Google Drive',
      YANDEX,
      'Local folder',
      'Custom template',
    ]);
  });

  it('stores Yandex Disk as the template service and asks for a login and an app password', async () => {
    const save = vi.fn();
    renderWithProviders(
      <Harness
        save={save}
        xdrive={{
          service: 'google-drive',
          remoteFolder: 'xdrive-test',
          secrets: ['id', 'secret', 'token'],
        }}
      />,
    );

    pick(YANDEX);
    expect(await screen.findByLabelText('Yandex login')).toBeDefined();
    expect(screen.queryByLabelText('Client ID')).toBeNull();
    expect(inputValue('Yandex login')).toBe('');
    fireEvent.change(screen.getByLabelText('Yandex login'), { target: { value: 'my-login' } });
    fireEvent.change(screen.getByLabelText('App password (Files WebDAV)'), {
      target: { value: 'my-app-password' },
    });

    expect(await submitted(save)).toMatchObject({
      service: 'template',
      remoteFolder: 'xdrive-test',
      secrets: ['my-login', 'my-app-password'],
      segmentBytes: 262144,
      pollIntervalMs: 300,
      concurrency: 8,
      template: {
        auth: { type: 'basic', username: '{secret0}', password: '{secret1}' },
        list: { method: 'PROPFIND', headers: { Depth: '1' } },
      },
    });
  });

  it('keeps the credentials of a custom template when it becomes Yandex Disk', async () => {
    renderWithProviders(
      <Harness
        save={vi.fn()}
        xdrive={{
          service: 'template',
          remoteFolder: 'xdrive-test',
          secrets: ['kept-login', 'kept-password'],
          template: { put: { method: 'PUT', url: 'https://example.com/{name}' } },
        }}
      />,
    );
    expect(shownService()).toBe('Custom template');

    pick(YANDEX);

    expect(await screen.findByLabelText('Yandex login')).toBeDefined();
    expect(inputValue('Yandex login')).toBe('kept-login');
    expect(inputValue('App password (Files WebDAV)')).toBe('kept-password');
  });

  it('opens a stored Yandex Disk configuration as Yandex Disk, without the JSON editor', () => {
    renderWithProviders(
      <Harness save={vi.fn()} xdrive={stored(['stored-login', 'stored-password'])} />,
    );
    expect(shownService()).toBe(YANDEX);
    expect(inputValue('Yandex login')).toBe('stored-login');
    expect(screen.queryByLabelText('Secrets')).toBeNull();
    expect(document.body.textContent).not.toContain('PROPFIND');
  });

  it('opens any other template as a custom one, with its JSON', () => {
    renderWithProviders(
      <Harness
        save={vi.fn()}
        xdrive={{
          service: 'template',
          remoteFolder: 'shared',
          secrets: [],
          template: { put: { method: 'PUT', url: 'https://example.com/{name}' } },
        }}
      />,
    );
    expect(shownService()).toBe('Custom template');
    expect(document.body.textContent).toContain('example.com');
    expect(screen.queryByLabelText('Yandex login')).toBeNull();
  });

  it('shows the template of a Yandex Disk configuration once it is opened as custom', async () => {
    const save = vi.fn();
    renderWithProviders(<Harness save={save} xdrive={stored(['login', 'password'])} />);

    pick('Custom template');

    await waitFor(() => expect(document.body.textContent).toContain('PROPFIND'));
    expect(shownService()).toBe('Custom template');
    expect(await submitted(save)).toMatchObject({
      service: 'template',
      secrets: ['login', 'password'],
      template: { list: { method: 'PROPFIND' } },
    });
  });

  it('does not show the Yandex credentials in the Google Drive fields', async () => {
    const save = vi.fn();
    renderWithProviders(<Harness save={save} xdrive={stored(['login', 'password'])} />);

    pick('Google Drive');

    expect(await screen.findByLabelText('Client ID')).toBeDefined();
    expect(inputValue('Client ID')).toBe('');
    expect(await submitted(save)).toMatchObject({
      service: 'google-drive',
      secrets: ['', '', ''],
    });
  });
});
