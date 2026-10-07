import type { ReactNode } from 'react';
import { Form } from 'antd';
import type { MessageInstance } from 'antd/es/message/interface';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { FormProvider, useForm } from 'react-hook-form';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import NaiveProxyFields from '@/pages/inbounds/form/protocols/naiveproxy';
import { certsPollInterval, type NaiveProxyCert } from '@/api/queries/useNaiveProxyCertsQuery';
import { keys } from '@/api/queryKeys';
import { setDatepicker } from '@/hooks/useDatepicker';
import { HttpUtil, Msg } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';
import { makeTestQueryClient, renderWithProviders } from './test-utils';

const NOW = new Date('2026-10-06T12:00:00Z').getTime();
const DAY = 24 * 60 * 60 * 1000;

// Without an instance the toast goes through antd's static API, which mounts a React root that
// no cleanup reaches and that keeps rendering after the file is torn down.
const toast = { success: vi.fn(), info: vi.fn(), warning: vi.fn(), error: vi.fn() };

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(NOW);
  // Also marks the setting as loaded, so the notice never asks the panel for it.
  setDatepicker('gregorian');
  Object.values(toast).forEach((method) => method.mockReset());
  setMessageInstance(toast as unknown as MessageInstance);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

function Harness({
  children,
  certMode,
  domain,
}: {
  children: ReactNode;
  certMode?: 'auto' | 'manual';
  domain?: string;
}) {
  const methods = useForm({
    defaultValues: {
      settings: { publicPort: 443, certMode, domain: domain ?? 'naive.example.com' },
    },
  });
  return (
    <FormProvider {...methods}>
      <Form>{children}</Form>
    </FormProvider>
  );
}

function inDays(days: number): string {
  return new Date(NOW + days * DAY).toISOString();
}

function cert(partial: Partial<NaiveProxyCert>): NaiveProxyCert {
  return {
    inboundId: 7,
    domain: 'naive.example.com',
    enable: true,
    mode: 'auto',
    state: 'obtained',
    notAfter: inDays(60),
    ...partial,
  };
}

// The engine is installed so its banner stays out of the way; the certs endpoint answers `certs`.
function mockApi(certs: NaiveProxyCert[]) {
  const posts: { url: string; data: unknown }[] = [];
  vi.spyOn(HttpUtil, 'post').mockImplementation(async (url: string, data?: unknown) => {
    posts.push({ url, data });
    if (url.endsWith('/naiveproxy/status')) {
      return new Msg(true, '', { installed: true, supported: true, platform: 'linux/amd64' });
    }
    if (url.endsWith('/naiveproxy/certs')) return new Msg(true, '', certs);
    return new Msg(true, '', null);
  });
  return posts;
}

function renderFields(props: {
  inboundId: number | null;
  certMode?: 'auto' | 'manual';
  domain?: string;
}) {
  const queryClient = makeTestQueryClient();
  renderWithProviders(
    <Harness certMode={props.certMode} domain={props.domain}>
      <NaiveProxyFields inboundId={props.inboundId} />
    </Harness>,
    { queryClient },
  );
  return queryClient;
}

function notice(): HTMLElement | null {
  return document.querySelector('.ant-alert');
}

// The notice first shows the hint of an inbound not saved yet, then what the panel answers.
async function expectNotice(pattern: RegExp) {
  await waitFor(() => expect(notice()?.textContent ?? '').toMatch(pattern));
}

async function certsLoaded(queryClient: ReturnType<typeof makeTestQueryClient>) {
  await waitFor(() =>
    expect(queryClient.getQueryState(keys.xray.naiveProxyCerts())?.status).toBe('success'),
  );
}

