import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Col, Form, Input, InputNumber, Modal, Row, Select, Switch, message } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import type { NodeRecord } from '@/api/queries/useNodesQuery';
import type { Msg } from '@/utils';
import { AgentFormSchema, type AgentFormValues } from '@/schemas/node';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { useNodeOutboundOptions } from './useNodeOutboundOptions';

// 8443 is what the installer's docs use; any free TCP port works.
const DEFAULT_AGENT_PORT = 8443;

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
    enable: true,
    allowPrivateAddress: false,
    outboundTag: '',
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

            <Row gutter={16}>
              <Col xs={24} md={16}>
                <FormField
                  label={t('pages.nodes.address')}
                  name="address"
                  rules={{ validate: rhfZodValidate(AgentFormSchema.shape.address) }}
                >
                  <Input placeholder={t('pages.nodes.addressPlaceholder')} />
                </FormField>
              </Col>
              <Col xs={24} md={8}>
                <FormField
                  label={t('pages.nodes.port')}
                  name="port"
                  rules={{ validate: rhfZodValidate(AgentFormSchema.shape.port) }}
                  tooltip={t('pages.nodes.agent.portHint')}
                >
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                </FormField>
              </Col>
            </Row>

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
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
