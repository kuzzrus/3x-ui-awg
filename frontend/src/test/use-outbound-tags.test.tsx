import type { ReactNode } from 'react';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { useOutboundTagGroups, useOutboundTags } from '@/api/queries/useOutboundTags';
import { makeTestQueryClient } from '@/test/test-utils';
import { HttpUtil, Msg } from '@/utils';

afterEach(() => {
  vi.restoreAllMocks();
});

// The core lowercases a protocol id before it resolves the handler, so a
// template that spells the block outbound "Blackhole" still drops traffic.
function mockConfig() {
  const payload = {
    xraySetting: {
      outbounds: [
        { tag: 'direct', protocol: 'freedom' },
        { tag: 'blocked', protocol: 'Blackhole' },
        { tag: 'warp', protocol: 'wireguard' },
      ],
    },
  };
  vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', JSON.stringify(payload)));
}

function wrapperFor() {
  const queryClient = makeTestQueryClient();
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

describe('outbound tag pickers', () => {
  it('excludes a block outbound whose id is spelled differently', async () => {
    mockConfig();

    const { result } = renderHook(() => useOutboundTags({ excludeBlackhole: true }), {
      wrapper: wrapperFor(),
    });

    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data).toEqual(['direct', 'warp']);
  });

  it('keeps the same tag in the grouped picker out of its outbound list', async () => {
    mockConfig();

    const { result } = renderHook(() => useOutboundTagGroups({ excludeBlackhole: true }), {
      wrapper: wrapperFor(),
    });

    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data?.outbounds).toEqual(['direct', 'warp']);
  });

  it('leaves out an outbound that only some agent nodes have, which the panel itself lacks', async () => {
    const payload = {
      xraySetting: {
        outbounds: [
          { tag: 'direct', protocol: 'freedom' },
          { tag: 'eu-hop', protocol: 'freedom', nodeTags: ['eu'] },
          { tag: 'any-hop', protocol: 'freedom', nodeTags: [] },
        ],
      },
    };
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', JSON.stringify(payload)));

    const flat = renderHook(() => useOutboundTags(), { wrapper: wrapperFor() });
    const grouped = renderHook(() => useOutboundTagGroups(), { wrapper: wrapperFor() });

    await waitFor(() => expect(flat.result.current.data).toBeDefined());
    await waitFor(() => expect(grouped.result.current.data).toBeDefined());
    expect(flat.result.current.data).toEqual(['direct', 'any-hop']);
    expect(grouped.result.current.data?.outbounds).toEqual(['direct', 'any-hop']);
  });

  it('offers every tag when the caller does not exclude blocks', async () => {
    mockConfig();

    const { result } = renderHook(() => useOutboundTags(), { wrapper: wrapperFor() });

    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data).toEqual(['direct', 'blocked', 'warp']);
  });
});
