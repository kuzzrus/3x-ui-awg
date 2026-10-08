import { useTranslation } from 'react-i18next';
import { Alert, Button, Input, Modal, Space, Typography, message } from 'antd';
import { CopyOutlined } from '@ant-design/icons';

import { ClipboardManager } from '@/utils';
import './AgentBundleModal.css';

export const AGENT_INSTALL_COMMAND =
  'bash <(curl -fsSL https://raw.githubusercontent.com/kuzzrus/3x-ui-awg/main/install-agent.sh)';

export interface AgentPairingView {
  name: string;
  port: number;
  bundle: string;
}

interface AgentBundleModalProps {
  pairing: AgentPairingView | null;
  onClose: () => void;
}

// The bundle holds the agent's private key and is not stored, so the modal only closes from
// its button: a stray click on the mask or Esc must not lose it.
export default function AgentBundleModal({ pairing, onClose }: AgentBundleModalProps) {
  const { t } = useTranslation();
  const [messageApi, messageContextHolder] = message.useMessage();

  async function copy(text: string) {
    if (await ClipboardManager.copyText(text)) {
      messageApi.success(t('pages.nodes.agent.copied'));
    } else {
      messageApi.error(t('somethingWentWrong'));
    }
  }

  return (
    <>
      {messageContextHolder}
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
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          title={t('pages.nodes.agent.bundleWarning')}
        />

        <Typography.Text strong>{t('pages.nodes.agent.bundleLabel')}</Typography.Text>
        <Input.TextArea
          readOnly
          autoSize={{ minRows: 3, maxRows: 6 }}
          value={pairing?.bundle ?? ''}
          className="agent-bundle-text"
          aria-label={t('pages.nodes.agent.bundleLabel')}
          onFocus={(e) => e.target.select()}
        />
        <Button
          icon={<CopyOutlined />}
          style={{ marginTop: 8 }}
          onClick={() => copy(pairing?.bundle ?? '')}
        >
          {t('pages.nodes.agent.copyBundle')}
        </Button>

        <ol className="agent-install-steps">
          <li>{t('pages.nodes.agent.stepFirewall', { port: pairing?.port ?? '' })}</li>
          <li>
            <Space orientation="vertical" size={4} style={{ width: '100%' }}>
              <span>{t('pages.nodes.agent.stepRun')}</span>
              <Space.Compact style={{ width: '100%' }}>
                <Input readOnly value={AGENT_INSTALL_COMMAND} className="agent-bundle-text" />
                <Button
                  icon={<CopyOutlined />}
                  aria-label={t('pages.nodes.agent.copyCommand')}
                  onClick={() => copy(AGENT_INSTALL_COMMAND)}
                />
              </Space.Compact>
            </Space>
          </li>
          <li>{t('pages.nodes.agent.stepPaste')}</li>
        </ol>
      </Modal>
    </>
  );
}
