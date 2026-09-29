import { useTranslation } from 'react-i18next';
import { Input, InputNumber } from 'antd';

import { FormField } from '@/components/form/rhf';

export default function NaiveProxyFields() {
  const { t } = useTranslation();
  return (
    <>
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
    </>
  );
}
