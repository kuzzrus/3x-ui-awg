import { useTranslation } from 'react-i18next';
import { Alert, Button } from 'antd';

import { countryName } from '../outbounds/outbounds-tab-helpers';
import { usePsiphonNewRegions } from './usePsiphonNewRegions';

// A country in the language of the panel, or its code where the browser does not know the name.
function regionLabel(code: string, language: string): string {
  const name = countryName(code, language);
  return name && name !== code ? `${name} (${code})` : code;
}

interface PsiphonNewRegionsBannerProps {
  /** Opens the window of Psiphon, where the scan lists the regions. */
  onOpen: () => void;
}

/** Says that a scan of Psiphon found a region that none before it had, until the admin dismisses it. */
export default function PsiphonNewRegionsBanner({ onOpen }: PsiphonNewRegionsBannerProps) {
  const { t, i18n } = useTranslation();
  const { regions, dismiss } = usePsiphonNewRegions();
  if (regions.length === 0) return null;
  return (
    <Alert
      style={{ marginBottom: 16 }}
      type="success"
      showIcon
      closable={{ onClose: dismiss }}
      title={t('pages.xray.psiphon.newRegionBanner', {
        regions: regions.map((code) => regionLabel(code, i18n.language)).join(', '),
      })}
      action={
        <Button size="small" onClick={onOpen}>
          {t('pages.xray.psiphon.newRegionOpen')}
        </Button>
      }
    />
  );
}
