import type { ReactNode } from 'react';
import { Form } from 'antd';
import { waitFor } from '@testing-library/react';
import { FormProvider, useForm } from 'react-hook-form';
import { afterEach, describe, expect, it, vi } from 'vitest';

import NaiveProxyFields from '@/pages/inbounds/form/protocols/naiveproxy';
import { HttpUtil, Msg } from '@/utils';
import { listSelectOptions, renderWithProviders } from './test-utils';

afterEach(() => {
  vi.restoreAllMocks();
});

// The picker exists so proxied traffic can be sent through an Xray outbound;
// offering the block outbound there looks like a working selection and drops it.
function mockPanel() {
  const config = {
    xraySetting: {
      outbounds: [
        { tag: 'direct', protocol: 'freedom' },
        { tag: 'blocked', protocol: 'blackhole' },
        { tag: 'warp', protocol: 'wireguard' },
      ],
    },
  };
  vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string) => {
    if (url.endsWith('/naiveproxy/status')) {
      return new Msg(true, '', { installed: true, supported: true, platform: 'linux/amd64' });
    }
    return new Msg(true, '', JSON.stringify(config));
  });
}

function Harness({ routed, children }: { routed: boolean; children: ReactNode }) {
  const methods = useForm({ defaultValues: { settings: { routeThroughXray: routed } } });
  return (
    <FormProvider {...methods}>
      <Form>{children}</Form>
    </FormProvider>
  );
}

const EGRESS_FIELD = 'naiveProxyOutboundTag';

describe('naiveproxy egress picker', () => {
  it('offers the routable tags and not the block outbound', async () => {
    mockPanel();
    renderWithProviders(
      <Harness routed>
        <NaiveProxyFields />
      </Harness>,
    );

    await waitFor(() => expect(listSelectOptions(EGRESS_FIELD)).toContain('direct'));

    const options = listSelectOptions(EGRESS_FIELD);
    expect(options).toContain('warp');
    expect(options).not.toContain('blocked');
  });

  it('shows no outbound picker while routing through Xray is off', async () => {
    mockPanel();
    renderWithProviders(
      <Harness routed={false}>
        <NaiveProxyFields />
      </Harness>,
    );

    await waitFor(() => expect(document.querySelector('.ant-switch')).not.toBeNull());
    expect(document.getElementById(EGRESS_FIELD)).toBeNull();
  });
});
