import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Progress, Table, Tag, Tooltip, Typography } from 'antd';
import type { TableColumnsType } from 'antd';
import { ScanOutlined, StopOutlined } from '@ant-design/icons';

import {
  sortScanResults,
  type PsiphonRegion,
  type PsiphonScanResult,
  type PsiphonScanStatus,
} from './psiphon-regions';

interface PsiphonScanProps {
  scan: PsiphonScanStatus | null;
  regions: PsiphonRegion[];
  busy: boolean;
  onStart: () => void;
  onCancel: () => void;
}

export default function PsiphonScan({ scan, regions, busy, onStart, onCancel }: PsiphonScanProps) {
  const { t } = useTranslation();
  const running = !!scan?.running;
  const results = scan?.results;
  const rows = useMemo(() => sortScanResults(results ?? []), [results]);
  const done = results?.length ?? 0;
  const nameOf = (code: string) => regions.find((r) => r.code === code)?.name ?? code;

  const columns: TableColumnsType<PsiphonScanResult> = [
    {
      title: t('pages.xray.psiphon.colCountry'),
      key: 'country',
      render: (_: unknown, r) => `${nameOf(r.region)} (${r.region})`,
    },
    {
      title: t('pages.xray.psiphon.colState'),
      key: 'state',
      render: (_: unknown, r) =>
        r.connected ? (
          <Tag color="green">{t('pages.xray.psiphon.countryConnected')}</Tag>
        ) : (
          <Tooltip title={r.error}>
            <Tag color="red">{t('pages.xray.psiphon.scanNoTunnel')}</Tag>
          </Tooltip>
        ),
    },
    {
      title: t('pages.xray.psiphon.scanColConnect'),
      key: 'connect',
      render: (_: unknown, r) =>
        r.connectMs
          ? t('pages.xray.psiphon.scanSeconds', { s: (r.connectMs / 1000).toFixed(1) })
          : '—',
    },
    {
      title: t('pages.xray.psiphon.scanColLatency'),
      key: 'latency',
      render: (_: unknown, r) => (
        <Tooltip title={r.error}>
          <span>
            {r.latencyMs ? t('pages.xray.psiphon.probeResult', { ms: r.latencyMs }) : '—'}
          </span>
        </Tooltip>
      ),
    },
    {
      title: t('pages.xray.psiphon.scanColExit'),
      key: 'exit',
      render: (_: unknown, r) => (r.ip ? `${r.ip} (${r.country ?? '?'})` : '—'),
    },
  ];

  return (
    <>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t('pages.xray.psiphon.scanHint')}
      </Typography.Paragraph>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        {running ? (
          <Button danger icon={<StopOutlined aria-hidden />} loading={busy} onClick={onCancel}>
            {t('pages.xray.psiphon.scanCancelButton')}
          </Button>
        ) : (
          <Button
            type="primary"
            icon={<ScanOutlined aria-hidden />}
            loading={busy}
            onClick={onStart}
          >
            {t('pages.xray.psiphon.scanButton')}
          </Button>
        )}
        {running &&
          (scan.discovering ? (
            <span>{t('pages.xray.psiphon.scanDiscovering')}</span>
          ) : (
            <span>{t('pages.xray.psiphon.scanProgress', { done, total: scan.total })}</span>
          ))}
      </div>
      {running && !scan.discovering && scan.total > 0 && (
        <Progress
          size="small"
          showInfo={false}
          percent={Math.round((done / scan.total) * 100)}
          style={{ marginTop: 8 }}
        />
      )}
      {running && scan.current.length > 0 && (
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
          {t('pages.xray.psiphon.scanCurrent', { regions: scan.current.join(', ') })}
        </Typography.Paragraph>
      )}
      {scan?.error && <Alert style={{ marginTop: 8 }} type="error" showIcon title={scan.error} />}
      {!running && scan?.cancelled && (
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
          {t('pages.xray.psiphon.scanCancelled')}
        </Typography.Paragraph>
      )}
      {rows.length > 0 && (
        <>
          {!running && !!scan?.finishedAt && (
            <Typography.Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
              {t('pages.xray.psiphon.scanLast', {
                time: new Date(scan.finishedAt * 1000).toLocaleString(),
              })}
            </Typography.Paragraph>
          )}
          <Table<PsiphonScanResult>
            style={{ marginTop: 8 }}
            size="small"
            rowKey="region"
            pagination={false}
            columns={columns}
            dataSource={rows}
            scroll={{ x: 'max-content' }}
          />
        </>
      )}
    </>
  );
}
