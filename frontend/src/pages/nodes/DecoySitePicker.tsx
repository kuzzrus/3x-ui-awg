import { useRef } from 'react';
import type { ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Popconfirm, Select, Space, message } from 'antd';
import { DeleteOutlined, UploadOutlined } from '@ant-design/icons';

import { useDecoySiteMutations, useDecoySites } from '@/api/queries/useDecoySites';

interface DecoySitePickerProps {
  // The form field that holds the choice passes these, and the name of its aria attributes.
  id?: string;
  value?: string;
  onChange?: (site: string) => void;
  'aria-describedby'?: string;
  'aria-invalid'?: boolean | 'true' | 'false';
}

function sizeOf(bytes: number): string {
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MiB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KiB`;
}

// The static site an agent node serves as its decoy: one of the zips the panel holds, or a new one to upload. A site is
// kept by the panel and sent to the node, so several nodes can serve the same one.
export default function DecoySitePicker({ id, value, onChange, ...aria }: DecoySitePickerProps) {
  const { t } = useTranslation();
  const [messageApi, messageContextHolder] = message.useMessage();
  const sites = useDecoySites();
  const { upload, uploading, remove, removing } = useDecoySiteMutations();
  const fileInput = useRef<HTMLInputElement>(null);

  const chosen = sites.find((site) => site.sha256 === value);
  const options = sites.map((site) => ({
    value: site.sha256,
    label: t('pages.nodes.agent.decoySiteChosen', { name: site.name, size: sizeOf(site.size) }),
  }));

  async function onFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    // The same file may be chosen again after it was deleted.
    event.target.value = '';
    if (!file) return;
    const msg = await upload(file);
    if (msg?.success && msg.obj) {
      onChange?.(msg.obj.sha256);
      messageApi.success(t('pages.nodes.agent.decoySiteUploaded'));
    }
  }

  async function onDelete() {
    if (!chosen) return;
    const msg = await remove(chosen.sha256);
    if (msg?.success) onChange?.('');
  }

  return (
    <>
      {messageContextHolder}
      <Space.Compact style={{ width: '100%' }}>
        <Select
          id={id}
          value={value || undefined}
          options={options}
          placeholder={t('pages.nodes.agent.decoySitePlaceholder')}
          onChange={(site: string) => onChange?.(site)}
          style={{ flex: 1, minWidth: 0 }}
          {...aria}
        />
        <Button
          icon={<UploadOutlined aria-hidden />}
          loading={uploading}
          onClick={() => fileInput.current?.click()}
        >
          {t('pages.nodes.agent.decoySiteUpload')}
        </Button>
        <Popconfirm
          title={t('pages.nodes.agent.decoySiteDeleteConfirm', { name: chosen?.name ?? '' })}
          disabled={!chosen}
          onConfirm={onDelete}
        >
          <Button
            danger
            disabled={!chosen}
            loading={removing}
            icon={<DeleteOutlined aria-hidden />}
            aria-label={t('pages.nodes.agent.decoySiteDelete')}
          />
        </Popconfirm>
      </Space.Compact>
      <input
        ref={fileInput}
        type="file"
        accept=".zip,application/zip"
        hidden
        data-testid="decoy-site-file"
        onChange={onFile}
      />
    </>
  );
}
