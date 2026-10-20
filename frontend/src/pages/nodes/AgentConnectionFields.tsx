import { useTranslation } from 'react-i18next';
import { Alert, Input, InputNumber, Radio, Tag, Typography } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { isPublicDomainName, type AgentFormValues } from '@/schemas/node';

// 8443 is what the installer's docs use; any free TCP port works.
export const DEFAULT_AGENT_PORT = 8443;

// The panel as the browser reaches it, which is also where a node is likeliest to reach it.
export function defaultMasterUrl(): string {
  const base = window.X_UI_BASE_PATH || '/';
  return `${window.location.origin}${base.startsWith('/') ? base : `/${base}`}`;
}

// A panel opened over https at a public name is one that a node can dial, which needs no open port on the
// node. At an IP address or at localhost it is one that a node may not reach, and the panel dials the node.
export function nodeCanDialPanel(): boolean {
  return window.location.protocol === 'https:' && isPublicDomainName(window.location.hostname);
}

type ConnectionValues = Pick<AgentFormValues, 'pull' | 'port' | 'masterUrl' | 'masterPin'>;

interface AgentConnectionFieldsProps {
  // How the agent reaches the panel when that is not the admin's to choose, as for a node that is only edited.
  fixed?: 'push' | 'pull';
  // Says in the form what each way costs, which the add wizard needs and the compact forms leave to a tooltip.
  explain?: boolean;
}

function Choice({ title, hint, tag }: { title: string; hint: string; tag?: string }) {
  return (
    <span>
      {title}
      {tag && (
        <Tag color="green" style={{ marginInlineStart: 8 }}>
          {tag}
        </Tag>
      )}
      <br />
      <Typography.Text type="secondary">{hint}</Typography.Text>
    </span>
  );
}

// Who opens the connection between the panel and the node, and what that needs: the port the agent listens
// on when the panel dials it, or the address of the panel when the agent does.
export default function AgentConnectionFields({ fixed, explain }: AgentConnectionFieldsProps) {
  const { t } = useTranslation();
  const { control } = useFormContext<ConnectionValues>();
  const chosen = useWatch({ control, name: 'pull' });
  const pull = fixed ? fixed === 'pull' : chosen;
  const push = t('pages.nodes.agent.connectionPush');
  const dial = t('pages.nodes.agent.connectionPull');

  return (
    <>
      {!fixed && (
        <FormField
          label={t('pages.nodes.agent.connection')}
          name="pull"
          tooltip={explain ? undefined : t('pages.nodes.agent.connectionHint')}
        >
          <Radio.Group
            orientation={explain ? 'vertical' : undefined}
            options={[
              {
                value: false,
                label: explain ? (
                  <Choice title={push} hint={t('pages.nodes.agent.connectionPushHint')} />
                ) : (
                  push
                ),
              },
              {
                value: true,
                label: explain ? (
                  <Choice
                    title={dial}
                    hint={t('pages.nodes.agent.connectionPullHint')}
                    tag={nodeCanDialPanel() ? t('pages.nodes.agent.recommended') : undefined}
                  />
                ) : (
                  dial
                ),
              },
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
          label={t('pages.nodes.agent.port')}
          name="port"
          tooltip={explain ? undefined : t('pages.nodes.agent.portHint')}
          extra={explain ? t('pages.nodes.agent.portWhy') : undefined}
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
            extra={t('pages.nodes.agent.masterUrlNote')}
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
