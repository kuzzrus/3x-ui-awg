import { describe, expect, it } from 'vitest';

import {
  cleanupOrphanedBalancerLoopbacks,
  ensureMissingBalancerLoopbacks,
} from '@/pages/xray/balancers/balancer-loopback';
import {
  applyBalancerDeletion,
  applyDeletionByTags,
  applyOutboundDeletion,
  planBalancerDeletion,
  planDeletionByTags,
  planOutboundDeletion,
} from '@/pages/xray/reference-cleanup';
import type { XraySettingsValue } from '@/hooks/useXraySetting';

function tpl(parts: Record<string, unknown>): XraySettingsValue {
  return parts as unknown as XraySettingsValue;
}

const loopbackOutbound = (target: string) => ({
  tag: `_bl_${target}`,
  protocol: 'loopback',
  settings: { inboundTag: `_bl_${target}` },
});

const loopbackRule = (target: string) => ({
  type: 'field',
  inboundTag: [`_bl_${target}`],
  balancerTag: target,
});

// Rules whose balancerTag names no balancer: Xray refuses such a config
// ("balancer <tag> not found") and the core does not start.
function danglingBalancerTags(tt: XraySettingsValue): string[] {
  const known = new Set((tt.routing?.balancers ?? []).map((b) => b?.tag));
  return (tt.routing?.rules ?? [])
    .map((r) => r?.balancerTag)
    .filter((tag): tag is string => typeof tag === 'string' && tag !== '' && !known.has(tag));
}

function dialerProxyOf(tt: XraySettingsValue, tag: string): string | undefined {
  const o = tt.outbounds?.find((x) => x?.tag === tag);
  return (o as { streamSettings?: { sockopt?: { dialerProxy?: string } } } | undefined)
    ?.streamSettings?.sockopt?.dialerProxy;
}

