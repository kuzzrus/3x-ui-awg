import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import PortGuardCard from '@/pages/settings/PortGuardCard';
import { getMessage } from '@/utils/messageBus';
import type { GuardPlan, GuardStatus } from '@/pages/settings/usePortGuard';
import { HttpUtil, Msg } from '@/utils';
import { renderWithProviders } from './test-utils';

const BASE = '/panel/api/xray/portguard';

function status(patch: Partial<GuardStatus> = {}): GuardStatus {
  const whole: GuardStatus = {
    available: true,
    closed: [],
    pending: null,
    protected: [
      { port: 22, proto: 'tcp' },
      { port: 443, proto: 'tcp' },
    ],
    candidates: [
      {
        port: 8443,
        proto: 'tcp',
        label: 'cdn (vless)',
        inboundId: 3,
        viaFrontProxy: true,
        loopback: false,
        closed: false,
        protected: false,
      },
      {
        port: 443,
        proto: 'tcp',
        label: 'edge (vless)',
        viaFrontProxy: false,
        loopback: false,
        closed: false,
        protected: true,
      },
      {
        port: 51820,
        proto: 'udp',
        label: 'awg (amneziawg)',
        viaFrontProxy: false,
        loopback: true,
        closed: false,
        protected: false,
      },
    ],
    leaseSeconds: 120,
    rulesMissing: false,
    firewall: { readable: true, managers: ['ufw'], incoming4: 'deny', incoming6: 'deny' },
    ...patch,
  };
  // As the server answers: a candidate says whether its port is closed.
  return {
    ...whole,
    candidates: whole.candidates.map((c) => ({
      ...c,
      closed: whole.closed.some((p) => p.port === c.port && p.proto === c.proto),
    })),
  };
}

// The server: GET is the status, and an action answers with the status that follows it.
function serve(initial: GuardStatus, answers: Record<string, GuardStatus | GuardPlan> = {}) {
  const get = vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) => {
    if (url === BASE) return new Msg(true, '', initial);
    return new Msg(true, '', {});
  });
  const post = vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string) => {
    const path = url.replace(BASE, '');
    if (path in answers) return new Msg(true, '', answers[path]);
    return new Msg(true, '', {});
  });
  return { get, post };
}

