/* oxlint-disable react/set-state-in-effect -- same fetch-on-open shape as PsiphonModal.tsx: the state is set
   when a response lands, at modal-open frequency and on a slow poll while a tunnel connects, not on a hot path. */
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Modal, Select, Switch, Table, Tag, Tooltip, Typography, message } from 'antd';
import type { TableColumnsType } from 'antd';
import {
  DeleteOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  SyncOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';

import type { XraySettingsValue } from '@/hooks/useXraySetting';
import { syncObservatories } from '@/pages/xray/balancers/balancer-helpers';
import { loopbackTagFor } from '@/pages/xray/balancers/balancer-loopback';
import DeletionImpactList from '@/pages/xray/DeletionImpactList';
import {
  applyBalancerDeletion,
  applyOutboundDeletion,
  planBalancerDeletion,
  planOutboundDeletion,
} from '@/pages/xray/reference-cleanup';
import type { DeletionImpact } from '@/pages/xray/reference-cleanup';
import { HttpUtil } from '@/utils';

import { regionPickerOptions, regionsKnown, type PsiphonRegion } from './psiphon-regions';

interface TunnelStatus {
  connected: boolean;
  tunnelCount: number;
  serverRegion?: string;
  clientRegion?: string;
}

interface Country {
  region: string;
  port: number;
  enabled: boolean;
  running: boolean;
  tunnel: TunnelStatus;
  lastLog?: string;
  error?: string;
}

interface CountriesView {
  max: number;
  countries: Country[];
}

interface ProbeResult {
  region: string;
  latencyMs: number;
  ip?: string;
  country?: string;
  error?: string;
}

interface PsiphonCountriesProps {
  open: boolean;
  regions: PsiphonRegion[];
  templateSettings: XraySettingsValue | null;
  onMutateTemplate: (mutator: (tt: XraySettingsValue) => void) => void;
  // Called when one more tunnel has connected: it has told which regions have servers, so the window reads that list again.
  onTunnelConnected: () => void;
}

const COUNTRIES_URL = '/panel/api/xray/psiphon/countries';

// Every country outbound is called psiphon-<CC>, so one prefix selects them all: for the balancer below, and
// apart from the default outbound, which is plain "psiphon".
export const COUNTRY_PREFIX = 'psiphon-';
export const BALANCER_TAG = 'psiphon-balancer';
// How often the list is read again while a tunnel is still coming up.
const POLL_MS = 3000;

const outboundTag = (region: string) => `${COUNTRY_PREFIX}${region}`;

const noImpact = (): DeletionImpact => ({
  rules: [],
  balancers: [],
  observatory: false,
  burst: false,
  chains: [],
});

function outboundIndex(tt: XraySettingsValue | null, region: string): number {
  return tt?.outbounds?.findIndex((o) => o?.tag === outboundTag(region)) ?? -1;
}

function outboundPort(tt: XraySettingsValue | null, region: string): number | null {
  const outbound = tt?.outbounds?.find((o) => o?.tag === outboundTag(region)) as
    | { settings?: { servers?: { port?: number }[] } }
    | undefined;
  if (!outbound) return null;
  return outbound.settings?.servers?.[0]?.port ?? 0;
}

export default function PsiphonCountries({
  open,
  regions,
  templateSettings,
  onMutateTemplate,
  onTunnelConnected,
}: PsiphonCountriesProps) {
  const { t } = useTranslation();
  const [messageApi, messageContextHolder] = message.useMessage();
  const [modal, modalContextHolder] = Modal.useModal();
  const [view, setView] = useState<CountriesView | null>(null);
  const [probes, setProbes] = useState<Record<string, ProbeResult>>({});
  const [busy, setBusy] = useState('');
  const [probing, setProbing] = useState<string[]>([]);
  const [pick, setPick] = useState<string | undefined>();

  // Counts the actions, so that an answer which left before one finished is not taken for the newer list.
  const generation = useRef(0);

  const refresh = useCallback(async () => {
    const asked = generation.current;
    const msg = await HttpUtil.get<CountriesView>(COUNTRIES_URL);
    if (asked === generation.current && msg?.success && msg.obj) setView(msg.obj);
  }, []);

  useEffect(() => {
    if (open) void refresh();
  }, [open, refresh]);

  // A tunnel takes seconds to come up and sometimes much longer, and one that is down is started again by the
  // supervisor, so the list is read again for as long as one is connecting or waiting for that.
  const settling = !!view?.countries.some(
    (c) => (c.running && !c.tunnel.connected) || (c.enabled && !c.running),
  );
  useEffect(() => {
    if (!open || !settling) return;
    const timer = setInterval(() => void refresh(), POLL_MS);
    return () => clearInterval(timer);
  }, [open, settling, refresh]);

  // A tunnel that has just connected has told which regions have servers; the first list read says nothing of that, since
  // the tunnels in it were up before.
  const connectedCount = (view?.countries ?? []).filter(
    (c) => c.running && c.tunnel.connected,
  ).length;
  const connectedBefore = useRef<number | null>(null);
  useEffect(() => {
    if (!view) return;
    if (connectedBefore.current !== null && connectedCount > connectedBefore.current) {
      onTunnelConnected();
    }
    connectedBefore.current = connectedCount;
  }, [view, connectedCount, onTunnelConnected]);

  // One action on the list. The answer carries the list also when the action failed, so a country that did not
  // start shows why in its row.
  async function act(key: string, action: string, region?: string): Promise<boolean> {
    setBusy(key);
    generation.current++;
    try {
      const msg = await HttpUtil.post<CountriesView>(
        `${COUNTRIES_URL}/${action}`,
        region ? { region } : {},
      );
      if (msg?.obj && 'countries' in msg.obj) setView(msg.obj);
      if (!msg?.success) {
        messageApi.error(msg?.msg || t('pages.xray.psiphon.countryActionFailed'));
        return false;
      }
      return true;
    } finally {
      generation.current++;
      setBusy('');
    }
  }

  async function probe(region?: string) {
    const targets = region
      ? [region]
      : (view?.countries ?? []).filter((c) => c.running).map((c) => c.region);
    if (targets.length === 0) return;
    setProbing((prev) => [...new Set([...prev, ...targets])]);
    try {
      const msg = await HttpUtil.post<ProbeResult[]>(
        `${COUNTRIES_URL}/probe`,
        region ? { region } : {},
      );
      if (msg?.success && Array.isArray(msg.obj)) {
        // The answer also names the countries that are down, which this call did not ask about.
        const rows = msg.obj.filter((r) => targets.includes(r.region));
        setProbes((prev) => ({ ...prev, ...Object.fromEntries(rows.map((r) => [r.region, r])) }));
      } else {
        messageApi.error(msg?.msg || t('pages.xray.psiphon.countryActionFailed'));
      }
    } finally {
      setProbing((prev) => prev.filter((r) => !targets.includes(r)));
    }
  }

  async function addCountry() {
    if (!pick) return;
    if (await act('add', 'add', pick)) {
      const region = pick;
      setPick(undefined);
      // A country that was only added is of no use stopped: start it at once, and let its row say why when it cannot.
      await act(region, 'start', region);
    }
  }

  async function removeCountry(region: string) {
    if (await act(region, 'remove', region)) {
      setProbes((prev) => {
        const next = { ...prev };
        delete next[region];
        return next;
      });
      // Its outbound would point at a port nothing listens on.
      dropOutbound(region);
    }
  }

  function dropOutbound(region: string) {
    onMutateTemplate((tt) => {
      const idx = outboundIndex(tt, region);
      if (idx >= 0) applyOutboundDeletion(tt, idx);
    });
  }

  function impactOfDropping(region: string): DeletionImpact {
    const idx = outboundIndex(templateSettings, region);
    return idx >= 0 && templateSettings ? planOutboundDeletion(templateSettings, idx) : noImpact();
  }

  function confirmRemove(country: Country) {
    modal.confirm({
      title: t('pages.xray.psiphon.removeCountryConfirm', { region: country.region }),
      content: <DeletionImpactList impact={impactOfDropping(country.region)} />,
      okText: t('delete'),
      okType: 'danger',
      cancelText: t('cancel'),
      onOk: () => removeCountry(country.region),
    });
  }

  function setCountryOutbound(country: Country, on: boolean) {
    if (!on) {
      // Asks first like both tabs: switching back on adds only the bare outbound, so what the dialog lists does not come back.
      modal.confirm({
        title: t('pages.xray.psiphon.outboundOffConfirm', { region: country.region }),
        content: <DeletionImpactList impact={impactOfDropping(country.region)} />,
        okText: t('delete'),
        okType: 'danger',
        cancelText: t('cancel'),
        onOk: () => dropOutbound(country.region),
      });
      return;
    }
    onMutateTemplate((tt) => {
      if (outboundIndex(tt, country.region) >= 0) return;
      if (!Array.isArray(tt.outbounds)) tt.outbounds = [];
      tt.outbounds.push({
        tag: outboundTag(country.region),
        protocol: 'socks',
        settings: { servers: [{ address: '127.0.0.1', port: country.port }] },
      } as never);
    });
  }

  // An outbound that was edited, or made before the country was moved, may point at another port.
  function fixOutboundPort(country: Country) {
    const tag = outboundTag(country.region);
    onMutateTemplate((tt) => {
      const outbound = tt.outbounds?.find((o) => o?.tag === tag) as
        | { settings?: { servers?: { address?: string; port?: number }[] } }
        | undefined;
      if (!outbound) return;
      outbound.settings = {
        ...outbound.settings,
        servers: [{ address: '127.0.0.1', port: country.port }],
      };
    });
  }

  const balancerIndex =
    templateSettings?.routing?.balancers?.findIndex((b) => b?.tag === BALANCER_TAG) ?? -1;

  function addBalancer() {
    onMutateTemplate((tt) => {
      if (!tt.routing) tt.routing = { rules: [], balancers: [] };
      if (!Array.isArray(tt.routing.balancers)) tt.routing.balancers = [];
      if (tt.routing.balancers.some((b) => b?.tag === BALANCER_TAG)) return;
      tt.routing.balancers.push({
        tag: BALANCER_TAG,
        selector: [COUNTRY_PREFIX],
        strategy: { type: 'leastPing' },
      });
      // leastPing reads the latency a health monitor measures; this adds the monitor it needs.
      syncObservatories(tt);
    });
  }

  // The same refusal and preview as the Balancers tab: a balancer that another one falls back to is not deleted.
  function confirmRemoveBalancer() {
    if (!templateSettings || balancerIndex < 0) return;
    const fallbackTag = loopbackTagFor(BALANCER_TAG);
    const dependents = (templateSettings.routing?.balancers ?? [])
      .filter((b) => b.tag !== BALANCER_TAG && b.fallbackTag === fallbackTag)
      .map((b) => b.tag);
    if (dependents.length > 0) {
      messageApi.error(
        t('pages.xray.balancer.balancerDeleteInUse', { names: dependents.join(', ') }),
      );
      return;
    }
    modal.confirm({
      title: t('pages.xray.psiphon.balancerRemoveConfirm'),
      content: (
        <DeletionImpactList impact={planBalancerDeletion(templateSettings, balancerIndex)} />
      ),
      okText: t('delete'),
      okType: 'danger',
      cancelText: t('cancel'),
      onOk: () =>
        onMutateTemplate((tt) => {
          const idx = tt.routing?.balancers?.findIndex((b) => b?.tag === BALANCER_TAG) ?? -1;
          if (idx >= 0) applyBalancerDeletion(tt, idx);
        }),
    });
  }

  const added = new Set((view?.countries ?? []).map((c) => c.region));
  const options = regionPickerOptions(
    regions.filter((r) => !added.has(r.code)),
    t('pages.xray.psiphon.regionNoServers'),
  );
  const nameOf = (code: string) => regions.find((r) => r.code === code)?.name ?? code;
  const full = !!view && view.countries.length >= view.max;
  const anyRunning = !!view?.countries.some((c) => c.running);

  function stateTag(c: Country) {
    if (c.running && c.tunnel.connected) {
      const where = c.tunnel.serverRegion ? ` (${c.tunnel.serverRegion})` : '';
      return (
        <Tag color="green">
          {t('pages.xray.psiphon.countryConnected')}
          {where}
        </Tag>
      );
    }
    if (c.running) {
      return (
        <Tag icon={<SyncOutlined spin aria-hidden />} color="processing">
          {t('pages.xray.psiphon.countryConnecting')}
        </Tag>
      );
    }
    const why = c.error || c.lastLog;
    // One that should be running is started again by the supervisor, whatever stopped it; a start that the
    // operator saw fail is the only one nobody retries.
    if (c.enabled) {
      return (
        <Tooltip title={why}>
          <Tag color="orange">{t('pages.xray.psiphon.countryRestarting')}</Tag>
        </Tooltip>
      );
    }
    if (c.error) {
      return (
        <Tooltip title={why}>
          <Tag color="red">{t('pages.xray.psiphon.countryFailed')}</Tag>
        </Tooltip>
      );
    }
    return <Tag>{t('pages.xray.psiphon.countryStopped')}</Tag>;
  }

  const columns: TableColumnsType<Country> = [
    {
      title: t('pages.xray.psiphon.colCountry'),
      key: 'country',
      render: (_: unknown, c) => `${nameOf(c.region)} (${c.region})`,
    },
    { title: t('pages.xray.psiphon.colPort'), dataIndex: 'port', key: 'port' },
    {
      title: t('pages.xray.psiphon.colState'),
      key: 'state',
      render: (_: unknown, c) => stateTag(c),
    },
    {
      title: t('pages.xray.psiphon.colProbe'),
      key: 'probe',
      render: (_: unknown, c) => {
        const result = probes[c.region];
        if (!result) return '—';
        if (result.error && !result.latencyMs) {
          return (
            <Tooltip title={result.error}>
              <Tag color="red">{t('pages.xray.psiphon.probeFailed')}</Tag>
            </Tooltip>
          );
        }
        return (
          <Tooltip title={result.error}>
            <span>
              {t('pages.xray.psiphon.probeResult', { ms: result.latencyMs })}
              {result.ip ? ` · ${result.ip} (${result.country ?? '?'})` : ''}
            </span>
          </Tooltip>
        );
      },
    },
    {
      title: t('pages.xray.psiphon.colOutbound'),
      key: 'outbound',
      render: (_: unknown, c) => {
        const port = outboundPort(templateSettings, c.region);
        return (
          <>
            <Switch
              size="small"
              aria-label={t('pages.xray.psiphon.countryOutboundSwitch', { region: c.region })}
              checked={port !== null}
              onChange={(on) => setCountryOutbound(c, on)}
            />
            {port !== null && port !== c.port && (
              <Tooltip
                title={t('pages.xray.psiphon.outboundPortMismatch', { port, expected: c.port })}
              >
                <Button size="small" type="link" danger onClick={() => fixOutboundPort(c)}>
                  {t('pages.xray.psiphon.outboundPortFix')}
                </Button>
              </Tooltip>
            )}
          </>
        );
      },
    },
    {
      title: '',
      key: 'actions',
      align: 'end',
      render: (_: unknown, c) => (
        <span style={{ whiteSpace: 'nowrap' }}>
          <Tooltip title={t('pages.xray.psiphon.probeButton')}>
            <Button
              size="small"
              aria-label={t('pages.xray.psiphon.probeButton')}
              icon={<ThunderboltOutlined aria-hidden />}
              disabled={!c.running}
              loading={probing.includes(c.region)}
              onClick={() => void probe(c.region)}
            />
          </Tooltip>{' '}
          {c.running || c.enabled ? (
            <Tooltip title={t('pages.xray.psiphon.stopButton')}>
              <Button
                size="small"
                danger
                aria-label={t('pages.xray.psiphon.stopButton')}
                icon={<PauseCircleOutlined aria-hidden />}
                loading={busy === c.region}
                disabled={!!busy && busy !== c.region}
                onClick={() => void act(c.region, 'stop', c.region)}
              />
            </Tooltip>
          ) : (
            <Tooltip title={t('pages.xray.psiphon.startButton')}>
              <Button
                size="small"
                type="primary"
                aria-label={t('pages.xray.psiphon.startButton')}
                icon={<PlayCircleOutlined aria-hidden />}
                loading={busy === c.region}
                disabled={!!busy && busy !== c.region}
                onClick={() => void act(c.region, 'start', c.region)}
              />
            </Tooltip>
          )}{' '}
          <Button
            size="small"
            danger
            aria-label={t('pages.xray.psiphon.removeCountryButton')}
            icon={<DeleteOutlined aria-hidden />}
            disabled={!!busy}
            onClick={() => confirmRemove(c)}
          />
        </span>
      ),
    },
  ];

  return (
    <>
      {messageContextHolder}
      {modalContextHolder}
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t('pages.xray.psiphon.countriesHint')}
      </Typography.Paragraph>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <Select
          id="psiphon-add-country"
          showSearch
          allowClear
          aria-label={t('pages.xray.psiphon.addCountryPlaceholder')}
          placeholder={t('pages.xray.psiphon.addCountryPlaceholder')}
          style={{ minWidth: 220 }}
          value={pick}
          options={options}
          optionFilterProp="label"
          disabled={full}
          onChange={(v) => setPick(v)}
        />
        <Button
          type="primary"
          icon={<PlusOutlined aria-hidden />}
          disabled={!pick || full || (!!busy && busy !== 'add')}
          loading={busy === 'add'}
          onClick={() => void addCountry()}
        >
          {t('pages.xray.psiphon.addCountryButton')}
        </Button>
        {view && (
          <span style={{ fontSize: 12, color: '#888' }}>
            {t('pages.xray.psiphon.countriesCount', { n: view.countries.length, max: view.max })}
          </span>
        )}
      </div>
      {regions.length > 0 && !regionsKnown(regions) && (
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
          {t('pages.xray.psiphon.regionsUnknownHint')}
        </Typography.Paragraph>
      )}

      <Table<Country>
        style={{ marginTop: 12 }}
        size="small"
        rowKey="region"
        pagination={false}
        loading={!view}
        columns={columns}
        dataSource={view?.countries ?? []}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: t('pages.xray.psiphon.countriesEmpty') }}
      />

      {!!view?.countries.length && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 12 }}>
          <Button
            icon={<PlayCircleOutlined aria-hidden />}
            loading={busy === 'all-start'}
            disabled={!!busy && busy !== 'all-start'}
            onClick={() => void act('all-start', 'start')}
          >
            {t('pages.xray.psiphon.startAllButton')}
          </Button>
          <Button
            icon={<PauseCircleOutlined aria-hidden />}
            loading={busy === 'all-stop'}
            disabled={!!busy && busy !== 'all-stop'}
            onClick={() => void act('all-stop', 'stop')}
          >
            {t('pages.xray.psiphon.stopAllButton')}
          </Button>
          <Button
            icon={<ThunderboltOutlined aria-hidden />}
            disabled={!anyRunning}
            loading={probing.length > 0}
            onClick={() => void probe()}
          >
            {t('pages.xray.psiphon.probeAllButton')}
          </Button>
        </div>
      )}

      <Typography.Title level={5} style={{ marginTop: 16 }}>
        {t('pages.xray.psiphon.balancerTitle')}
      </Typography.Title>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t('pages.xray.psiphon.balancerHint')}
      </Typography.Paragraph>
      {balancerIndex >= 0 ? (
        <>
          <Tag color="green">{t('enabled')}</Tag>
          <Button type="primary" danger className="ml-8" onClick={confirmRemoveBalancer}>
            {t('delete')}
          </Button>
        </>
      ) : (
        <>
          <Tag color="orange">{t('disabled')}</Tag>
          <Button type="primary" className="ml-8" onClick={addBalancer}>
            {t('pages.xray.psiphon.balancerAdd')}
          </Button>
        </>
      )}
      <Typography.Paragraph type="secondary" style={{ marginTop: 8, fontSize: 12 }}>
        {t('pages.xray.psiphon.countriesSaveHint')}
      </Typography.Paragraph>
    </>
  );
}
