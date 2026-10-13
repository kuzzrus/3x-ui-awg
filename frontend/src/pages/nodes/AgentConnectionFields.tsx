import { useTranslation } from 'react-i18next';
import { Alert, Input, InputNumber, Radio } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import type { AgentFormValues } from '@/schemas/node';

// 8443 is what the installer's docs use; any free TCP port works.
export const DEFAULT_AGENT_PORT = 8443;

// The panel as the browser reaches it, which is also where a node is likeliest to reach it.
export function defaultMasterUrl(): string {
  const base = window.X_UI_BASE_PATH || '/';
  return `${window.location.origin}${base.startsWith('/') ? base : `/${base}`}`;
}

type ConnectionValues = Pick<AgentFormValues, 'pull' | 'port' | 'masterUrl' | 'masterPin'>;

interface AgentConnectionFieldsProps {
  // How the agent reaches the panel when that is not the admin's to choose, as for a node that is only edited.
  fixed?: 'push' | 'pull';
}

// Who opens the connection between the panel and the node, and what that needs: the port the agent listens
// on when the panel dials it, or the address of the panel when the agent does.
export default function AgentConnectionFields({ fixed }: AgentConnectionFieldsProps) {
  const { t } = useTranslation();
  const { control } = useFormContext<ConnectionValues>();
  const chosen = useWatch({ control, name: 'pull' });
  const pull = fixed ? fixed === 'pull' : chosen;

  return (
    <>
      {!fixed && (
        <FormField
          label={t('pages.nodes.agent.connection')}
          name="pull"
          tooltip={t('pages.nodes.agent.connectionHint')}
        >
          <Radio.Group
            options={[
              { value: false, label: t('pages.nodes.agent.connectionPush') },
              { value: true, label: t('pages.nodes.agent.connectionPull') },
            ]}
          />
        </FormField>
      )}
      {fixed === 'pull' && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          title={t('pages.nodes.agent.pullFixed')}
        />
      )}
      {!pull && (
        <FormField
          label={t('pages.nodes.port')}
          name="port"
          tooltip={t('pages.nodes.agent.portHint')}
        >
          <InputNumber min={1} max={65535} style={{ width: '100%' }} />
        </FormField>
      )}
      {pull && !fixed && (
        <>
          <FormField
            label={t('pages.nodes.agent.masterUrl')}
            name="masterUrl"
            tooltip={t('pages.nodes.agent.masterUrlHint')}
          >
            <Input placeholder="https://panel.example.com/path/" />
          </FormField>
          <FormField
            label={t('pages.nodes.agent.masterPin')}
            name="masterPin"
            tooltip={t('pages.nodes.agent.masterPinHint')}
          >
            <Input placeholder={t('pages.nodes.agent.masterPinPlaceholder')} />
          </FormField>
        </>
      )}
    </>
  );
}
