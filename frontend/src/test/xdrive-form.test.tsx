import { Form } from 'antd';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { FormProvider, useForm } from 'react-hook-form';
import { describe, expect, it, vi } from 'vitest';

import XDriveForm from '@/lib/xray/forms/transport/XDriveForm';
import { renderWithProviders } from './test-utils';

function Harness({
  service,
  save,
  extra = {},
}: {
  service: string;
  save: (value: unknown) => void;
  extra?: Record<string, unknown>;
}) {
  const methods = useForm({
    defaultValues: {
      streamSettings: {
        network: 'xdrive',
        xdriveSettings: {
          service,
          remoteFolder: 'xdrive-test',
          secrets: ['my-login', 'my-app-password'],
          ...extra,
        },
      },
    },
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

const PRESET_BUTTON = 'Use Yandex Disk WebDAV template';

describe('XDRIVE form', () => {
  it('loads the Yandex Disk template and tuning and keeps the folder and the secrets', async () => {
    const save = vi.fn();
    renderWithProviders(<Harness service="template" save={save} extra={{ eagerWindowMs: 1500 }} />);

    fireEvent.click(screen.getByRole('button', { name: PRESET_BUTTON }));
    fireEvent.click(screen.getByRole('button', { name: 'Save XDRIVE' }));

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      streamSettings: {
        xdriveSettings: {
          service: 'template',
          remoteFolder: 'xdrive-test',
          secrets: ['my-login', 'my-app-password'],
          eagerWindowMs: 1500,
          segmentBytes: 262144,
          pollIntervalMs: 300,
          concurrency: 8,
          template: {
            auth: { type: 'basic', username: '{secret0}', password: '{secret1}' },
            list: { method: 'PROPFIND', headers: { Depth: '1' } },
          },
        },
      },
    });
  });

  it('shows the loaded template in the editor', async () => {
    renderWithProviders(<Harness service="template" save={vi.fn()} />);
    expect(document.body.textContent).not.toContain('PROPFIND');

    fireEvent.click(screen.getByRole('button', { name: PRESET_BUTTON }));

    await waitFor(() => expect(document.body.textContent).toContain('PROPFIND'));
  });

  it('offers the preset only for the custom template service', () => {
    renderWithProviders(<Harness service="local" save={vi.fn()} />);
    expect(screen.queryByRole('button', { name: PRESET_BUTTON })).toBeNull();
  });
});
