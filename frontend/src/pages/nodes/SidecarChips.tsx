import { useTranslation } from 'react-i18next';
import { Tag, Tooltip } from 'antd';
import { LoadingOutlined } from '@ant-design/icons';

import type { NodeRecord } from '@/api/queries/useNodesQuery';

// What each state the agent reports looks like. A state this panel does not know is shown plain.
const STATE_COLORS: Record<string, string> = {
  running: 'success',
  installing: 'processing',
  failed: 'error',
};

interface SidecarChipsProps {
  node: NodeRecord;
  // Writes the last thing each program said beside its chip, which the table keeps to the tooltip.
  detailed?: boolean;
}

// The helper programs an agent node runs, each with how it stands, as the agent last said. What the
// program said last, or why it failed, is in the tooltip: for Tor it tells how far it got.
export default function SidecarChips({ node, detailed = false }: SidecarChipsProps) {
  const { t } = useTranslation();
  if (node.kind !== 'agent' || !node.sidecars?.length) return null;
  return (
    <span className={detailed ? 'node-sidecars is-detailed' : 'node-sidecars'}>
      {node.sidecars.map((name) => {
        const reported = node.sidecarStates?.find((one) => one.name === name);
        const state = reported?.state ?? 'pending';
        const label = t(`pages.nodes.agent.sidecarNames.${name}`, { defaultValue: name });
        const stateLabel = t(`pages.nodes.agent.sidecarStates.${state}`, { defaultValue: state });
        const message = reported?.message;
        return (
          <span key={name} className="node-sidecar">
            <Tooltip title={message || stateLabel}>
              <Tag
                color={STATE_COLORS[state]}
                icon={state === 'installing' ? <LoadingOutlined aria-hidden /> : undefined}
              >
                {label}: {stateLabel}
              </Tag>
            </Tooltip>
            {detailed && message && <span className="sidecar-message">{message}</span>}
          </span>
        );
      })}
    </span>
  );
}
