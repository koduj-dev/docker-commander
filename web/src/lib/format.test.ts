import { describe, it, expect } from "vitest";
import { bytes, rate } from "./format";

describe("bytes / rate", () => {
  it("formats ordinary sizes", () => {
    expect(bytes(0)).toBe("0 B");
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1536)).toBe("1.5 KB");
    expect(bytes(5 * 1024 ** 3)).toBe("5.0 GB");
    expect(rate(2 * 1024 ** 2)).toBe("2.0 MB/s");
  });

  // Regression: a sub-byte rate (idle container, 0.8 B/s) has a negative log,
  // which indexed the unit table at -1 and printed "819.2 undefined/s".
  it("never prints 'undefined' for a fractional byte figure", () => {
    for (const n of [0.8, 0.0001, 0.999, 1e-9]) {
      expect(bytes(n)).not.toContain("undefined");
      expect(rate(n)).not.toContain("undefined");
    }
    expect(rate(0.8)).toBe("1 B/s");
  });

  it("degrades to 0 for values that are not a size", () => {
    expect(bytes(-5)).toBe("0 B");
    expect(bytes(NaN)).toBe("0 B");
    expect(bytes(Infinity)).toBe("0 B");
  });

  it("clamps past the largest unit instead of overflowing the table", () => {
    expect(bytes(5 * 1024 ** 5)).toBe("5120.0 TB");
    expect(bytes(5 * 1024 ** 5)).not.toContain("undefined");
  });
});
