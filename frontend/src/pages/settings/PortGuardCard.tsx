import { useTranslation } from 'react-i18next';
import { Alert, Button, Card, Checkbox, Modal, Space, Table, Tag, Typography } from 'antd';
import type { TableColumnsType } from 'antd';

import { getMessage } from '@/utils/messageBus';

import { portKey, usePortGuard } from './usePortGuard';
import type { GuardPort, GuardRow } from './usePortGuard';

const mmss = (seconds: number) =>
  `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;

function PortTags({ ports }: { ports: GuardPort[] }) {
  return (
    <>
      {ports.map((p) => (
        <Tag key={portKey(p)}>{portKey(p)}</Tag>
      ))}
    </>
  );
}

// Closes ports of the host in the firewall; the engine, its 2-minute lease and the rules are internal/portguard's.
export default function PortGuardCard() {
  const { t } = useTranslation();
  const guard = usePortGuard();
  const { status, plan } = guard;
  const none = !status || guard.rows.length === 0;

  const columns: TableColumnsType<GuardRow> = [
    {
      title: t('pages.settings.frontProxy.portGuard.colClose'),
      key: 'close',
      width: 80,
      render: (_: unknown, row) => (
        <Checkbox
          aria-label={t('pages.settings.frontProxy.portGuard.closePort', { port: portKey(row) })}
          checked={guard.selected.has(row.key)}
          disabled={row.protected || !status?.available}
          onChange={(e) => guard.toggle(row.key, e.target.checked)}
        />
      ),
    },
    {
      title: t('pages.settings.frontProxy.portGuard.colPort'),
      key: 'port',
      width: 120,
      render: (_: unknown, row) => <Typography.Text code>{portKey(row)}</Typography.Text>,
    },
    {
      title: t('pages.settings.frontProxy.portGuard.colUsedBy'),
      key: 'label',
      render: (_: unknown, row) => row.label || '—',
    },
    {
      title: t('pages.settings.frontProxy.portGuard.colState'),
      key: 'state',
      render: (_: unknown, row) => (
        <>
          {row.closed && (
            <Tag color="red">{t('pages.settings.frontProxy.portGuard.tagClosed')}</Tag>
          )}
          {row.viaFrontProxy && (
            <Tag color="green">{t('pages.settings.frontProxy.portGuard.tagFrontProxy')}</Tag>
          )}
          {row.loopback && <Tag>{t('pages.settings.frontProxy.portGuard.tagLoopback')}</Tag>}
          {row.protected && (
            <Tag color="gold">{t('pages.settings.frontProxy.portGuard.tagProtected')}</Tag>
          )}
        </>
      ),
    },
  ];

  const confirm = async () => {
    if (await guard.confirm())
      getMessage().success(t('pages.settings.frontProxy.portGuard.confirmed'));
  };
  const rollback = async () => {
    if (await guard.rollback())
      getMessage().success(t('pages.settings.frontProxy.portGuard.rolledBack'));
  };

  return (
    <Card size="small" className="mt-12">
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        {status && !status.available && (
          <Alert type="warning" showIcon title={t('pages.settings.frontProxy.portGuard.noNft')} />
        )}
        {status?.pending && (
          <Alert
            type="warning"
            showIcon
            title={t('pages.settings.frontProxy.portGuard.pendingTitle', {
              time: mmss(guard.secondsLeft),
            })}
            description={t('pages.settings.frontProxy.portGuard.pendingDesc')}
            action={
              <Space>
                <Button type="primary" size="small" loading={guard.busy} onClick={confirm}>
                  {t('confirm')}
                </Button>
                <Button danger size="small" loading={guard.busy} onClick={rollback}>
                  {t('pages.settings.frontProxy.portGuard.rollback')}
                </Button>
              </Space>
            }
          />
        )}
        <Table<GuardRow>
          size="small"
          rowKey="key"
          pagination={false}
          columns={columns}
          dataSource={guard.rows}
          loading={guard.loading}
          locale={{ emptyText: t('pages.settings.frontProxy.portGuard.empty') }}
          scroll={{ x: 'max-content', y: guard.rows.length > 12 ? 360 : undefined }}
        />
        <Space wrap>
          <Button loading={guard.busy} disabled={none || !guard.dirty} onClick={guard.preview}>
            {t('pages.settings.frontProxy.portGuard.preview')}
          </Button>
          <Button
            type="primary"
            loading={guard.busy}
            disabled={none || !guard.dirty}
            onClick={guard.apply}
          >
            {t('pages.settings.frontProxy.portGuard.apply')}
          </Button>
        </Space>
        <Typography.Paragraph type="secondary" style={{ margin: 0, fontSize: 12 }}>
          {t('pages.settings.frontProxy.portGuard.recovery')}{' '}
          <Typography.Text code>x-ui setting -openClosedPorts</Typography.Text>
        </Typography.Paragraph>
      </Space>

      <Modal
        open={!!plan}
        title={t('pages.settings.frontProxy.portGuard.planTitle')}
        onCancel={guard.closePlan}
        footer={<Button onClick={guard.closePlan}>{t('close')}</Button>}
        width={640}
      >
        {plan && (
          <Space orientation="vertical" size={10} style={{ width: '100%' }}>
            {plan.close.length === 0 && plan.open.length === 0 && plan.refused.length === 0 && (
              <Typography.Text type="secondary">
                {t('pages.settings.frontProxy.portGuard.planNothing')}
              </Typography.Text>
            )}
            {plan.close.length > 0 && (
              <div>
                <Typography.Text strong>
                  {t('pages.settings.frontProxy.portGuard.planClose')}
                </Typography.Text>{' '}
                <PortTags ports={plan.close} />
              </div>
            )}
            {plan.open.length > 0 && (
              <div>
                <Typography.Text strong>
                  {t('pages.settings.frontProxy.portGuard.planOpen')}
                </Typography.Text>{' '}
                <PortTags ports={plan.open} />
              </div>
            )}
            {plan.refused.length > 0 && (
              <Alert
                type="error"
                showIcon
                title={t('pages.settings.frontProxy.portGuard.planRefused')}
                description={
                  <ul style={{ margin: 0, paddingLeft: 18 }}>
                    {plan.refused.map((r) => (
                      <li key={portKey(r.port)}>
                        {portKey(r.port)}: {r.reason}
                      </li>
                    ))}
                  </ul>
                }
              />
            )}
            {plan.lease && (
              <Alert
                type="warning"
                showIcon
                title={t('pages.settings.frontProxy.portGuard.planLease', {
                  seconds: plan.windowSeconds,
                })}
              />
            )}
            <Typography.Text strong>
              {t('pages.settings.frontProxy.portGuard.planRules')}
            </Typography.Text>
            <pre style={{ maxHeight: 240, overflow: 'auto', margin: 0, fontSize: 12 }}>
              {plan.ruleset}
            </pre>
          </Space>
        )}
      </Modal>
    </Card>
  );
}
