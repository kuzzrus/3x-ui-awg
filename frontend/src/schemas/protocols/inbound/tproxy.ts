import { z } from 'zod';

// A tproxy (Telegram WEB proxy) inbound client (multi-client model). Each
// client is one named MTProxy secret the shared tproxy-server relay and this
// inbound's own MTProxy engine serve; `tproxySecret` is 32 lowercase hex
// characters (optionally a "dd"-prefixed 34-char form -- ValidTproxySecret's
// exact rule, internal/tproxy's own). Unlike mtproto's secret, it carries no
// embedded domain: tproxy has one shared front-proxy domain for the whole
// panel (Settings -> Reverse Proxy), not a per-inbound fakeTlsDomain.
export const TproxyClientSchema = z.object({
  tproxySecret: z.string().default(''),
  email: z.string().min(1),
  limitIp: z.number().int().min(0).default(0),
  totalGB: z.number().int().min(0).default(0),
  expiryTime: z.number().int().default(0),
  enable: z.boolean().default(true),
  tgId: z
    .union([z.number(), z.string()])
    .transform((v) => Number(v) || 0)
    .default(0),
  subId: z.string().default(''),
  comment: z.string().default(''),
  reset: z.number().int().min(0).default(0),
  created_at: z.number().int().optional(),
  updated_at: z.number().int().optional(),
});
export type TproxyClient = z.infer<typeof TproxyClientSchema>;

// tproxy (Telegram WEB proxy) inbound. Bridges a Telegram Desktop app's
// WebView-carried MTProto traffic to a stock MTProxy engine through the
// panel's front proxy, sharing port 443 with everything else -- served
// entirely outside Xray, so it has no stream settings. The engine's
// client/stats ports are allocated and owned by the backend, same as
// routeXrayPort below; the only settings edited here are the client list
// and the egress-routing toggle.
export const TproxyInboundSettingsSchema = z.object({
  clients: z.array(TproxyClientSchema).default([]),
  // The real MTProxy engine (unlike mtproto's mtg sidecar) has no proxy
  // dial-out of its own -- when set, the panel transparently redirects its
  // outbound connections at the OS level into a loopback Xray bridge
  // instead, so the engine itself is never told anything. `outboundTag`
  // optionally forces that traffic out a specific outbound/balancer.
  // `routeXrayPort` is the bridge port; it is allocated and owned by the
  // backend (never edited here).
  routeThroughXray: z.boolean().optional(),
  outboundTag: z.string().optional(),
  routeXrayPort: z.number().int().min(0).max(65535).optional(),
});
export type TproxyInboundSettings = z.infer<typeof TproxyInboundSettingsSchema>;
