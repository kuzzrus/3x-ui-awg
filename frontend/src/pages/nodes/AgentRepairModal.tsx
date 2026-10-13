import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Form, Modal, message } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import type { NodeRecord } from '@/api/queries/useNodesQuery';
import type { Msg } from '@/utils';
import { AgentRepairFormSchema, type AgentRepairValues } from '@/schemas/node';
import AgentConnectionFields, {
  DEFAULT_AGENT_PORT,
  defaultMasterUrl,
} from './AgentConnectionFields';

interface AgentRepairModalProps {
  // The node to pair again, or null when the dialog is closed.
  node: NodeRecord | null;
  pair: (node: NodeRecord, values: AgentRepairValues) => Promise<Msg<unknown>>;
  onClose: () => void;
}

function initialValues(node: NodeRecord): AgentRepairValues {
  const pull = node.pull === true;
  return {
    pull,
    // The port a node that dials the panel holds is only a placeholder, so a node switched to push starts from the default.
    port: pull ? DEFAULT_AGENT_PORT : (node.port ?? DEFAULT_AGENT_PORT),
    masterUrl: defaultMasterUrl(),
    masterPin: '',
  };
}

// Pairs an agent node again, the way it was or the other way: the agent that holds the old bundle is cut off at once.
export default function AgentRepairModal({ node, pair, onClose }: AgentRepairModalProps) {
  const { t } = useTranslation();
  const methods = useForm<AgentRepairValues>({
    defaultValues: { pull: false, port: DEFAULT_AGENT_PORT, masterUrl: '', masterPin: '' },
  });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<NodeRecord | null>(null);
  if (!node) {
    if (synced) setSynced(null);
  } else if (synced !== node) {
    setSynced(node);
    methods.reset(initialValues(node));
  }

  async function onFinish(values: AgentRepairValues) {
    const result = AgentRepairFormSchema.safeParse(values);
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.nodes.toasts.fillRequired'));
      return;
    }
    if (!node) return;
    setSubmitting(true);
    try {
      const msg = await pair(node, result.data);
      if (msg?.success) onClose();
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      {messageContextHolder}
      <Modal
        open={node !== null}
        title={t('pages.nodes.agent.repairConfirmTitle', { name: node?.name ?? '' })}
        confirmLoading={submitting}
        okText={t('pages.nodes.agent.repair')}
        okButtonProps={{ danger: true }}
        cancelText={t('cancel')}
        mask={{ closable: false }}
        width="520px"
        onOk={methods.handleSubmit(onFinish)}
        onCancel={() => {
          if (!submitting) onClose();
        }}
        destroyOnHidden
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          title={t('pages.nodes.agent.repairConfirmContent')}
        />
        <FormProvider {...methods}>
          <Form layout="vertical">
            <AgentConnectionFields />
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
