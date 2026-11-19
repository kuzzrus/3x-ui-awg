import { Form } from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, waitFor } from '@testing-library/react';

import AmneziawgFields from '@/pages/inbounds/form/protocols/amneziawg';
import type { CpsSlot, I1ProfileChoice } from '@/lib/xray/i1Generators';
import { HttpUtil, Msg } from '@/utils';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

interface FormValues {
  nodeId: number | null;
  settings: { server: { kernelMode: boolean; routeBetweenClients?: boolean } };
}

const noop = () => {};
const CPS_PROFILES: Record<CpsSlot, I1ProfileChoice> = {
  i1: 'random',
  i2: 'random',
  i3: 'random',
  i4: 'random',
  i5: 'random',
};

// Shows the stored value next to the form, so a switch bound to the wrong field cannot pass.
function Stored() {
  const value = useWatch({ name: 'settings.server.routeBetweenClients' });
  return <output data-testid="stored">{String(value)}</output>;
}

function Harness({
  kernelMode,
  routeBetweenClients,
}: {
  kernelMode: boolean;
  routeBetweenClients?: boolean;
}) {
  const form = useForm<FormValues>({
    defaultValues: { nodeId: null, settings: { server: { kernelMode, routeBetweenClients } } },
  });
  return (
    <FormProvider {...form}>
      <Form>
        <AmneziawgFields
          awgPubKey="public-key"
          inboundId={null}
          regenInboundAwg={noop}
          regenInboundAwgObfuscation={noop}
          regenInboundAwgHeaderProtectionKey={noop}
          regenInboundAwgContentPaddingAddition={noop}
          onAwgVersionChange={noop}
          regenInboundAwgRekeyAfterTime={noop}
          regenInboundAwgRekeyTimeout={noop}
          regenInboundAwgRejectAfterTime={noop}
          regenInboundAwgKeepaliveTimeout={noop}
          regenInboundAwgMaxHandshakeAttempts={noop}
          cpsProfiles={CPS_PROFILES}
          onCpsProfileChange={noop}
          onRegenCps={noop}
          quicCapturing={false}
          onQuicCapture={noop}
          onQuicCaptureAll={noop}
        />
        <Stored />
      </Form>
    </FormProvider>
  );
}

function render(props: { kernelMode: boolean; routeBetweenClients?: boolean }) {
  vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', { ready: true, blockers: [] }));
  return renderWithProviders(<Harness {...props} />, { queryClient: makeTestQueryClient() });
}

function routingSwitch(root: HTMLElement): HTMLElement | null {
  const label = Array.from(root.querySelectorAll('label')).find(
    (l) => l.textContent === 'Routing between clients',
  );
  return label?.closest('.ant-form-item')?.querySelector<HTMLElement>('[role="switch"]') ?? null;
}

describe('AmneziaWG routing between clients in the inbound form', () => {
  afterEach(() => vi.restoreAllMocks());

  it('offers the switch off while the inbound runs on the embedded datapath', async () => {
    const { container, getByTestId } = render({ kernelMode: false });

    await waitFor(() => expect(routingSwitch(container)).toBeTruthy());
    expect(routingSwitch(container)?.getAttribute('aria-checked')).toBe('false');
    expect(getByTestId('stored').textContent).toBe('undefined');
  });

  it('writes the choice to the routeBetweenClients setting of the server', async () => {
    const { container, getByTestId } = render({ kernelMode: false });
    await waitFor(() => expect(routingSwitch(container)).toBeTruthy());

    fireEvent.click(routingSwitch(container)!);
    await waitFor(() => expect(getByTestId('stored').textContent).toBe('true'));
    expect(routingSwitch(container)?.getAttribute('aria-checked')).toBe('true');

    fireEvent.click(routingSwitch(container)!);
    await waitFor(() => expect(getByTestId('stored').textContent).toBe('false'));
  });

  it('shows a saved choice as it was saved', async () => {
    const { container } = render({ kernelMode: false, routeBetweenClients: true });

    await waitFor(() => expect(routingSwitch(container)).toBeTruthy());
    expect(routingSwitch(container)?.getAttribute('aria-checked')).toBe('true');
  });

  it('does not offer the switch in kernel mode, which always routes between clients', async () => {
    const { container, getByText } = render({ kernelMode: true });

    await waitFor(() => expect(getByText('Kernel mode')).toBeTruthy());
    expect(routingSwitch(container)).toBeNull();
  });
});
