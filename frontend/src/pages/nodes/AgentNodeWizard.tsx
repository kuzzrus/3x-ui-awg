import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Checkbox,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Steps,
  Switch,
  Typography,
  message,
} from 'antd';
import { LoadingOutlined } from '@ant-design/icons';
import { FormProvider, useForm } from 'react-hook-form';

import type { AgentPairing } from '@/api/queries/useNodeMutations';
import { useNodesQuery } from '@/api/queries/useNodesQuery';
import { useNodeTags } from '@/api/queries/useNodeTags';
import type { Msg } from '@/utils';
import {
  AgentFormSchema,
  MAX_NODE_TAGS,
  NODE_SIDECARS,
  type AgentFormValues,
} from '@/schemas/node';
import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { useNodeOutboundOptions } from './useNodeOutboundOptions';
import AgentConnectionFields from './AgentConnectionFields';
import AgentDecoyFields from './AgentDecoyFields';
import AgentInstallGuide, { type AgentPairingView } from './AgentInstallGuide';
import { defaultAgentValues, withDecoyDomain } from './agentFormValues';

const SERVER = 0;
const LINK = 1;
const DECOY = 2;
const MORE = 3;
const INSTALL = 4;
const STEP_KEYS = ['server', 'link', 'decoy', 'more', 'install'];

// What each step asks for, so that a step is held back only by its own fields.
const STEP_FIELDS: string[][] = [
  ['name', 'remark', 'address'],
  ['pull', 'port', 'masterUrl', 'masterPin'],
  [
    'decoyMode',
    'decoyTemplate',
    'decoyProxyUrl',
    'decoyDomain',
    'decoyCert',
    'decoyEmail',
    'decoyCertFile',
    'decoyKeyFile',
    'decoyPort',
  ],
  ['allowPrivateAddress', 'outboundTag', 'tags', 'sidecars'],
];

function ServerStep() {
  const { t } = useTranslation();
  return (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        title={t('pages.nodes.agent.addHint')}
      />
      <Typography.Title level={5}>{t('pages.nodes.agent.wizard.serverTitle')}</Typography.Title>
      <FormField
        label={t('pages.nodes.name')}
        name="name"
        rules={{ validate: rhfZodValidate(AgentFormSchema.shape.name) }}
      >
        <Input placeholder={t('pages.nodes.namePlaceholder')} />
      </FormField>
      <FormField
        label={t('pages.nodes.agent.address')}
        name="address"
        extra={t('pages.nodes.agent.wizard.addressHint')}
        rules={{ validate: rhfZodValidate(AgentFormSchema.shape.address) }}
      >
        <Input placeholder={t('pages.nodes.agent.addressPlaceholder')} />
      </FormField>
      <FormField label={t('pages.nodes.remark')} name="remark">
        <Input />
      </FormField>
    </>
  );
}

function LinkStep() {
  const { t } = useTranslation();
  return (
    <>
      <Typography.Title level={5}>{t('pages.nodes.agent.wizard.linkTitle')}</Typography.Title>
      <AgentConnectionFields explain />
    </>
  );
}

function DecoyStep() {
  const { t } = useTranslation();
  return (
    <>
      <Typography.Title level={5}>{t('pages.nodes.agent.wizard.decoyTitle')}</Typography.Title>
      <Typography.Paragraph type="secondary">
        {t('pages.nodes.agent.wizard.decoyLead')}
      </Typography.Paragraph>
      <AgentDecoyFields explain />
    </>
  );
}

function MoreStep() {
  const { t } = useTranslation();
  const outboundOptions = useNodeOutboundOptions();
  const tagOptions = useNodeTags().map((tag) => ({ label: tag, value: tag }));
  const sidecarOptions = NODE_SIDECARS.map((name) => ({
    label: t(`pages.nodes.agent.sidecarDescriptions.${name}`),
    value: name,
  }));
  return (
    <>
      <Typography.Title level={5}>{t('pages.nodes.agent.wizard.moreTitle')}</Typography.Title>
      <Typography.Paragraph type="secondary">
        {t('pages.nodes.agent.wizard.moreLead')}
      </Typography.Paragraph>
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
  );
}

