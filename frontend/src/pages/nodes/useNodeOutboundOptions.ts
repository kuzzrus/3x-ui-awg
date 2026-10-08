import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import { useOutboundTagGroups } from '@/api/queries/useOutboundTags';

type Option = { label: string; value: string };
export type NodeOutboundOptions = (Option | { label: string; options: Option[] })[];

// Outbounds and balancers share one picker (like the panel-outbound selector); when balancers
// exist they get a labeled group so it is clear the selection routes through a balancer.
export function useNodeOutboundOptions(): NodeOutboundOptions {
  const { t } = useTranslation();
  const { data: outboundGroups } = useOutboundTagGroups({ excludeBlackhole: true });
  return useMemo(() => {
    const outOpts = (outboundGroups?.outbounds ?? []).map((tag) => ({ label: tag, value: tag }));
    if (!outboundGroups?.balancers.length) return outOpts;
    return [
      { label: t('pages.xray.Outbounds'), options: outOpts },
      {
        label: t('pages.xray.Balancers'),
        options: outboundGroups.balancers.map((tag) => ({ label: tag, value: tag })),
      },
    ];
  }, [outboundGroups, t]);
}
