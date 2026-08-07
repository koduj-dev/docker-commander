import { describe, expect, it } from "vitest";
import { usageDomainMax } from "./MetricsHistory";

// The axis was pinned to [0, 100] while CPU is reported in the `docker stats`
// convention — 100% is one core, so a busy container on a multi-core host reads
// well past it. recharts treats an explicit domain as a hint, so the overflow
// did not clamp; it rendered a five-digit top label above ticks spaced by 80.
describe("usageDomainMax", () => {
  it("keeps the familiar 0-100 scale while nothing exceeds one core", () => {
    expect(usageDomainMax(0)).toBe(100);
    expect(usageDomainMax(18.2)).toBe(100);
    expect(usageDomainMax(100)).toBe(100);
  });

  it("grows in whole cores once the data overflows", () => {
    // The value from the run that exposed this: 316.1% across 16 cores.
    expect(usageDomainMax(316.1)).toBe(400);
    expect(usageDomainMax(101)).toBe(200);
    expect(usageDomainMax(200)).toBe(200);
    expect(usageDomainMax(1550)).toBe(1600);
  });

  it("never returns a top below the data, which is what broke the axis", () => {
    for (const v of [0, 18.2, 99.9, 100, 100.1, 242.1, 316.1, 999, 1600]) {
      expect(usageDomainMax(v)).toBeGreaterThanOrEqual(v);
    }
  });

  it("survives the empty-series case rather than producing NaN bounds", () => {
    expect(usageDomainMax(NaN)).toBe(100);
    expect(usageDomainMax(-Infinity)).toBe(100);
  });
});
