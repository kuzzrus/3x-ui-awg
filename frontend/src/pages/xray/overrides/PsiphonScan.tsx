import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Progress, Table, Tag, Tooltip, Typography } from 'antd';
import type { TableColumnsType } from 'antd';
import { ScanOutlined, StopOutlined } from '@ant-design/icons';

import {
  seenDate,
  sortScanResults,
  type PsiphonRegion,
  type PsiphonScanResult,
  type PsiphonScanStatus,
  type PsiphonSeenRegion,
} from './psiphon-regions';

interface PsiphonScanProps {
  scan: PsiphonScanStatus | null;
  regions: PsiphonRegion[];
  busy: boolean;
  /** True asks for every region of the picker, and false for those a tunnel said have servers. */
  onStart: (all: boolean) => void;
  onCancel: () => void;
}

// A table longer than this is kept in a box that scrolls, with its header in view: a scan of every region lists over 200.
const TALL_TABLE = 12;

export default function PsiphonScan({ scan, regions, busy, onStart, onCancel }: PsiphonScanProps) {
  const { t } = useTranslation();
  const running = !!scan?.running;
  const results = scan?.results;
  const rows = useMemo(() => sortScanResults(results ?? []), [results]);
  const done = results?.length ?? 0;
  const nameOf = (code: string) => regions.find((r) => r.code === code)?.name ?? code;
  // The regions that connected in an earlier scan: once this one has ended, those it did not look at are listed, and in the table
  // those it looked at and did not connect get a tag with the day.
  const seen = scan?.seen;
  const earlier = useMemo(() => new Map((seen ?? []).map((s) => [s.region, s])), [seen]);
  const onlyEarlier = useMemo(() => {
    const scanned = new Set((results ?? []).map((r) => r.region));
    return (seen ?? []).filter((s) => !scanned.has(s.region));
  }, [seen, results]);
  // The tag says the day; its tooltip the moment, and what that scan measured, which is all the record keeps.
  const earlierSeen = (s: PsiphonSeenRegion) =>
    [
      new Date(s.at * 1000).toLocaleString(),
      s.connectMs
        ? t('pages.xray.psiphon.scanSeconds', { s: (s.connectMs / 1000).toFixed(1) })
        : '',
      s.latencyMs ? t('pages.xray.psiphon.probeResult', { ms: s.latencyMs }) : '',
      s.ip ? `${s.ip} (${s.country ?? '?'})` : '',
    ]
      .filter(Boolean)
      .join(' — ');

  const columns: TableColumnsType<PsiphonScanResult> = [
    {
      title: t('pages.xray.psiphon.colCountry'),
      key: 'country',
      render: (_: unknown, r) => `${nameOf(r.region)} (${r.region})`,
    },
    {
      title: t('pages.xray.psiphon.colState'),
      key: 'state',
      render: (_: unknown, r) => {
        if (r.connected) return <Tag color="green">{t('pages.xray.psiphon.countryConnected')}</Tag>;
        const before = earlier.get(r.region);
        return (
          <>
            <Tooltip title={r.error}>
              <Tag color="red">{t('pages.xray.psiphon.scanNoTunnel')}</Tag>
            </Tooltip>
            {before && (
              <Tooltip title={earlierSeen(before)}>
                <Tag color="blue">
                  {t('pages.xray.psiphon.scanSeenTag', { date: seenDate(before.at) })}
                </Tag>
              </Tooltip>
            )}
          </>
        );
      },
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
          <>
            <Button
              type="primary"
              icon={<ScanOutlined aria-hidden />}
              loading={busy}
              onClick={() => onStart(false)}
            >
              {t('pages.xray.psiphon.scanButton')}
            </Button>
            <Button loading={busy} onClick={() => onStart(true)}>
              {t('pages.xray.psiphon.scanAllButton')}
            </Button>
          </>
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
      {!running && rows.length > 0 && onlyEarlier.length > 0 && (
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
          {t('pages.xray.psiphon.scanEarlier', {
            regions: onlyEarlier.map((s) => `${nameOf(s.region)} (${s.region})`).join(', '),
          })}
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
            scroll={{ x: 'max-content', y: rows.length > TALL_TABLE ? 360 : undefined }}
          />
        </>
      )}
    </>
  );
}
