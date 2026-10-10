import { screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import LogModal from '@/pages/index/LogModal';
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
});