describe('NaiveProxy certificate notice', () => {
  it('shows when a healthy automatic certificate ends', async () => {
    mockApi([cert({ notAfter: inDays(60) })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await waitFor(() => expect(document.querySelector('.ant-alert-success')).not.toBeNull());
    expect(notice()?.textContent).toMatch(/days left: 60/);
  });

  it('warns once a certificate is within two weeks of its end and is still not renewed', async () => {
    mockApi([cert({ notAfter: inDays(10) })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await waitFor(() => expect(document.querySelector('.ant-alert-warning')).not.toBeNull());
    expect(notice()?.textContent).toMatch(/days left: 10.*renewal is overdue/);
  });

  it('raises the alarm in the last three days', async () => {
    mockApi([cert({ notAfter: inDays(2) })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await waitFor(() => expect(document.querySelector('.ant-alert-error')).not.toBeNull());
    expect(notice()?.textContent).toMatch(/days left: 2/);
  });

  it('counts a part of a day as a whole one', async () => {
    mockApi([cert({ notAfter: new Date(NOW + 20 * 60 * 60 * 1000).toISOString() })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/days left: 1\b/);
  });

  it('says the certificate expired once its end has passed', async () => {
    mockApi([cert({ notAfter: inDays(-1), state: 'obtained' })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await waitFor(() => expect(document.querySelector('.ant-alert-error')).not.toBeNull());
    expect(notice()?.textContent).toMatch(/expired on/);
  });

  it('says it is ordering while nothing has been issued yet', async () => {
    mockApi([cert({ state: 'obtaining', notAfter: undefined })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/Ordering the certificate/);
    expect(document.querySelector('.ant-alert-info')).not.toBeNull();
  });

  it('keeps the old expiry on screen while a renewal runs', async () => {
    mockApi([cert({ state: 'obtaining', notAfter: inDays(20) })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/Valid until .*Renewing now/);
  });

  it('shows why an order failed and retries it on request', async () => {
    const posts = mockApi([
      cert({ state: 'failed', notAfter: undefined, error: 'DNS problem: NXDOMAIN looking up A' }),
    ]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/Could not get the certificate: DNS problem: NXDOMAIN/);
    fireEvent.click(await screen.findByRole('button', { name: /try again/i }));

    await waitFor(() =>
      expect(posts.some((p) => p.url.endsWith('/naiveproxy/certRetry'))).toBe(true),
    );
    expect(posts.find((p) => p.url.endsWith('/naiveproxy/certRetry'))?.data).toEqual({
      inboundId: 7,
    });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Trying again...'));
  });

  it('shows a failed renewal next to the certificate that is still valid', async () => {
    mockApi([cert({ state: 'failed', notAfter: inDays(20), error: 'port 80 is in use' })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/days left: 20.*last renewal failed: port 80 is in use/);
    expect(document.querySelector('.ant-alert-warning')).not.toBeNull();
  });

  it('shows the end of a certificate file the admin keeps up, and no retry button for it', async () => {
    mockApi([cert({ mode: 'manual', notAfter: inDays(10) })]);
    renderFields({ inboundId: 7, certMode: 'manual' });

    await expectNotice(/Renew the certificate file before then/);
    expect(screen.queryByRole('button', { name: /try again/i })).toBeNull();
  });

  it('says so when a manual certificate file cannot be read', async () => {
    mockApi([
      cert({ mode: 'manual', state: 'failed', notAfter: undefined, error: 'no such file' }),
    ]);
    renderFields({ inboundId: 7, certMode: 'manual' });

    await expectNotice(/Cannot read the certificate file: no such file/);
  });

  // What the notice reports is what was saved: after the admin flips the mode or retypes the domain
  // it would describe a certificate the form no longer shows.
  it('describes only the saved certificate', async () => {
    mockApi([cert({ mode: 'manual', notAfter: inDays(60) })]);
    const queryClient = renderFields({ inboundId: 7, certMode: 'auto' });

    await certsLoaded(queryClient);
    expect(notice()?.textContent).toMatch(/orders the certificate from Let's Encrypt/);
    expect(document.querySelector('.ant-alert-success')).toBeNull();
  });

  it('describes only the saved domain', async () => {
    mockApi([cert({ domain: 'old.example.com' })]);
    const queryClient = renderFields({ inboundId: 7, certMode: 'auto', domain: 'new.example.com' });

    await certsLoaded(queryClient);
    expect(notice()?.textContent).toMatch(/orders the certificate from Let's Encrypt/);
    expect(document.querySelector('.ant-alert-success')).toBeNull();
  });

  it('explains the automatic certificate before the inbound exists, without asking the panel', async () => {
    const posts = mockApi([]);
    renderFields({ inboundId: null, certMode: 'auto' });

    await expectNotice(/port 80 must be free/);
    expect(posts.some((p) => p.url.endsWith('/naiveproxy/certs'))).toBe(false);
  });

  it('asks for the certificate files in manual mode, not for an email', async () => {
    mockApi([]);
    renderFields({ inboundId: null, certMode: 'manual' });

    await waitFor(() => expect(screen.getByText('Certificate file')).toBeTruthy());
    expect(screen.queryByText("Let's Encrypt email")).toBeNull();
  });

  it('asks for an email in automatic mode, not for the certificate files', async () => {
    mockApi([]);
    renderFields({ inboundId: null, certMode: 'auto' });

    await waitFor(() => expect(screen.getByText("Let's Encrypt email")).toBeTruthy());
    expect(screen.queryByText('Certificate file')).toBeNull();
  });

  // An inbound saved before the mode existed has no certMode and acts as manual. The switch has to
  // show that, or its first segment looks selected and clicking it does nothing.
  it('shows an inbound saved before the mode existed as using its own files, and switches in one click', async () => {
    mockApi([]);
    renderFields({ inboundId: null, certMode: undefined });

    const own = await screen.findByRole('radio', { name: 'My own files' });
    expect((own as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText('Certificate file')).toBeTruthy();

    fireEvent.click(screen.getByRole('radio', { name: "Automatic (Let's Encrypt)" }));

    await waitFor(() => expect(screen.getByText("Let's Encrypt email")).toBeTruthy());
    expect(screen.queryByText('Certificate file')).toBeNull();
  });

  it('says the certificate is dead when the renewal that should have replaced it keeps failing', async () => {
    mockApi([cert({ state: 'failed', notAfter: inDays(-3), error: 'port 80 is in use' })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/expired on.*last renewal failed: port 80 is in use/);
    expect(document.querySelector('.ant-alert-error')).not.toBeNull();
    expect(notice()?.textContent).not.toMatch(/Valid until/);
  });

  it('writes the end date in the calendar the panel is set to', async () => {
    setDatepicker('jalalian');
    const expiry = inDays(60);
    mockApi([cert({ notAfter: expiry })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    const jalali = new Intl.DateTimeFormat('fa-IR', { dateStyle: 'medium' }).format(
      new Date(expiry),
    );
    const gregorian = new Intl.DateTimeFormat('en-US', { dateStyle: 'medium' }).format(
      new Date(expiry),
    );
    expect(jalali).not.toBe(gregorian);
    await expectNotice(new RegExp(`Valid until ${jalali}`));
  });

  it('does not promise an order for an inbound that is switched off', async () => {
    mockApi([cert({ enable: false, state: '', notAfter: undefined })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/inbound is disabled/);
    expect(notice()?.textContent).not.toMatch(/as soon as the panel picks/);
  });

  // What Let's Encrypt says when another program answers on port 80 is no help to an admin
  // who does not know the panel serves the challenge there.
  it("tells what to check when Let's Encrypt cannot reach the panel on port 80", async () => {
    mockApi([
      cert({
        state: 'failed',
        notAfter: undefined,
        error: 'Invalid response from http://naive.example.com/.well-known/acme-challenge/x: 403',
        hint: 'reach',
      }),
    ]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/Could not get the certificate: Invalid response/);
    expect(notice()?.textContent).toMatch(/stop whatever else answers on port 80/);
  });

  it('adds no port hint to a failure that does not carry one', async () => {
    mockApi([cert({ state: 'failed', notAfter: undefined, error: 'rate limited' })]);
    renderFields({ inboundId: 7, certMode: 'auto' });

    await expectNotice(/Could not get the certificate: rate limited/);
    expect(notice()?.textContent).not.toMatch(/port 80/);
  });
});

describe('NaiveProxy certificate polling', () => {
  it('polls quickly while an enabled automatic inbound waits for its order', () => {
    expect(certsPollInterval([cert({ state: '', notAfter: undefined })])).toBe(3_000);
    expect(certsPollInterval([cert({ state: 'obtaining', notAfter: undefined })])).toBe(3_000);
  });

  it('settles to the slow poll once every order has an answer', () => {
    expect(certsPollInterval(undefined)).toBe(30_000);
    expect(certsPollInterval([cert({ state: 'obtained' })])).toBe(30_000);
    expect(certsPollInterval([cert({ state: 'failed', error: 'no' })])).toBe(30_000);
    expect(certsPollInterval([cert({ mode: 'manual', state: '' })])).toBe(30_000);
  });

  // A switched-off automatic inbound stays idle for good: it must not pin the poll at three seconds.
  it('ignores an inbound that is switched off', () => {
    expect(certsPollInterval([cert({ enable: false, state: '', notAfter: undefined })])).toBe(
      30_000,
    );
  });
});
