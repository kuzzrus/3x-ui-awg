import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Input, InputNumber, Select, Typography } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';

import { JsonEditor } from '@/components/form';
import { FormField } from '@/components/form/rhf';
import { isYandexDiskTemplate, yandexDiskPreset } from '@/lib/xray/xdrive-presets';
import { XDriveServiceSchema } from '@/schemas/protocols/stream/xdrive';

const BASE = ['streamSettings', 'xdriveSettings'] as const;
const BASE_PATH = BASE.join('.');

// A choice of this dropdown only: it is stored as the template service with Yandex Disk's template.
const YANDEX_DISK = 'yandex-disk';

const SERVICE_LABEL_KEYS: Partial<Record<string, string>> = {
  local: 'pages.inbounds.form.xdrive.serviceLocal',
  template: 'pages.inbounds.form.xdrive.serviceTemplate',
  [YANDEX_DISK]: 'pages.inbounds.form.xdrive.serviceYandex',
};
const SERVICE_NAMES: Partial<Record<string, string>> = { 'google-drive': 'Google Drive' };

// Google Drive reads three named secrets and the template services a list: a switch between
// kinds starts the secrets over, so one service's credentials never show up in another's fields.
const SECRETS_KIND: Partial<Record<string, string>> = {
  'google-drive': 'named',
  template: 'list',
  [YANDEX_DISK]: 'list',
};

// Core defaults (transport/internet/xdrive/params.go), shown as placeholders so an
// empty field keeps meaning "use the core's value".
const TUNING_FIELDS = [
  ['segmentBytes', 'segmentBytes', 524288],
  ['flushIntervalMs', 'flushInterval', 20],
  ['pollIntervalMs', 'pollInterval', 50],
  ['maxPollIntervalMs', 'maxPollInterval', 500],
  ['eagerWindowMs', 'eagerWindow', 2000],
  ['holeTimeoutMs', 'holeTimeout', 30000],
  ['sessionTtlSeconds', 'sessionTtl', 300],
  ['concurrency', 'concurrency', 8],
] as const;

const GOOGLE_DRIVE_SECRETS = ['clientId', 'clientSecret', 'refreshToken'] as const;

function ServiceSelect({
  id,
  mode,
  options,
  onPick,
}: {
  id?: string;
  mode: string;
  options: { value: string; label: string }[];
  onPick: (next: string) => void;
}) {
  return <Select id={id} value={mode} options={options} onChange={onPick} />;
}