function postsTo(post: ReturnType<typeof serve>['post'], path: string) {
  return post.mock.calls.filter((c) => c[0] === BASE + path);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('PortGuardCard', () => {
  it('lists the ports with their states, and a protected one cannot be ticked', async () => {
    serve(status({ closed: [{ port: 51820, proto: 'udp' }] }));
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText('8443/tcp')).toBeTruthy();
    expect(screen.getByText('cdn (vless)')).toBeTruthy();
    expect(screen.getByText('Also on 443')).toBeTruthy();
    expect(screen.getByText('Always open')).toBeTruthy();
    expect(screen.getByText('Loopback only')).toBeTruthy();

    const closeBox = (port: string) => screen.getByLabelText(`Close ${port}`) as HTMLInputElement;
    expect(closeBox('443/tcp').disabled).toBe(true);
    expect(closeBox('8443/tcp').disabled).toBe(false);
    expect(closeBox('8443/tcp').checked).toBe(false);
  });

  it('shows a closed port that no service lists any more, so that it can be opened', async () => {
    serve(status({ closed: [{ port: 9999, proto: 'tcp' }] }));
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText('9999/tcp')).toBeTruthy();
    expect((screen.getByLabelText('Close 9999/tcp') as HTMLInputElement).checked).toBe(true);
  });

  it('applies exactly the ticked ports, and only once the selection differs from what is closed', async () => {
    const closed = status({ closed: [{ port: 8443, proto: 'tcp' }] });
    const { post } = serve(status(), { '/apply': closed });
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    const apply = screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement;
    expect(apply.disabled).toBe(true);
    fireEvent.click(screen.getByLabelText('Close 8443/tcp'));
    expect(apply.disabled).toBe(false);
    fireEvent.click(apply);

    await waitFor(() => expect(postsTo(post, '/apply')).toHaveLength(1));
    expect(postsTo(post, '/apply')[0][1]).toEqual({ ports: [{ port: 8443, proto: 'tcp' }] });
    expect(postsTo(post, '/apply')[0][2]).toMatchObject({
      headers: { 'Content-Type': 'application/json' },
    });
    // The answer is the new state: 8443 is closed, nothing left to apply.
    await waitFor(() =>
      expect((screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement).disabled).toBe(
        true,
      ),
    );
  });

  it('opens a closed port by unticking it', async () => {
    const { post } = serve(status({ closed: [{ port: 8443, proto: 'tcp' }] }), {
      '/apply': status(),
    });
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    fireEvent.click(screen.getByLabelText('Close 8443/tcp'));
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));

    await waitFor(() => expect(postsTo(post, '/apply')).toHaveLength(1));
    expect(postsTo(post, '/apply')[0][1]).toEqual({ ports: [] });
  });

  it('says what a change would do before it is made', async () => {
    const plan: GuardPlan = {
      closed: [{ port: 8443, proto: 'tcp' }],
      close: [{ port: 8443, proto: 'tcp' }],
      open: [],
      refused: [
        {
          port: { port: 22, proto: 'tcp' },
          reason: 'this port keeps the admin able to reach the host',
        },
      ],
      lease: true,
      windowSeconds: 120,
      ruleset: 'add table inet x_ui_portguard\nflush table inet x_ui_portguard\n',
    };
    const { post } = serve(status(), { '/plan': plan });
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    fireEvent.click(screen.getByLabelText('Close 8443/tcp'));
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Will be closed:')).toBeTruthy();
    expect(within(dialog).getByText('8443/tcp')).toBeTruthy();
    expect(within(dialog).getByText(/22\/tcp: this port keeps the admin able/)).toBeTruthy();
    expect(within(dialog).getByText(/rolled back by itself/)).toBeTruthy();
    expect(within(dialog).getByText(/add table inet x_ui_portguard/)).toBeTruthy();
    expect(postsTo(post, '/plan')).toHaveLength(1);
    expect(postsTo(post, '/apply')).toHaveLength(0);
  });

  it('counts down a change that waits, and confirms it', async () => {
    const waiting = status({
      closed: [{ port: 8443, proto: 'tcp' }],
      pending: { deadlineMs: Date.now() + 90_000, remainingMs: 90_000, previous: [] },
    });
    const { post } = serve(waiting, {
      '/confirm': status({ closed: [{ port: 8443, proto: 'tcp' }] }),
    });
    renderWithProviders(<PortGuardCard />);

    expect(
      await screen.findByText(/A change waits for your confirmation: 1:(29|30) left/),
    ).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));

    await waitFor(() => expect(postsTo(post, '/confirm')).toHaveLength(1));
    await waitFor(() =>
      expect(screen.queryByText(/A change waits for your confirmation/)).toBeNull(),
    );
  });

  it('rolls a waiting change back on request', async () => {
    const waiting = status({
      closed: [{ port: 8443, proto: 'tcp' }],
      pending: { deadlineMs: Date.now() + 90_000, remainingMs: 90_000, previous: [] },
    });
    const { post } = serve(waiting, { '/rollback': status() });
    renderWithProviders(<PortGuardCard />);

    fireEvent.click(await screen.findByRole('button', { name: 'Roll back now' }));

    await waitFor(() => expect(postsTo(post, '/rollback')).toHaveLength(1));
    await waitFor(() =>
      expect(screen.queryByText(/A change waits for your confirmation/)).toBeNull(),
    );
  });

  it('cannot close anything on a host where nft is not available', async () => {
    serve(status({ available: false }));
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText(/nft \(nftables\) is not available/)).toBeTruthy();
    expect((screen.getByLabelText('Close 8443/tcp') as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });

  it('keeps the way back over SSH in view', async () => {
    serve(status());
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText('x-ui setting -openClosedPorts')).toBeTruthy();
  });
});

