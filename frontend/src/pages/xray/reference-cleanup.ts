import type { XraySettingsValue } from '@/hooks/useXraySetting';
import type { BalancerObject, RuleObject } from '@/schemas/routing';
import { NODES_SELECTOR, syncObservatories } from './balancers/balancer-helpers';
import {
  balancerTagFromLoopback,
  isBalancerLoopbackTag,
  removeBalancerLoopback,
} from './balancers/balancer-loopback';

/**
 * Reference cleanup for the Xray-config blob: when an outbound or balancer is
 * deleted, routing rules and balancers that point at it must be repaired in the
 * same edit, or the saved config breaks the core (a dangling balancerTag stops
 * Router.Init; a dangling outboundTag black-holes matched traffic).
 *
 * Keep/drop a rule by its destination: after the deletion, a rule that still has
 * an outboundTag or balancerTag is kept (the dead reference is dropped); a rule
 * left with neither is removed, since a destination-less rule black-holes the
 * traffic it matches. Deleting an outbound cascades: if it empties a balancer's
 * selector, that balancer is removed too, and its rules are repaired the same way.
 */

export type RuleFate = 'removed' | 'modified';

export interface RuleImpact {
  index: number;
  label: string;
  fate: RuleFate;
  keeps?: string;
}

export interface BalancerImpact {
  tag: string;
  reason: 'selectorEmptied' | 'fallbackCleared';
}

export interface DeletionImpact {
  rules: RuleImpact[];
  balancers: BalancerImpact[];
  observatory: boolean;
  burst: boolean;
}

const emptyImpact = (): DeletionImpact => ({
  rules: [],
  balancers: [],
  observatory: false,
  burst: false,
});

function ruleList(tt: XraySettingsValue): RuleObject[] {
  const r = tt.routing?.rules;
  return Array.isArray(r) ? r : [];
}

function balancerList(tt: XraySettingsValue): BalancerObject[] {
  const b = tt.routing?.balancers;
  return Array.isArray(b) ? b : [];
}

function outboundTagAt(tt: XraySettingsValue, index: number): string {
  const o = tt.outbounds?.[index];
  return typeof o?.tag === 'string' ? o.tag : '';
}

function balancerTagAt(tt: XraySettingsValue, index: number): string {
  const b = balancerList(tt)[index];
  return typeof b?.tag === 'string' ? b.tag : '';
}

function ruleLabel(rule: RuleObject, index: number): string {
  const tag = typeof rule.ruleTag === 'string' ? rule.ruleTag.trim() : '';
  return tag || `#${index + 1}`;
}

// An entry without a tag is named by its position, as a rule without a ruleTag is.
const nameOf = (tag: unknown, index: number): string =>
  typeof tag === 'string' && tag !== '' ? tag : `#${index + 1}`;

const hasNodes = (b: BalancerObject): boolean => (b.nodeMembers?.nodeIds?.length ?? 0) > 0;

/** Balancers whose selector is left empty once `removedOutbounds` are gone, and that have no nodes to go on with. */
function balancersEmptiedBy(tt: XraySettingsValue, removedOutbounds: Set<string>): string[] {
  if (removedOutbounds.size === 0) return [];
  const emptied: string[] = [];
  for (const b of balancerList(tt)) {
    const tag = typeof b?.tag === 'string' ? b.tag : '';
    if (tag === '' || hasNodes(b)) continue;
    const selector = Array.isArray(b?.selector) ? b.selector : [];
    if (selector.length === 0) continue;
    if (selector.every((s) => removedOutbounds.has(s))) emptied.push(tag);
  }
  return emptied;
}

/**
 * Single source of truth for how a deletion affects one rule, shared by the
 * preview (`ruleImpacts`) and the mutation (`applyCleanup`) so the two can never
 * disagree. Returns null when the rule is untouched; otherwise `keeps` names the
 * surviving destination, or is '' when none remains and the rule must be dropped.
 */
function classifyRule(
  rule: RuleObject,
  removedOutbounds: Set<string>,
  removedBalancers: Set<string>,
): { losesOut: boolean; losesBal: boolean; keeps: string } | null {
  const out = typeof rule?.outboundTag === 'string' ? rule.outboundTag : '';
  const bal = typeof rule?.balancerTag === 'string' ? rule.balancerTag : '';
  const losesOut = out !== '' && removedOutbounds.has(out);
  const losesBal = bal !== '' && removedBalancers.has(bal);
  if (!losesOut && !losesBal) return null;
  const keptOut = out !== '' && !losesOut ? out : '';
  const keptBal = bal !== '' && !losesBal ? bal : '';
  return { losesOut, losesBal, keeps: keptOut || keptBal };
}

