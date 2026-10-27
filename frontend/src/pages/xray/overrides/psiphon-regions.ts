/** A region of the picker. `available` is left out until a tunnel has said which regions it has servers in. */
export interface PsiphonRegion {
  code: string;
  name: string;
  available?: boolean;
}

/** True once a tunnel has said which regions have Psiphon servers. */
export const regionsKnown = (regions: PsiphonRegion[]): boolean =>
  regions.some((r) => r.available !== undefined);

/**
 * Options of a region picker. A region without Psiphon servers stays in the list, greyed out and marked, since a tunnel
 * through it would never connect, and comes after the ones that have servers.
 */
export function regionPickerOptions(regions: PsiphonRegion[], noServers: string) {
  const option = (r: PsiphonRegion) => ({
    value: r.code,
    label: `${r.name} (${r.code})${r.available === false ? ` — ${noServers}` : ''}`,
    disabled: r.available === false,
  });
  const served = regions.filter((r) => r.available !== false).map(option);
  const unserved = regions.filter((r) => r.available === false).map(option);
  return [...served, ...unserved];
}
