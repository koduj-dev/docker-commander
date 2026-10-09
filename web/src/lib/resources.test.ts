import { describe, it, expect } from "vitest";
import { sortUsage, filterUsage } from "./resources";
import { cpuCores } from "./format";
import type { ResourceUsage } from "./types";

const u = (name: string, cpuPercent: number, memBytes: number): ResourceUsage => ({
  id: name, name, cpuPercent, memBytes, memPercent: 0, netRxRate: 0, netTxRate: 0,
});
const rows = [u("b", 1, 300), u("a", 5, 100), u("c", 5, 200)];

describe("sortUsage", () => {
  it("sorts by memory descending", () => {
    expect(sortUsage(rows, "mem", true).map((r) => r.name)).toEqual(["b", "c", "a"]);
  });
  it("breaks ties by name so equal rows keep a stable order", () => {
    expect(sortUsage(rows, "cpu", true).map((r) => r.name)).toEqual(["a", "c", "b"]);
  });
  it("sorts by name ascending and does not mutate the input", () => {
    const copy = [...rows];
    expect(sortUsage(rows, "name", false).map((r) => r.name)).toEqual(["a", "b", "c"]);
    expect(rows).toEqual(copy);
  });
});

describe("filterUsage", () => {
  it("matches case-insensitively on a substring, blank keeps everything", () => {
    expect(filterUsage([u("Postgres", 0, 0), u("redis", 0, 0)], "POST").map((r) => r.name)).toEqual(["Postgres"]);
    expect(filterUsage(rows, "  ")).toHaveLength(3);
  });
});

describe("cpuCores", () => {
  it("converts a share of host CPU into cores", () => {
    expect(cpuCores(25, 16)).toBe(4);
  });
});