function ruleImpacts(
  tt: XraySettingsValue,
  removedOutbounds: Set<string>,
  removedBalancers: Set<string>,
): RuleImpact[] {
  const impacts: RuleImpact[] = [];
  ruleList(tt).forEach((rule, index) => {
    const verdict = classifyRule(rule, removedOutbounds, removedBalancers);
    if (!verdict) return;
    impacts.push(
      verdict.keeps
        ? { index, label: ruleLabel(rule, index), fate: 'modified', keeps: verdict.keeps }
        : { index, label: ruleLabel(rule, index), fate: 'removed' },
    );
  });
  return impacts;
}

// A balancer loses its fallback when it is a removed outbound or the loopback of a removed one.
function losesFallback(
  b: BalancerObject,
  removedOutbounds: Set<string>,
  removedBalancers: Set<string>,
): boolean {
  const fallback = typeof b?.fallbackTag === 'string' ? b.fallbackTag : '';
  if (removedOutbounds.has(fallback)) return true;
  const target = balancerTagFromLoopback(fallback);
  return target !== null && removedBalancers.has(target);
}

/** Balancers that survive but lose their fallback, which `applyCleanup` clears. */
function balancersLosingFallback(
  tt: XraySettingsValue,
  removedOutbounds: Set<string>,
  removedBalancers: Set<string>,
): BalancerImpact[] {
  const losing: BalancerImpact[] = [];
  balancerList(tt).forEach((b, index) => {
    if (!b || removedBalancers.has(b.tag)) return;
    if (losesFallback(b, removedOutbounds, removedBalancers)) {
      losing.push({ tag: nameOf(b.tag, index), reason: 'fallbackCleared' });
    }
  });
  return losing;
}

function applyCleanup(
  tt: XraySettingsValue,
  removedOutbounds: Set<string>,
  removedBalancers: Set<string>,
): void {
  if (tt.routing && Array.isArray(tt.routing.rules)) {
    const next: RuleObject[] = [];
    for (const rule of tt.routing.rules) {
      const verdict = classifyRule(rule, removedOutbounds, removedBalancers);
      if (!verdict) {
        next.push(rule);
        continue;
      }
      if (verdict.losesOut) delete rule.outboundTag;
      if (verdict.losesBal) delete rule.balancerTag;
      if (verdict.keeps) next.push(rule);
    }
    tt.routing.rules = next;
  }

  if (tt.routing && Array.isArray(tt.routing.balancers)) {
    const survivors: BalancerObject[] = [];
    for (const balancer of tt.routing.balancers) {
      if (!balancer) continue;
      if (removedBalancers.has(balancer.tag)) continue;
      if (removedOutbounds.size > 0 && Array.isArray(balancer.selector)) {
        balancer.selector = balancer.selector.filter((s) => !removedOutbounds.has(s));
        if (balancer.selector.length === 0 && hasNodes(balancer)) {
          balancer.selector = [NODES_SELECTOR];
        }
      }
      if (losesFallback(balancer, removedOutbounds, removedBalancers)) {
        balancer.fallbackTag = '';
      }
      survivors.push(balancer);
    }
    tt.routing.balancers = survivors;
  }

  if (removedOutbounds.size > 0 && Array.isArray(tt.outbounds)) {
    tt.outbounds = tt.outbounds.filter(
      (o) => !(typeof o?.tag === 'string' && removedOutbounds.has(o.tag)),
    );
    for (const outbound of tt.outbounds) {
      const sockopt = (outbound as { streamSettings?: { sockopt?: { dialerProxy?: string } } })
        ?.streamSettings?.sockopt;
      if (
        sockopt &&
        typeof sockopt.dialerProxy === 'string' &&
        removedOutbounds.has(sockopt.dialerProxy)
      ) {
        delete sockopt.dialerProxy;
      }
    }
  }

  // The panel hides loopbacks, so the cascade has to take a removed balancer's along with it.
  for (const tag of removedBalancers) removeBalancerLoopback(tt, tag);

  syncObservatories(tt);
}

function observersRemovedBy(
  tt: XraySettingsValue,
  removedOutbounds: Set<string>,
  removedBalancers: Set<string>,
): { observatory: boolean; burst: boolean } {
  const hadObservatory = !!tt.observatory;
  const hadBurst = !!tt.burstObservatory;
  if (!hadObservatory && !hadBurst) return { observatory: false, burst: false };
  const clone = JSON.parse(JSON.stringify(tt)) as XraySettingsValue;
  applyCleanup(clone, removedOutbounds, removedBalancers);
  return {
    observatory: hadObservatory && !clone.observatory,
    burst: hadBurst && !clone.burstObservatory,
  };
}

