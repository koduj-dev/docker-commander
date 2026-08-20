import { describe, it, expect } from "vitest";
import { formatLogEntries, type Entry } from "./Logs";

function entry(overrides: Partial<Entry> = {}): Entry {
  return {
    containerId: "c1",
    source: "web",
    color: "#fff",
    stream: "stdout",
    level: "info",
    timestamp: "2026-08-20T10:00:00.000Z",
    t: 0,
    message: "hello",
    ...overrides,
  };
}

describe("formatLogEntries", () => {
  it("formats one line per entry with timestamp, source, stream and message", () => {
    const out = formatLogEntries([entry()]);
    expect(out).toBe("2026-08-20T10:00:00.000Z [web] (stdout) hello");
  });

  it("joins multiple entries with newlines, preserving the given order", () => {
    const out = formatLogEntries([
      entry({ message: "first" }),
      entry({ message: "second", stream: "stderr", source: "db" }),
    ]);
    expect(out).toBe(
      "2026-08-20T10:00:00.000Z [web] (stdout) first\n" + "2026-08-20T10:00:00.000Z [db] (stderr) second",
    );
  });

  it("falls back to the arrival time when a line has no server timestamp", () => {
    const out = formatLogEntries([entry({ timestamp: undefined, t: 0 })]);
    expect(out).toBe(`${new Date(0).toISOString()} [web] (stdout) hello`);
  });

  it("returns an empty string for no entries, not a stray newline", () => {
    expect(formatLogEntries([])).toBe("");
  });
});
