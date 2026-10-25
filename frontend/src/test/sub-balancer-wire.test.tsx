import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';

import { setupHttp } from '@/api/http-init';
import { useSubBalancerMutations } from '@/api/queries/useSubBalancerMutations';
import { HttpUtil } from '@/utils';
import { makeTestQueryClient } from './test-utils';

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeTestQueryClient()}>{children}</QueryClientProvider>;
}

// The Go side reads the lists from repeated form keys, and an empty list has to be no key at all.
describe('subscription balancer request body', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // The component tests stub the post of HttpUtil before each test; this one follows it down to the body on the wire.
    vi.mocked(HttpUtil.post).mockRestore();
    document.head.innerHTML = '<meta name="csrf-token" content="tok">';
    setupHttp();
    fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ success: true, msg: '', obj: null }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.head.innerHTML = '';
  });

  function sentForm(): URLSearchParams {
    expect(fetchMock).toHaveBeenCalledTimes(1);
    return new URLSearchParams((fetchMock.mock.calls[0][1] as RequestInit).body as string);
  }

  it('sends the nodes of a balancer as repeated nodeIds keys, and no inboundIds', async () => {
    const { result } = renderHook(() => useSubBalancerMutations(), { wrapper });
    await act(async () => {
      await result.current.create({
        remark: 'by node',
        strategy: 'random',
        inboundIds: [],
        nodeIds: [10, 11],
        sortOrder: 1,
        enabled: true,
      });
    });
    const form = sentForm();
    expect(form.getAll('nodeIds')).toEqual(['10', '11']);
    expect(form.has('inboundIds')).toBe(false);
    expect(form.get('remark')).toBe('by node');
  });

  it('sends the inbounds and the nodes of an update together', async () => {
    const { result } = renderHook(() => useSubBalancerMutations(), { wrapper });
    await act(async () => {
      await result.current.update(7, {
        remark: 'both',
        strategy: 'leastLoad',
        inboundIds: [1, 2],
        nodeIds: [10],
        memberWeights: { '1': 2.5 },
        sortOrder: 2,
        enabled: false,
      });
    });
    expect(fetchMock.mock.calls[0][0]).toBe('/panel/api/sub-balancers/7');
    const form = sentForm();
    expect(form.getAll('inboundIds')).toEqual(['1', '2']);
    expect(form.getAll('nodeIds')).toEqual(['10']);
    expect(form.get('memberWeights')).toBe('{"1":2.5}');
    expect(form.get('enabled')).toBe('false');
  });
});
