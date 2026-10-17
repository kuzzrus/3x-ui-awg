import { afterEach, describe, expect, it, vi } from 'vitest';

import { fetchXrayConfig } from '@/hooks/useXraySetting';
import { HttpUtil, Msg } from '@/utils';

// The template is loaded through a schema and what that returns is what the page edits and saves back,
// so a key the schema does not name is gone from the next save.
describe('the Xray template keeps the keys it was loaded with', () => {
  afterEach(() => vi.restoreAllMocks());

  async function load(xraySetting: unknown) {
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(
      new Msg(true, '', JSON.stringify({ xraySetting })),
    );
    const payload = await fetchXrayConfig();
    return payload.xraySetting;
  }

  it('keeps the nodeTags of a routing rule', async () => {
    const template = await load({
      routing: {
        rules: [
          { type: 'field', outboundTag: 'tor', nodeTags: ['eu', 'exit'] },
          { type: 'field', outboundTag: 'direct' },
        ],
      },
    });
    const rules = template.routing?.rules as Array<{ nodeTags?: string[] }>;
    expect(rules[0].nodeTags).toEqual(['eu', 'exit']);
    expect(rules[1].nodeTags).toBeUndefined();
  });

  it('keeps the nodeTags of an outbound', async () => {
    const template = await load({
      outbounds: [{ tag: 'tor', protocol: 'socks', nodeTags: ['eu'] }],
    });
    const outbound = template.outbounds?.[0] as { nodeTags?: string[] } | undefined;
    expect(outbound?.nodeTags).toEqual(['eu']);
  });

  it('keeps a key of a rule or a balancer that the schema does not know', async () => {
    const template = await load({
      routing: {
        rules: [{ type: 'field', outboundTag: 'direct', futureRuleKey: { a: 1 } }],
        balancers: [{ tag: 'pool', selector: ['a-'], futureBalancerKey: [1, 2] }],
      },
    });
    const rule = template.routing?.rules?.[0] as Record<string, unknown>;
    const balancer = template.routing?.balancers?.[0] as Record<string, unknown>;
    expect(rule.futureRuleKey).toEqual({ a: 1 });
    expect(balancer.futureBalancerKey).toEqual([1, 2]);
  });
});
