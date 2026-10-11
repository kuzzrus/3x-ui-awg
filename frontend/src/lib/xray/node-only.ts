// An outbound with nodeTags is only on some agent nodes, so the panel's own core lacks it.
export function isNodeOnly(entry: unknown): boolean {
  const tags = (entry as { nodeTags?: unknown } | null)?.nodeTags;
  return Array.isArray(tags) && tags.length > 0;
}

// The balancers that select outbounds, all of them limited to agent nodes: the panel's own core has them
// empty. `shared` lists the tags every core has besides the template's, those of the subscriptions' outbounds.
export function nodeOnlyBalancers(
  outbounds: unknown[],
  balancers: unknown[],
  shared: string[] = [],
): Set<string> {
  const limited = new Set<string>();
  for (const balancer of balancers) {
    const rec = balancer as { tag?: unknown; selector?: unknown } | null;
    if (typeof rec?.tag !== 'string' || !Array.isArray(rec.selector)) continue;
    const prefixes = rec.selector.filter((entry): entry is string => typeof entry === 'string');
    const selects = (tag: string) => prefixes.some((prefix) => tag.startsWith(prefix));
    const members = outbounds.filter((o) => {
      const tag = (o as { tag?: unknown } | null)?.tag;
      return typeof tag === 'string' && selects(tag);
    });
    if (members.length > 0 && !members.some((o) => !isNodeOnly(o)) && !shared.some(selects)) {
      limited.add(rec.tag);
    }
  }
  return limited;
}
