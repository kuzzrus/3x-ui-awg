import { useState } from 'react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import LogModal, { type LogSource } from '@/pages/index/LogModal';
import { renderWithProviders } from '@/test/test-utils';
import { HttpUtil, Msg } from '@/utils';

afterEach(() => {
  vi.restoreAllMocks();
});

const LINE = '2026/10/10 12:00:00 INFO - hello from the log';

describe('LogModal', () => {
  it("reads the panel's own log by default, with the SysLog switch", async () => {
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', [LINE]));
    renderWithProviders(<LogModal open onClose={() => {}} />);

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/panel/api/server/logs/20', {
        level: 'info',
        syslog: false,
      }),
    );
    expect(screen.getByText('SysLog')).toBeTruthy();
  });

  it('reads another log from its own address, under its own title and without the SysLog switch', async () => {
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue(new Msg(true, '', [LINE]));
    renderWithProviders(
      <LogModal
        open
        onClose={() => {}}
        source={{
          path: '/panel/api/nodes/agentLogs/7',
          title: 'Log of edge',
          fileName: 'edge.log',
        }}
      />,
    );

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/panel/api/nodes/agentLogs/7/20', { level: 'info' }),
    );
    expect(screen.getByText('Log of edge')).toBeTruthy();
    expect(screen.queryByText('SysLog')).toBeNull();
    await waitFor(() => expect(document.body.textContent).toContain('hello from the log'));
  });

  // One dialog serves every agent row, so lines of the log it left must not stay on screen under
  // the title of the next one when that one does not answer.
  it('does not show the lines of one source under the title of another that fails', async () => {
    const sources: Record<'a' | 'b', LogSource> = {
      a: { path: '/panel/api/nodes/agentLogs/1', title: 'Log of A', fileName: 'a.log' },
      b: { path: '/panel/api/nodes/agentLogs/2', title: 'Log of B', fileName: 'b.log' },
    };
    const post = vi
      .spyOn(HttpUtil, 'post')
      .mockImplementation(async (url: string) =>
        url.includes('/agentLogs/1/')
          ? new Msg(true, '', ['2026/10/10 12:00:00 INFO - only the log of A has this'])
          : new Msg(false, 'the agent is down', null),
      );
    function Harness() {
      const [which, setWhich] = useState<'a' | 'b'>('a');
      return (
        <>
          <button onClick={() => setWhich('b')}>switch</button>
          <LogModal open onClose={() => {}} source={sources[which]} />
        </>
      );
    }
    renderWithProviders(<Harness />);
    await waitFor(() => expect(document.body.textContent).toContain('only the log of A has this'));

    fireEvent.click(screen.getByText('switch'));

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/panel/api/nodes/agentLogs/2/20', { level: 'info' }),
    );
    expect(screen.getByText('Log of B')).toBeTruthy();
    expect(document.body.textContent).not.toContain('only the log of A has this');
  });
});
