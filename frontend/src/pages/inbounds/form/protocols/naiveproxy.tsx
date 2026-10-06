import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { Alert, Button, Input, InputNumber, Segmented, Select, Switch } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { keys } from '@/api/queryKeys';
import { useAllSettings } from '@/api/queries/useAllSettings';
import { useNaiveProxyEngineQuery } from '@/api/queries/useNaiveProxyEngineQuery';
import {
  useNaiveProxyCertsQuery,
  type NaiveProxyCert,
} from '@/api/queries/useNaiveProxyCertsQuery';
import { useOutboundTags } from '@/api/queries/useOutboundTags';
import { FormField } from '@/components/form/rhf';
import { HttpUtil, LanguageManager } from '@/utils';
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

const DAY_MS = 24 * 60 * 60 * 1000;
// A healthy certificate is renewed about 30 days before it ends, so any of these means it is not happening.
const CERT_WARN_DAYS = 14;
const CERT_ALARM_DAYS = 3;

interface CertView {
  type: 'info' | 'success' | 'warning' | 'error';
  message: string;
  retry: boolean;
}

type Translate = (key: string, options?: Record<string, unknown>) => string;

// Days left rounded up: a certificate with 20 hours to go still has one.
function daysUntil(notAfter: Date, now: number): number {
  return Math.ceil((notAfter.getTime() - now) / DAY_MS);
}

function describeCert(cert: NaiveProxyCert, t: Translate, now: number): CertView {
  const key = (name: string) => `pages.inbounds.form.${name}`;
  const parsed = cert.notAfter ? new Date(cert.notAfter) : null;
  const expiry = parsed && Number.isFinite(parsed.getTime()) ? parsed : null;
  const days = expiry ? daysUntil(expiry, now) : 0;
  const date = expiry
    ? new Intl.DateTimeFormat(LanguageManager.getLanguage(), { dateStyle: 'medium' }).format(expiry)
    : '';
  const error = cert.error ?? '';
  const auto = cert.mode === 'auto';

  // A certificate that is in place, however it got there.
  const inPlace = (): CertView => {
    if (days <= 0) {
      return { type: 'error', message: t(key('naiveProxyCertExpired'), { date }), retry: auto };
    }
    if (days > CERT_WARN_DAYS) {
      return {
        type: 'success',
        message: t(key('naiveProxyCertValid'), { date, days }),
        retry: false,
      };
    }
    const text = auto ? 'naiveProxyCertExpiring' : 'naiveProxyCertExpiringManual';
    return {
      type: days <= CERT_ALARM_DAYS ? 'error' : 'warning',
      message: t(key(text), { date, days }),
      retry: auto,
    };
  };
  const waiting: CertView = {
    type: 'info',
    message: t(key('naiveProxyCertWaiting')),
    retry: false,
  };

  if (!auto) {
    if (cert.state === 'failed' || !expiry) {
      return {
        type: 'error',
        message: t(key('naiveProxyCertFileUnreadable'), { error }),
        retry: false,
      };
    }
    return inPlace();
  }
  switch (cert.state) {
    case 'obtaining':
      return {
        type: 'info',
        message: expiry
          ? t(key('naiveProxyCertRenewing'), { date })
          : t(key('naiveProxyCertObtaining')),
        retry: false,
      };
    case 'failed':
      if (!expiry) {
        return { type: 'error', message: t(key('naiveProxyCertFailed'), { error }), retry: true };
      }
      return {
        type: days <= CERT_ALARM_DAYS ? 'error' : 'warning',
        message: t(key('naiveProxyCertRenewalFailed'), { date, days: Math.max(days, 0), error }),
        retry: true,
      };
    case 'obtained':
      return expiry ? inPlace() : waiting;
    default:
      return waiting;
  }
}

// Says where this inbound's certificate stands: what the panel has ordered, or when the file the
// admin set ends. It describes what is saved, so an unsaved change of mode or domain hides it.
function CertNotice({
  inboundId,
  mode,
  domain,
}: {
  inboundId: number | null;
  mode: 'auto' | 'manual';
  domain: string;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { data: certs, dataUpdatedAt } = useNaiveProxyCertsQuery(inboundId != null);
  const [retrying, setRetrying] = useState(false);

  const cert = certs?.find((c) => c.inboundId === inboundId);
  const saved =
    cert !== undefined &&
    cert.mode === mode &&
    cert.domain.trim().toLowerCase() === domain.trim().toLowerCase();

  async function retry() {
    setRetrying(true);
    try {
      const msg = await HttpUtil.post('/panel/api/xray/naiveproxy/certRetry', { inboundId });
      if (msg?.success) getMessage().success(t('pages.inbounds.form.naiveProxyCertRetryStarted'));
      await queryClient.invalidateQueries({ queryKey: keys.xray.naiveProxyCerts() });
    } finally {
      setRetrying(false);
    }
  }

  if (!saved) {
    return mode === 'auto' ? (
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t('pages.inbounds.form.naiveProxyCertAutoHint')}
      />
    ) : null;
  }
  // The time of the last fetch stands in for now: reading the clock during render is not pure.
  const view = describeCert(cert, t as Translate, dataUpdatedAt);
  return (
    <Alert
      type={view.type}
      showIcon
      style={{ marginBottom: 16 }}
      message={view.message}
      action={
        view.retry ? (
          <Button size="small" loading={retrying} onClick={retry}>
            {t('pages.inbounds.form.naiveProxyCertRetry')}
          </Button>
        ) : undefined
      }
    />
  );
}

export default function NaiveProxyFields({ inboundId = null }: { inboundId?: number | null }) {
  const { t } = useTranslation();
  const { control } = useFormContext();
  const { allSetting } = useAllSettings();
  const certMode =
    (useWatch({ control, name: 'settings.certMode' }) as 'auto' | 'manual' | undefined) ?? 'manual';
  const domain = (useWatch({ control, name: 'settings.domain' }) as string | undefined) ?? '';
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
        name={['settings', 'certMode']}
        label={t('pages.inbounds.form.naiveProxyCertMode')}
      >
        <Segmented
          options={[
            { label: t('pages.inbounds.form.naiveProxyCertModeAuto'), value: 'auto' },
            { label: t('pages.inbounds.form.naiveProxyCertModeManual'), value: 'manual' },
          ]}
        />
      </FormField>
      <CertNotice inboundId={inboundId} mode={certMode} domain={domain} />
      {certMode === 'auto' ? (
        <FormField
          name={['settings', 'acmeEmail']}
          label={t('pages.inbounds.form.naiveProxyAcmeEmail')}
          tooltip={t('pages.inbounds.form.naiveProxyAcmeEmailHint')}
        >
          <Input placeholder={allSetting.frontProxyEmail || 'admin@example.com'} />
        </FormField>
      ) : (
        <>
          <FormField
            name={['settings', 'certFile']}
            label={t('pages.inbounds.form.naiveProxyCertFile')}
          >
            <Input placeholder="/etc/letsencrypt/live/naive.example.com/fullchain.pem" />
          </FormField>
          <FormField
            name={['settings', 'keyFile']}
            label={t('pages.inbounds.form.naiveProxyKeyFile')}
          >
            <Input placeholder="/etc/letsencrypt/live/naive.example.com/privkey.pem" />
          </FormField>
        </>
      )}
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
