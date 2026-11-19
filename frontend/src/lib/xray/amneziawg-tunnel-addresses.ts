const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;
const IPV6 = /^[0-9a-f:.]+$/i;

interface Entry {
  v6: boolean;
  host: boolean;
}

function parseEntry(raw: string): Entry | null {
  const [addr, bits, extra] = raw.trim().split('/');
  if (extra !== undefined || !addr) return null;
  const v4 = IPV4.exec(addr);
  const v6 =
    !v4 && addr.includes(':') && IPV6.test(addr) && !addr.toLowerCase().startsWith('::ffff:');
  if (v4 ? v4.slice(1).some((o) => Number(o) > 255) : !v6) return null;
  const max = v6 ? 128 : 32;
  if (bits !== undefined && !(/^\d{1,3}$/.test(bits) && Number(bits) <= max)) return null;
  return { v6, host: bits === undefined || Number(bits) === max };
}

// A client's own end of the tunnel among its AllowedIPs: the first single-host entry (/32, /128) of each family. Every other
// entry is a network behind it; an IPv4 list with none keeps its first. Mirrors amneziawg.TunnelAddresses on the Go side.
export function tunnelAddresses(allowedIPs: readonly string[]): string[] {
  let v4 = -1;
  let v6 = -1;
  let v4First = -1;
  allowedIPs.forEach((raw, i) => {
    const entry = parseEntry(raw);
    if (!entry) return;
    if (entry.v6) {
      if (entry.host && v6 < 0) v6 = i;
      return;
    }
    if (v4First < 0) v4First = i;
    if (entry.host && v4 < 0) v4 = i;
  });
  if (v4 < 0) v4 = v4First;
  return [v4, v6]
    .filter((i) => i >= 0)
    .sort((a, b) => a - b)
    .map((i) => allowedIPs[i].trim());
}

// The same for the comma-separated string a client record carries; a string that holds nothing but tunnel addresses is
// returned as it is.
export function tunnelAddressesText(raw: string): string {
  const entries = raw.split(',').filter((e) => e.trim() !== '');
  const tunnel = tunnelAddresses(entries);
  return tunnel.length === entries.length ? raw : tunnel.join(', ');
}
