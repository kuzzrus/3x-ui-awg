import { z } from 'zod';

export const MAX_NODE_TAGS = 16;
export const MAX_NODE_TAG_LENGTH = 32;
const TAGS_INVALID = 'pages.nodes.agent.tagsInvalid';
const DECOY_INCOMPLETE = 'pages.nodes.agent.decoyIncomplete';
const MASTER_URL_INVALID = 'pages.nodes.agent.masterUrlInvalid';
const MASTER_PIN_INVALID = 'pages.nodes.agent.masterPinInvalid';

// The loopback port an agent's decoy listens on when none is chosen, which the REALITY fallback of the node points at.
export const DEFAULT_DECOY_PORT = 7443;

// The helper programs an agent can run beside its core, which the panel offers on the node's form.
export const NODE_SIDECARS = ['tor', 'psiphon'] as const;

export const NodeRecordSchema = z
  .object({
    id: z.number(),
    name: z.string().optional(),
    remark: z.string().optional(),
    // A thin x-ui-agent node; a response without it is a full panel.
    kind: z.enum(['panel', 'agent']).optional(),
    // An agent node whose agent dials this panel, which has no port of its own for the panel to reach.
    pull: z.boolean().optional(),
    scheme: z.string().optional(),
    address: z.string().optional(),
    port: z.number().optional(),
    basePath: z.string().optional(),
    apiToken: z.string().optional(),
    hasApiToken: z.boolean().optional(),
    enable: z.boolean().optional(),
    status: z.string().optional(),
    latencyMs: z.number().optional(),
    cpuPct: z.number().optional(),
    memPct: z.number().optional(),
    xrayVersion: z.string().optional(),
    panelVersion: z.string().optional(),
    uptimeSecs: z.number().optional(),
    inboundCount: z.number().optional(),
    clientCount: z.number().optional(),
    onlineCount: z.number().optional(),
    activeCount: z.number().optional(),
    disabledCount: z.number().optional(),
    depletedCount: z.number().optional(),
    lastHeartbeat: z.number().optional(),
    lastError: z.string().optional(),
    // Xray state captured from the remote node's own /panel/api/server/status.
    // Lets the nodes list show a distinct indicator when the panel API is reachable
    // (status=online) but the Xray core on that node has failed.
    xrayState: z.string().optional(),
    xrayError: z.string().optional(),
    allowPrivateAddress: z.boolean().optional(),
    tlsVerifyMode: z.enum(['verify', 'skip', 'pin', 'mtls']).optional(),
    pinnedCertSha256: z.string().optional(),
    inboundSyncMode: z.enum(['all', 'selected']).optional(),
    // Backend serializes a nil []string as null for nodes saved before #5178.
    inboundTags: z.array(z.string()).nullish(),
    outboundTag: z.string().optional(),
    // An agent node's tags, which pick the routing rules it runs.
    tags: z.array(z.string()).nullish(),
    // The helper programs an agent node runs beside its core, and how each stood at the last heartbeat.
    sidecars: z.array(z.string()).nullish(),
    sidecarStates: z
      .array(z.object({ name: z.string(), state: z.string(), message: z.string().optional() }))
      .nullish(),
    // The decoy site an agent node serves (no mode is none), and how it stood at the last heartbeat.
    decoy: z
      .object({
        mode: z.string(),
        template: z.string(),
        proxyUrl: z.string(),
        domain: z.string(),
        cert: z.string(),
        email: z.string(),
        certFile: z.string(),
        keyFile: z.string(),
        port: z.number(),
      })
      .partial()
      .nullish(),
    decoyState: z
      .object({
        state: z.string(),
        cert: z.string(),
        certNotAfter: z.number(),
        message: z.string(),
      })
      .partial()
      .nullish(),
    // Multi-hop node tree (#4983): a node's stable GUID, its parent's GUID, and
    // whether it's a read-only transitive sub-node surfaced from a downstream node.
    guid: z.string().optional(),
    parentGuid: z.string().optional(),
    transitive: z.boolean().optional(),
  })
  .loose();

export const NodeListSchema = z.array(NodeRecordSchema);

export const ProbeResultSchema = z
  .object({
    status: z.string(),
    latencyMs: z.number().optional(),
    xrayVersion: z.string().optional(),
    error: z.string().optional(),
    // Present on successful probe; used to surface "connected to panel, but xray failed on node".
    xrayState: z.string().optional(),
    xrayError: z.string().optional(),
  })
  .loose();