describe('PortGuardCard, more', () => {
  it('lists the ports in the order of their numbers, and marks the closed ones', async () => {
    serve(
      status({
        closed: [
          { port: 9999, proto: 'tcp' },
          { port: 51820, proto: 'udp' },
        ],
      }),
    );
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    const order = screen
      .getAllByRole('row')
      .map((r) => r.textContent ?? '')
      .filter((text) => /\d+\/(tcp|udp)/.test(text))
      .map((text) => /(\d+)\/(tcp|udp)/.exec(text)?.[0]);
    expect(order).toEqual(['443/tcp', '8443/tcp', '9999/tcp', '51820/udp']);
    expect(screen.getAllByText('Closed')).toHaveLength(2);
  });

  it('keeps the selection when an apply fails, so that it can be tried again', async () => {
    serve(status());
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(false, 'nft: no luck'));
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    fireEvent.click(screen.getByLabelText('Close 8443/tcp'));
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }));

    await waitFor(() => expect(HttpUtil.post).toHaveBeenCalled());
    expect((screen.getByLabelText('Close 8443/tcp') as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole('button', { name: 'Apply' }) as HTMLButtonElement).disabled).toBe(
      false,
    );
  });

  it('says when a plan changes nothing', async () => {
    const plan: GuardPlan = {
      closed: [],
      close: [],
      open: [],
      refused: [],
      lease: false,
      windowSeconds: 120,
      ruleset: 'delete table inet x_ui_portguard\n',
    };
    serve(status({ closed: [{ port: 8443, proto: 'tcp' }] }), { '/plan': plan });
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    fireEvent.click(screen.getByLabelText('Close 8443/tcp'));
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Nothing changes.')).toBeTruthy();
    expect(within(dialog).queryByText(/rolled back by itself/)).toBeNull();
  });

  it('writes the seconds of the countdown with two digits', async () => {
    serve(
      status({
        closed: [{ port: 8443, proto: 'tcp' }],
        pending: { deadlineMs: Date.now() + 5_000, remainingMs: 5_000, previous: [] },
      }),
    );
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText(/confirmation: 0:0[45] left/)).toBeTruthy();
  });

  it('looks again while a change waits, so that a rollback by the timer shows', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] });
    try {
      let waiting = true;
      vi.spyOn(HttpUtil, 'get').mockImplementation(
        async () =>
          new Msg(
            true,
            '',
            waiting
              ? status({
                  closed: [{ port: 8443, proto: 'tcp' }],
                  pending: { deadlineMs: Date.now() + 90_000, remainingMs: 90_000, previous: [] },
                })
              : status(),
          ),
      );
      renderWithProviders(<PortGuardCard />);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(100);
      });
      expect(screen.queryByText(/A change waits for your confirmation/)).not.toBeNull();

      waiting = false;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3_200);
      });
      expect(screen.queryByText(/A change waits for your confirmation/)).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('PortGuardCard, edges', () => {
  it('shows the truth again when the server moved while an edit was in progress', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] });
    try {
      let waiting = true;
      vi.spyOn(HttpUtil, 'get').mockImplementation(
        async () =>
          new Msg(
            true,
            '',
            waiting
              ? status({
                  closed: [{ port: 8443, proto: 'tcp' }],
                  pending: { deadlineMs: Date.now() + 90_000, remainingMs: 90_000, previous: [] },
                })
              : status(),
          ),
      );
      renderWithProviders(<PortGuardCard />);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(100);
      });
      const box = (port: string) => screen.getByLabelText(`Close ${port}`) as HTMLInputElement;
      fireEvent.click(box('51820/udp'));
      expect(box('51820/udp').checked).toBe(true);
      expect(box('8443/tcp').checked).toBe(true);

      // The lease runs out and the server rolls 8443 back: what was ticked against the old state is stale.
      waiting = false;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3_200);
      });
      expect(box('8443/tcp').checked).toBe(false);
      expect(box('51820/udp').checked).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it('shows a countdown that has run out as 0:00, not a negative time', async () => {
    serve(
      status({
        closed: [{ port: 8443, proto: 'tcp' }],
        pending: { deadlineMs: Date.now() - 5_000, remainingMs: 0, previous: [] },
      }),
    );
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText(/confirmation: 0:00 left/)).toBeTruthy();
  });

  it('does not crash on an answer that has no lists, and shows nothing to close', async () => {
    vi.spyOn(HttpUtil, 'get').mockResolvedValue(new Msg(true, '', {}));
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText(/nft \(nftables\) is not available/)).toBeTruthy();
    expect(screen.getByText('No ports to show')).toBeTruthy();
    // What nobody said is not a firewall that was read.
    expect(screen.getByText(/The firewall of this host could not be read/)).toBeTruthy();
    expect(screen.queryByText(/their rules are not in the firewall/)).toBeNull();
  });

  it('does not say a change is confirmed when it was not', async () => {
    const waiting = status({
      closed: [{ port: 8443, proto: 'tcp' }],
      pending: { deadlineMs: Date.now() + 90_000, remainingMs: 90_000, previous: [] },
    });
    serve(waiting);
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(false, 'no change waits'));
    const success = vi.spyOn(getMessage(), 'success');
    renderWithProviders(<PortGuardCard />);

    fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }));

    await waitFor(() => expect(HttpUtil.post).toHaveBeenCalled());
    expect(success).not.toHaveBeenCalled();
  });

  it('offers a preview only for a selection that differs from what is closed', async () => {
    serve(status());
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    const preview = screen.getByRole('button', { name: 'Preview' }) as HTMLButtonElement;
    expect(preview.disabled).toBe(true);
    fireEvent.click(screen.getByLabelText('Close 8443/tcp'));
    expect(preview.disabled).toBe(false);
  });

  it('lists the ports that would be opened in a plan', async () => {
    const plan: GuardPlan = {
      closed: [{ port: 8443, proto: 'tcp' }],
      close: [],
      open: [{ port: 51820, proto: 'udp' }],
      refused: [],
      lease: false,
      windowSeconds: 120,
      ruleset: 'x',
    };
    serve(status({ closed: [{ port: 51820, proto: 'udp' }] }), { '/plan': plan });
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    fireEvent.click(screen.getByLabelText('Close 51820/udp'));
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Will be opened:')).toBeTruthy();
    expect(within(dialog).queryByText('Will be closed:')).toBeNull();
  });
});