function TemplateEditor({
  value,
  onChange,
}: {
  value?: Record<string, unknown>;
  onChange?: (next: Record<string, unknown> | undefined) => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState(() => (value ? JSON.stringify(value, null, 2) : ''));
  const [invalid, setInvalid] = useState(false);
  return (
    <>
      <JsonEditor
        value={text}
        minHeight="160px"
        onChange={(next) => {
          setText(next);
          if (next.trim() === '') {
            setInvalid(false);
            onChange?.(undefined);
            return;
          }
          try {
            const parsed: unknown = JSON.parse(next);
            const isObject = !!parsed && typeof parsed === 'object' && !Array.isArray(parsed);
            setInvalid(!isObject);
            if (isObject) onChange?.(parsed as Record<string, unknown>);
          } catch {
            setInvalid(true);
          }
        }}
      />
      {invalid && (
        <Typography.Text type="danger">
          {t('pages.inbounds.form.xdrive.templateInvalid')}
        </Typography.Text>
      )}
    </>
  );
}

export default function XDriveForm() {
  const { t } = useTranslation();
  const { control, getValues, setValue } = useFormContext();
  const service = useWatch({ control, name: `${BASE_PATH}.service` }) as string | undefined;
  const template = useWatch({ control, name: `${BASE_PATH}.template` });
  // A Yandex Disk template opened as a custom one stays in the custom editor.
  const [customPinned, setCustomPinned] = useState(false);
  const stored = service ?? 'google-drive';
  const mode =
    stored === 'template' && !customPinned && isYandexDiskTemplate(template) ? YANDEX_DISK : stored;

  const chooseService = (next: string) => {
    const current = (getValues(BASE_PATH) ?? {}) as Record<string, unknown>;
    const sameKind = SECRETS_KIND[mode] === SECRETS_KIND[next];
    // Blank strings, not an empty list: a field with no value falls back to the form's loaded one.
    const blank = next === 'google-drive' ? ['', '', ''] : [];
    const secrets =
      sameKind && Array.isArray(current.secrets) ? (current.secrets as string[]) : blank;
    if (next === YANDEX_DISK) {
      setCustomPinned(false);
      const credentials = [secrets[0] ?? '', secrets[1] ?? ''];
      setValue(
        BASE_PATH,
        { ...current, ...yandexDiskPreset(), service: 'template', secrets: credentials },
        { shouldDirty: true },
      );
      return;
    }
    setCustomPinned(next === 'template');
    setValue(BASE_PATH, { ...current, service: next, secrets }, { shouldDirty: true });
  };

  const serviceOptions = XDriveServiceSchema.options
    .flatMap((value) => (value === 'local' ? [YANDEX_DISK, value] : [value]))
    .map((value) => {
      const labelKey = SERVICE_LABEL_KEYS[value];
      return { value, label: labelKey ? t(labelKey) : (SERVICE_NAMES[value] ?? value) };
    });

  return (
    <>
      <FormField
        label={t('pages.inbounds.form.xdrive.service')}
        name={[...BASE, 'service']}
        extra={mode === YANDEX_DISK ? t('pages.inbounds.form.xdrive.yandexDesc') : undefined}
      >
        <ServiceSelect mode={mode} options={serviceOptions} onPick={chooseService} />
      </FormField>
      <FormField
        label={t('pages.inbounds.form.xdrive.remoteFolder')}
        name={[...BASE, 'remoteFolder']}
        required
      >
        <Input placeholder={mode === 'local' ? '/var/lib/xdrive' : 'xray-tunnel'} />
      </FormField>
      {mode === 'google-drive' &&
        GOOGLE_DRIVE_SECRETS.map((key, index) => (
          <FormField
            key={key}
            label={t(`pages.inbounds.form.xdrive.${key}`)}
            name={[...BASE, 'secrets', index]}
            required
          >
            {key === 'clientId' ? <Input /> : <Input.Password />}
          </FormField>
        ))}
      {mode === YANDEX_DISK && (
        <>
          <FormField
            label={t('pages.inbounds.form.xdrive.yandexLogin')}
            name={[...BASE, 'secrets', 0]}
            required
          >
            <Input autoComplete="off" />
          </FormField>
          <FormField
            label={t('pages.inbounds.form.xdrive.yandexPassword')}
            name={[...BASE, 'secrets', 1]}
            required
          >
            <Input.Password autoComplete="off" />
          </FormField>
        </>
      )}
      {mode === 'template' && (
        <>
          <FormField
            label={t('pages.inbounds.form.xdrive.secrets')}
            tooltip={t('pages.inbounds.form.xdrive.secretsDesc')}
            name={[...BASE, 'secrets']}
          >
            <Select mode="tags" tokenSeparators={[',']} open={false} />
          </FormField>
          <FormField
            label={t('pages.inbounds.form.xdrive.template')}
            name={[...BASE, 'template']}
            required
          >
            <TemplateEditor />
          </FormField>
        </>
      )}
      {service !== 'local' && (
        <>
          <FormField
            label={t('pages.inbounds.form.xdrive.front')}
            tooltip={t('pages.inbounds.form.xdrive.frontDesc')}
            name={['streamSettings', 'address']}
            transform={{ output: (raw) => String(raw ?? '').trim() || undefined }}
          >
            <Input placeholder="www.googleapis.com" />
          </FormField>
          <FormField
            label={t('pages.inbounds.form.xdrive.frontPort')}
            name={['streamSettings', 'port']}
          >
            <InputNumber min={1} max={65535} placeholder="443" />
          </FormField>
        </>
      )}
      {TUNING_FIELDS.map(([key, label, placeholder]) => (
        <FormField key={key} label={t(`pages.inbounds.form.xdrive.${label}`)} name={[...BASE, key]}>
          <InputNumber min={0} placeholder={String(placeholder)} style={{ width: '100%' }} />
        </FormField>
      ))}
    </>
  );
}
