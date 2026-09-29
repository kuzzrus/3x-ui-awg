import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Alert, Button, Input, InputNumber, Select, Switch } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { keys } from '@/api/queryKeys';
import { useNaiveProxyEngineQuery } from '@/api/queries/useNaiveProxyEngineQuery';
import { useOutboundTags } from '@/api/queries/useOutboundTags';
import { FormField } from '@/components/form/rhf';
import { HttpUtil } from '@/utils';
import { getMessage } from '@/utils/messageBus';

// The Caddy build every NaiveProxy inbound runs is downloaded on demand, so a
// fresh panel has none: say so here, where the inbound is created, and install it in place.
function EngineNotice() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { data: status } = useNaiveProxyEngineQuery();
  const [installing, setInstalling] = useState(false);

  async function install() {
    setInstalling(true);
    try {
      const msg = await HttpUtil.post('/panel/api/xray/naiveproxy/install');
      if (msg?.success) getMessage().success(t('pages.inbounds.form.naiveProxyEngineInstalled'));
      await queryClient.invalidateQueries({ queryKey: keys.xray.naiveProxyEngine() });
    } finally {
      setInstalling(false);
    }
  }

  if (!status || status.installed) return null;
  if (!status.supported) {
    return (
      <Alert
        type="error"
        showIcon
        style={{ marginBottom: 16 }}
        message={t('pages.inbounds.form.naiveProxyEngineUnsupported', {
          platform: status.platform,
        })}
      />
    );
  }
  return (
    <Alert
      type="warning"
      showIcon
      style={{ marginBottom: 16 }}
      message={t('pages.inbounds.form.naiveProxyEngineMissing')}
      description={t('pages.inbounds.form.naiveProxyEngineHint')}
      action={
        <Button size="small" type="primary" loading={installing} onClick={install}>
          {t('pages.inbounds.form.naiveProxyEngineInstall')}
        </Button>
      }
    />
  );
}

export default function NaiveProxyFields() {
  const { t } = useTranslation();
  const { control } = useFormContext();
  const routeThroughXray = useWatch({ control, name: 'settings.routeThroughXray' }) as
    | boolean
    | undefined;
  const { data: outboundTags } = useOutboundTags({ excludeBlackhole: true });
  return (
    <>
      <EngineNotice />
      <FormField
        name={['settings', 'domain']}
        label={t('pages.inbounds.form.naiveProxyDomain')}
        tooltip={t('pages.inbounds.form.naiveProxyDomainHint')}
      >
        <Input placeholder="naive.example.com" />
      </FormField>
      <FormField
        name={['settings', 'certFile']}
        label={t('pages.inbounds.form.naiveProxyCertFile')}
      >
        <Input placeholder="/etc/letsencrypt/live/naive.example.com/fullchain.pem" />
      </FormField>
      <FormField name={['settings', 'keyFile']} label={t('pages.inbounds.form.naiveProxyKeyFile')}>
        <Input placeholder="/etc/letsencrypt/live/naive.example.com/privkey.pem" />
      </FormField>
      <FormField
        name={['settings', 'publicPort']}
        label={t('pages.inbounds.form.naiveProxyPublicPort')}
        tooltip={t('pages.inbounds.form.naiveProxyPublicPortHint')}
      >
        <InputNumber min={1} max={65535} placeholder="443" />
      </FormField>
      <FormField
        name={['settings', 'routeThroughXray']}
        label={t('pages.inbounds.form.naiveProxyRouteThroughXray')}
        tooltip={t('pages.inbounds.form.naiveProxyRouteThroughXrayHint')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      {routeThroughXray && (
        <FormField
          name={['settings', 'outboundTag']}
          label={t('pages.inbounds.form.naiveProxyRouteOutbound')}
          tooltip={t('pages.inbounds.form.naiveProxyRouteOutboundHint')}
        >
          <Select
            id="naiveProxyOutboundTag"
            allowClear
            showSearch
            placeholder={t('pages.inbounds.form.naiveProxyRouteOutboundPlaceholder')}
            options={(outboundTags ?? []).map((tag) => ({ value: tag, label: tag }))}
          />
        </FormField>
      )}
    </>
  );
}
