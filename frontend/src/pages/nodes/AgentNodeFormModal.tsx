import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Checkbox,
  Col,
  Collapse,
  Form,
  Input,
  Modal,
  Row,
  Select,
  Switch,
  message,
} from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import type { NodeRecord } from '@/api/queries/useNodesQuery';
import type { Msg } from '@/utils';
import {
  AgentFormSchema,
  MAX_NODE_TAGS,
  NODE_SIDECARS,
  type AgentFormValues,
} from '@/schemas/node';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { useNodeTags } from '@/api/queries/useNodeTags';
import { useNodeOutboundOptions } from './useNodeOutboundOptions';
import AgentConnectionFields from './AgentConnectionFields';
import AgentDecoyFields from './AgentDecoyFields';
import { defaultAgentValues, withDecoyDomain } from './agentFormValues';

interface AgentNodeFormModalProps {
  open: boolean;
  // The agent node to edit: adding one is the wizard's.
  node: NodeRecord;
  save: (values: AgentFormValues) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

// The decoy of a node as the form edits it: what the node holds, over the defaults of a new one.
function decoyValues(node: NodeRecord, base: AgentFormValues): Partial<AgentFormValues> {
  const decoy = node.decoy;
  if (!decoy?.mode) return {};
  return {
    decoyMode: decoy.mode === 'proxy' || decoy.mode === 'adguard' ? decoy.mode : 'template',
    decoyTemplate: decoy.template || base.decoyTemplate,
    decoyProxyUrl: decoy.proxyUrl ?? '',
    decoyDomain: decoy.domain ?? '',
    decoyCert: decoy.cert === 'manual' ? 'manual' : 'auto',
    decoyEmail: decoy.email ?? '',
    decoyCertFile: decoy.certFile ?? '',
    decoyKeyFile: decoy.keyFile ?? '',
    decoyPort: decoy.port || base.decoyPort,
  };
}

// The options that most nodes leave as they are, which an edited node shows open when it has changed one.
function usesAdvancedOptions(node: NodeRecord): boolean {
  return (
    !!node.allowPrivateAddress ||
    !!node.outboundTag ||
    !!node.tags?.length ||
    !!node.sidecars?.length
  );
}

// Edits an agent node. The agent's secret and certificate are not fields: the panel mints them, and the
// pairing bundle that carries them comes back once.
export default function AgentNodeFormModal({
  open,
  node,
  save,
  onOpenChange,
}: AgentNodeFormModalProps) {
  const { t } = useTranslation();
  const methods = useForm<AgentFormValues>({ defaultValues: defaultAgentValues() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const outboundOptions = useNodeOutboundOptions();
  const tagOptions = useNodeTags().map((tag) => ({ label: tag, value: tag }));
  const sidecarOptions = NODE_SIDECARS.map((name) => ({
    label: t(`pages.nodes.agent.sidecarDescriptions.${name}`),
    value: name,
  }));

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<NodeRecord | null>(null);
  if (!open) {
    if (synced) setSynced(null);
  } else if (synced !== node) {
    setSynced(node);
    setAdvancedOpen(usesAdvancedOptions(node));
    const base = defaultAgentValues();
    methods.reset({
      ...base,
      id: node.id,
      name: node.name ?? '',
      remark: node.remark ?? '',
      address: node.address ?? '',
      port: node.port ?? base.port,
      enable: node.enable ?? true,
      allowPrivateAddress: node.allowPrivateAddress ?? false,
      outboundTag: node.outboundTag ?? '',
      tags: node.tags ?? [],
      sidecars: node.sidecars ?? [],
      ...decoyValues(node, base),
    });
  }

  async function onFinish(values: AgentFormValues) {
    const result = AgentFormSchema.safeParse(withDecoyDomain(values));
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.nodes.toasts.fillRequired'));
      return;
    }
    setSubmitting(true);
    try {
      const msg = await save({
        ...result.data,
        remark: result.data.remark?.trim() || '',
        outboundTag: result.data.outboundTag || '',
      });
      if (msg?.success) onOpenChange(false);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      {messageContextHolder}
      <Modal
        open={open}
        title={t('pages.nodes.editNode')}
        confirmLoading={submitting}
        okText={t('save')}
        cancelText={t('cancel')}
        mask={{ closable: false }}
        width="560px"
        onOk={methods.handleSubmit(onFinish)}
        onCancel={() => {
          if (!submitting) onOpenChange(false);
        }}
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          title={t('pages.nodes.agent.editHint')}
        />
        <FormProvider {...methods}>
          <Form layout="vertical">
            <Row gutter={16}>
              <Col xs={24} md={12}>
                <FormField
                  label={t('pages.nodes.name')}
                  name="name"
                  rules={{ validate: rhfZodValidate(AgentFormSchema.shape.name) }}
                >
                  <Input placeholder={t('pages.nodes.namePlaceholder')} />
                </FormField>
              </Col>
              <Col xs={24} md={12}>
                <FormField label={t('pages.nodes.remark')} name="remark">
                  <Input />
                </FormField>
              </Col>
            </Row>

            <FormField
              label={t('pages.nodes.agent.address')}
              name="address"
              tooltip={t('pages.nodes.agent.addressHint')}
              rules={{ validate: rhfZodValidate(AgentFormSchema.shape.address) }}
            >
              <Input placeholder={t('pages.nodes.agent.addressPlaceholder')} />
            </FormField>

            <AgentConnectionFields fixed={node.pull ? 'pull' : 'push'} />

            <FormField label={t('pages.nodes.enable')} name="enable" valueProp="checked">
              <Switch />
            </FormField>

            <AgentDecoyFields />

            <Collapse
              ghost
              size="small"
              activeKey={advancedOpen ? ['more'] : []}
              onChange={(keys) => setAdvancedOpen(keys.length > 0)}
              items={[
                {
                  key: 'more',
                  label: t('pages.nodes.agent.advanced'),
                  forceRender: true,
                  children: (
                    <>
                      <FormField
                        label={t('pages.nodes.allowPrivateAddress')}
                        name="allowPrivateAddress"
                        valueProp="checked"
                        tooltip={t('pages.nodes.allowPrivateAddressHint')}
                      >
                        <Switch />
                      </FormField>

                      <FormField
                        label={t('pages.nodes.outboundTag')}
                        name="outboundTag"
                        tooltip={t('pages.nodes.outboundTagHint')}
                        transform={{ input: (v) => (v as string) || undefined }}
                      >
                        <Select
                          allowClear
                          showSearch
                          placeholder={t('pages.nodes.outboundTagPlaceholder')}
                          options={outboundOptions}
                        />
                      </FormField>

                      <FormField
                        label={t('pages.nodes.agent.tags')}
                        name="tags"
                        tooltip={t('pages.nodes.agent.tagsHint')}
                      >
                        <Select
                          mode="tags"
                          maxCount={MAX_NODE_TAGS}
                          tokenSeparators={[',']}
                          placeholder={t('pages.nodes.agent.tagsPlaceholder')}
                          options={tagOptions}
                        />
                      </FormField>

                      <FormField
                        label={t('pages.nodes.agent.sidecars')}
                        name="sidecars"
                        tooltip={t('pages.nodes.agent.sidecarsHint')}
                      >
                        <Checkbox.Group options={sidecarOptions} />
                      </FormField>
                    </>
                  ),
                },
              ]}
            />
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