// The node list is pushed to the page, so the node shows up online here by itself once its agent is running.
function InstallStep({ pairing }: { pairing: AgentPairingView }) {
  const { t } = useTranslation();
  const { nodes } = useNodesQuery();
  const online = nodes.find((n) => n.id === pairing.nodeId)?.status === 'online';
  return (
    <>
      <Alert
        type="success"
        showIcon
        style={{ marginBottom: 16 }}
        title={t('pages.nodes.agent.wizard.created', { name: pairing.name })}
      />
      <AgentInstallGuide pairing={pairing} />
      {online ? (
        <Alert
          type="success"
          showIcon
          style={{ marginTop: 16 }}
          title={t('pages.nodes.agent.wizard.online')}
        />
      ) : (
        <Alert
          type="info"
          showIcon
          style={{ marginTop: 16 }}
          icon={<LoadingOutlined aria-hidden />}
          title={t('pages.nodes.agent.wizard.waiting')}
          description={t('pages.nodes.agent.wizard.waitingHint')}
        />
      )}
    </>
  );
}

interface AgentNodeWizardProps {
  create: (values: AgentFormValues) => Promise<Msg<AgentPairing>>;
  onClose: () => void;
}

// Adds an agent node one question at a time. It is mounted only while it is open, so a new one starts
// clean and the pairing bundle, which is shown once, does not outlive it.
export default function AgentNodeWizard({ create, onClose }: AgentNodeWizardProps) {
  const { t } = useTranslation();
  const methods = useForm<AgentFormValues>({ defaultValues: defaultAgentValues() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [step, setStep] = useState(SERVER);
  const [submitting, setSubmitting] = useState(false);
  const [pairing, setPairing] = useState<AgentPairingView | null>(null);

  function problemAt(at: number, values: AgentFormValues): string | null {
    const result = AgentFormSchema.safeParse(withDecoyDomain(values));
    if (result.success) return null;
    const issue = result.error.issues.find((i) => STEP_FIELDS[at].includes(String(i.path[0])));
    return issue ? t(issue.message) : null;
  }

  async function next() {
    if (step === SERVER && !(await methods.trigger(['name', 'address']))) return;
    const problem = problemAt(step, methods.getValues());
    if (problem) {
      messageApi.error(problem);
      return;
    }
    setStep(step + 1);
  }

  async function createNode() {
    const result = AgentFormSchema.safeParse(withDecoyDomain(methods.getValues()));
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.nodes.toasts.fillRequired'));
      return;
    }
    setSubmitting(true);
    try {
      const msg = await create({
        ...result.data,
        remark: result.data.remark?.trim() || '',
        outboundTag: result.data.outboundTag || '',
      });
      if (msg?.success && msg.obj) {
        setPairing({
          name: result.data.name,
          port: result.data.port,
          bundle: msg.obj.bundle,
          masterUrl: result.data.pull ? result.data.masterUrl : undefined,
          nodeId: msg.obj.node?.id,
        });
        setStep(INSTALL);
      }
    } finally {
      setSubmitting(false);
    }
  }

  const installing = step === INSTALL && pairing !== null;

  return (
    <>
      {messageContextHolder}
      <Modal
        open
        title={t('pages.nodes.agent.addTitle')}
        closable={!installing}
        keyboard={!installing}
        mask={{ closable: false }}
        width="600px"
        onCancel={() => {
          if (!submitting) onClose();
        }}
        footer={
          installing ? (
            <Button type="primary" onClick={onClose}>
              {t('pages.nodes.agent.done')}
            </Button>
          ) : (
            <Space>
              <Button disabled={submitting} onClick={onClose}>
                {t('cancel')}
              </Button>
              {step > SERVER && (
                <Button disabled={submitting} onClick={() => setStep(step - 1)}>
                  {t('pages.nodes.agent.wizard.back')}
                </Button>
              )}
              {step < MORE ? (
                <Button type="primary" onClick={next}>
                  {t('pages.nodes.agent.wizard.next')}
                </Button>
              ) : (
                <Button type="primary" loading={submitting} onClick={createNode}>
                  {t('pages.nodes.agent.wizard.create')}
                </Button>
              )}
            </Space>
          )
        }
      >
        <Steps
          size="small"
          current={step}
          responsive={false}
          titlePlacement="vertical"
          style={{ marginBottom: 24 }}
          styles={{ itemTitle: { fontSize: 12 } }}
          items={STEP_KEYS.map((key) => ({ title: t(`pages.nodes.agent.wizard.steps.${key}`) }))}
        />
        <FormProvider {...methods}>
          <Form layout="vertical">
            {step === SERVER && <ServerStep />}
            {step === LINK && <LinkStep />}
            {step === DECOY && <DecoyStep />}
            {step === MORE && <MoreStep />}
            {installing && <InstallStep pairing={pairing} />}
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
