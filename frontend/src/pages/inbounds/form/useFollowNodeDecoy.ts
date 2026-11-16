import { useEffect, useRef } from 'react';
import type { UseFormReturn } from 'react-hook-form';

import type { NodeDecoyTarget } from '@/lib/xray/node-decoy';
import type { InboundFormValues } from '@/schemas/forms/inbound-form';

interface Filled {
  target: string;
  serverName: string;
}

/*
 * The target and the SNI that the decoy of a node filled in belong to that node. When the inbound is moved to another node they
 * follow it to the decoy of the new one, and are cleared when it has none, so that a loopback port and a domain are not saved for a
 * host they do not belong to. A target or SNI that the operator wrote is left as it is.
 */
export function useFollowNodeDecoy(
  methods: UseFormReturn<InboundFormValues>,
  nodeDecoy: NodeDecoyTarget | null,
) {
  const target = nodeDecoy?.target;
  const serverName = nodeDecoy?.serverName;
  const previous = useRef<Filled | null>(target && serverName ? { target, serverName } : null);
  useEffect(() => {
    const before = previous.current;
    const current = target && serverName ? { target, serverName } : null;
    previous.current = current;
    if (
      !before ||
      (before.target === current?.target && before.serverName === current?.serverName)
    ) {
      return;
    }
    const setValue = methods.setValue as unknown as (name: string, value: unknown) => void;
    const getValues = methods.getValues as unknown as (name: string) => unknown;
    const names = getValues('streamSettings.realitySettings.serverNames');
    const filled =
      getValues('streamSettings.realitySettings.target') === before.target &&
      Array.isArray(names) &&
      names.length === 1 &&
      names[0] === before.serverName;
    if (!filled) return;
    setValue('streamSettings.realitySettings.target', current?.target ?? '');
    setValue('streamSettings.realitySettings.serverNames', current ? [current.serverName] : []);
  }, [methods, target, serverName]);
}
