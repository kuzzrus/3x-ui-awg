import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';

import DeletionImpactList from '@/pages/xray/DeletionImpactList';
import { planOutboundDeletion } from '@/pages/xray/reference-cleanup';
import type { XraySettingsValue } from '@/hooks/useXraySetting';

describe('DeletionImpactList', () => {
  it('says that a balancer loses its fallback when the balancer behind it is removed', () => {
    const tt = {
      outbounds: [
        { tag: 'a' },
        { tag: 'c' },
        { tag: '_bl_b1', protocol: 'loopback', settings: { inboundTag: '_bl_b1' } },
      ],
      routing: {
        rules: [{ type: 'field', inboundTag: ['_bl_b1'], balancerTag: 'b1' }],
        balancers: [
          { tag: 'b1', selector: ['a'] },
          { tag: 'b2', selector: ['c'], fallbackTag: '_bl_b1' },
        ],
      },
    } as unknown as XraySettingsValue;

    render(<DeletionImpactList impact={planOutboundDeletion(tt, 0)} />);

    expect(screen.getByText('Balancer b1 — removed (no targets left)')).toBeTruthy();
    expect(screen.getByText('Balancer b2 — its fallback is cleared')).toBeTruthy();
  });
});
