import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { Form } from 'antd';
import type { ReactNode } from 'react';
import { FormProvider, useForm } from 'react-hook-form';

import InboundFormModal from '@/pages/inbounds/form/InboundFormModal';
import { RealityForm } from '@/pages/inbounds/form/security';
import type { NodeRecord } from '@/schemas/node';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';
import type { RealityScanResult } from '@/generated/types';
import { HttpUtil } from '@/utils';
import { chooseSelectOption, renderWithProviders } from './test-utils';

const noop = () => {};

function FormHarness({ children }: { children: ReactNode }) {
  const methods = useForm<InboundFormValues>();
  return (
    <FormProvider {...methods}>
      <Form>{children}</Form>
    </FormProvider>
  );
}

function renderRealityForm(props: { nodeDecoy?: { target: string; serverName: string } | null }) {
  return renderWithProviders(
    <FormHarness>
      <RealityForm
        saving={false}
        scanning={false}
        scanResult={null}
        scanRealityTarget={noop}
        scanRealityCandidates={async () => []}
        applyRealityScanResult={noop}
        applyNodeDecoy={noop}
        randomizeShortIds={noop}
        randomizeSpiderX={noop}
        genRealityKeypair={noop}
        clearRealityKeypair={noop}
        genMldsa65={noop}
        clearMldsa65={noop}
        {...props}
      />
    </FormHarness>,
  );
}

const useDecoyButton = () => screen.queryByRole('button', { name: /^Use the decoy of this node/ });

describe('the REALITY form on a node that serves a decoy', () => {
  it('offers the decoy as the fallback, with the address and the name it hands a probe to', () => {
    renderRealityForm({ nodeDecoy: { target: '127.0.0.1:7443', serverName: 'site.example.com' } });
    expect(useDecoyButton()?.textContent).toBe(
      'Use the decoy of this node: 127.0.0.1:7443, SNI site.example.com',
    );
  });

  it('offers nothing where the node serves no decoy', () => {
    renderRealityForm({ nodeDecoy: null });
    expect(useDecoyButton()).toBeNull();
  });
});

const edge: NodeRecord = {
  id: 7,
  name: 'edge',
  kind: 'agent',
  enable: true,
  status: 'online',
  decoy: { mode: 'template', domain: 'site.example.com' },
};
const bare: NodeRecord = { id: 8, name: 'bare', kind: 'agent', enable: true, status: 'online' };

function renderAddModal(nodes: NodeRecord[]) {
  return renderWithProviders(
    <InboundFormModal
      open
      mode="add"
      dbInbound={null}
      dbInbounds={[]}
      availableNodes={nodes}
      onClose={noop}
      onSaved={noop}
    />,
  );
}

const targetInput = () => screen.getByPlaceholderText('example.com:443') as HTMLInputElement;

// The field has no id of its own, so the select is found by the label that names it.
function chooseNode(name: string) {
  chooseSelectOption(screen.getByLabelText('Deploy to').id, name);
}

function chooseReality() {
  fireEvent.click(screen.getByRole('tab', { name: 'Security' }));
  fireEvent.click(screen.getByRole('radio', { name: 'Reality' }));
}

describe('an inbound on a node that serves a decoy', () => {
  it('takes the decoy as the REALITY fallback once REALITY is chosen, and again on the button', async () => {
    renderAddModal([edge, bare]);
    chooseNode('edge');
    chooseReality();
    await waitFor(() => expect(targetInput().value).toBe('127.0.0.1:7443'));
    expect(screen.getByText('site.example.com')).toBeTruthy();

    fireEvent.change(targetInput(), { target: { value: 'www.example.org:443' } });
    fireEvent.click(useDecoyButton()!);
    await waitFor(() => expect(targetInput().value).toBe('127.0.0.1:7443'));
  });

  it('leaves the fallback to be chosen on a node that serves none, and on the panel', async () => {
    renderAddModal([edge, bare]);
    chooseNode('bare');
    chooseReality();
    await waitFor(() => expect(screen.getByRole('radio', { name: 'Reality' })).toBeTruthy());
    await waitFor(() => expect(targetInput().value).toBe(''));
    expect(useDecoyButton()).toBeNull();
  });

  it('drops the scan of the old target when the decoy is taken', async () => {
    const scan: RealityScanResult = {
      alpn: 'h2',
      certChainBytes: 3500,
      certChainValid: true,
      certIssuer: 'CA',
      certSubject: 'www.example.org',
      certValid: true,
      curveID: 'X25519',
      feasible: true,
      h2: true,
      host: 'www.example.org',
      ip: '93.184.216.34',
      latencyMs: 90,
      notAfter: '2027-01-01T00:00:00Z',
      port: 443,
      privateTarget: false,
      reason: '',
      serverNames: ['www.example.org'],
      target: 'www.example.org:443',
      tls13: true,
      tlsVersion: '1.3',
      x25519: true,
    };
    renderAddModal([edge]);
    chooseNode('edge');
    chooseReality();
    await waitFor(() => expect(targetInput().value).toBe('127.0.0.1:7443'));

    fireEvent.change(targetInput(), { target: { value: 'www.example.org:443' } });
    vi.mocked(HttpUtil.post).mockResolvedValueOnce({ success: true, obj: scan } as never);
    fireEvent.click(screen.getByRole('button', { name: 'Scan' }));
    await screen.findByText('SNI used');
    expect(screen.queryByText('site.example.com')).toBeNull();

    fireEvent.click(useDecoyButton()!);
    await waitFor(() => expect(screen.queryByText('SNI used')).toBeNull());
    expect(screen.getByText('site.example.com')).toBeTruthy();
  });
});
