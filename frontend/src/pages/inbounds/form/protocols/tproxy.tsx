import { useTranslation } from 'react-i18next';
import { Select, Switch } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { FormField } from '@/components/form/rhf';
import { useOutboundTags } from '@/api/queries/useOutboundTags';

// tproxy's only inbound-level setting: whether the shared MTProxy engine's
// own outbound traffic is transparently redirected through a chosen Xray
// outbound (internal/web/service/xray.go's injectTproxyEgress +
// internal/tproxy/firewall.go's OS-level redirect -- the engine itself has
// no proxy dial-out of its own, unlike mtproto's mtg). Deliberately mirrors
// MtprotoFields' identical routeThroughXray/outboundTag block: same
// contract, same UI shape. No routeXrayPort field here either -- that port
// is allocated and owned entirely by the backend.
export default function TproxyFields() {
  const { t } = useTranslation();
  const { control } = useFormContext();
  const routeThroughXray = useWatch({ control, name: 'settings.routeThroughXray' }) as
    | boolean
    | undefined;
  const { data: outboundTags } = useOutboundTags({ excludeBlackhole: true });
  return (
    <>
      <FormField
        name={['settings', 'routeThroughXray']}
        label={t('pages.inbounds.form.tproxyRouteThroughXray')}
        tooltip={t('pages.inbounds.form.tproxyRouteThroughXrayHint')}
        valueProp="checked"
      >
        <Switch />
      </FormField>
      {routeThroughXray && (
        <FormField
          name={['settings', 'outboundTag']}
          label={t('pages.inbounds.form.tproxyRouteOutbound')}
          tooltip={t('pages.inbounds.form.tproxyRouteOutboundHint')}
        >
          <Select
            id="tproxyOutboundTag"
            allowClear
            showSearch
            placeholder={t('pages.inbounds.form.tproxyRouteOutboundPlaceholder')}
            options={(outboundTags ?? []).map((tag) => ({ value: tag, label: tag }))}
          />
        </FormField>
      )}
    </>
  );
}