describe('outbound deletion', () => {
  it('drops a rule whose only destination was the deleted outbound', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [{ type: 'field', inboundTag: ['in-443'], outboundTag: 'proxy-us' }],
        balancers: [],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.rules).toEqual([]);
    expect(tt.outbounds).toEqual([]);
  });

  it('keeps a rule that still has a balancer, dropping only the dead outboundTag', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [{ type: 'field', outboundTag: 'proxy-us', balancerTag: 'eu-pool' }],
        balancers: [{ tag: 'eu-pool', selector: ['direct'] }],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.rules).toHaveLength(1);
    expect(tt.routing!.rules![0].outboundTag).toBeUndefined();
    expect(tt.routing!.rules![0].balancerTag).toBe('eu-pool');
  });

  it('reduces a multi-target selector and leaves the balancer and its rules intact', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }, { tag: 'proxy-uk' }],
      routing: {
        rules: [{ type: 'field', inboundTag: ['in'], balancerTag: 'pool' }],
        balancers: [{ tag: 'pool', selector: ['proxy-us', 'proxy-uk'] }],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.balancers![0].selector).toEqual(['proxy-uk']);
    expect(tt.routing!.rules).toHaveLength(1);
    expect(tt.routing!.rules![0].balancerTag).toBe('pool');
    expect((tt.outbounds || []).map((o) => o?.tag)).toEqual(['proxy-uk']);
  });

  it('keeps a balancer that has nodes when the last outbound of its selector goes', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [{ type: 'field', inboundTag: ['in'], balancerTag: 'pool' }],
        balancers: [
          {
            tag: 'pool',
            selector: ['proxy-us'],
            nodeMembers: { client: 'relay@example.com', nodeIds: [10] },
          },
        ],
      },
    });
    expect(planOutboundDeletion(tt, 0).balancers).toEqual([]);
    applyOutboundDeletion(tt, 0);
    // The nodes go on selecting for it, and the placeholder keeps the stored selector from being empty.
    expect(tt.routing!.balancers![0].selector).toEqual(['xui-node-']);
    expect(tt.routing!.rules).toHaveLength(1);
    expect(tt.routing!.rules![0].balancerTag).toBe('pool');
  });

  it('leaves the placeholder of a balancer of nodes alone when an outbound goes', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [{ type: 'field', inboundTag: ['in'], balancerTag: 'exits' }],
        balancers: [
          {
            tag: 'exits',
            selector: ['xui-node-'],
            nodeMembers: { client: 'relay@example.com', nodeIds: [10] },
          },
        ],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.balancers![0].selector).toEqual(['xui-node-']);
    expect(tt.routing!.rules).toHaveLength(1);
  });

  it('cascade-removes a balancer whose selector is emptied, repairing its rules', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [
          { type: 'field', inboundTag: ['in'], balancerTag: 'pool' },
          { type: 'field', outboundTag: 'direct', balancerTag: 'pool' },
        ],
        balancers: [{ tag: 'pool', selector: ['proxy-us'] }],
      },
    });
    const impact = planOutboundDeletion(tt, 0);
    expect(impact.balancers).toEqual([{ tag: 'pool', reason: 'selectorEmptied' }]);
    expect(impact.rules).toEqual([
      { index: 0, label: '#1', fate: 'removed' },
      { index: 1, label: '#2', fate: 'modified', keeps: 'direct' },
    ]);

    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.balancers).toEqual([]);
    expect(tt.routing!.rules).toHaveLength(1);
    expect(tt.routing!.rules![0].outboundTag).toBe('direct');
    expect(tt.routing!.rules![0].balancerTag).toBeUndefined();
  });

  it('cascade-removes the burst observer when deleting an outbound removes the last leastLoad balancer', () => {
    const tt = tpl({
      outbounds: [{ tag: 'll-out' }],
      routing: {
        rules: [],
        balancers: [{ tag: 'll', selector: ['ll-out'], strategy: { type: 'leastLoad' } }],
      },
      burstObservatory: { subjectSelector: ['ll-out'] },
    });
    const impact = planOutboundDeletion(tt, 0);
    expect(impact.balancers).toEqual([{ tag: 'll', reason: 'selectorEmptied' }]);
    expect(impact.burst).toBe(true);
    applyOutboundDeletion(tt, 0);
    expect(tt.burstObservatory).toBeUndefined();
    expect(tt.routing!.balancers).toEqual([]);
  });

  it('cascade-switches from burst to regular observer when only leastPing remains', () => {
    const tt = tpl({
      outbounds: [{ tag: 'lp-out' }, { tag: 'll-out' }],
      routing: {
        rules: [],
        balancers: [
          { tag: 'lp', selector: ['lp-out'], strategy: { type: 'leastPing' } },
          { tag: 'll', selector: ['ll-out'], strategy: { type: 'leastLoad' } },
        ],
      },
      burstObservatory: { subjectSelector: ['lp-out', 'll-out'] },
    });
    const impact = planOutboundDeletion(tt, 1);
    expect(impact.balancers).toEqual([{ tag: 'll', reason: 'selectorEmptied' }]);
    expect(impact.burst).toBe(true);
    applyOutboundDeletion(tt, 1);
    expect(tt.burstObservatory).toBeUndefined();
    expect((tt.observatory as { subjectSelector: string[] }).subjectSelector).toEqual(['lp-out']);
    expect(tt.routing!.balancers).toEqual([
      { tag: 'lp', selector: ['lp-out'], strategy: { type: 'leastPing' } },
    ]);
  });

  it('cascade-keeps burst observer when leastPing is removed but leastLoad remains', () => {
    const tt = tpl({
      outbounds: [{ tag: 'lp-out' }, { tag: 'll-out' }],
      routing: {
        rules: [],
        balancers: [
          { tag: 'lp', selector: ['lp-out'], strategy: { type: 'leastPing' } },
          { tag: 'll', selector: ['ll-out'], strategy: { type: 'leastLoad' } },
        ],
      },
      burstObservatory: { subjectSelector: ['lp-out', 'll-out'] },
    });
    const impact = planOutboundDeletion(tt, 0);
    expect(impact.balancers).toEqual([{ tag: 'lp', reason: 'selectorEmptied' }]);
    expect(impact.burst).toBe(false);
    applyOutboundDeletion(tt, 0);
    expect(tt.observatory).toBeUndefined();
    expect((tt.burstObservatory as { subjectSelector: string[] }).subjectSelector).toEqual([
      'll-out',
    ]);
    expect(tt.routing!.balancers).toEqual([
      { tag: 'll', selector: ['ll-out'], strategy: { type: 'leastLoad' } },
    ]);
  });

  it('clears a fallbackTag and a dialerProxy pointing at the deleted outbound', () => {
    const tt = tpl({
      outbounds: [
        { tag: 'proxy-us' },
        { tag: 'chain', streamSettings: { sockopt: { dialerProxy: 'proxy-us' } } },
      ],
      routing: {
        rules: [],
        balancers: [{ tag: 'pool', selector: ['proxy-us', 'proxy-uk'], fallbackTag: 'proxy-us' }],
      },
    });
    expect(planOutboundDeletion(tt, 0).balancers).toEqual([
      { tag: 'pool', reason: 'fallbackCleared' },
    ]);
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.balancers![0].selector).toEqual(['proxy-uk']);
    expect(tt.routing!.balancers![0].fallbackTag).toBe('');
    expect(dialerProxyOf(tt, 'chain')).toBeUndefined();
  });

  it('names a balancer without a tag by its position when it loses its fallback', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [],
        balancers: [
          { tag: 'pool', selector: ['keep-me'] },
          { tag: '', selector: ['keep-me'], fallbackTag: 'proxy-us' },
        ],
      },
    });
    expect(planOutboundDeletion(tt, 0).balancers).toEqual([
      { tag: '#2', reason: 'fallbackCleared' },
    ]);
  });

  it('never cascade-removes a tagless balancer (an empty tag must not match others)', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [],
        balancers: [
          { tag: '', selector: ['proxy-us'] },
          { tag: '', selector: ['keep-me'] },
        ],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.balancers).toHaveLength(2);
  });

  it('does not throw on null entries in rules/balancers/outbounds (unvalidated config)', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }, null],
      routing: {
        rules: [null, { type: 'field', inboundTag: ['in'], outboundTag: 'proxy-us' }],
        balancers: [null, { tag: 'pool', selector: ['keep'] }],
      },
    });
    expect(() => planOutboundDeletion(tt, 0)).not.toThrow();
    expect(() => applyOutboundDeletion(tt, 0)).not.toThrow();
    expect(tt.routing!.balancers).toEqual([{ tag: 'pool', selector: ['keep'] }]);
  });

  it('drops a rule that loses BOTH destinations in one cascade', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [{ type: 'field', outboundTag: 'proxy-us', balancerTag: 'pool' }],
        balancers: [{ tag: 'pool', selector: ['proxy-us'] }],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.rules).toEqual([]);
    expect(tt.routing!.balancers).toEqual([]);
  });

  it('cleans a disabled rule too', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [{ type: 'field', enabled: false, inboundTag: ['in'], outboundTag: 'proxy-us' }],
        balancers: [],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.rules).toEqual([]);
  });

  it('leaves unrelated rules and outbounds untouched', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }, { tag: 'direct' }],
      routing: {
        rules: [
          { type: 'field', inboundTag: ['in'], outboundTag: 'proxy-us' },
          { type: 'field', inboundTag: ['in2'], outboundTag: 'direct' },
        ],
        balancers: [],
      },
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.routing!.rules).toHaveLength(1);
    expect(tt.routing!.rules![0].outboundTag).toBe('direct');
    expect((tt.outbounds || []).map((o) => o?.tag)).toEqual(['direct']);
  });

  it('removes a referenced outbound with no rules and reports an empty impact', () => {
    const tt = tpl({ outbounds: [{ tag: 'lonely' }], routing: { rules: [], balancers: [] } });
    expect(planOutboundDeletion(tt, 0)).toEqual({
      rules: [],
      balancers: [],
      observatory: false,
      burst: false,
    });
    applyOutboundDeletion(tt, 0);
    expect(tt.outbounds).toEqual([]);
  });

  it('uses ruleTag as the impact label when present', () => {
    const tt = tpl({
      outbounds: [{ tag: 'x' }],
      routing: {
        rules: [{ type: 'field', ruleTag: 'block-ads', outboundTag: 'x' }],
        balancers: [],
      },
    });
    expect(planOutboundDeletion(tt, 0).rules[0].label).toBe('block-ads');
  });

  it('does not mutate the template when only planning', () => {
    const tt = tpl({
      outbounds: [{ tag: 'proxy-us' }],
      routing: {
        rules: [{ type: 'field', outboundTag: 'proxy-us', balancerTag: 'pool' }],
        balancers: [{ tag: 'pool', selector: ['proxy-us'] }],
      },
      burstObservatory: { subjectSelector: ['proxy-us'] },
    });
    const before = JSON.stringify(tt);
    planOutboundDeletion(tt, 0);
    expect(JSON.stringify(tt)).toBe(before);
  });

  it('predicts the surviving rule count exactly (plan/apply parity)', () => {
    const make = () =>
      tpl({
        outbounds: [{ tag: 'proxy-us' }],
        routing: {
          rules: [
            { type: 'field', inboundTag: ['a'], outboundTag: 'proxy-us' },
            { type: 'field', outboundTag: 'proxy-us', balancerTag: 'pool' },
            { type: 'field', inboundTag: ['b'], outboundTag: 'direct' },
            { type: 'field', inboundTag: ['c'], balancerTag: 'pool' },
          ],
          balancers: [{ tag: 'pool', selector: ['proxy-us'] }],
        },
      });
    const planned = make();
    const applied = make();
    const total = planned.routing!.rules!.length;
    const removed = planOutboundDeletion(planned, 0).rules.filter(
      (r) => r.fate === 'removed',
    ).length;
    applyOutboundDeletion(applied, 0);
    expect(applied.routing!.rules!.length).toBe(total - removed);
  });
});

