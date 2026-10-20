import { useTranslation } from 'react-i18next';
import { Alert, Button, Input, Space, Typography, message } from 'antd';
import { CopyOutlined } from '@ant-design/icons';

import { ClipboardManager } from '@/utils';
import './AgentBundleModal.css';

export const AGENT_INSTALL_COMMAND =
  'bash <(curl -fsSL https://raw.githubusercontent.com/kuzzrus/3x-ui-awg/main/install-agent.sh)';

export interface AgentPairingView {
  name: string;
  port: number;
  bundle: string;
  // The address of this panel that an agent in pull mode dials, which has no port to open.
  masterUrl?: string;
  // The node the bundle is for, so that a window can follow it until it is online.
  nodeId?: number;
}

interface AgentInstallGuideProps {
  pairing: AgentPairingView | null;
}

// The bundle holds the agent's private key and is not stored, so this is where it is copied from, with
// what to do on the server once it is.
export default function AgentInstallGuide({ pairing }: AgentInstallGuideProps) {
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
        <li>
          {pairing?.masterUrl
            ? t('pages.nodes.agent.stepReach', { url: pairing.masterUrl })
            : t('pages.nodes.agent.stepFirewall', { port: pairing?.port ?? '' })}
        </li>
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
    </>
  );
}
