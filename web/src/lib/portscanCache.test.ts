/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach } from "vitest";
import { clearScans, readScan, writeScan } from "./portscanCache";

const ROW = { port: 22 } as never;

describe("port scan cache", () => {
  beforeEach(() => localStorage.clear());

  it("keeps each account's scan to itself", () => {
    writeScan(1, null, [ROW]);
    expect(readScan(1, null)?.rows).toHaveLength(1);
    expect(readScan(2, null)).toBeNull();
  });

  it("keeps each host's scan apart", () => {
    writeScan(1, 7, [ROW]);
    expect(readScan(1, null)).toBeNull();
    expect(readScan(1, 7)).not.toBeNull();
  });

  it("clearScans removes every scan, including the old per-host-only ones", () => {
    writeScan(1, null, [ROW]);
    writeScan(2, 7, [ROW]);
    localStorage.setItem("dc.portscan.local", JSON.stringify({ rows: [ROW], at: 1 })); // pre-1.7.0 key
    localStorage.setItem("dc.host", "7"); // not a scan
    clearScans();
    expect(Object.keys(localStorage).filter((k) => k.startsWith("dc.portscan."))).toEqual([]);
    expect(localStorage.getItem("dc.host")).toBe("7");
  });
});
