import { describe, it, expect } from 'vitest';

import {
  newRegionsOf,
  type PsiphonScanResult,
  type PsiphonScanStatus,
} from '@/pages/xray/overrides/psiphon-regions';

const FINISHED = 1793650044;
const EARLIER = FINISHED - 700000;

const connected = (region: string): PsiphonScanResult => ({ region, connected: true });
const failed = (region: string): PsiphonScanResult => ({ region, connected: false });
const scan = (patch: Partial<PsiphonScanStatus>): PsiphonScanStatus => ({
  running: false,
  total: 0,
  current: [],
  results: [],
  ...patch,
});

describe('newRegionsOf', () => {
  it('names none without a scan', () => {
    expect(newRegionsOf(null)).toEqual([]);
    expect(newRegionsOf(undefined)).toEqual([]);
  });

  describe('once the scan has ended', () => {
    it('names the regions whose first time is that of the scan, when earlier scans had found others', () => {
      const seen = [
        { region: 'DE', at: FINISHED, firstAt: EARLIER },
        { region: 'US', at: FINISHED, firstAt: FINISHED },
        { region: 'FR', at: EARLIER, firstAt: EARLIER },
      ];
      expect(newRegionsOf(scan({ finishedAt: FINISHED, seen }))).toEqual(['US']);
    });

    it('names none in the first scan that found anything', () => {
      const seen = [
        { region: 'DE', at: FINISHED, firstAt: FINISHED },
        { region: 'US', at: FINISHED, firstAt: FINISHED },
      ];
      expect(newRegionsOf(scan({ finishedAt: FINISHED, seen }))).toEqual([]);
    });

    it('names none whose record has no first time', () => {
      const seen = [
        { region: 'DE', at: EARLIER },
        { region: 'US', at: FINISHED },
      ];
      expect(newRegionsOf(scan({ finishedAt: FINISHED, seen }))).toEqual([]);
    });
  });

  describe('while it runs', () => {
    const before = [{ region: 'DE', at: EARLIER, firstAt: EARLIER }];

    it('names the regions that connected and that no scan before it had found', () => {
      const results = [connected('DE'), connected('US'), failed('JP')];
      expect(newRegionsOf(scan({ running: true, results, seen: before }))).toEqual(['US']);
    });

    it('does not take a region that did not connect for a new one', () => {
      expect(newRegionsOf(scan({ running: true, results: [failed('JP')], seen: before }))).toEqual(
        [],
      );
    });

    it('names none when no scan came before it', () => {
      const results = [connected('DE'), connected('US')];
      expect(newRegionsOf(scan({ running: true, results, seen: [] }))).toEqual([]);
      expect(newRegionsOf(scan({ running: true, results }))).toEqual([]);
    });
  });
});
