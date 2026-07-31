import { describe, expect, it } from "vitest";
import { netRates } from "./format";

// Docker reports network traffic as cumulative counters, so a rate only exists
// as a difference between two samples. The interesting cases are all the ones
// where naive subtraction produces a number that is confidently wrong.

const s = (timestamp: number, netRx: number, netTx: number) => ({ timestamp, netRx, netTx });

describe("netRates", () => {
  it("derives bytes per second from the delta and the elapsed time", () => {
    const r = netRates([s(1000, 0, 0), s(2000, 1000, 500)]);
    expect(r).toHaveLength(1);
    expect(r[0].rx).toBe(1000); // 1000 bytes over 1s
    expect(r[0].tx).toBe(500);
  });

  it("uses the actual interval, not an assumed one", () => {
    // Samples do not arrive on an exact tick; dividing by a hard-coded period
    // would report double the real rate here.
    const r = netRates([s(0, 0, 0), s(2000, 1000, 0)]);
    expect(r[0].rx).toBe(500);
  });

  it("treats a counter reset as no traffic rather than a negative rate", () => {
    // The container was recreated, so the counter starts again from zero. The
    // subtraction is negative, which would plot below the axis or — if the sign
    // were dropped — invent a huge spike out of nothing.
    const r = netRates([s(1000, 5_000_000, 1_000_000), s(2000, 0, 0)]);
    expect(r[0].rx).toBe(0);
    expect(r[0].tx).toBe(0);
  });

  it("resumes normally after a reset", () => {
    const r = netRates([s(1000, 5_000_000, 0), s(2000, 0, 0), s(3000, 300, 0)]);
    expect(r).toHaveLength(2);
    expect(r[0].rx).toBe(0);
    expect(r[1].rx).toBe(300);
  });

  it("skips duplicate or out-of-order frames instead of dividing by zero", () => {
    // A zero interval would give Infinity, which renders as a blank or a spike
    // depending on the chart library's mood.
    const r = netRates([s(1000, 0, 0), s(1000, 500, 0), s(900, 900, 0)]);
    expect(r.every((x) => Number.isFinite(x.rx))).toBe(true);
    expect(r).toHaveLength(0);
  });

  it("returns nothing for fewer than two samples", () => {
    expect(netRates([])).toHaveLength(0);
    expect(netRates([s(1000, 10, 10)])).toHaveLength(0);
  });

  it("reports a flat counter as zero, not as missing data", () => {
    // An idle container is a real answer and must plot as a line at zero.
    const r = netRates([s(1000, 1000, 1000), s(2000, 1000, 1000)]);
    expect(r[0].rx).toBe(0);
    expect(r[0].tx).toBe(0);
  });
});
