import { useCallback, useRef, useState } from 'react';

import { HttpUtil } from '@/utils';
import { coerceInboundJsonField } from '@/models/dbinbound';

export interface PathRouteRow {
  rowKey: string;
  childId: number | null;
  path: string;
}

interface PathTargetOption {
  label: string;
  value: number;
}

// XHTTP/WS are the only transports with a path of their own to route on --
// raw TCP/gRPC/etc. carry no HTTP framing for a path-based picker to read
// (see the design note this mirrors: internal/frontproxy's own path routing
// only ever dials a loopback XHTTP/WS backend).
const ROUTABLE_NETWORKS = new Set(['xhttp', 'ws']);

// Self-contained, unlike useInboundFallbacks: this settings tab has no
// parent inbound-form context handing it an already-fetched inbound list,
// so it fetches its own -- the *full* list (not /list/slim), since the slim
// projection deliberately omits streamSettings and this picker needs
// streamSettings.network to filter down to XHTTP/WS.
export function useFrontProxyPathRoutes() {
  const rowKeyRef = useRef(0);
  const [routes, setRoutes] = useState<PathRouteRow[]>([]);
  const [pathTargetOptions, setPathTargetOptions] = useState<PathTargetOption[]>([]);

  const loadInboundOptions = useCallback(async () => {
    const msg = await HttpUtil.get('/panel/api/inbounds/list', undefined, { silent: true });
    if (!msg?.success || !Array.isArray(msg.obj)) {
      setPathTargetOptions([]);
      return;
    }
    const options = (msg.obj as Record<string, unknown>[])
      .filter((ib) => {
        const stream = coerceInboundJsonField(ib.streamSettings);
        return ROUTABLE_NETWORKS.has(typeof stream.network === 'string' ? stream.network : '');
      })
      .map((ib) => ({
        label: `${(ib.remark as string) || `#${ib.id}`} · ${ib.protocol}:${ib.port}`,
        value: ib.id as number,
      }));
    setPathTargetOptions(options);
  }, []);

  const loadRoutes = useCallback(async () => {
    const msg = await HttpUtil.get('/panel/api/xray/frontproxy/pathRoutes', undefined, {
      silent: true,
    });
    if (!msg?.success || !Array.isArray(msg.obj)) {
      setRoutes([]);
      return;
    }
    setRoutes(
      (msg.obj as { childId: number; path: string }[]).map((r) => ({
        rowKey: `pr-${++rowKeyRef.current}`,
        childId: r.childId && r.childId > 0 ? r.childId : null,
        path: r.path || '',
      })),
    );
  }, []);

  // Returns null on success, or an error string -- never both undefined, so
  // the caller never has to guess which one an empty response meant.
  const saveRoutes = useCallback(async (): Promise<string | null> => {
    const payload = {
      routes: routes
        .filter((r) => r.childId && r.path.trim())
        .map((r, i) => ({ childId: r.childId, path: r.path.trim(), sortOrder: i })),
    };
    const msg = await HttpUtil.post('/panel/api/xray/frontproxy/pathRoutes', payload, {
      headers: { 'Content-Type': 'application/json' },
    });
    if (msg?.success) return null;
    return msg?.msg || 'failed';
  }, [routes]);

  const addRoute = useCallback(() => {
    setRoutes((prev) => [
      ...prev,
      { rowKey: `pr-${++rowKeyRef.current}`, childId: null, path: '' },
    ]);
  }, []);

  const updateRoute = useCallback((rowKey: string, patch: Partial<PathRouteRow>) => {
    setRoutes((prev) => prev.map((r) => (r.rowKey === rowKey ? { ...r, ...patch } : r)));
  }, []);

  const removeRoute = useCallback((idx: number) => {
    setRoutes((prev) => prev.filter((_, i) => i !== idx));
  }, []);

  const moveRoute = useCallback((idx: number, direction: -1 | 1) => {
    setRoutes((prev) => {
      const target = idx + direction;
      if (target < 0 || target >= prev.length) return prev;
      const next = prev.slice();
      [next[idx], next[target]] = [next[target], next[idx]];
      return next;
    });
  }, []);

  return {
    routes,
    pathTargetOptions,
    loadInboundOptions,
    loadRoutes,
    saveRoutes,
    addRoute,
    updateRoute,
    removeRoute,
    moveRoute,
  };
}
