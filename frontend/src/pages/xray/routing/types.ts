export interface RuleRow {
  key: number;
  enabled?: boolean;
  comment?: string;
  domain?: string;
  ip?: string;
  port?: string;
  sourcePort?: string;
  vlessRoute?: string;
  network?: string;
  sourceIP?: string;
  user?: string;
  inboundTag?: string;
  nodeTags?: string;
  protocol?: string;
  attrs?: string;
  outboundTag?: string;
  balancerTag?: string;
}
