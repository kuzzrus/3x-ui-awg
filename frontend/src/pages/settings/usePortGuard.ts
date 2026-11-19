import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { HttpUtil } from '@/utils';
import { keys } from '@/api/queryKeys';

export interface GuardPort {
  port: number;
  proto: 'tcp' | 'udp';
}

export interface GuardCandidate extends GuardPort {
  label: string;
  inboundId?: number;
  viaFrontProxy: boolean;
  loopback: boolean;
  closed: boolean;
  protected: boolean;
}

export interface GuardStatus {
  available: boolean;
  closed: GuardPort[];
  pending: { deadlineMs: number; previous: GuardPort[] } | null;
  protected: GuardPort[];
  candidates: GuardCandidate[];
  leaseSeconds: number;
}

export interface GuardPlan {
  closed: GuardPort[];
  close: GuardPort[];
  open: GuardPort[];
  refused: { port: GuardPort; reason: string }[];
  lease: boolean;
  windowSeconds: number;
  ruleset: string;
}

// One row of the table: a port some service listens on, or a port that is closed though nothing lists it any more.
export interface GuardRow extends GuardPort {
  key: string;
  label: string;
  viaFrontProxy: boolean;
  loopback: boolean;
  protected: boolean;
  closed: boolean;
}

const BASE = '/panel/api/xray/portguard';
const JSON_BODY = { headers: { 'Content-Type': 'application/json' } };
// While a change waits to be confirmed the server may roll it back, so the page looks again every few seconds.
const POLL_MS = 3000;

export const portKey = (p: GuardPort) => `${p.port}/${p.proto}`;

// Whatever answers is made a whole status: the page reads its lists without asking whether they are there.
function wholeStatus(obj: Partial<GuardStatus>): GuardStatus {
  return {
    available: !!obj.available,
    closed: obj.closed ?? [],
    pending: obj.pending ?? null,
    protected: obj.protected ?? [],
    candidates: obj.candidates ?? [],
    leaseSeconds: obj.leaseSeconds ?? 120,
  };
}

async function fetchStatus(): Promise<GuardStatus> {
  const msg = await HttpUtil.get<GuardStatus>(BASE, undefined, { silent: true });
  if (!msg?.success || !msg.obj) throw new Error(msg?.msg || 'Failed to fetch the closed ports');
  return wholeStatus(msg.obj);
}

// The rows to show: the ports of the services, and the closed ones that no service lists any more, so that they can be opened.
function rowsOf(status: GuardStatus): GuardRow[] {
  const out: GuardRow[] = status.candidates.map((c) => ({
    port: c.port,
    proto: c.proto,
    key: portKey(c),
    label: c.label,
    viaFrontProxy: c.viaFrontProxy,
    loopback: c.loopback,
    protected: c.protected,
    closed: c.closed,
  }));
  for (const p of status.closed) {
    if (!out.some((r) => r.key === portKey(p))) {
      out.push({
        ...p,
        key: portKey(p),
        label: '',
        viaFrontProxy: false,
        loopback: false,
        protected: false,
        closed: true,
      });
    }
  }
  return out.sort((a, b) => a.port - b.port || a.proto.localeCompare(b.proto));
}

export function usePortGuard() {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: keys.xray.portGuard(),
    queryFn: fetchStatus,
    refetchInterval: (q) => (q.state.data?.pending ? POLL_MS : false),
  });
  const status = query.data ?? null;

  // The admin's edit of the selection, kept with the closed set it was made against: when the server's set moves (a rollback by
  // the timer, say) the edit is stale and the selection is the truth again.
  const closedKey = status ? status.closed.map(portKey).sort().join(',') : '';
  const [edit, setEdit] = useState<{ base: string; keys: Set<string> } | null>(null);
  const selected = useMemo(
    () =>
      edit && edit.base === closedKey ? edit.keys : new Set(status?.closed.map(portKey) ?? []),
    [edit, closedKey, status],
  );

  const rows = useMemo(() => (status ? rowsOf(status) : []), [status]);
  const desired = useMemo<GuardPort[]>(
    () => rows.filter((r) => selected.has(r.key)).map((r) => ({ port: r.port, proto: r.proto })),
    [rows, selected],
  );
  const dirty = status ? [...selected].sort().join(',') !== closedKey : false;

  const toggle = (key: string, on: boolean) => {
    const next = new Set(selected);
    if (on) next.add(key);
    else next.delete(key);
    setEdit({ base: closedKey, keys: next });
  };

  const [plan, setPlan] = useState<GuardPlan | null>(null);
  const previewMutation = useMutation({
    mutationFn: async () => {
      const msg = await HttpUtil.post<GuardPlan>(
        BASE + '/plan',
        { ports: desired },
        { ...JSON_BODY, silent: true },
      );
      if (!msg?.success || !msg.obj) throw new Error(msg?.msg || 'Failed to plan');
      return msg.obj;
    },
    onSuccess: setPlan,
  });

  // The answer of an action is the new status, which takes the place of the one on the page.
  const action = useMutation({
    mutationFn: async ({ path, body }: { path: string; body?: unknown }) => {
      const msg = await HttpUtil.post<GuardStatus>(BASE + path, body, body ? JSON_BODY : {});
      if (!msg?.success || !msg.obj) throw new Error(msg?.msg || 'Failed');
      return wholeStatus(msg.obj);
    },
    onSuccess: (next) => {
      queryClient.setQueryData(keys.xray.portGuard(), next);
      setEdit(null);
    },
  });
  const run = async (path: string, body?: unknown) => {
    try {
      await action.mutateAsync({ path, body });
      return true;
    } catch {
      return false; // HttpUtil has already told the admin what went wrong
    }
  };

  // The countdown: a tick a second while a change waits, from the moment the status that said so came.
  const pending = status?.pending ?? null;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!pending) return undefined;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(tick);
  }, [pending]);
  const secondsLeft = pending
    ? Math.max(0, Math.ceil((pending.deadlineMs - Math.max(now, query.dataUpdatedAt)) / 1000))
    : 0;

  return {
    status,
    loading: query.isPending,
    rows,
    selected,
    dirty,
    busy: action.isPending || previewMutation.isPending,
    plan,
    secondsLeft,
    toggle,
    preview: () => previewMutation.mutate(),
    apply: () => run('/apply', { ports: desired }),
    confirm: () => run('/confirm'),
    rollback: () => run('/rollback'),
    closePlan: () => setPlan(null),
  };
}
