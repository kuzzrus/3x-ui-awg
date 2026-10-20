import { DEFAULT_DECOY_PORT, isDomainName, type AgentFormValues } from '@/schemas/node';
import { DEFAULT_AGENT_PORT, defaultMasterUrl, nodeCanDialPanel } from './AgentConnectionFields';

// What a new agent node starts as, for the wizard that adds one and the form that edits it.
export function defaultAgentValues(): AgentFormValues {
  return {
    id: 0,
    name: '',
    remark: '',
    address: '',
    port: DEFAULT_AGENT_PORT,
    pull: nodeCanDialPanel(),
    masterUrl: defaultMasterUrl(),
    masterPin: '',
    enable: true,
    allowPrivateAddress: false,
    outboundTag: '',
    tags: [],
    sidecars: [],
    decoyMode: '',
    decoyTemplate: 'maintenance',
    decoyProxyUrl: '',
    decoyDomain: '',
    decoyCert: 'auto',
    decoyEmail: '',
    decoyCertFile: '',
    decoyKeyFile: '',
    decoyPort: DEFAULT_DECOY_PORT,
  };
}

// The decoy is for the name of the node unless another is given.
export function withDecoyDomain(values: AgentFormValues): AgentFormValues {
  return values.decoyMode !== '' && values.decoyDomain.trim() === '' && isDomainName(values.address)
    ? { ...values, decoyDomain: values.address.trim() }
    : values;
}
