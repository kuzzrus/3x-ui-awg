import type { ReactNode } from 'react';
import { Form } from 'antd';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { FormProvider, useForm } from 'react-hook-form';
import { afterEach, describe, expect, it, vi } from 'vitest';

import NaiveProxyFields from '@/pages/inbounds/form/protocols/naiveproxy';
import { HttpUtil, Msg } from '@/utils';
import { keys } from '@/api/queryKeys';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

afterEach(() => {
  vi.restoreAllMocks();
});

function Harness({ children }: { children: ReactNode }) {
  const methods = useForm({ defaultValues: { settings: { publicPort: 443 } } });
  return (
    <FormProvider {...methods}>
      <Form>{children}</Form>
    </FormProvider>
  );
}

// A fresh panel has no engine binary; the form has to say so and let the admin
// install it right there, otherwise the inbound just never starts.
function mockEngine(installed: boolean, supported = true) {
  const state = { installed };
  const calls: string[] = [];
  vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string) => {
    calls.push(url);
    if (url.endsWith('/naiveproxy/status')) {
      return new Msg(true, '', { installed: state.installed, supported, platform: 'linux/arm64' });
    }
    if (url.endsWith('/naiveproxy/install')) {
      state.installed = true;
      return new Msg(true, '', null);
    }
    return new Msg(true, '', null);
  });
  return calls;
}

function renderFields() {
  const queryClient = makeTestQueryClient();
  renderWithProviders(
    <Harness>
      <NaiveProxyFields />
    </Harness>,
    { queryClient },
  );
  return queryClient;
}

describe('NaiveProxy engine notice', () => {
  it('offers to install a missing engine and clears once it is installed', async () => {
    const calls = mockEngine(false);
    renderFields();

    const button = await screen.findByRole('button', { name: /^install$/i });
    fireEvent.click(button);

    await waitFor(() => expect(calls.some((u) => u.endsWith('/naiveproxy/install'))).toBe(true));
    await waitFor(() => expect(screen.queryByRole('button', { name: /^install$/i })).toBeNull());
  });

  it('shows nothing when the engine is already installed', async () => {
    mockEngine(true);
    const queryClient = renderFields();

    await waitFor(() =>
      expect(queryClient.getQueryState(keys.xray.naiveProxyEngine())?.status).toBe('success'),
    );
    expect(screen.queryByRole('button', { name: /^install$/i })).toBeNull();
    expect(document.querySelector('.ant-alert')).toBeNull();
  });

  it('explains an unsupported platform instead of offering an install that cannot work', async () => {
    mockEngine(false, false);
    renderFields();

    await waitFor(() => expect(document.querySelector('.ant-alert-error')).not.toBeNull());
    expect(screen.queryByRole('button', { name: /^install$/i })).toBeNull();
  });
});
