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
    decoySite: '',
    decoyDomain: '',
    decoyCert: 'auto',
    decoyEmail: '',
    decoyCertFile: '',
    decoyKeyFile: '',
    decoyPort: DEFAULT_DECOY_PORT,
  };
}

// What the form of an agent node and the wizard that adds one have in common, as the panel takes it.
export function agentEndpoint(values: AgentFormValues) {
  return {
    name: values.name,
    remark: values.remark,
    address: values.address,
    port: values.port,
    allowPrivateAddress: values.allowPrivateAddress,
    outboundTag: values.outboundTag,
    tags: values.tags,
    sidecars: values.sidecars,
    decoyMode: values.decoyMode,
    decoyTemplate: values.decoyTemplate,
    decoyProxyUrl: values.decoyProxyUrl,
    decoySite: values.decoySite,
    decoyDomain: values.decoyDomain,
    decoyCert: values.decoyCert,
    decoyEmail: values.decoyEmail,
    decoyCertFile: values.decoyCertFile,
    decoyKeyFile: values.decoyKeyFile,
    decoyPort: values.decoyPort,
  };
}

// The decoy is for the name of the node unless another is given.
export function withDecoyDomain(values: AgentFormValues): AgentFormValues {
  return values.decoyMode !== '' && values.decoyDomain.trim() === '' && isDomainName(values.address)
    ? { ...values, decoyDomain: values.address.trim() }
    : values;
}
