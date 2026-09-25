import { describe, it, expect } from "vitest";
import { toastableEvents } from "./alertStream";
import type { AlertEvent } from "./types";

const ev = (id: number, suppressed?: boolean) => ({ id, suppressed } as AlertEvent);

describe("toastableEvents", () => {
  it("keeps ordinary events, in order", () => {
    expect(toastableEvents([ev(1), ev(2)]).map((e) => e.id)).toEqual([1, 2]);
  });

  it("drops events a maintenance window silenced — they stay in the feed, but never pop up", () => {
    expect(toastableEvents([ev(1), ev(2, true), ev(3, false)]).map((e) => e.id)).toEqual([1, 3]);
  });

  it("returns nothing when every fresh event was silenced", () => {
    expect(toastableEvents([ev(1, true), ev(2, true)])).toEqual([]);
  });
});
