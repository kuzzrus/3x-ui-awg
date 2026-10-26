import { useTranslation } from 'react-i18next';
import { Tag, Tooltip } from 'antd';
import { LoadingOutlined } from '@ant-design/icons';

import type { NodeRecord } from '@/api/queries/useNodesQuery';
import { DEFAULT_DECOY_PORT } from '@/schemas/node';

interface Standing {
  key: string; // the name of its word in the translations
  color?: string;
  busy?: boolean;
}

// How the decoy stands, from what the agent reported: the listener first, and once it runs the
// certificate, which is the part that fails for a name that does not point at the node.
function standingOf(node: NodeRecord): Standing {
  const reported = node.decoyState;
  switch (reported?.state) {
    case 'failed':
      return { key: 'failed', color: 'error' };
    case 'starting':
      return { key: 'starting', color: 'processing', busy: true };
    case 'running':
      switch (reported.cert) {
        case 'obtaining':
          return { key: 'certObtaining', color: 'processing', busy: true };
        case 'failed':
          return { key: 'certFailed', color: 'error' };
        default:
          return { key: 'running', color: 'success' };
      }
    default:
      // The agent serves none yet: it is old, or has not heard of this decoy.
      return { key: 'waiting' };
  }
}

interface DecoyChipProps {
  node: NodeRecord;
  // Adds where the REALITY fallback of the node has to point, which the table leaves to the dialog.
  detailed?: boolean;
}

// The decoy of an agent node and how it stands. The tooltip says why it failed, or until when its
// certificate is good.
export default function DecoyChip({ node, detailed = false }: DecoyChipProps) {
  const { t } = useTranslation();
  const decoy = node.decoy;
  if (node.kind !== 'agent' || !decoy?.mode) return null;

  const standing = standingOf(node);
  const word = t(`pages.nodes.agent.decoyStates.${standing.key}`);
  const reported = node.decoyState;
  const until = reported?.certNotAfter
    ? t('pages.nodes.agent.decoyCertUntil', {
        date: new Date(reported.certNotAfter * 1000).toLocaleDateString(),
      })
    : '';
  const tip = reported?.message || until || word;
  return (
    <span className="node-decoy">
      <Tooltip title={tip}>
        <Tag
          color={standing.color}
          icon={standing.busy ? <LoadingOutlined aria-hidden /> : undefined}
        >
          {t('pages.nodes.agent.decoyChip')}: {word}
        </Tag>
      </Tooltip>
      {detailed && (
        <span className="decoy-message">
          {t('pages.nodes.agent.decoyTarget', {
            target: `127.0.0.1:${decoy.port || DEFAULT_DECOY_PORT}`,
            domain: decoy.domain ?? '',
          })}
          {(reported?.message || until) && ` · ${reported?.message || until}`}
        </span>
      )}
    </span>
  );
}
