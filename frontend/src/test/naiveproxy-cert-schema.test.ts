import { describe, expect, it } from 'vitest';

import { createDefaultNaiveProxyInboundSettings } from '@/lib/xray/inbound-defaults';
import { NaiveproxyInboundSettingsSchema } from '@/schemas/protocols/inbound/naiveproxy';

function issues(settings: Record<string, unknown>) {
  const result = NaiveproxyInboundSettingsSchema.safeParse(settings);
  return result.success ? [] : result.error.issues.map((i) => `${i.path.join('.')}: ${i.message}`);
}

describe('NaiveProxy certificate settings', () => {
  it('needs no files when the panel orders the certificate', () => {
    expect(issues({ domain: 'naive.example.com', certMode: 'auto' })).toEqual([]);
  });

  it('needs both files when the admin keeps the certificate', () => {
    expect(issues({ domain: 'naive.example.com', certMode: 'manual' })).toEqual([
      'certFile: pages.inbounds.form.naiveProxyCertFileRequired',
      'keyFile: pages.inbounds.form.naiveProxyKeyFileRequired',
    ]);
    expect(
      issues({ domain: 'naive.example.com', certMode: 'manual', certFile: '/c.pem', keyFile: '' }),
    ).toEqual(['keyFile: pages.inbounds.form.naiveProxyKeyFileRequired']);
  });

  // Inbounds saved before the mode existed carry no certMode and always had files.
  it('reads an inbound saved before the mode existed as manual', () => {
    const parsed = NaiveproxyInboundSettingsSchema.parse({
      domain: 'naive.example.com',
      certFile: '/c.pem',
      keyFile: '/k.pem',
    });
    expect(parsed.certMode).toBe('manual');
    expect(issues({ domain: 'naive.example.com' })).toContain(
      'certFile: pages.inbounds.form.naiveProxyCertFileRequired',
    );
  });

  it('still requires a domain in both modes', () => {
    expect(issues({ domain: '', certMode: 'auto' })).toHaveLength(1);
    expect(
      issues({ domain: '', certMode: 'manual', certFile: '/c.pem', keyFile: '/k.pem' }),
    ).toHaveLength(1);
  });

  it('rejects a mode it does not know instead of guessing', () => {
    expect(issues({ domain: 'naive.example.com', certMode: 'letsencrypt' })).not.toEqual([]);
  });

  it('starts a new inbound on the automatic certificate', () => {
    const defaults = createDefaultNaiveProxyInboundSettings();
    expect(defaults.certMode).toBe('auto');
    expect(defaults.acmeEmail).toBe('');
  });
});