describe('PortGuardCard, clocks and failures', () => {
  // The host's deadline is on the host's clock: a browser whose clock is far ahead or behind must not be what decides.
  it('counts down from what the host says is left, not from its deadline', async () => {
    serve(
      status({
        closed: [{ port: 8443, proto: 'tcp' }],
        pending: { deadlineMs: Date.now() - 10 * 60_000, remainingMs: 90_000, previous: [] },
      }),
    );
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText(/confirmation: 1:(29|30) left/)).toBeTruthy();
  });

  it('goes on counting down while a change waits', async () => {
    vi.useFakeTimers({
      toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'],
    });
    try {
      vi.spyOn(HttpUtil, 'get').mockResolvedValue(
        new Msg(
          true,
          '',
          status({
            closed: [{ port: 8443, proto: 'tcp' }],
            pending: { deadlineMs: Date.now() + 90_000, remainingMs: 90_000, previous: [] },
          }),
        ),
      );
      renderWithProviders(<PortGuardCard />);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(100);
      });
      expect(screen.queryByText(/confirmation: 1:(29|30) left/)).not.toBeNull();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
      expect(screen.queryByText(/confirmation: 1:2[7-8] left/)).not.toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('stops the countdown at 0:00 and does not go negative', async () => {
    vi.useFakeTimers({
      toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout', 'Date'],
    });
    try {
      // The server has not rolled it back yet (its job runs every 5 seconds): the status still says it waits.
      vi.spyOn(HttpUtil, 'get').mockResolvedValue(
        new Msg(
          true,
          '',
          status({
            closed: [{ port: 8443, proto: 'tcp' }],
            pending: { deadlineMs: Date.now() + 1_000, remainingMs: 1_000, previous: [] },
          }),
        ),
      );
      renderWithProviders(<PortGuardCard />);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(100);
      });
      expect(screen.queryByText(/confirmation: 0:01 left/)).not.toBeNull();

      // Two seconds on, before the next look at the server (every 3): a second more has gone than there was.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_000);
      });
      expect(screen.queryByText(/confirmation: 0:00 left/)).not.toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('tells the admin when the closed ports cannot be read, and reads them again on request', async () => {
    const get = vi
      .spyOn(HttpUtil, 'get')
      .mockResolvedValue(new Msg(false, 'the stored state is unreadable'));
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText('Could not read the closed ports')).toBeTruthy();
    expect(screen.getByText('the stored state is unreadable')).toBeTruthy();

    get.mockResolvedValue(new Msg(true, '', status()));
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));

    expect(await screen.findByText('8443/tcp')).toBeTruthy();
    await waitFor(() => expect(screen.queryByText('Could not read the closed ports')).toBeNull());
  });

  it('tells the admin when a preview fails', async () => {
    serve(status());
    vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(false, 'the plan cannot be made'));
    const error = vi.spyOn(getMessage(), 'error');
    renderWithProviders(<PortGuardCard />);
    await screen.findByText('8443/tcp');

    fireEvent.click(screen.getByLabelText('Close 8443/tcp'));
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }));

    await waitFor(() => expect(error).toHaveBeenCalledWith('the plan cannot be made'));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('names the firewall of the host and what it does with traffic that no rule lets in', async () => {
    serve(
      status({
        firewall: { readable: true, managers: ['ufw'], incoming4: 'deny', incoming6: 'allow' },
      }),
    );
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText('Managed by ufw')).toBeTruthy();
    expect(
      screen.getByText('Incoming traffic that no rule lets in. IPv4: blocked, IPv6: let in.'),
    ).toBeTruthy();
    expect(screen.queryByText(/found no firewall that drops incoming traffic/)).toBeNull();
  });

  it('shows a firewall that has no front end by its policy alone', async () => {
    serve(
      status({ firewall: { readable: true, managers: [], incoming4: 'deny', incoming6: 'deny' } }),
    );
    renderWithProviders(<PortGuardCard />);

    expect(
      await screen.findByText(
        'Incoming traffic that no rule lets in. IPv4: blocked, IPv6: blocked.',
      ),
    ).toBeTruthy();
    expect(screen.queryByText(/Managed by/)).toBeNull();
  });

  it('names a front end even when the policy of its rules could not be read', async () => {
    serve(
      status({
        firewall: { readable: true, managers: ['firewalld'], incoming4: '', incoming6: '' },
      }),
    );
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText('Managed by firewalld')).toBeTruthy();
    expect(
      screen.getByText('Incoming traffic that no rule lets in. IPv4: unknown, IPv6: unknown.'),
    ).toBeTruthy();
    expect(screen.queryByText(/found no firewall that drops incoming traffic/)).toBeNull();
  });

  it.each([
    ['IPv4', 'deny', 'allow', 'IPv4: blocked, IPv6: let in.'],
    ['IPv6', 'allow', 'deny', 'IPv4: let in, IPv6: blocked.'],
  ] as const)(
    'counts a firewall that blocks only %s as filtering',
    async (_family, incoming4, incoming6, line) => {
      serve(status({ firewall: { readable: true, managers: [], incoming4, incoming6 } }));
      renderWithProviders(<PortGuardCard />);

      expect(
        await screen.findByText(`Incoming traffic that no rule lets in. ${line}`),
      ).toBeTruthy();
      expect(screen.queryByText(/found no firewall that drops incoming traffic/)).toBeNull();
    },
  );

  it('says so when nothing on the host filters incoming traffic', async () => {
    serve(
      status({
        firewall: { readable: true, managers: [], incoming4: 'allow', incoming6: 'allow' },
      }),
    );
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText(/found no firewall that drops incoming traffic/)).toBeTruthy();
    expect(screen.queryByText(/Managed by/)).toBeNull();
  });

  it('says so when the firewall of the host could not be read', async () => {
    serve(status({ firewall: { readable: false, managers: [], incoming4: '', incoming6: '' } }));
    renderWithProviders(<PortGuardCard />);

    expect(await screen.findByText(/The firewall of this host could not be read/)).toBeTruthy();
    expect(screen.queryByText(/found no firewall that drops incoming traffic/)).toBeNull();
  });

  it('warns that closed ports are open when their rules are gone, and loads the rules again', async () => {
    const closed = [{ port: 8443, proto: 'tcp' as const }];
    const { post } = serve(status({ closed, rulesMissing: true }), {
      '/apply': status({ closed, rulesMissing: false }),
    });
    const success = vi.spyOn(getMessage(), 'success');
    renderWithProviders(<PortGuardCard />);

    expect(
      await screen.findByText(
        'The closed ports are saved, but their rules are not in the firewall',
      ),
    ).toBeTruthy();
    // An edit that was not applied is not what the rules are put back for: only the ports that are closed.
    await screen.findByText('51820/udp');
    fireEvent.click(screen.getByLabelText('Close 51820/udp'));
    fireEvent.click(screen.getByRole('button', { name: 'Load the rules again' }));

    await waitFor(() => expect(postsTo(post, '/apply')).toHaveLength(1));
    expect(postsTo(post, '/apply')[0][1]).toEqual({ ports: closed });
    await waitFor(() => expect(success).toHaveBeenCalledWith('The rules are loaded again'));
    await waitFor(() =>
      expect(screen.queryByText(/their rules are not in the firewall/)).toBeNull(),
    );
  });

  it('does not warn about rules when they are in the kernel', async () => {
    serve(status({ closed: [{ port: 8443, proto: 'tcp' }], rulesMissing: false }));
    renderWithProviders(<PortGuardCard />);

    await screen.findByText('8443/tcp');
    expect(screen.queryByText(/their rules are not in the firewall/)).toBeNull();
  });
});