describe('balancer deletion', () => {
  it('drops a rule whose only destination was the deleted balancer', () => {
    const tt = tpl({
      routing: {
        rules: [{ type: 'field', inboundTag: ['in'], balancerTag: 'pool' }],
        balancers: [{ tag: 'pool', selector: ['a'] }],
      },
    });
    applyBalancerDeletion(tt, 0);
    expect(tt.routing!.balancers).toEqual([]);
    expect(tt.routing!.rules).toEqual([]);
  });

  it('keeps a rule that still has an outbound, dropping only the dead balancerTag', () => {
    const tt = tpl({
      routing: {
        rules: [{ type: 'field', outboundTag: 'direct', balancerTag: 'pool' }],
        balancers: [{ tag: 'pool', selector: ['a'] }],
      },
    });
    applyBalancerDeletion(tt, 0);
    expect(tt.routing!.rules).toHaveLength(1);
    expect(tt.routing!.rules![0].balancerTag).toBeUndefined();
    expect(tt.routing!.rules![0].outboundTag).toBe('direct');
  });

  it('reports and removes the observer when deleting the last leastPing balancer', () => {
    const tt = tpl({
      routing: {
        rules: [],
        balancers: [{ tag: 'b1', selector: ['a'], strategy: { type: 'leastPing' } }],
      },
      observatory: { subjectSelector: ['a'] },
    });
    expect(planBalancerDeletion(tt, 0).observatory).toBe(true);
    applyBalancerDeletion(tt, 0);
    expect(tt.observatory).toBeUndefined();
    expect(tt.routing!.balancers).toEqual([]);
  });

  it('reports and removes the burst observer when deleting the last leastLoad balancer', () => {
    const tt = tpl({
      routing: {
        rules: [],
        balancers: [{ tag: 'll', selector: ['a'], strategy: { type: 'leastLoad' } }],
      },
      burstObservatory: { subjectSelector: ['a'] },
    });
    expect(planBalancerDeletion(tt, 0).burst).toBe(true);
    applyBalancerDeletion(tt, 0);
    expect(tt.burstObservatory).toBeUndefined();
    expect(tt.routing!.balancers).toEqual([]);
  });

  it('reports and removes the burst observer when deleting the last fallback balancer', () => {
    const tt = tpl({
      routing: { rules: [], balancers: [{ tag: 'rf', selector: ['a'], fallbackTag: 'direct' }] },
      burstObservatory: { subjectSelector: ['a'] },
    });
    expect(planBalancerDeletion(tt, 0).burst).toBe(true);
    applyBalancerDeletion(tt, 0);
    expect(tt.burstObservatory).toBeUndefined();
    expect(tt.routing!.balancers).toEqual([]);
  });

  it('switches from burst to regular observer when the deleted balancer was the last burst-required one', () => {
    const tt = tpl({
      routing: {
        rules: [],
        balancers: [
          { tag: 'lp', selector: ['lp-out'], strategy: { type: 'leastPing' } },
          { tag: 'll', selector: ['ll-out'], strategy: { type: 'leastLoad' } },
        ],
      },
      burstObservatory: { subjectSelector: ['lp-out', 'll-out'] },
    });
    const impact = planBalancerDeletion(tt, 1);
    expect(impact.burst).toBe(true);
    expect(impact.observatory).toBe(false);
    applyBalancerDeletion(tt, 1);
    expect(tt.burstObservatory).toBeUndefined();
    expect((tt.observatory as { subjectSelector: string[] }).subjectSelector).toEqual(['lp-out']);
    expect(tt.routing!.balancers).toEqual([
      { tag: 'lp', selector: ['lp-out'], strategy: { type: 'leastPing' } },
    ]);
  });

  it('keeps burst observer when deleting leastPing but a burst-required balancer remains', () => {
    const tt = tpl({
      routing: {
        rules: [],
        balancers: [
          { tag: 'lp', selector: ['lp-out'], strategy: { type: 'leastPing' } },
          { tag: 'll', selector: ['ll-out'], strategy: { type: 'leastLoad' } },
        ],
      },
      burstObservatory: { subjectSelector: ['lp-out', 'll-out'] },
    });
    const impact = planBalancerDeletion(tt, 0);
    expect(impact.burst).toBe(false);
    expect(impact.observatory).toBe(false);
    applyBalancerDeletion(tt, 0);
    expect(tt.observatory).toBeUndefined();
    expect((tt.burstObservatory as { subjectSelector: string[] }).subjectSelector).toEqual([
      'll-out',
    ]);
    expect(tt.routing!.balancers).toEqual([
      { tag: 'll', selector: ['ll-out'], strategy: { type: 'leastLoad' } },
    ]);
  });

  it('does not report rules when the deleted balancer is unreferenced', () => {
    const tt = tpl({
      routing: {
        rules: [{ type: 'field', inboundTag: ['in'], outboundTag: 'direct' }],
        balancers: [{ tag: 'pool', selector: ['a'] }],
      },
    });
    expect(planBalancerDeletion(tt, 0).rules).toEqual([]);
    applyBalancerDeletion(tt, 0);
    expect(tt.routing!.rules).toHaveLength(1);
  });

  it('removes its loopback and clears the fallback of a balancer that still used it', () => {
    const tt = tpl({
      outbounds: [loopbackOutbound('b1')],
      routing: {
        rules: [loopbackRule('b1')],
        balancers: [
          { tag: 'b1', selector: ['a'] },
          { tag: 'b2', selector: ['c'], fallbackTag: '_bl_b1' },
        ],
      },
    });
    expect(planBalancerDeletion(tt, 0).balancers).toEqual([
      { tag: 'b2', reason: 'fallbackCleared' },
    ]);
    applyBalancerDeletion(tt, 0);
    expect(tt.outbounds).toEqual([]);
    expect(tt.routing!.rules).toEqual([]);
    expect(tt.routing!.balancers).toEqual([{ tag: 'b2', selector: ['c'], fallbackTag: '' }]);
  });
});