export function planBalancerDeletion(tt: XraySettingsValue, index: number): DeletionImpact {
  const tag = balancerTagAt(tt, index);
  if (!tag) return emptyImpact();
  const removedOutbounds = new Set<string>();
  const removedBalancers = new Set([tag]);
  const obs = observersRemovedBy(tt, removedOutbounds, removedBalancers);
  return {
    rules: ruleImpacts(tt, removedOutbounds, removedBalancers),
    balancers: balancersLosingFallback(tt, removedOutbounds, removedBalancers),
    observatory: obs.observatory,
    burst: obs.burst,
  };
}

export function applyBalancerDeletion(tt: XraySettingsValue, index: number): void {
  const tag = balancerTagAt(tt, index);
  if (!tag) {
    if (tt.routing && Array.isArray(tt.routing.balancers)) tt.routing.balancers.splice(index, 1);
    syncObservatories(tt);
    return;
  }
  applyCleanup(tt, new Set<string>(), new Set([tag]));
}

export function planOutboundDeletion(tt: XraySettingsValue, index: number): DeletionImpact {
  const tag = outboundTagAt(tt, index);
  if (!tag) return emptyImpact();
  const removedOutbounds = new Set([tag]);
  const cascaded = balancersEmptiedBy(tt, removedOutbounds);
  const removedBalancers = new Set(cascaded);
  const obs = observersRemovedBy(tt, removedOutbounds, removedBalancers);
  return {
    rules: ruleImpacts(tt, removedOutbounds, removedBalancers),
    balancers: [
      ...cascaded.map((bTag) => ({ tag: bTag, reason: 'selectorEmptied' as const })),
      ...balancersLosingFallback(tt, removedOutbounds, removedBalancers),
    ],
    observatory: obs.observatory,
    burst: obs.burst,
  };
}

// The rule that a loopback fallback makes is the panel's own: it goes with the loopback, and the preview does not list it.
const isLoopbackRule = (rule: RuleObject | undefined): boolean => {
  const inbound = rule?.inboundTag;
  return Array.isArray(inbound) && inbound.some(isBalancerLoopbackTag);
};

/** The plan for several outbounds and balancers at once: one cascade, with the rule labels of the template as it stands. */
export function planDeletionByTags(
  tt: XraySettingsValue,
  outboundTags: string[],
  balancerTags: string[] = [],
): DeletionImpact {
  const removedOutbounds = new Set(outboundTags.filter(Boolean));
  const named = new Set(balancerTags.filter(Boolean));
  const cascaded = balancersEmptiedBy(tt, removedOutbounds).filter((tag) => !named.has(tag));
  const removedBalancers = new Set([...named, ...cascaded]);
  const obs = observersRemovedBy(tt, removedOutbounds, removedBalancers);
  return {
    ...emptyImpact(),
    rules: ruleImpacts(tt, removedOutbounds, removedBalancers).filter(
      (r) => !isLoopbackRule(ruleList(tt)[r.index]),
    ),
    balancers: [
      ...cascaded.map((tag) => ({ tag, reason: 'selectorEmptied' as const })),
      ...balancersLosingFallback(tt, removedOutbounds, removedBalancers),
    ],
    observatory: obs.observatory,
    burst: obs.burst,
  };
}

export function applyDeletionByTags(
  tt: XraySettingsValue,
  outboundTags: string[],
  balancerTags: string[] = [],
): void {
  const removedOutbounds = new Set(outboundTags.filter(Boolean));
  const removedBalancers = new Set([
    ...balancerTags.filter(Boolean),
    ...balancersEmptiedBy(tt, removedOutbounds),
  ]);
  applyCleanup(tt, removedOutbounds, removedBalancers);
}

export function applyOutboundDeletion(tt: XraySettingsValue, index: number): void {
  const tag = outboundTagAt(tt, index);
  if (!tag) {
    if (Array.isArray(tt.outbounds)) tt.outbounds.splice(index, 1);
    syncObservatories(tt);
    return;
  }
  const removedOutbounds = new Set([tag]);
  const removedBalancers = new Set(balancersEmptiedBy(tt, removedOutbounds));
  applyCleanup(tt, removedOutbounds, removedBalancers);
}
