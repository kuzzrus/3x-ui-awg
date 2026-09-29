import { z } from 'zod';

// A NaiveProxy client: HTTP Basic Auth credentials Caddy's forward_proxy
// checks. The username is this client's Email, not a separate field.
export const NaiveproxyClientSchema = z.object({
  naiveProxyPassword: z.string().default(''),
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
export type NaiveproxyClient = z.infer<typeof NaiveproxyClientSchema>;

// A NaiveProxy inbound. Served by its own Caddy sidecar, not Xray, reached on
// its own domain via internal/frontproxy's SNI-relay -- so it has no stream
// settings, and (unlike every Xray protocol) genuinely needs its own domain
// and certificate rather than sharing the panel's.
export const NaiveproxyInboundSettingsSchema = z.object({
  // All three are required: an untouched '' reaches frontproxy's SNITargets
  // (dropped silently) and Caddy's own "tls ..." directive (fails to start).
  domain: z.string().min(1).default(''),
  certFile: z.string().min(1).default(''),
  keyFile: z.string().min(1).default(''),
  publicPort: z.number().int().min(1).max(65535).default(443),
  // Opt-in: Caddy dials out through a loopback Xray SOCKS bridge; routeXrayPort is
  // allocated and owned by the backend, never edited here.
  routeThroughXray: z.boolean().optional(),
  outboundTag: z.string().optional(),
  routeXrayPort: z.number().int().min(0).max(65535).optional(),
  clients: z.array(NaiveproxyClientSchema).default([]),
});
export type NaiveproxyInboundSettings = z.infer<typeof NaiveproxyInboundSettingsSchema>;