describe('an outbound deletion that cascade-removes a balancer another one falls back to', () => {
  const fallbackTemplate = (extra: Record<string, unknown> = {}) =>
    tpl({
      outbounds: [{ tag: 'a' }, { tag: 'c' }, loopbackOutbound('b1')],
      routing: {
        rules: [loopbackRule('b1')],
        balancers: [
          { tag: 'b1', selector: ['a'] },
          { tag: 'b2', selector: ['c'], fallbackTag: '_bl_b1' },
        ],
      },
      ...extra,
    });

  it('removes the loopback of the removed balancer and clears the fallback that used it', () => {
    const tt = fallbackTemplate();
    applyOutboundDeletion(tt, 0);
    expect(tt.outbounds).toEqual([{ tag: 'c' }]);
    expect(tt.routing!.rules).toEqual([]);
    expect(tt.routing!.balancers).toEqual([{ tag: 'b2', selector: ['c'], fallbackTag: '' }]);
  });

  it('leaves no rule for the save-time loopback repair to point at the removed balancer', () => {
    const tt = fallbackTemplate();
    applyOutboundDeletion(tt, 0);
    // The two repairs XrayPage.onSaveAll runs on the template before it is saved.
    ensureMissingBalancerLoopbacks(tt);
    cleanupOrphanedBalancerLoopbacks(tt);
    expect(danglingBalancerTags(tt)).toEqual([]);
  });

  it('lists the balancer whose fallback is cleared after the balancer that goes', () => {
    expect(planOutboundDeletion(fallbackTemplate(), 0).balancers).toEqual([
      { tag: 'b1', reason: 'selectorEmptied' },
      { tag: 'b2', reason: 'fallbackCleared' },
    ]);
  });

  it('keeps the loopback and the fallback of a balancer that stays', () => {
    const tt = tpl({
      outbounds: [{ tag: 'a' }, { tag: 'c' }, loopbackOutbound('b1'), loopbackOutbound('b3')],
      routing: {
        rules: [loopbackRule('b1'), loopbackRule('b3')],
        balancers: [
          { tag: 'b1', selector: ['a'] },
          { tag: 'b3', selector: ['c'] },
          { tag: 'b4', selector: ['c'], fallbackTag: '_bl_b3' },
        ],
      },
    });
    expect(planOutboundDeletion(tt, 0).balancers).toEqual([
      { tag: 'b1', reason: 'selectorEmptied' },
    ]);
    applyOutboundDeletion(tt, 0);
    expect(tt.outbounds).toEqual([{ tag: 'c' }, loopbackOutbound('b3')]);
    expect(tt.routing!.rules).toEqual([loopbackRule('b3')]);
    expect(tt.routing!.balancers![1].fallbackTag).toBe('_bl_b3');
  });

  it('has no fallback to clear when the balancer that used it is removed as well', () => {
    const tt = tpl({
      outbounds: [{ tag: 'a' }, loopbackOutbound('b1')],
      routing: {
        rules: [loopbackRule('b1')],
        balancers: [
          { tag: 'b1', selector: ['a'] },
          { tag: 'b2', selector: ['a'], fallbackTag: '_bl_b1' },
        ],
      },
    });
    expect(planOutboundDeletion(tt, 0).balancers).toEqual([
      { tag: 'b1', reason: 'selectorEmptied' },
      { tag: 'b2', reason: 'selectorEmptied' },
    ]);
    applyOutboundDeletion(tt, 0);
    expect(tt.outbounds).toEqual([]);
    expect(tt.routing!.balancers).toEqual([]);
  });

  it('drops the burst observer that only the cleared fallback required', () => {
    const tt = fallbackTemplate({ burstObservatory: { subjectSelector: ['c'] } });
    expect(planOutboundDeletion(tt, 0).burst).toBe(true);
    applyOutboundDeletion(tt, 0);
    expect(tt.burstObservatory).toBeUndefined();
  });
});

