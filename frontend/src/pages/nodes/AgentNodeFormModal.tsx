import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Checkbox, Col, Form, Input, Modal, Row, Select, Switch, message } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import type { NodeRecord } from '@/api/queries/useNodesQuery';
import type { Msg } from '@/utils';
import {
  AgentFormSchema,
  DEFAULT_DECOY_PORT,
  MAX_NODE_TAGS,
  NODE_SIDECARS,
  type AgentFormValues,
} from '@/schemas/node';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { useNodeTags } from '@/api/queries/useNodeTags';
import { useNodeOutboundOptions } from './useNodeOutboundOptions';
import AgentConnectionFields, {
  DEFAULT_AGENT_PORT,
  defaultMasterUrl,
} from './AgentConnectionFields';
import AgentDecoyFields from './AgentDecoyFields';

interface AgentNodeFormModalProps {
  open: boolean;
  node: NodeRecord | null;
  save: (values: AgentFormValues) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

function defaultValues(): AgentFormValues {
  return {
    id: 0,
    name: '',
    remark: '',
    address: '',
    port: DEFAULT_AGENT_PORT,
    pull: false,
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

// Adds an agent node (node === null) or edits one. The agent's secret and certificate are not
// fields: the panel mints them, and the pairing bundle that carries them comes back once.
export default function AgentNodeFormModal({
  open,
  node,
  save,
  onOpenChange,
}: AgentNodeFormModalProps) {
  const { t } = useTranslation();
  const methods = useForm<AgentFormValues>({ defaultValues: defaultValues() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);
  const outboundOptions = useNodeOutboundOptions();
  const tagOptions = useNodeTags().map((tag) => ({ label: tag, value: tag }));
  const sidecarOptions = NODE_SIDECARS.map((name) => ({
    label: t(`pages.nodes.agent.sidecarDescriptions.${name}`),
    value: name,
  }));
  const editing = node !== null;

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<{ node: NodeRecord | null } | null>(null);
  if (!open) {
    if (synced) setSynced(null);
  } else if (!synced || synced.node !== node) {
    setSynced({ node });
    const base = defaultValues();
    methods.reset(
      node
        ? {
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
          }
        : base,
    );
  }

  async function onFinish(values: AgentFormValues) {
    const result = AgentFormSchema.safeParse(values);
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
        title={editing ? t('pages.nodes.editNode') : t('pages.nodes.agent.addTitle')}
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
          title={editing ? t('pages.nodes.agent.editHint') : t('pages.nodes.agent.addHint')}
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
              label={t('pages.nodes.address')}
              name="address"
              rules={{ validate: rhfZodValidate(AgentFormSchema.shape.address) }}
            >
              <Input placeholder={t('pages.nodes.addressPlaceholder')} />
            </FormField>

            <AgentConnectionFields fixed={editing ? (node.pull ? 'pull' : 'push') : undefined} />

            {editing && (
              <FormField label={t('pages.nodes.enable')} name="enable" valueProp="checked">
                <Switch />
              </FormField>
            )}

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

            <AgentDecoyFields />
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
