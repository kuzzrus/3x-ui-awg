import { useTranslation } from 'react-i18next';
import { Col, Input, InputNumber, Radio, Row, Select } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { useDecoyTemplates } from '@/api/queries/useDecoyTemplates';
import { FormField } from '@/components/form/rhf';
import type { AgentFormValues } from '@/schemas/node';

// The decoy of an agent node: what whoever probes the port its REALITY fallback points at is shown. The
// fields after the mode are the ones that mode and the certificate source need.
export default function AgentDecoyFields() {
  const { t } = useTranslation();
  const { control } = useFormContext<AgentFormValues>();
  const mode = useWatch({ control, name: 'decoyMode' });
  const cert = useWatch({ control, name: 'decoyCert' });
  const chosenTemplate = useWatch({ control, name: 'decoyTemplate' });
  const templates = useDecoyTemplates(mode === 'template');
  const templateOptions = (
    templates.includes(chosenTemplate) ? templates : [chosenTemplate, ...templates]
  )
    .filter(Boolean)
    .map((name) => ({
      value: name,
      label: t(`pages.settings.frontProxy.templates.${name}`, { defaultValue: name }),
    }));

  return (
    <>
      <FormField
        label={t('pages.nodes.agent.decoy')}
        name="decoyMode"
        tooltip={t('pages.nodes.agent.decoyHint')}
      >
        <Select
          options={[
            { value: '', label: t('pages.nodes.agent.decoyModes.none') },
            { value: 'template', label: t('pages.nodes.agent.decoyModes.template') },
            { value: 'proxy', label: t('pages.nodes.agent.decoyModes.proxy') },
            { value: 'adguard', label: t('pages.nodes.agent.decoyModes.adguard') },
          ]}
        />
      </FormField>

      {mode === 'template' && (
        <FormField label={t('pages.nodes.agent.decoyTemplate')} name="decoyTemplate">
          <Select showSearch options={templateOptions} />
        </FormField>
      )}
      {mode === 'proxy' && (
        <FormField
          label={t('pages.nodes.agent.decoyProxyUrl')}
          name="decoyProxyUrl"
          tooltip={t('pages.nodes.agent.decoyProxyUrlHint')}
        >
          <Input placeholder="https://example.com" />
        </FormField>
      )}

      {mode !== '' && (
        <>
          <FormField
            label={t('pages.nodes.agent.decoyDomain')}
            name="decoyDomain"
            tooltip={t('pages.nodes.agent.decoyDomainHint')}
          >
            <Input placeholder="node.example.com" />
          </FormField>
          <FormField label={t('pages.nodes.agent.decoyCert')} name="decoyCert">
            <Radio.Group
              options={[
                { value: 'auto', label: t('pages.nodes.agent.decoyCerts.auto') },
                { value: 'manual', label: t('pages.nodes.agent.decoyCerts.manual') },
              ]}
            />
          </FormField>
          {cert === 'manual' ? (
            <Row gutter={16}>
              <Col xs={24} md={12}>
                <FormField label={t('pages.nodes.agent.decoyCertFile')} name="decoyCertFile">
                  <Input placeholder="/etc/ssl/node.pem" />
                </FormField>
              </Col>
              <Col xs={24} md={12}>
                <FormField label={t('pages.nodes.agent.decoyKeyFile')} name="decoyKeyFile">
                  <Input placeholder="/etc/ssl/node.key" />
                </FormField>
              </Col>
            </Row>
          ) : (
            <FormField label={t('pages.nodes.agent.decoyEmail')} name="decoyEmail">
              <Input placeholder="admin@example.com" />
            </FormField>
          )}
          <FormField
            label={t('pages.nodes.agent.decoyPort')}
            name="decoyPort"
            tooltip={t('pages.nodes.agent.decoyPortHint')}
          >
            <InputNumber min={1024} max={65535} style={{ width: '100%' }} />
          </FormField>
        </>
      )}
    </>
  );
}