export const NodeFormSchema = z
  .object({
    id: z.number().optional(),
    name: z.string().trim().min(1, 'pages.nodes.toasts.fillRequired'),
    remark: z.string().optional(),
    scheme: z.enum(['http', 'https']),
    address: z.string().trim().min(1, 'pages.nodes.toasts.fillRequired'),
    port: z.number().int().min(1).max(65535),
    basePath: z.string(),
    // mTLS nodes authenticate via the client certificate, so the token is optional
    // there; every other verify mode still requires one (matches remote.do()).
    apiToken: z.string().trim(),
    hasStoredToken: z.boolean().optional().default(false),
    enable: z.boolean(),
    allowPrivateAddress: z.boolean(),
    tlsVerifyMode: z.enum(['verify', 'skip', 'pin', 'mtls']),
    pinnedCertSha256: z.string().optional().default(''),
    inboundSyncMode: z.enum(['all', 'selected']).optional().default('all'),
    // Unmounted when sync mode is "all" (absent from antd onFinish values) and
    // serialized as null by the backend for a nil slice — tolerate both.
    inboundTags: z
      .array(z.string())
      .nullish()
      .transform((tags) => tags ?? []),
    outboundTag: z.string().optional(),
  })
  .superRefine((val, ctx) => {
    if (val.tlsVerifyMode !== 'mtls' && val.apiToken.length === 0 && !val.hasStoredToken) {
      ctx.addIssue({
        code: 'custom',
        path: ['apiToken'],
        message: 'pages.nodes.toasts.fillRequired',
      });
    }
  });

// The address a node that dials this panel is given: https, with no login, query or fragment.
export function isMasterUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && !url.username && !url.password && !url.search && !url.hash;
  } catch {
    return false;
  }
}

// A SHA-256 pin as a browser shows it: 64 hex digits, with or without colons.
export function isCertPin(value: string): boolean {
  return /^[0-9a-f]{64}$/i.test(value.replace(/:/g, ''));
}

// An agent node is added by where it will be reachable, or by the panel it dials: its secret (and
// certificate) are minted by the panel and come back once, in the pairing bundle.
export const AgentFormSchema = z
  .object({
    id: z.number().optional(),
    name: z.string().trim().min(1, 'pages.nodes.toasts.fillRequired'),
    remark: z.string().optional(),
    address: z.string().trim().min(1, 'pages.nodes.toasts.fillRequired'),
    port: z.number().int().min(1).max(65535),
    // The agent dials the panel at masterUrl, which has no use for the port above.
    pull: z.boolean(),
    masterUrl: z.string().trim(),
    masterPin: z.string().trim(),
    enable: z.boolean(),
    allowPrivateAddress: z.boolean(),
    outboundTag: z.string().optional(),
    tags: z
      .array(z.string().trim().min(1, TAGS_INVALID).max(MAX_NODE_TAG_LENGTH, TAGS_INVALID))
      .max(MAX_NODE_TAGS, TAGS_INVALID),
    sidecars: z.array(z.string()),
    decoyMode: z.enum(['', 'template', 'proxy', 'adguard']),
    decoyTemplate: z.string(),
    decoyProxyUrl: z.string().trim(),
    decoyDomain: z.string().trim(),
    decoyCert: z.enum(['auto', 'manual']),
    decoyEmail: z.string().trim(),
    decoyCertFile: z.string().trim(),
    decoyKeyFile: z.string().trim(),
    // A cleared box means the default, and the port is only checked when there is a decoy to serve it.
    decoyPort: z
      .number()
      .nullable()
      .transform((port) => port ?? DEFAULT_DECOY_PORT),
  })
  .superRefine((val, ctx) => {
    if (val.pull) {
      if (!isMasterUrl(val.masterUrl)) {
        ctx.addIssue({ code: 'custom', path: ['masterUrl'], message: MASTER_URL_INVALID });
      }
      if (val.masterPin && !isCertPin(val.masterPin)) {
        ctx.addIssue({ code: 'custom', path: ['masterPin'], message: MASTER_PIN_INVALID });
      }
    }
    if (val.decoyMode === '') return;
    const missing =
      !Number.isInteger(val.decoyPort) ||
      val.decoyPort < 1024 ||
      val.decoyPort > 65535 ||
      !val.decoyDomain ||
      (val.decoyMode === 'proxy' && !val.decoyProxyUrl) ||
      (val.decoyCert === 'manual' && (!val.decoyCertFile || !val.decoyKeyFile));
    if (missing) ctx.addIssue({ code: 'custom', path: ['decoyMode'], message: DECOY_INCOMPLETE });
  });

// How an agent node is paired again: the way the agent reaches the panel, and what that way needs.
export const AgentRepairFormSchema = z
  .object({
    pull: z.boolean(),
    port: z.number().int().min(1).max(65535),
    masterUrl: z.string().trim(),
    masterPin: z.string().trim(),
  })
  .superRefine((val, ctx) => {
    if (!val.pull) return;
    if (!isMasterUrl(val.masterUrl)) {
      ctx.addIssue({ code: 'custom', path: ['masterUrl'], message: MASTER_URL_INVALID });
    }
    if (val.masterPin && !isCertPin(val.masterPin)) {
      ctx.addIssue({ code: 'custom', path: ['masterPin'], message: MASTER_PIN_INVALID });
    }
  });

export const AgentPairingSchema = z
  .object({
    node: NodeRecordSchema.nullish(),
    bundle: z.string(),
  })
  .loose();

export type NodeRecord = z.infer<typeof NodeRecordSchema>;
export type ProbeResult = z.infer<typeof ProbeResultSchema>;
export type NodeFormValues = z.infer<typeof NodeFormSchema>;
export type AgentFormValues = z.infer<typeof AgentFormSchema>;
export type AgentRepairValues = z.infer<typeof AgentRepairFormSchema>;
export type AgentPairing = z.infer<typeof AgentPairingSchema>;