describe('deletion by tags', () => {
  const psiphon = () =>
    tpl({
      outbounds: [
        { tag: 'psiphon' },
        { tag: 'psiphon-DE' },
        { tag: 'psiphon-US' },
        { tag: 'direct' },
      ],
      routing: {
        rules: [
          { type: 'field', domain: ['a.example'], outboundTag: 'psiphon-DE' },
          { type: 'field', domain: ['b.example'], balancerTag: 'psiphon-balancer' },
          { type: 'field', domain: ['c.example'], outboundTag: 'psiphon', balancerTag: 'main' },
          { type: 'field', domain: ['d.example'], outboundTag: 'direct' },
        ],
        balancers: [
          { tag: 'psiphon-balancer', selector: ['psiphon-'], strategy: { type: 'leastPing' } },
          { tag: 'main', selector: ['direct'] },
        ],
      },
      observatory: { subjectSelector: ['psiphon-'] },
    });

  it('removes several outbounds and a balancer in one cascade and repairs the rules of all of them', () => {
    const tt = psiphon();
    applyDeletionByTags(tt, ['psiphon', 'psiphon-DE', 'psiphon-US'], ['psiphon-balancer']);

    expect((tt.outbounds ?? []).map((o) => o?.tag)).toEqual(['direct']);
    expect(tt.routing!.balancers!.map((b) => b.tag)).toEqual(['main']);
    // The first two lose their only destination, the third keeps its balancer, the last is not theirs.
    expect(tt.routing!.rules!.map((r) => r.domain)).toEqual([['c.example'], ['d.example']]);
    expect(tt.routing!.rules![0].outboundTag).toBeUndefined();
    expect(tt.observatory).toBeUndefined();
  });

  it('plans what it applies, and changes nothing while it plans', () => {
    const tt = psiphon();
    const before = JSON.stringify(tt);
    const impact = planDeletionByTags(
      tt,
      ['psiphon', 'psiphon-DE', 'psiphon-US'],
      ['psiphon-balancer'],
    );

    expect(impact.rules.map((r) => [r.index, r.fate])).toEqual([
      [0, 'removed'],
      [1, 'removed'],
      [2, 'modified'],
    ]);
    expect(impact.rules[2].keeps).toBe('main');
    expect(impact.balancers).toEqual([]);
    expect(impact.observatory).toBe(true);
    expect(JSON.stringify(tt)).toBe(before);
  });

  it('reports a balancer the removed outbounds empty, but not one the caller named', () => {
    const make = () =>
      tpl({
        outbounds: [{ tag: 'a' }, { tag: 'b' }],
        routing: {
          rules: [],
          balancers: [
            { tag: 'pool', selector: ['a', 'b'] },
            { tag: 'named', selector: ['a'] },
          ],
        },
      });
    expect(planDeletionByTags(make(), ['a', 'b'], ['named']).balancers).toEqual([
      { tag: 'pool', reason: 'selectorEmptied' },
    ]);
    const tt = make();
    applyDeletionByTags(tt, ['a', 'b'], ['named']);
    expect(tt.routing!.balancers).toEqual([]);
  });

  it('lists the balancer that loses its fallback, and not the loopback rule that goes with the balancer', () => {
    const tt = tpl({
      outbounds: [{ tag: 'direct' }, { tag: '_bl_psiphon-balancer', protocol: 'loopback' }],
      routing: {
        rules: [
          { type: 'field', inboundTag: ['_bl_psiphon-balancer'], balancerTag: 'psiphon-balancer' },
          { type: 'field', domain: ['a.example'], balancerTag: 'psiphon-balancer' },
        ],
        balancers: [
          { tag: 'psiphon-balancer', selector: ['psiphon-'] },
          { tag: 'main', selector: ['direct'], fallbackTag: '_bl_psiphon-balancer' },
        ],
      },
    });
    const impact = planDeletionByTags(tt, [], ['psiphon-balancer']);

    expect(impact.balancers).toEqual([{ tag: 'main', reason: 'fallbackCleared' }]);
    expect(impact.rules.map((r) => r.index)).toEqual([1]);

    applyDeletionByTags(tt, [], ['psiphon-balancer']);
    expect(tt.routing!.balancers).toEqual([{ tag: 'main', selector: ['direct'], fallbackTag: '' }]);
    expect(tt.routing!.rules).toEqual([]);
    expect((tt.outbounds ?? []).map((o) => o?.tag)).toEqual(['direct']);
  });

  it('does the same for one outbound as the deletion by index', () => {
    const byIndex = psiphon();
    const byTag = psiphon();
    expect(planDeletionByTags(byTag, ['psiphon-DE'])).toEqual(planOutboundDeletion(byIndex, 1));
    applyOutboundDeletion(byIndex, 1);
    applyDeletionByTags(byTag, ['psiphon-DE']);
    expect(byTag).toEqual(byIndex);
  });
});
