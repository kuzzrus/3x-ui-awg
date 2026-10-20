import { useTranslation } from 'react-i18next';
import { Button, Modal } from 'antd';

import AgentInstallGuide, { type AgentPairingView } from './AgentInstallGuide';

export { AGENT_INSTALL_COMMAND } from './AgentInstallGuide';
export type { AgentPairingView } from './AgentInstallGuide';

interface AgentBundleModalProps {
  pairing: AgentPairingView | null;
  onClose: () => void;
}

// The bundle holds the agent's private key and is not stored, so the modal only closes from
// its button: a stray click on the mask or Esc must not lose it.
export default function AgentBundleModal({ pairing, onClose }: AgentBundleModalProps) {
  const { t } = useTranslation();

  return (
    <Modal
      open={pairing !== null}
      title={t('pages.nodes.agent.bundleTitle', { name: pairing?.name ?? '' })}
      closable={false}
      keyboard={false}
      mask={{ closable: false }}
      width="640px"
      footer={
        <Button type="primary" onClick={onClose}>
          {t('pages.nodes.agent.done')}
        </Button>
      }
      onCancel={onClose}
      destroyOnHidden
    >
      <AgentInstallGuide pairing={pairing} />
    </Modal>
  );
}
