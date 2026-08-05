import { describe, it, expect } from "vitest";
import { describeClient, sinceLabel } from "./userAgent";

// Real strings, copied from real clients. A hand-written "Mozilla/5.0 Chrome"
// would pass any parser and prove nothing — the whole difficulty is that every
// browser claims to be several others.
const UA = {
  firefoxLinux: "Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
  chromeWindows: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
  edgeWindows: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0",
  safariMac: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
  safariIPhone: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  chromeAndroid: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
  iPad: "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/604.1",
  curl: "curl/8.5.0",
  headless: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/126.0.0.0 Safari/537.36",
};

describe("describeClient", () => {
  it("names the browser and the system", () => {
    expect(describeClient(UA.firefoxLinux).label).toBe("Firefox on Linux");
    expect(describeClient(UA.safariMac).label).toBe("Safari on macOS");
  });

  it("prefers the specific brand over the one it impersonates", () => {
    // Edge says Chrome AND Safari; Chrome says Safari. First match wins, so the
    // ordering of the table is the actual logic here.
    expect(describeClient(UA.edgeWindows).label).toBe("Edge on Windows");
    expect(describeClient(UA.chromeWindows).label).toBe("Chrome on Windows");
  });

  it("tells phones, tablets and desktops apart", () => {
    expect(describeClient(UA.safariIPhone).kind).toBe("mobile");
    expect(describeClient(UA.chromeAndroid).kind).toBe("mobile");
    expect(describeClient(UA.iPad).kind).toBe("tablet");
    expect(describeClient(UA.firefoxLinux).kind).toBe("desktop");
  });

  it("names headless Chrome instead of calling it Safari", () => {
    // It drops the Chrome/ token and keeps Safari/, so the naive table order puts
    // it under Safari. Found by pointing the parser at a real headless run.
    expect(describeClient(UA.headless).label).toBe("Headless Chrome on Linux");
  });

  it("names non-browser clients instead of parsing them", () => {
    const d = describeClient(UA.curl);
    expect(d.label).toBe("curl");
    expect(d.kind).toBe("tool");
  });

  it("shows an unrecognised agent verbatim rather than guessing", () => {
    const d = describeClient("SomeInternalTool/2.1");
    expect(d.label).toBe("SomeInternalTool/2.1");
    expect(d.kind).toBe("unknown");
  });

  it("survives an empty or absurd agent", () => {
    expect(describeClient("").label).toBe("Unknown client");
    const long = describeClient("Z".repeat(300));
    expect(long.label.length).toBeLessThanOrEqual(61);
    expect(long.raw).toHaveLength(300);
  });
});

describe("sinceLabel", () => {
  const now = new Date("2026-08-05T12:00:00Z").getTime();
  const ago = (ms: number) => new Date(now - ms).toISOString();

  it("reads recent times as elapsed time", () => {
    expect(sinceLabel(ago(5_000), now)).toBe("just now");
    expect(sinceLabel(ago(10 * 60_000), now)).toBe("10 minutes ago");
    expect(sinceLabel(ago(3 * 3_600_000), now)).toBe("3 hours ago");
    expect(sinceLabel(ago(26 * 3_600_000), now)).toBe("yesterday");
  });

  it("falls back to a date once the minute stops mattering", () => {
    expect(sinceLabel(ago(30 * 24 * 3_600_000), now)).toMatch(/2026/);
  });

  it("does not render a clock skew as the future", () => {
    expect(sinceLabel(new Date(now + 60_000).toISOString(), now)).toBe("just now");
  });

  it("says so when the timestamp is not one", () => {
    expect(sinceLabel("not-a-date", now)).toBe("unknown");
  });
});
