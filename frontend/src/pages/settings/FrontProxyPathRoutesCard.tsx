import { useTranslation } from 'react-i18next';
import { Button, Card, Empty, Input, Select, Space } from 'antd';
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  PlusOutlined,
} from '@ant-design/icons';

import type { PathRouteRow } from './useFrontProxyPathRoutes';

interface FrontProxyPathRoutesCardProps {
  routes: PathRouteRow[];
  pathTargetOptions: { label: string; value: number }[];
  addRoute: () => void;
  updateRoute: (rowKey: string, patch: Partial<PathRouteRow>) => void;
  removeRoute: (idx: number) => void;
  moveRoute: (idx: number, direction: -1 | 1) => void;
}

// Mirrors FallbacksCard.tsx's shape (same Card-per-row, Up/Down/Delete
// pattern), simplified to just (inbound, path) -- SNI/ALPN/Dest/xver are
// REALITY-fallback-specific fields this feature has no use for.
export default function FrontProxyPathRoutesCard({
  routes,
  pathTargetOptions,
  addRoute,
  updateRoute,
  removeRoute,
  moveRoute,
}: FrontProxyPathRoutesCardProps) {
  const { t } = useTranslation();

  return (
    <Card
      size="small"
      className="mt-12"
      title={t('pages.settings.frontProxy.pathRoutes.title') || 'Path routing'}
      extra={
        <Button type="primary" ghost size="small" icon={<PlusOutlined />} onClick={addRoute}>
          {t('pages.settings.frontProxy.pathRoutes.add') || 'Add route'}
        </Button>
      }
    >
      {routes.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          styles={{ image: { height: 36 } }}
          description={t('pages.settings.frontProxy.pathRoutes.empty') || 'No path routes yet'}
          style={{ margin: '4px 0 12px' }}
        />
      ) : (
        routes.map((record, idx) => (
          <Card
            key={record.rowKey}
            type="inner"
            size="small"
            style={{ marginBottom: 8 }}
            styles={{ body: { padding: 12 } }}
          >
            <Space.Compact block>
              <Select
                aria-label={t('pages.settings.frontProxy.pathRoutes.pickInbound')}
                value={record.childId}
                options={pathTargetOptions}
                placeholder={
                  t('pages.settings.frontProxy.pathRoutes.pickInbound') ||
                  'Pick an XHTTP/WS inbound'
                }
                allowClear
                showSearch={{
                  filterOption: (input, option) =>
                    ((option?.label as string) || '').toLowerCase().includes(input.toLowerCase()),
                }}
                style={{ width: '45%' }}
                onChange={(v) => updateRoute(record.rowKey, { childId: v ?? null })}
              />
              <Input
                prefix="/"
                placeholder={
                  t('pages.settings.frontProxy.pathRoutes.pathPlaceholder') || 'cdn-path'
                }
                value={record.path}
                onChange={(e) => updateRoute(record.rowKey, { path: e.target.value })}
              />
              <Button
                aria-label={t('pages.inbounds.form.moveUp')}
                disabled={idx === 0}
                onClick={() => moveRoute(idx, -1)}
                title={t('pages.inbounds.form.moveUp')}
                icon={<ArrowUpOutlined />}
              />
              <Button
                aria-label={t('pages.inbounds.form.moveDown')}
                disabled={idx === routes.length - 1}
                onClick={() => moveRoute(idx, 1)}
                title={t('pages.inbounds.form.moveDown')}
                icon={<ArrowDownOutlined />}
              />
              <Button
                aria-label={t('delete')}
                danger
                onClick={() => removeRoute(idx)}
                icon={<DeleteOutlined />}
              />
            </Space.Compact>
          </Card>
        ))
      )}
    </Card>
  );
}
